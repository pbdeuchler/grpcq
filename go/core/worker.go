package core

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

var errWorkerStopped = errors.New("worker stopped")

// Worker consumes messages from a queue and processes them using registered handlers.
type Worker struct {
	adapter  QueueAdapter
	registry *Registry
	config   WorkerConfig
	logger   *slog.Logger

	// Internal state
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{} // closed when Start() returns
	startErr error        // result of Start(), readable after done is closed
}

// NewWorker creates a new Worker with the given adapter, registry, and config.
// An optional *slog.Logger can be provided via WorkerConfig.Logger; if nil,
// slog.Default() is used.
func NewWorker(adapter QueueAdapter, registry *Registry, config WorkerConfig) *Worker {
	if config.MaxBatch <= 0 {
		config.MaxBatch = 10
	}
	if config.Concurrency <= 0 {
		config.Concurrency = 10
	}
	if config.PollIntervalMs <= 0 {
		config.PollIntervalMs = 1000
	}

	l := config.Logger
	if l == nil {
		l = slog.Default()
	}

	return &Worker{
		adapter:  adapter,
		registry: registry,
		config:   config,
		logger:   l,
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start begins consuming and processing messages from the queue.
// This method blocks until the context is cancelled or Stop is called.
// The returned error is also available to concurrent Stop callers via the done channel.
func (w *Worker) Start(ctx context.Context) error {
	defer func() {
		// Drain in-flight work before signalling completion.
		w.wg.Wait()
		close(w.done)
	}()

	sem := make(chan struct{}, w.config.Concurrency)
	pollInterval := time.Duration(w.config.PollIntervalMs) * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			w.startErr = ctx.Err()
			return w.startErr
		case <-w.stopCh:
			return nil
		default:
		}

		result, err := w.adapter.Consume(ctx, w.config.QueueName, w.config.MaxBatch)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				w.startErr = ctxErr
				return w.startErr
			}
			w.startErr = err
			return w.startErr
		}

		if result == nil || len(result.Items) == 0 {
			if err := w.waitForNextPoll(ctx, pollInterval); err != nil {
				if errors.Is(err, errWorkerStopped) {
					return nil
				}
				w.startErr = err
				return w.startErr
			}
			continue
		}

		for _, item := range result.Items {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				w.startErr = ctx.Err()
				return w.startErr
			case <-w.stopCh:
				return nil
			}

			w.wg.Add(1)
			go func(item MessageItem) {
				defer w.wg.Done()
				defer func() { <-sem }()
				w.processMessage(ctx, item)
			}(item)
		}
	}
}

// Stop gracefully signals the worker to stop and waits for in-flight work to drain.
// Multiple concurrent calls to Stop all block until Start returns.
func (w *Worker) Stop(_ context.Context) error {
	w.stopOnce.Do(func() { close(w.stopCh) })
	<-w.done
	return w.startErr
}

func (w *Worker) waitForNextPoll(ctx context.Context, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		return nil
	}

	timer := time.NewTimer(pollInterval)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.stopCh:
		return errWorkerStopped
	}
}

// processMessage handles a single message item.
func (w *Worker) processMessage(ctx context.Context, item MessageItem) {
	msg := item.Message

	w.logger.InfoContext(ctx, "processing message",
		slog.String("message_id", msg.MessageId),
		slog.String("topic", msg.Topic),
		slog.String("action", msg.Action))

	err := w.registry.Handle(ctx, msg)

	if err != nil {
		w.logger.ErrorContext(ctx, "handler failed",
			slog.String("message_id", msg.MessageId),
			slog.Any("error", err))
		if nackErr := item.Receipt.Nack(ctx); nackErr != nil {
			w.logger.ErrorContext(ctx, "failed to nack message",
				slog.String("message_id", msg.MessageId),
				slog.Any("error", nackErr))
		}
		return
	}

	if ackErr := item.Receipt.Ack(ctx); ackErr != nil {
		w.logger.ErrorContext(ctx, "failed to ack message",
			slog.String("message_id", msg.MessageId),
			slog.Any("error", ackErr))
		return
	}

	w.logger.InfoContext(ctx, "message processed",
		slog.String("message_id", msg.MessageId))
}

// WorkerPool manages multiple workers for horizontal scaling.
type WorkerPool struct {
	workers []*Worker
	stopCh  chan struct{} // signals all workers to stop
	once    sync.Once
	done    chan struct{} // closed when Start returns
}

// NewWorkerPool creates a pool of workers.
func NewWorkerPool(adapter QueueAdapter, registry *Registry, config WorkerConfig, numWorkers int) *WorkerPool {
	workers := make([]*Worker, numWorkers)
	for i := range numWorkers {
		workers[i] = NewWorker(adapter, registry, config)
	}
	return &WorkerPool{
		workers: workers,
		stopCh:  make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Start starts all workers in the pool.
// If any worker encounters an error, all workers are stopped gracefully.
// The function blocks until the parent context is cancelled or an error occurs.
func (p *WorkerPool) Start(ctx context.Context) error {
	defer close(p.done)

	poolCtx, poolCancel := context.WithCancel(ctx)
	defer poolCancel()

	errCh := make(chan error, len(p.workers))
	var wg sync.WaitGroup

	for _, worker := range p.workers {
		wg.Add(1)
		go func(w *Worker) {
			defer wg.Done()
			if err := w.Start(poolCtx); err != nil && err != context.Canceled {
				select {
				case errCh <- err:
				default:
				}
				poolCancel()
			}
		}(worker)
	}

	// Also listen for external Stop signal
	go func() {
		select {
		case <-p.stopCh:
			poolCancel()
		case <-poolCtx.Done():
		}
	}()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-ctx.Done():
		poolCancel()
		<-doneCh
		return ctx.Err()
	case err := <-errCh:
		<-doneCh
		return err
	case <-doneCh:
		return nil
	}
}

// Stop stops all workers in the pool gracefully and blocks until Start returns.
func (p *WorkerPool) Stop(_ context.Context) error {
	p.once.Do(func() { close(p.stopCh) })
	<-p.done
	return nil
}

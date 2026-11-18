package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Worker consumes messages concurrently. Shutdown stops intake and drains every
// received batch, including handler execution and acknowledgement.
type Worker struct {
	adapter  QueueAdapter
	registry *Registry
	config   WorkerConfig
	logger   *slog.Logger
	wg       sync.WaitGroup
	started  atomic.Bool
	stop     context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	startErr error // Readable after done is closed.
}

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
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	stop, cancel := context.WithCancel(context.Background())
	return &Worker{
		adapter:  adapter,
		registry: registry,
		config:   config,
		logger:   logger,
		stop:     stop,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

// Start runs once. Cancelling ctx stops intake without cancelling received work.
func (w *Worker) Start(ctx context.Context) (outcome error) {
	if !w.started.CompareAndSwap(false, true) {
		return errors.New("worker has already been started")
	}
	intake, cancel := context.WithCancel(ctx)
	unregister := context.AfterFunc(w.stop, cancel)
	if w.stop.Err() != nil {
		cancel()
	}
	failures := make(chan error, 1)
	defer func() {
		unregister()
		cancel()
		w.wg.Wait()
		select {
		case failure := <-failures:
			if outcome == nil || errors.Is(outcome, context.Canceled) || errors.Is(outcome, context.DeadlineExceeded) {
				outcome = failure
			}
		default:
		}
		w.startErr = outcome
		close(w.done)
	}()

	// Preserve context values while allowing handlers and receipts to drain.
	processing := context.WithoutCancel(ctx)
	sem := make(chan struct{}, w.config.Concurrency)
	for intake.Err() == nil {
		result, err := w.adapter.Consume(intake, w.config.QueueName, w.config.MaxBatch)
		if err != nil {
			if intake.Err() != nil {
				break
			}
			return err
		}
		if result == nil || len(result.Items) == 0 {
			timer := time.NewTimer(time.Duration(w.config.PollIntervalMs) * time.Millisecond)
			select {
			case <-timer.C:
			case <-intake.Done():
			}
			timer.Stop()
			continue
		}
		// Ownership transfers on receive; finish the entire batch before exiting.
		for _, item := range result.Items {
			sem <- struct{}{}
			w.wg.Add(1)
			go func(item MessageItem) {
				defer w.wg.Done()
				defer func() { <-sem }()
				if err := w.processMessage(processing, item); err != nil {
					select {
					case failures <- err:
					default:
					}
					cancel()
				}
			}(item)
		}
	}
	if w.stop.Err() != nil {
		return nil
	}
	return ctx.Err()
}

// Stop stops intake and waits for received work. Its context bounds the wait;
// work continues draining if that context expires. Calling Stop before Start is safe.
func (w *Worker) Stop(ctx context.Context) error {
	w.cancel()
	if !w.started.Load() {
		return nil
	}
	select {
	case <-w.done:
		return w.startErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processMessage handles a single message item.
func (w *Worker) processMessage(ctx context.Context, item MessageItem) error {
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
			err = errors.Join(err, fmt.Errorf("failed to nack message: %w", nackErr))
		}
		return fmt.Errorf("failed to handle message %s (%s/%s): %w", msg.MessageId, msg.Topic, msg.Action, err)
	}

	if ackErr := item.Receipt.Ack(ctx); ackErr != nil {
		w.logger.ErrorContext(ctx, "failed to ack message",
			slog.String("message_id", msg.MessageId),
			slog.Any("error", ackErr))
		return fmt.Errorf("failed to ack message %s (%s/%s): %w", msg.MessageId, msg.Topic, msg.Action, ackErr)
	}

	w.logger.InfoContext(ctx, "message processed",
		slog.String("message_id", msg.MessageId))
	return nil
}

// WorkerPool runs workers together and stops siblings on the first failure.
type WorkerPool struct {
	workers []*Worker
	started atomic.Bool
	stop    context.Context
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewWorkerPool(adapter QueueAdapter, registry *Registry, config WorkerConfig, numWorkers int) *WorkerPool {
	workers := make([]*Worker, max(numWorkers, 1))
	for i := range workers {
		workers[i] = NewWorker(adapter, registry, config)
	}
	stop, cancel := context.WithCancel(context.Background())
	return &WorkerPool{
		workers: workers,
		stop:    stop,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
}

func (p *WorkerPool) Start(ctx context.Context) error {
	if !p.started.CompareAndSwap(false, true) {
		return errors.New("worker pool has already been started")
	}
	defer close(p.done)
	intake, cancel := context.WithCancel(ctx)
	defer cancel()
	unregister := context.AfterFunc(p.stop, cancel)
	defer unregister()
	if p.stop.Err() != nil {
		cancel()
	}

	results := make(chan error, len(p.workers))
	for _, worker := range p.workers {
		go func() { results <- worker.Start(intake) }()
	}
	var failure error
	for range p.workers {
		if err := <-results; err != nil {
			if failure == nil && err != intake.Err() {
				failure = err
			}
			cancel()
		}
	}
	if failure != nil {
		return failure
	}
	return ctx.Err()
}

// Stop stops all workers and waits for their received work, bounded by ctx.
func (p *WorkerPool) Stop(ctx context.Context) error {
	p.cancel()
	if !p.started.Load() {
		return nil
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

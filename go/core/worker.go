package core

import (
	"context"
	"log"
	"sync"
	"time"
)

// Worker consumes messages from a queue and processes them using registered handlers.
type Worker struct {
	adapter  QueueAdapter
	registry *Registry
	config   WorkerConfig

	// Internal state
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewWorker creates a new Worker with the given adapter, registry, and config.
func NewWorker(adapter QueueAdapter, registry *Registry, config WorkerConfig) *Worker {
	// Apply defaults
	if config.MaxBatch <= 0 {
		config.MaxBatch = 10
	}
	if config.Concurrency <= 0 {
		config.Concurrency = 10
	}
	if config.PollIntervalMs <= 0 {
		config.PollIntervalMs = 1000
	}

	return &Worker{
		adapter:  adapter,
		registry: registry,
		config:   config,
		stopCh:   make(chan struct{}),
	}
}

// Start begins consuming and processing messages from the queue.
// This method blocks until the context is cancelled or Stop is called.
func (w *Worker) Start(ctx context.Context) error {
	// Create a semaphore to limit concurrency
	sem := make(chan struct{}, w.config.Concurrency)

	pollInterval := time.Duration(w.config.PollIntervalMs) * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return w.shutdown(ctx)
		case <-w.stopCh:
			return w.shutdown(ctx)
		default:
		}

		// Consume a batch of messages
		result, err := w.adapter.Consume(ctx, w.config.QueueName, w.config.MaxBatch)
		if err != nil {
			// Log the error but continue processing
			log.Printf("error consuming messages: %v", err)
			time.Sleep(pollInterval)
			continue
		}

		// If no messages, wait before polling again
		if len(result.Items) == 0 {
			time.Sleep(pollInterval)
			continue
		}

		// Process each message concurrently
		for _, item := range result.Items {
			// Acquire semaphore slot
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return w.shutdown(ctx)
			case <-w.stopCh:
				return w.shutdown(ctx)
			}

			w.wg.Add(1)
			go func(item MessageItem) {
				defer w.wg.Done()
				defer func() { <-sem }() // Release semaphore slot

				w.processMessage(ctx, item)
			}(item)
		}
	}
}

// Stop gracefully shuts down the worker.
// It waits for all in-flight messages to complete processing.
func (w *Worker) Stop(ctx context.Context) error {
	var shutdownErr error
	w.stopOnce.Do(func() {
		close(w.stopCh)
		shutdownErr = w.shutdown(ctx)
	})
	return shutdownErr
}

// shutdown waits for all in-flight messages to complete.
func (w *Worker) shutdown(ctx context.Context) error {
	// Wait for all message processing to complete
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processMessage handles a single message item.
func (w *Worker) processMessage(ctx context.Context, item MessageItem) {
	msg := item.Message

	// Log the message being processed
	log.Printf("processing message: id=%s topic=%s action=%s", msg.MessageId, msg.Topic, msg.Action)

	// Handle the message using the registry
	err := w.registry.Handle(ctx, msg)

	if err != nil {
		// Handler failed, nack the message
		log.Printf("handler failed for message %s: %v", msg.MessageId, err)
		if nackErr := item.Receipt.Nack(ctx); nackErr != nil {
			log.Printf("failed to nack message %s: %v", msg.MessageId, nackErr)
		}
		return
	}

	// Handler succeeded, ack the message
	if ackErr := item.Receipt.Ack(ctx); ackErr != nil {
		log.Printf("failed to ack message %s: %v", msg.MessageId, ackErr)
		return
	}

	log.Printf("successfully processed message: id=%s", msg.MessageId)
}

// WorkerPool manages multiple workers for horizontal scaling.
type WorkerPool struct {
	workers []*Worker
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewWorkerPool creates a pool of workers.
func NewWorkerPool(adapter QueueAdapter, registry *Registry, config WorkerConfig, numWorkers int) *WorkerPool {
	workers := make([]*Worker, numWorkers)
	for i := 0; i < numWorkers; i++ {
		workers[i] = NewWorker(adapter, registry, config)
	}
	return &WorkerPool{
		workers: workers,
	}
}

// Start starts all workers in the pool.
// If any worker encounters an error, all workers are stopped gracefully.
func (p *WorkerPool) Start(ctx context.Context) error {
	// Create a child context that we control
	p.ctx, p.cancel = context.WithCancel(ctx)
	defer p.cancel() // Ensure cleanup on return

	errCh := make(chan error, len(p.workers))

	for _, worker := range p.workers {
		p.wg.Add(1)
		go func(w *Worker) {
			defer p.wg.Done()
			if err := w.Start(p.ctx); err != nil && err != context.Canceled {
				errCh <- err
				// Cancel context to stop other workers
				p.cancel()
			}
		}(worker)
	}

	// Wait for all workers to complete
	go func() {
		p.wg.Wait()
		close(errCh)
	}()

	// Collect all errors
	var firstErr error
	for err := range errCh {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// Stop stops all workers in the pool gracefully.
func (p *WorkerPool) Stop(ctx context.Context) error {
	// Cancel the pool context to signal shutdown
	if p.cancel != nil {
		p.cancel()
	}

	// Wait for all workers to complete with timeout from context
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

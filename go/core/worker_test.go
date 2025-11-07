package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/pbdeuchler/grpcq/go/proto"
)

type stubAdapter struct {
	item       MessageItem
	delivered  bool
	deliverMux sync.Mutex
}

func (a *stubAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	return nil
}

func (a *stubAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	a.deliverMux.Lock()
	defer a.deliverMux.Unlock()

	if !a.delivered {
		a.delivered = true
		return &ConsumeResult{Items: []MessageItem{a.item}}, nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// No more messages available
	return &ConsumeResult{Items: nil}, nil
}

type stubReceipt struct {
	acked   bool
	nacked  bool
	mu      sync.Mutex
	ackOnce sync.Once
	ackCh   chan struct{}
}

func newStubReceipt() *stubReceipt {
	return &stubReceipt{
		ackCh: make(chan struct{}),
	}
}

func (r *stubReceipt) Ack(ctx context.Context) error {
	r.mu.Lock()
	r.acked = true
	r.mu.Unlock()

	r.ackOnce.Do(func() { close(r.ackCh) })
	return nil
}

func (r *stubReceipt) Nack(ctx context.Context) error {
	r.mu.Lock()
	r.nacked = true
	r.mu.Unlock()
	return nil
}

func TestWorkerShutdownWaitsForInFlight(t *testing.T) {
	receipt := newStubReceipt()
	handlerStarted := make(chan struct{})
	handlerRelease := make(chan struct{})

	registry := NewRegistry()
	registry.Register("svc", "action", func(ctx context.Context, msg *pb.Message) error {
		close(handlerStarted)
		<-handlerRelease
		return nil
	})

	adapter := &stubAdapter{
		item: MessageItem{
			Message: &pb.Message{Topic: "svc", Action: "action", MessageId: "1"},
			Receipt: receipt,
		},
	}

	worker := NewWorker(adapter, registry, WorkerConfig{
		QueueName:      "queue",
		MaxBatch:       1,
		Concurrency:    1,
		PollIntervalMs: 10,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- worker.Start(ctx)
	}()

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	cancel()

	// Ensure ack has not happened yet
	select {
	case <-receipt.ackCh:
		t.Fatal("ack completed before handler finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(handlerRelease)

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit in time")
	}

	if !receipt.acked {
		t.Fatal("expected message to be acked")
	}
}

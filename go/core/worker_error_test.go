package core

import (
	"context"
	"errors"
	"testing"

	pb "github.com/pbdeuchler/grpcq/go/proto"
)

type consumeErrorAdapter struct {
	calls int
}

func (a *consumeErrorAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	return nil
}

func (a *consumeErrorAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	a.calls++
	if a.calls == 1 {
		return &ConsumeResult{}, nil
	}
	return nil, errors.New("boom")
}

func TestWorkerReturnsConsumeErrors(t *testing.T) {
	registry := NewRegistry()
	worker := NewWorker(&consumeErrorAdapter{}, registry, WorkerConfig{
		QueueName:      "queue",
		Concurrency:    1,
		MaxBatch:       1,
		PollIntervalMs: 1,
	})

	err := worker.Start(context.Background())
	if err == nil {
		t.Fatal("expected consume error, got nil")
	}
	if err.Error() != "boom" {
		t.Fatalf("expected consume error to be returned, got %v", err)
	}
}

package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pbdeuchler/grpcq/go/adapters/memory"
	"github.com/pbdeuchler/grpcq/go/core"
	pb "github.com/pbdeuchler/grpcq/go/proto"
)

type failingReceipt struct {
	ackError, nackError error
	acks, nacks         int
	cancel              context.CancelFunc
}

func (r *failingReceipt) Ack(context.Context) error  { r.acks++; r.cancel(); return r.ackError }
func (r *failingReceipt) Nack(context.Context) error { r.nacks++; r.cancel(); return r.nackError }

func TestWorkerReturnsProcessingAndSettlementFailures(t *testing.T) {
	handlerFailure := errors.New("handler failed")
	ackFailure := errors.New("delete failed")
	nackFailure := errors.New("requeue failed")
	for _, tc := range []struct {
		name               string
		handler, ack, nack error
	}{
		{name: "success"},
		{name: "handler", handler: handlerFailure},
		{name: "ack", ack: ackFailure},
		{name: "handler and nack", handler: handlerFailure, nack: nackFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			receipt := &failingReceipt{ackError: tc.ack, nackError: tc.nack, cancel: cancel}
			delivered := false
			adapter := receiveAdapter{receive: func(ctx context.Context) (*core.ConsumeResult, error) {
				if !delivered {
					delivered = true
					return &core.ConsumeResult{Items: []core.MessageItem{{Message: &pb.Message{MessageId: "message-1", Topic: "svc", Action: "call"}, Receipt: receipt}}}, nil
				}
				<-ctx.Done()
				return nil, ctx.Err()
			}}
			registry := core.NewRegistry()
			registry.Register("svc", "call", func(context.Context, *pb.Message) error { return tc.handler })
			worker := core.NewWorker(adapter, registry, core.WorkerConfig{QueueName: "q", Concurrency: 1})
			err := worker.Start(ctx)
			if tc.handler == nil && tc.ack == nil {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("successful processing error = %v", err)
				}
			} else {
				for _, cause := range []error{tc.handler, tc.ack, tc.nack} {
					if cause != nil && !errors.Is(err, cause) {
						t.Errorf("lost error %v in %v", cause, err)
					}
				}
				for _, detail := range []string{"message-1", "svc", "call"} {
					if err == nil || !strings.Contains(err.Error(), detail) {
						t.Errorf("missing delivery context %q: %v", detail, err)
					}
				}
			}
			if tc.handler == nil {
				if receipt.acks != 1 || receipt.nacks != 0 {
					t.Fatalf("successful handler settlement: ack=%d nack=%d", receipt.acks, receipt.nacks)
				}
			} else if receipt.acks != 0 || receipt.nacks != 1 {
				t.Fatalf("failed handler settlement: ack=%d nack=%d", receipt.acks, receipt.nacks)
			}
		})
	}
}

func TestPoisonMessageStopsWorkerAndPreservesReceivedBatch(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown route", true: "handler error"}[registered], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			memory := memory.NewAdapter(2)
			if err := memory.Publish(ctx, "q", &pb.Message{MessageId: "bad", Topic: "svc", Action: "call"}, &pb.Message{MessageId: "good", Topic: "svc", Action: "call"}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			registry := core.NewRegistry()
			if registered {
				registry.Register("svc", "call", func(_ context.Context, msg *pb.Message) error {
					calls++
					if msg.MessageId == "bad" {
						return errors.New("permanent failure")
					}
					return nil
				})
			}
			worker := core.NewWorker(memory, registry, core.WorkerConfig{QueueName: "q", Concurrency: 1, MaxBatch: 2})
			err := worker.Start(ctx)
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("poison message was silently retried: %v", err)
			}
			if registered {
				if calls != 2 || memory.QueueDepth("q") != 1 {
					t.Fatalf("received batch not drained once: calls=%d depth=%d", calls, memory.QueueDepth("q"))
				}
			} else if memory.QueueDepth("q") != 2 {
				t.Fatal("unknown messages disappeared")
			}
		})
	}
}

func TestHandlerCancellationRemainsAProcessingFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	adapter := memory.NewAdapter(1)
	if err := adapter.Publish(ctx, "q", &pb.Message{MessageId: "cancelled-handler", Topic: "svc", Action: "call"}); err != nil {
		t.Fatal(err)
	}
	registry := core.NewRegistry()
	registry.Register("svc", "call", func(context.Context, *pb.Message) error { return context.Canceled })
	pool := core.NewWorkerPool(adapter, registry, core.DefaultWorkerConfig("q"), 2)
	err := pool.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "failed to handle message cancelled-handler") {
		t.Fatalf("handler cancellation was mistaken for pool shutdown: %v", err)
	}
	if adapter.QueueDepth("q") != 1 {
		t.Fatal("failed message was lost")
	}
}

package core_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pbdeuchler/grpcq/go/adapters/memory"
	"github.com/pbdeuchler/grpcq/go/core"
	pb "github.com/pbdeuchler/grpcq/go/proto"
)

type receiveAdapter struct {
	receive func(context.Context) (*core.ConsumeResult, error)
}

func (a receiveAdapter) Publish(context.Context, string, ...*pb.Message) error { return nil }
func (a receiveAdapter) Consume(ctx context.Context, _ string, _ int) (*core.ConsumeResult, error) {
	return a.receive(ctx)
}

func await[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle operation timed out")
		var zero T
		return zero
	}
}

func TestShutdownDrainsReceivedBatch(t *testing.T) {
	for _, stop := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			name := "cancel"
			if stop {
				name = "stop"
			}
			if fail {
				name += "/handler-error"
			} else {
				name += "/success"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				adapter := memory.NewAdapter(2)
				messages := []*pb.Message{{Topic: "svc", Action: "call"}, {Topic: "svc", Action: "call"}}
				if err := adapter.Publish(ctx, "q", messages...); err != nil {
					t.Fatal(err)
				}
				started := make(chan struct{})
				release := make(chan struct{})
				var count atomic.Int32
				registry := core.NewRegistry()
				registry.Register("svc", "call", func(handlerCtx context.Context, _ *pb.Message) error {
					if count.Add(1) == 1 {
						close(started)
						<-release
					}
					if err := handlerCtx.Err(); err != nil {
						t.Errorf("shutdown cancelled handler context: %v", err)
					}
					if fail {
						return errors.New("handler failed")
					}
					return nil
				})
				worker := core.NewWorker(adapter, registry, core.WorkerConfig{QueueName: "q", Concurrency: 1, MaxBatch: 2})
				running := make(chan error, 1)
				go func() { running <- worker.Start(ctx) }()
				await(t, started)
				stopped := make(chan error, 1)
				if stop {
					deadline, end := context.WithCancel(context.Background())
					end()
					if err := worker.Stop(deadline); err != context.Canceled {
						t.Fatalf("stop must honor its deadline: %v", err)
					}
					go func() { stopped <- worker.Stop(context.Background()) }()
				} else {
					cancel()
				}
				select {
				case err := <-running:
					t.Fatalf("returned before draining: %v", err)
				default:
				}
				close(release)
				result := await(t, running)
				if fail {
					if result == nil || !strings.Contains(result.Error(), "handler failed") {
						t.Fatalf("handler failure was hidden by shutdown: %v", result)
					}
				} else if stop {
					if result != nil {
						t.Fatal(result)
					}
				} else if !errors.Is(result, context.Canceled) {
					t.Fatalf("start error = %v", result)
				}
				if stop {
					if err := await(t, stopped); err != result {
						t.Fatalf("stop error = %v, start error = %v", err, result)
					}
				}
				if count.Load() != 2 {
					t.Fatalf("processed %d messages, want 2", count.Load())
				}
				if fail {
					if adapter.QueueDepth("q") != 2 {
						t.Fatal("failed batch was not returned to queue")
					}
				} else if err := adapter.Publish(context.Background(), "q", messages...); err != nil {
					t.Fatalf("received batch not acknowledged: %v", err)
				}
			})
		}
	}
}

func TestWorkerStopInterruptsReceive(t *testing.T) {
	receiving := make(chan struct{})
	worker := core.NewWorker(receiveAdapter{receive: func(ctx context.Context) (*core.ConsumeResult, error) {
		close(receiving)
		<-ctx.Done()
		return nil, ctx.Err()
	}}, core.NewRegistry(), core.DefaultWorkerConfig("q"))
	result := make(chan error, 1)
	go func() { result <- worker.Start(context.Background()) }()
	await(t, receiving)
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerLifecycleRejectsRepeatedStart(t *testing.T) {
	for _, stoppedBeforeStart := range []bool{true, false} {
		t.Run(map[bool]string{true: "pre-stopped", false: "running"}[stoppedBeforeStart], func(t *testing.T) {
			receiving := make(chan struct{})
			worker := core.NewWorker(receiveAdapter{receive: func(ctx context.Context) (*core.ConsumeResult, error) {
				close(receiving)
				<-ctx.Done()
				return nil, ctx.Err()
			}}, core.NewRegistry(), core.DefaultWorkerConfig("q"))
			if stoppedBeforeStart {
				deadline, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := worker.Stop(deadline); err != nil {
					t.Fatal(err)
				}
				if err := worker.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				running := make(chan error, 1)
				go func() { running <- worker.Start(context.Background()) }()
				await(t, receiving)
				if err := worker.Start(context.Background()); err == nil {
					t.Fatal("concurrent start must fail")
				}
				if err := worker.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := await(t, running); err != nil {
					t.Fatal(err)
				}
			}
			if err := worker.Start(context.Background()); err == nil {
				t.Fatal("repeated start must fail")
			}
		})
	}
}

func TestPoolLifecycle(t *testing.T) {
	for _, operation := range []string{"stop", "cancel", "failure", "pre-stopped", "pre-cancelled"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			receiving := make(chan struct{}, 2)
			fail := errors.New("receive failed")
			adapter := receiveAdapter{receive: func(ctx context.Context) (*core.ConsumeResult, error) {
				receiving <- struct{}{}
				if operation == "failure" {
					return nil, fail
				}
				<-ctx.Done()
				return nil, ctx.Err()
			}}
			pool := core.NewWorkerPool(adapter, core.NewRegistry(), core.DefaultWorkerConfig("q"), 2)
			if operation == "pre-stopped" {
				stopCtx, end := context.WithTimeout(ctx, time.Second)
				defer end()
				if err := pool.Stop(stopCtx); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "pre-cancelled" {
				cancel()
			}
			result := make(chan error, 1)
			go func() { result <- pool.Start(ctx) }()
			if operation == "stop" || operation == "cancel" {
				await(t, receiving)
				if operation == "cancel" {
					cancel()
				} else {
					stopCtx, end := context.WithTimeout(ctx, time.Second)
					defer end()
					if err := pool.Stop(stopCtx); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := await(t, result)
			switch operation {
			case "failure":
				if !errors.Is(err, fail) {
					t.Fatalf("error = %v", err)
				}
			case "cancel", "pre-cancelled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := pool.Start(context.Background()); err == nil {
				t.Fatal("repeated pool start must fail")
			}
		})
	}
}

func TestPoolStopDeadlineAllowsDrainToContinue(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter(1)
	message := &pb.Message{Topic: "svc", Action: "call"}
	if err := adapter.Publish(ctx, "q", message); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	registry := core.NewRegistry()
	registry.Register("svc", "call", func(handlerCtx context.Context, _ *pb.Message) error {
		close(started)
		<-release
		if err := handlerCtx.Err(); err != nil {
			t.Errorf("pool shutdown cancelled handler: %v", err)
		}
		return nil
	})
	pool := core.NewWorkerPool(adapter, registry, core.DefaultWorkerConfig("q"), 2)
	running := make(chan error, 1)
	go func() { running <- pool.Start(ctx) }()
	await(t, started)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := pool.Stop(cancelled); err != context.Canceled {
		t.Fatalf("stop wait error = %v", err)
	}
	close(release)
	if err := await(t, running); err != nil {
		t.Fatal(err)
	}
	if err := pool.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Publish(ctx, "q", message); err != nil {
		t.Fatalf("pool did not acknowledge drained handler: %v", err)
	}
}

func TestPoolUsesAtLeastOneWorker(t *testing.T) {
	for _, count := range []int{-1, 0, 1} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		pool := core.NewWorkerPool(memory.NewAdapter(1), core.NewRegistry(), core.DefaultWorkerConfig("q"), count)
		if err := pool.Start(ctx); err != context.Canceled {
			t.Fatalf("worker count %d: error = %v", count, err)
		}
	}
}

func TestPoolPropagatesIndependentCancellationError(t *testing.T) {
	pool := core.NewWorkerPool(receiveAdapter{receive: func(context.Context) (*core.ConsumeResult, error) { return nil, context.Canceled }}, core.NewRegistry(), core.DefaultWorkerConfig("q"), 2)
	if err := pool.Start(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("independent receive failure was swallowed: %v", err)
	}
}

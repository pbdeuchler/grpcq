package memory

import (
	"context"
	"sync"
	"testing"

	pb "github.com/pbdeuchler/grpcq/go/proto"
)

func TestCapacityIncludesUnacknowledgedMessages(t *testing.T) {
	ctx := context.Background()
	adapter := NewAdapter(1)
	if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "original"}); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Consume(ctx, "queue", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "replacement"}); err == nil {
		t.Fatal("publish must not reuse capacity reserved by an unacknowledged message")
	}
	if err := result.Items[0].Receipt.Nack(ctx); err != nil {
		t.Fatal(err)
	}
	retried, err := adapter.Consume(ctx, "queue", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(retried.Items) != 1 || retried.Items[0].Message.MessageId != "original" {
		t.Fatalf("lost original message: %+v", retried.Items)
	}
	if err := retried.Items[0].Receipt.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "replacement"}); err != nil {
		t.Fatalf("ack must release capacity: %v", err)
	}
}

func TestReceiptSettlesExactlyOnce(t *testing.T) {
	for _, first := range []string{"ack", "nack"} {
		for _, second := range []string{"ack", "nack"} {
			t.Run(first+"/"+second, func(t *testing.T) {
				ctx := context.Background()
				adapter := NewAdapter(1)
				if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "original"}); err != nil {
					t.Fatal(err)
				}
				result, err := adapter.Consume(ctx, "queue", 1)
				if err != nil {
					t.Fatal(err)
				}
				receipt := result.Items[0].Receipt
				actions := map[string]func(context.Context) error{"ack": receipt.Ack, "nack": receipt.Nack}
				if err := actions[first](ctx); err != nil {
					t.Fatal(err)
				}
				if err := actions[second](ctx); err == nil {
					t.Fatal("second settlement must fail")
				}
				wantDepth := 0
				if first == "nack" {
					wantDepth = 1
				}
				if got := adapter.QueueDepth("queue"); got != wantDepth {
					t.Fatalf("depth = %d, want %d", got, wantDepth)
				}
				err = adapter.Publish(ctx, "queue", &pb.Message{MessageId: "next"})
				if (err == nil) != (first == "ack") {
					t.Fatalf("capacity changed after second settlement: %v", err)
				}
			})
		}
	}
}

func TestClearPreservesOutstandingReceipts(t *testing.T) {
	for _, settlement := range []string{"ack", "nack"} {
		t.Run(settlement, func(t *testing.T) {
			ctx := context.Background()
			adapter := NewAdapter(2)
			if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "in-flight"}, &pb.Message{MessageId: "queued"}); err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Consume(ctx, "queue", 1)
			if err != nil {
				t.Fatal(err)
			}
			adapter.Clear()
			if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "new"}); err != nil {
				t.Fatal(err)
			}
			if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "overflow"}); err == nil {
				t.Fatal("clear released in-flight capacity")
			}
			receipt := result.Items[0].Receipt
			if settlement == "ack" {
				if err := receipt.Ack(ctx); err != nil {
					t.Fatal(err)
				}
				if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "after-ack"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := receipt.Nack(ctx); err != nil {
					t.Fatal(err)
				}
				retried, err := adapter.Consume(ctx, "queue", 2)
				if err != nil {
					t.Fatal(err)
				}
				if len(retried.Items) != 2 || retried.Items[1].Message.MessageId != "in-flight" {
					t.Fatalf("clear lost outstanding message: %+v", retried.Items)
				}
			}
		})
	}
}

func TestConcurrentReceiptSettlement(t *testing.T) {
	ctx := context.Background()
	adapter := NewAdapter(1)
	if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "original"}); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Consume(ctx, "queue", 1)
	if err != nil {
		t.Fatal(err)
	}
	receipt := result.Items[0].Receipt
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for _, settle := range []func(context.Context) error{receipt.Ack, receipt.Nack} {
		wg.Add(1)
		go func() { defer wg.Done(); outcomes <- settle(ctx) }()
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("settlement successes = %d, want 1", successes)
	}
	depth := adapter.QueueDepth("queue")
	err = adapter.Publish(ctx, "queue", &pb.Message{MessageId: "next"})
	if (err == nil) != (depth == 0) {
		t.Fatalf("capacity disagrees with settlement: depth=%d, error=%v", depth, err)
	}
}

func TestCancelledOperationsPreserveDelivery(t *testing.T) {
	for _, operation := range []string{"publish", "consume", "ack", "nack"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			adapter := NewAdapter(1)
			original := &pb.Message{MessageId: "original"}
			if operation == "publish" {
				if err := adapter.Publish(cancelled, "queue", original); err != context.Canceled {
					t.Fatalf("publish error = %v", err)
				}
			}
			if err := adapter.Publish(ctx, "queue", original); err != nil {
				t.Fatal(err)
			}
			if operation == "consume" {
				if _, err := adapter.Consume(cancelled, "queue", 1); err != context.Canceled {
					t.Fatalf("consume error = %v", err)
				}
			}
			delivery, err := adapter.Consume(ctx, "queue", 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(delivery.Items) != 1 {
				t.Fatalf("delivery disappeared: %+v", delivery.Items)
			}
			receipt := delivery.Items[0].Receipt
			switch operation {
			case "ack":
				if err := receipt.Ack(cancelled); err != context.Canceled {
					t.Fatalf("ack error = %v", err)
				}
			case "nack":
				if err := receipt.Nack(cancelled); err != context.Canceled {
					t.Fatalf("nack error = %v", err)
				}
			}
			if err := receipt.Nack(ctx); err != nil {
				t.Fatalf("receipt must remain usable: %v", err)
			}
			retried, err := adapter.Consume(ctx, "queue", 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(retried.Items) != 1 || retried.Items[0].Message.MessageId != "original" {
				t.Fatal("cancelled operation lost original message")
			}
		})
	}
}

func TestBatchCapacityAndQueueIsolation(t *testing.T) {
	ctx := context.Background()
	adapter := NewAdapter(3)
	if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "a"}, &pb.Message{MessageId: "b"}); err != nil {
		t.Fatal(err)
	}
	delivery, err := adapter.Consume(ctx, "queue", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "c"}, &pb.Message{MessageId: "d"}); err == nil {
		t.Fatal("batch must respect in-flight capacity")
	}
	if adapter.QueueDepth("queue") != 0 {
		t.Fatal("rejected batch was partially accepted")
	}
	if err := adapter.Publish(ctx, "other", &pb.Message{MessageId: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Publish(ctx, "queue", &pb.Message{MessageId: "c"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range delivery.Items {
		if err := item.Receipt.Nack(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if adapter.QueueDepth("queue") != 3 || adapter.QueueDepth("other") != 1 {
		t.Fatal("capacity or queue isolation changed")
	}
}

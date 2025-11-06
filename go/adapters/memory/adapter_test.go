package memory

import (
	"context"
	"testing"

	pb "github.com/pbdeuchler/grpcq/proto/grpcq"
)

func TestAdapterPublishConsume(t *testing.T) {
	adapter := NewAdapter(100)
	ctx := context.Background()

	// Publish a message
	msg := &pb.Message{
		Originator:  "test",
		Topic:       "test.Service",
		Action:      "TestAction",
		MessageId:   "msg-1",
		TimestampMs: 123456789,
	}

	err := adapter.Publish(ctx, "test-queue", msg)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Verify queue depth
	depth := adapter.QueueDepth("test-queue")
	if depth != 1 {
		t.Errorf("Expected queue depth 1, got %d", depth)
	}

	// Consume the message
	result, err := adapter.Consume(ctx, "test-queue", 10)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(result.Items))
	}

	received := result.Items[0].Message
	if received.MessageId != msg.MessageId {
		t.Errorf("Expected message ID %s, got %s", msg.MessageId, received.MessageId)
	}

	// Verify queue is empty
	depth = adapter.QueueDepth("test-queue")
	if depth != 0 {
		t.Errorf("Expected queue depth 0, got %d", depth)
	}
}

func TestAdapterBatchPublish(t *testing.T) {
	adapter := NewAdapter(100)
	ctx := context.Background()

	messages := []*pb.Message{
		{MessageId: "msg-1"},
		{MessageId: "msg-2"},
		{MessageId: "msg-3"},
	}

	err := adapter.Publish(ctx, "test-queue", messages...)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	depth := adapter.QueueDepth("test-queue")
	if depth != 3 {
		t.Errorf("Expected queue depth 3, got %d", depth)
	}

	// Consume with max batch of 2
	result, err := adapter.Consume(ctx, "test-queue", 2)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}

	if len(result.Items) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(result.Items))
	}

	// One message should remain
	depth = adapter.QueueDepth("test-queue")
	if depth != 1 {
		t.Errorf("Expected queue depth 1, got %d", depth)
	}
}

func TestAdapterAck(t *testing.T) {
	adapter := NewAdapter(100)
	ctx := context.Background()

	msg := &pb.Message{MessageId: "msg-1"}
	adapter.Publish(ctx, "test-queue", msg)

	result, _ := adapter.Consume(ctx, "test-queue", 10)
	receipt := result.Items[0].Receipt

	// Ack the message
	err := receipt.Ack(ctx)
	if err != nil {
		t.Fatalf("Ack failed: %v", err)
	}

	// Verify queue is still empty (message was consumed)
	depth := adapter.QueueDepth("test-queue")
	if depth != 0 {
		t.Errorf("Expected queue depth 0 after ack, got %d", depth)
	}

	// Double ack should fail
	err = receipt.Ack(ctx)
	if err == nil {
		t.Error("Expected error on double ack")
	}
}

func TestAdapterNack(t *testing.T) {
	adapter := NewAdapter(100)
	ctx := context.Background()

	msg := &pb.Message{MessageId: "msg-1"}
	adapter.Publish(ctx, "test-queue", msg)

	result, _ := adapter.Consume(ctx, "test-queue", 10)
	receipt := result.Items[0].Receipt

	// Queue should be empty after consume
	depth := adapter.QueueDepth("test-queue")
	if depth != 0 {
		t.Errorf("Expected queue depth 0 after consume, got %d", depth)
	}

	// Nack the message (requeue)
	err := receipt.Nack(ctx)
	if err != nil {
		t.Fatalf("Nack failed: %v", err)
	}

	// Message should be back in queue
	depth = adapter.QueueDepth("test-queue")
	if depth != 1 {
		t.Errorf("Expected queue depth 1 after nack, got %d", depth)
	}

	// Double nack should fail
	err = receipt.Nack(ctx)
	if err == nil {
		t.Error("Expected error on double nack")
	}
}

func TestAdapterClear(t *testing.T) {
	adapter := NewAdapter(100)
	ctx := context.Background()

	// Publish to multiple queues
	adapter.Publish(ctx, "queue1", &pb.Message{MessageId: "msg-1"})
	adapter.Publish(ctx, "queue2", &pb.Message{MessageId: "msg-2"})

	if adapter.QueueDepth("queue1") != 1 || adapter.QueueDepth("queue2") != 1 {
		t.Error("Expected both queues to have depth 1")
	}

	// Clear all queues
	adapter.Clear()

	if adapter.QueueDepth("queue1") != 0 || adapter.QueueDepth("queue2") != 0 {
		t.Error("Expected both queues to be empty after clear")
	}
}

func TestAdapterEmptyConsume(t *testing.T) {
	adapter := NewAdapter(100)
	ctx := context.Background()

	// Consume from empty queue
	result, err := adapter.Consume(ctx, "empty-queue", 10)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}

	if len(result.Items) != 0 {
		t.Errorf("Expected 0 messages from empty queue, got %d", len(result.Items))
	}
}

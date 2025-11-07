package core

import (
	"context"
	"testing"

	pb "github.com/pbdeuchler/grpcq/go/proto"
)

// mockAdapter is a simple mock for testing
type mockAdapter struct {
	published []*pb.Message
}

func (m *mockAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	m.published = append(m.published, messages...)
	return nil
}

func (m *mockAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	return &ConsumeResult{}, nil
}

func TestProducerSend(t *testing.T) {
	adapter := &mockAdapter{}
	producer := NewProducer(adapter, "test-producer")

	// Use pb.Message itself as a proto message for testing
	msg := &pb.Message{Topic: "inner.test", Action: "inner.action"}
	metadata := map[string]string{"key": "value"}

	err := producer.Send(
		context.Background(),
		"test-queue",
		"test.Service",
		"TestAction",
		msg,
		metadata,
	)

	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if len(adapter.published) != 1 {
		t.Fatalf("Expected 1 published message, got %d", len(adapter.published))
	}

	published := adapter.published[0]

	if published.Originator != "test-producer" {
		t.Errorf("Expected originator 'test-producer', got '%s'", published.Originator)
	}

	if published.Topic != "test.Service" {
		t.Errorf("Expected topic 'test.Service', got '%s'", published.Topic)
	}

	if published.Action != "TestAction" {
		t.Errorf("Expected action 'TestAction', got '%s'", published.Action)
	}

	if published.MessageId == "" {
		t.Error("Expected non-empty message ID")
	}

	if published.TimestampMs == 0 {
		t.Error("Expected non-zero timestamp")
	}

	if published.Metadata["key"] != "value" {
		t.Error("Expected metadata to be preserved")
	}
}

func TestProducerSendBatch(t *testing.T) {
	adapter := &mockAdapter{}
	producer := NewProducer(adapter, "test-producer")

	specs := []MessageSpec{
		{
			Topic:        "test.Service",
			Action:       "Action1",
			ProtoMessage: &pb.Message{Topic: "inner1", Action: "action1"},
			Metadata:     map[string]string{"id": "1"},
		},
		{
			Topic:        "test.Service",
			Action:       "Action2",
			ProtoMessage: &pb.Message{Topic: "inner2", Action: "action2"},
			Metadata:     map[string]string{"id": "2"},
		},
	}

	err := producer.SendBatch(context.Background(), "test-queue", specs)
	if err != nil {
		t.Fatalf("SendBatch failed: %v", err)
	}

	if len(adapter.published) != 2 {
		t.Fatalf("Expected 2 published messages, got %d", len(adapter.published))
	}

	// Verify first message
	if adapter.published[0].Action != "Action1" {
		t.Errorf("Expected first action 'Action1', got '%s'", adapter.published[0].Action)
	}

	if adapter.published[0].Metadata["id"] != "1" {
		t.Error("Expected first message metadata id=1")
	}

	// Verify second message
	if adapter.published[1].Action != "Action2" {
		t.Errorf("Expected second action 'Action2', got '%s'", adapter.published[1].Action)
	}

	if adapter.published[1].Metadata["id"] != "2" {
		t.Error("Expected second message metadata id=2")
	}
}

func TestProducerSendMetadataIsCloned(t *testing.T) {
	adapter := &mockAdapter{}
	producer := NewProducer(adapter, "origin")

	metadata := map[string]string{"key": "value"}
	err := producer.Send(
		context.Background(),
		"test-queue",
		"test.Service",
		"Action",
		&pb.Message{},
		metadata,
	)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if len(adapter.published) != 1 {
		t.Fatalf("Expected 1 published message, got %d", len(adapter.published))
	}

	metadata["key"] = "mutated"
	if adapter.published[0].Metadata["key"] != "value" {
		t.Fatalf("Expected metadata to be cloned, got %s", adapter.published[0].Metadata["key"])
	}
}

func TestProducerSendBatchMetadataIsCloned(t *testing.T) {
	adapter := &mockAdapter{}
	producer := NewProducer(adapter, "origin")

	specs := []MessageSpec{
		{
			Topic:        "svc",
			Action:       "A",
			ProtoMessage: &pb.Message{},
			Metadata:     map[string]string{"key": "value"},
		},
	}

	if err := producer.SendBatch(context.Background(), "queue", specs); err != nil {
		t.Fatalf("SendBatch failed: %v", err)
	}

	if len(adapter.published) != 1 {
		t.Fatalf("Expected 1 published message, got %d", len(adapter.published))
	}

	specs[0].Metadata["key"] = "mutated"
	if adapter.published[0].Metadata["key"] != "value" {
		t.Fatalf("Expected metadata to be cloned, got %s", adapter.published[0].Metadata["key"])
	}
}

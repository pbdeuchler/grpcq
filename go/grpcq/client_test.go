package grpcq

import (
	"context"
	"testing"

	"github.com/pbdeuchler/grpcq/go/core"
	pb "github.com/pbdeuchler/grpcq/go/proto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type clientTestAdapter struct {
	queueName string
	messages  []*pb.Message
}

func (a *clientTestAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	a.queueName = queueName
	a.messages = append(a.messages, messages...)
	return nil
}

func (a *clientTestAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*core.ConsumeResult, error) {
	return &core.ConsumeResult{}, nil
}

func TestClientInvokePublishesMessage(t *testing.T) {
	adapter := &clientTestAdapter{}
	client := NewClient(
		adapter,
		WithClientQueueName("default-queue"),
		WithOriginator("producer"),
	)

	err := client.Invoke(
		context.Background(),
		"svc.Service",
		"DoThing",
		&emptypb.Empty{},
		WithQueueNameOption("override-queue"),
		WithMetadata(map[string]string{"trace-id": "123"}),
	)
	if err != nil {
		t.Fatalf("Invoke failed: %v", err)
	}

	if adapter.queueName != "override-queue" {
		t.Fatalf("expected queue override to be used, got %q", adapter.queueName)
	}

	if len(adapter.messages) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(adapter.messages))
	}

	msg := adapter.messages[0]
	if msg.Originator != "producer" {
		t.Fatalf("unexpected originator: %q", msg.Originator)
	}
	if msg.Topic != "svc.Service" || msg.Action != "DoThing" {
		t.Fatalf("unexpected routing: topic=%q action=%q", msg.Topic, msg.Action)
	}
	if msg.Metadata["trace-id"] != "123" {
		t.Fatalf("unexpected metadata: %+v", msg.Metadata)
	}

	payload := &emptypb.Empty{}
	if err := proto.Unmarshal(msg.Payload, payload); err != nil {
		t.Fatalf("failed to unmarshal published payload: %v", err)
	}
}

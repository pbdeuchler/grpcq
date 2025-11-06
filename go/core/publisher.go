package core

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	pb "github.com/pbdeuchler/grpcq/proto/grpcq"
	"google.golang.org/protobuf/proto"
)

// Publisher provides an API for publishing messages to a queue.
type Publisher struct {
	adapter    QueueAdapter
	originator string
}

// NewPublisher creates a new Publisher that uses the given adapter.
// originator identifies the sender (typically service name or instance ID).
func NewPublisher(adapter QueueAdapter, originator string) *Publisher {
	return &Publisher{
		adapter:    adapter,
		originator: originator,
	}
}

// Send publishes a message to the specified queue.
// It handles message ID generation, timestamping, and payload serialization.
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - queueName: The name of the queue to publish to
//   - topic: The gRPC service name (e.g., "user.UserService")
//   - action: The gRPC method name (e.g., "CreateUser")
//   - protoMessage: The request proto to serialize as the payload
//   - metadata: Optional key-value pairs for tracing, correlation, etc.
func (p *Publisher) Send(
	ctx context.Context,
	queueName string,
	topic string,
	action string,
	protoMessage proto.Message,
	metadata map[string]string,
) error {
	// Serialize the proto message
	payload, err := proto.Marshal(protoMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal proto message: %w", err)
	}

	// Create the message envelope
	msg := &pb.Message{
		Originator:  p.originator,
		Topic:       topic,
		Action:      action,
		Payload:     payload,
		MessageId:   uuid.New().String(),
		TimestampMs: time.Now().UnixMilli(),
		Metadata:    metadata,
	}

	// Publish to the queue
	if err := p.adapter.Publish(ctx, queueName, msg); err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}

	return nil
}

// SendBatch publishes multiple messages to the specified queue in a single operation.
// All messages must be published successfully or an error is returned.
//
// Each message is represented by a MessageSpec that contains the topic, action,
// proto message, and optional metadata.
func (p *Publisher) SendBatch(
	ctx context.Context,
	queueName string,
	specs []MessageSpec,
) error {
	messages := make([]*pb.Message, len(specs))

	for i, spec := range specs {
		// Serialize the proto message
		payload, err := proto.Marshal(spec.ProtoMessage)
		if err != nil {
			return fmt.Errorf("failed to marshal message %d: %w", i, err)
		}

		// Create the message envelope
		messages[i] = &pb.Message{
			Originator:  p.originator,
			Topic:       spec.Topic,
			Action:      spec.Action,
			Payload:     payload,
			MessageId:   uuid.New().String(),
			TimestampMs: time.Now().UnixMilli(),
			Metadata:    spec.Metadata,
		}
	}

	// Publish all messages
	if err := p.adapter.Publish(ctx, queueName, messages...); err != nil {
		return fmt.Errorf("failed to publish batch: %w", err)
	}

	return nil
}

// MessageSpec specifies a message to be published.
type MessageSpec struct {
	Topic        string
	Action       string
	ProtoMessage proto.Message
	Metadata     map[string]string
}

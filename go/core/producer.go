package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/pbdeuchler/grpcq/go/proto"
	"google.golang.org/protobuf/proto"
)

const (
	// MaxMessageSize is the maximum size of a serialized message (256KB for SQS compatibility)
	MaxMessageSize = 256 * 1024
)

// Producer provides an API for producing messages to a queue.
type Producer struct {
	adapter    QueueAdapter
	originator string
}

// NewProducer creates a new Producer that uses the given adapter.
// originator identifies the sender (typically service name or instance ID).
func NewProducer(adapter QueueAdapter, originator string) *Producer {
	return &Producer{
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
func (p *Producer) Send(
	ctx context.Context,
	queueName string,
	topic string,
	action string,
	protoMessage proto.Message,
	metadata map[string]string,
) error {
	if err := validateQueueName(queueName); err != nil {
		return err
	}
	if err := validateTopicAction(topic, action); err != nil {
		return err
	}

	payload, err := proto.Marshal(protoMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal payload for %s.%s: %w", topic, action, err)
	}

	if len(payload) > MaxMessageSize {
		return fmt.Errorf("payload for %s.%s exceeds %d bytes (got %d)",
			topic, action, MaxMessageSize, len(payload))
	}

	clonedMetadata := make(map[string]string, len(metadata))
	for k, v := range metadata {
		clonedMetadata[k] = v
	}

	msg := &pb.Message{
		Originator:  p.originator,
		Topic:       topic,
		Action:      action,
		Payload:     payload,
		MessageId:   uuid.New().String(),
		TimestampMs: time.Now().UnixMilli(),
		Metadata:    clonedMetadata,
	}

	return p.adapter.Publish(ctx, queueName, msg)
}

// SendBatch publishes multiple messages to the specified queue in a single operation.
// All messages must be published successfully or an error is returned.
//
// Each message is represented by a MessageSpec that contains the topic, action,
// proto message, and optional metadata.
func (p *Producer) SendBatch(
	ctx context.Context,
	queueName string,
	specs []MessageSpec,
) error {
	if len(specs) == 0 {
		return nil
	}

	// Validate queue name
	if err := validateQueueName(queueName); err != nil {
		return fmt.Errorf("invalid queue name: %w", err)
	}

	messages := make([]*pb.Message, len(specs))

	for i, spec := range specs {
		if err := validateTopicAction(spec.Topic, spec.Action); err != nil {
			return fmt.Errorf("message %d: %w", i, err)
		}

		payload, err := proto.Marshal(spec.ProtoMessage)
		if err != nil {
			return fmt.Errorf("failed to marshal message %d (%s.%s): %w", i, spec.Topic, spec.Action, err)
		}

		if len(payload) > MaxMessageSize {
			return fmt.Errorf("message %d (%s.%s) payload exceeds %d bytes (got %d)",
				i, spec.Topic, spec.Action, MaxMessageSize, len(payload))
		}

		clonedMetadata := make(map[string]string, len(spec.Metadata))
		for k, v := range spec.Metadata {
			clonedMetadata[k] = v
		}

		messages[i] = &pb.Message{
			Originator:  p.originator,
			Topic:       spec.Topic,
			Action:      spec.Action,
			Payload:     payload,
			MessageId:   uuid.New().String(),
			TimestampMs: time.Now().UnixMilli(),
			Metadata:    clonedMetadata,
		}
	}

	return p.adapter.Publish(ctx, queueName, messages...)
}

// MessageSpec specifies a message to be published.
type MessageSpec struct {
	Topic        string
	Action       string
	ProtoMessage proto.Message
	Metadata     map[string]string
}

// validateQueueName validates that a queue name is not empty and contains valid characters
func validateQueueName(queueName string) error {
	if queueName == "" {
		return fmt.Errorf("queue name cannot be empty")
	}
	if strings.TrimSpace(queueName) != queueName {
		return fmt.Errorf("queue name cannot have leading or trailing whitespace")
	}
	// Allow alphanumeric, hyphens, underscores, and dots (common for queue names)
	for _, ch := range queueName {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.') {
			return fmt.Errorf("queue name contains invalid character: %c", ch)
		}
	}
	return nil
}

// validateTopicAction validates that topic and action are not empty
func validateTopicAction(topic, action string) error {
	if topic == "" {
		return fmt.Errorf("topic cannot be empty")
	}
	if action == "" {
		return fmt.Errorf("action cannot be empty")
	}
	if strings.TrimSpace(topic) != topic {
		return fmt.Errorf("topic cannot have leading or trailing whitespace")
	}
	if strings.TrimSpace(action) != action {
		return fmt.Errorf("action cannot have leading or trailing whitespace")
	}
	return nil
}

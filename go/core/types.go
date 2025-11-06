// Package core provides the core abstractions and interfaces for grpcq.
// It defines the queue adapter interface, message handling, and worker/publisher APIs.
package core

import (
	"context"

	pb "github.com/pbdeuchler/grpcq/go/proto"
)

// QueueAdapter is the interface that queue implementations must satisfy.
// It provides methods for publishing messages to queues and consuming messages from them.
type QueueAdapter interface {
	// Publish sends one or more messages to the specified queue.
	// All messages must be published successfully or an error is returned.
	// Implementations should batch messages for efficiency when possible.
	Publish(ctx context.Context, queueName string, messages ...*pb.Message) error

	// Consume retrieves a batch of messages from the specified queue.
	// maxBatch specifies the maximum number of messages to retrieve.
	// Returns a ConsumeResult containing the messages and their receipts,
	// or an error if the operation fails.
	Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error)
}

// ConsumeResult contains the result of a Consume operation.
type ConsumeResult struct {
	// Items contains the messages and their associated receipts
	Items []MessageItem
}

// MessageItem pairs a message with its receipt for acknowledgment.
type MessageItem struct {
	// Message is the deserialized message from the queue
	Message *pb.Message

	// Receipt is used to acknowledge or reject the message
	Receipt Receipt
}

// Receipt represents a handle to a message that has been consumed from a queue.
// It provides methods to acknowledge successful processing or reject the message.
type Receipt interface {
	// Ack acknowledges successful processing of the message.
	// The message will be removed from the queue.
	Ack(ctx context.Context) error

	// Nack indicates that processing failed and the message should be requeued.
	// The message will be made available for retry according to the queue's policy.
	Nack(ctx context.Context) error
}

// Handler is a function that processes a message.
// It receives the context and the message to process.
// Returning an error will cause the message to be nacked.
type Handler func(ctx context.Context, msg *pb.Message) error

// WorkerConfig contains configuration for a Worker.
type WorkerConfig struct {
	// QueueName is the name of the queue to consume from
	QueueName string

	// MaxBatch is the maximum number of messages to fetch in one consume operation
	// Default: 10
	MaxBatch int

	// Concurrency is the maximum number of messages to process concurrently
	// Default: 10
	Concurrency int

	// PollIntervalMs is how long to wait between poll attempts when no messages are available
	// Default: 1000 (1 second)
	PollIntervalMs int
}

// DefaultWorkerConfig returns a WorkerConfig with sensible defaults.
func DefaultWorkerConfig(queueName string) WorkerConfig {
	return WorkerConfig{
		QueueName:      queueName,
		MaxBatch:       10,
		Concurrency:    10,
		PollIntervalMs: 1000,
	}
}

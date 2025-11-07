// Package memory provides an in-memory queue adapter for testing and local development.
package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/pbdeuchler/grpcq/go/core"
	pb "github.com/pbdeuchler/grpcq/go/proto"
)

// Adapter is an in-memory implementation of the QueueAdapter interface.
// It uses Go channels to simulate queue behavior.
// This adapter is NOT suitable for production use - it's designed for testing only.
type Adapter struct {
	mu         sync.RWMutex
	queues     map[string]chan *messageWithReceipt
	bufferSize int
}

// messageWithReceipt pairs a message with its receipt for in-memory handling.
type messageWithReceipt struct {
	message *pb.Message
	receipt *memoryReceipt
}

// memoryReceipt implements the Receipt interface for in-memory messages.
type memoryReceipt struct {
	msg      *pb.Message
	acked    bool
	nacked   bool
	mu       sync.Mutex
	requeueF func(*pb.Message)
}

// Ack marks the message as successfully processed.
func (r *memoryReceipt) Ack(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.acked {
		return fmt.Errorf("message already acknowledged")
	}
	if r.nacked {
		return fmt.Errorf("message already nacked")
	}

	r.acked = true
	return nil
}

// Nack marks the message as failed and requeues it.
func (r *memoryReceipt) Nack(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.acked {
		return fmt.Errorf("message already acknowledged")
	}
	if r.nacked {
		return fmt.Errorf("message already nacked")
	}

	r.nacked = true
	if r.requeueF != nil {
		r.requeueF(r.msg)
	}
	return nil
}

// NewAdapter creates a new in-memory queue adapter.
// bufferSize determines the capacity of each queue's channel.
func NewAdapter(bufferSize int) *Adapter {
	if bufferSize <= 0 {
		bufferSize = 1000 // Default buffer size
	}
	return &Adapter{
		queues:     make(map[string]chan *messageWithReceipt),
		bufferSize: bufferSize,
	}
}

// Publish sends messages to the specified queue.
func (a *Adapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Ensure queue exists
	if a.queues[queueName] == nil {
		// Create a buffered channel with configured capacity
		a.queues[queueName] = make(chan *messageWithReceipt, a.bufferSize)
	}

	queue := a.queues[queueName]

	// Publish all messages
	for _, msg := range messages {
		select {
		case queue <- &messageWithReceipt{message: msg}:
			// Message published successfully
		case <-ctx.Done():
			return ctx.Err()
		default:
			return fmt.Errorf("queue %s is full", queueName)
		}
	}

	return nil
}

// Consume retrieves messages from the specified queue.
func (a *Adapter) Consume(ctx context.Context, queueName string, maxBatch int) (*core.ConsumeResult, error) {
	a.mu.Lock()
	queue := a.queues[queueName]
	if queue == nil {
		// Create empty queue if it doesn't exist
		a.queues[queueName] = make(chan *messageWithReceipt, a.bufferSize)
		queue = a.queues[queueName]
	}
	a.mu.Unlock()

	items := make([]core.MessageItem, 0, maxBatch)

	// Try to consume up to maxBatch messages without blocking
	for i := 0; i < maxBatch; i++ {
		select {
		case msgWithReceipt := <-queue:
			// Create receipt with requeue function
			receipt := &memoryReceipt{
				msg: msgWithReceipt.message,
				requeueF: func(msg *pb.Message) {
					// Requeue the message (best effort)
					select {
					case queue <- &messageWithReceipt{message: msg}:
					default:
						// Queue full, message lost (acceptable for testing)
					}
				},
			}

			items = append(items, core.MessageItem{
				Message: msgWithReceipt.message,
				Receipt: receipt,
			})
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			// No more messages available
			return &core.ConsumeResult{Items: items}, nil
		}
	}

	return &core.ConsumeResult{Items: items}, nil
}

// QueueDepth returns the number of messages currently in the queue.
// This is useful for testing.
func (a *Adapter) QueueDepth(queueName string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()

	queue := a.queues[queueName]
	if queue == nil {
		return 0
	}

	return len(queue)
}

// Clear removes all messages from all queues.
// This is useful for testing.
func (a *Adapter) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()

	for name, queue := range a.queues {
		// Drain the channel
		for len(queue) > 0 {
			<-queue
		}
		// Recreate the channel
		a.queues[name] = make(chan *messageWithReceipt, a.bufferSize)
	}
}

// Ensure Adapter implements QueueAdapter
var _ core.QueueAdapter = (*Adapter)(nil)

// Package memory provides an in-memory queue adapter for testing and local development.
package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/pbdeuchler/grpcq/go/core"
	pb "github.com/pbdeuchler/grpcq/go/proto"
)

// Adapter is an in-memory queue for testing and local development.
// Capacity includes both queued and in-flight messages until acknowledgement.
type Adapter struct {
	mu         sync.RWMutex
	queues     map[string]*queue
	bufferSize int
}

type queue struct {
	messages []*pb.Message
	pending  int
}

type receiptState uint8

const (
	pending receiptState = iota
	acked
	nacked
)

type memoryReceipt struct {
	adapter *Adapter
	queue   *queue
	message *pb.Message
	state   receiptState // protected by adapter.mu
}

func (r *memoryReceipt) Ack(ctx context.Context) error {
	return r.settle(ctx, acked)
}

func (r *memoryReceipt) Nack(ctx context.Context) error {
	return r.settle(ctx, nacked)
}

func (r *memoryReceipt) settle(ctx context.Context, state receiptState) error {
	r.adapter.mu.Lock()
	defer r.adapter.mu.Unlock()

	switch r.state {
	case acked:
		return fmt.Errorf("message already acknowledged")
	case nacked:
		return fmt.Errorf("message already nacked")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if state == acked {
		r.queue.pending--
	} else {
		r.queue.messages = append(r.queue.messages, r.message)
	}
	r.state = state
	return nil
}

// NewAdapter creates a queue with space for bufferSize unacknowledged messages
// per queue. Non-positive sizes use the default capacity of 1000.
func NewAdapter(bufferSize int) *Adapter {
	if bufferSize <= 0 {
		bufferSize = 1000
	}
	return &Adapter{queues: make(map[string]*queue), bufferSize: bufferSize}
}

// Publish accepts the entire batch or returns an error without accepting any.
func (a *Adapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	if len(messages) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	q := a.getOrCreateQueue(queueName)
	if len(messages) > a.bufferSize-q.pending {
		return fmt.Errorf("queue %s is full", queueName)
	}
	q.messages = append(q.messages, messages...)
	q.pending += len(messages)
	return nil
}

// Consume retrieves messages without releasing their capacity reservations.
func (a *Adapter) Consume(ctx context.Context, queueName string, maxBatch int) (*core.ConsumeResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxBatch < 1 {
		maxBatch = 1
	}
	q := a.getOrCreateQueue(queueName)
	count := min(maxBatch, len(q.messages))
	items := make([]core.MessageItem, 0, count)
	for _, message := range q.messages[:count] {
		items = append(items, core.MessageItem{
			Message: message,
			Receipt: &memoryReceipt{adapter: a, queue: q, message: message},
		})
	}
	clear(q.messages[:count])
	q.messages = q.messages[count:]
	return &core.ConsumeResult{Items: items}, nil
}

// QueueDepth returns the number of messages available for consumption.
func (a *Adapter) QueueDepth(queueName string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if q := a.queues[queueName]; q != nil {
		return len(q.messages)
	}
	return 0
}

// Clear discards queued messages. In-flight messages retain their reservations
// and can still be acknowledged or returned to their queues.
func (a *Adapter) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, q := range a.queues {
		q.pending -= len(q.messages)
		q.messages = nil
	}
}

// getOrCreateQueue requires a.mu to be held for writing.
func (a *Adapter) getOrCreateQueue(name string) *queue {
	q := a.queues[name]
	if q == nil {
		q = &queue{}
		a.queues[name] = q
	}
	return q
}

var _ core.QueueAdapter = (*Adapter)(nil)

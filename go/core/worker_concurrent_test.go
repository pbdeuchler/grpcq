package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/pbdeuchler/grpcq/go/proto"
)

func TestWorkerConcurrentStop(t *testing.T) {
	adapter := &infiniteAdapter{
		stopCh: make(chan struct{}),
	}
	registry := NewRegistry()
	registry.Register("test", "action", func(ctx context.Context, msg *pb.Message) error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})

	config := WorkerConfig{
		QueueName:      "test-queue",
		Concurrency:    5,
		MaxBatch:       1,
		PollIntervalMs: 10,
	}

	worker := NewWorker(adapter, registry, config)

	// Start worker in background
	ctx := context.Background()
	go worker.Start(ctx)

	// Give worker time to start processing
	time.Sleep(50 * time.Millisecond)

	// Call Stop from multiple goroutines concurrently
	numStoppers := 10
	var wg sync.WaitGroup
	wg.Add(numStoppers)

	stopErrors := make([]error, numStoppers)
	for i := 0; i < numStoppers; i++ {
		go func(idx int) {
			defer wg.Done()
			stopCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			stopErrors[idx] = worker.Stop(stopCtx)
		}(i)
	}

	wg.Wait()
	close(adapter.stopCh)

	// All Stop calls should return the same error (or nil)
	firstErr := stopErrors[0]
	for i, err := range stopErrors {
		if err != firstErr {
			t.Errorf("Stop call %d returned different error: got %v, want %v", i, err, firstErr)
		}
	}
}

// infiniteAdapter provides an infinite stream of messages for testing
type infiniteAdapter struct {
	stopCh chan struct{}
	count  int64
}

func (a *infiniteAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	return nil
}

func (a *infiniteAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	select {
	case <-a.stopCh:
		return &ConsumeResult{Items: []MessageItem{}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		// Generate a message
		msgID := atomic.AddInt64(&a.count, 1)
		return &ConsumeResult{
			Items: []MessageItem{
				{
					Message: &pb.Message{
						MessageId: string(rune(msgID)),
						Topic:     "test",
						Action:    "action",
					},
					Receipt: &mockReceipt{},
				},
			},
		}, nil
	}
}

type mockReceipt struct{}

func (r *mockReceipt) Ack(ctx context.Context) error   { return nil }
func (r *mockReceipt) Nack(ctx context.Context) error  { return nil }

func TestWorkerPoolConcurrentStart(t *testing.T) {
	adapter := &controlledAdapter{
		messages: make(chan *pb.Message, 100),
	}

	registry := NewRegistry()
	processed := int64(0)
	registry.Register("test", "action", func(ctx context.Context, msg *pb.Message) error {
		atomic.AddInt64(&processed, 1)
		return nil
	})

	config := WorkerConfig{
		QueueName:      "test-queue",
		Concurrency:    5,
		MaxBatch:       5,
		PollIntervalMs: 10,
	}

	pool := NewWorkerPool(adapter, registry, config, 3)

	// Send some messages
	numMessages := 50
	for i := 0; i < numMessages; i++ {
		adapter.messages <- &pb.Message{
			MessageId: string(rune(i)),
			Topic:     "test",
			Action:    "action",
		}
	}

	// Start the pool
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- pool.Start(ctx)
	}()

	// Wait a bit for processing
	time.Sleep(200 * time.Millisecond)

	// Stop the pool
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopCancel()

	if err := pool.Stop(stopCtx); err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		t.Fatalf("Failed to stop pool: %v", err)
	}

	// Check that messages were processed
	processedCount := atomic.LoadInt64(&processed)
	if processedCount == 0 {
		t.Fatal("No messages were processed")
	}
	t.Logf("Processed %d messages", processedCount)
}

// controlledAdapter allows precise control over message flow
type controlledAdapter struct {
	messages chan *pb.Message
	mu       sync.Mutex
}

func (a *controlledAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	return nil
}

func (a *controlledAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	items := []MessageItem{}

	// Try to get up to maxBatch messages
	for i := 0; i < maxBatch; i++ {
		select {
		case msg := <-a.messages:
			items = append(items, MessageItem{
				Message: msg,
				Receipt: &mockReceipt{},
			})
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			// No more messages available
			if len(items) == 0 {
				// If we got no messages, wait a bit
				time.Sleep(10 * time.Millisecond)
			}
			return &ConsumeResult{Items: items}, nil
		}
	}

	return &ConsumeResult{Items: items}, nil
}

func TestWorkerPoolErrorPropagation(t *testing.T) {
	adapter := &errorAdapter{
		errorAfter: 3,
	}

	registry := NewRegistry()
	registry.Register("test", "action", func(ctx context.Context, msg *pb.Message) error {
		return nil
	})

	config := WorkerConfig{
		QueueName:      "test-queue",
		Concurrency:    2,
		MaxBatch:       1,
		PollIntervalMs: 10,
	}

	pool := NewWorkerPool(adapter, registry, config, 2)

	ctx := context.Background()
	err := pool.Start(ctx)

	if err == nil {
		t.Fatal("Expected error from pool, got nil")
	}

	if err.Error() != "consume error after 3 calls" {
		t.Fatalf("Expected specific error, got: %v", err)
	}
}

// errorAdapter returns an error after a certain number of consume calls
type errorAdapter struct {
	count      int
	errorAfter int
	mu         sync.Mutex
}

func (a *errorAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	return nil
}

func (a *errorAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.count++
	if a.count > a.errorAfter {
		return nil, errors.New("consume error after 3 calls")
	}

	return &ConsumeResult{
		Items: []MessageItem{
			{
				Message: &pb.Message{
					MessageId: string(rune(a.count)),
					Topic:     "test",
					Action:    "action",
				},
				Receipt: &mockReceipt{},
			},
		},
	}, nil
}

func TestWorkerConcurrentMessageProcessing(t *testing.T) {
	adapter := &burstAdapter{
		messages: make([]*pb.Message, 100),
	}

	// Generate messages
	for i := 0; i < 100; i++ {
		adapter.messages[i] = &pb.Message{
			MessageId: string(rune(i)),
			Topic:     "test",
			Action:    "action",
			Payload:   []byte{byte(i)},
		}
	}

	registry := NewRegistry()

	// Track which messages were processed and how many times
	processedMap := sync.Map{}
	registry.Register("test", "action", func(ctx context.Context, msg *pb.Message) error {
		// Simulate some work
		time.Sleep(5 * time.Millisecond)

		// Record that we processed this message
		count := 0
		if val, ok := processedMap.Load(msg.MessageId); ok {
			count = val.(int)
		}
		processedMap.Store(msg.MessageId, count+1)

		return nil
	})

	config := WorkerConfig{
		QueueName:      "test-queue",
		Concurrency:    10, // High concurrency to test race conditions
		MaxBatch:       10,
		PollIntervalMs: 10,
	}

	worker := NewWorker(adapter, registry, config)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if err := worker.Start(ctx); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("Worker failed: %v", err)
	}

	// Check that each message was processed exactly once
	processedCount := 0
	processedMap.Range(func(key, value interface{}) bool {
		processedCount++
		if count := value.(int); count != 1 {
			t.Errorf("Message %v was processed %d times, expected 1", key, count)
		}
		return true
	})

	if processedCount != 100 {
		t.Errorf("Processed %d messages, expected 100", processedCount)
	}
}

// burstAdapter returns all messages at once, then nothing
type burstAdapter struct {
	messages  []*pb.Message
	consumed  bool
	mu        sync.Mutex
}

func (a *burstAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	return nil
}

func (a *burstAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.consumed || len(a.messages) == 0 {
		// No more messages
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return &ConsumeResult{Items: []MessageItem{}}, nil
		}
	}

	// Return up to maxBatch messages
	numToReturn := maxBatch
	if numToReturn > len(a.messages) {
		numToReturn = len(a.messages)
	}

	items := make([]MessageItem, numToReturn)
	for i := 0; i < numToReturn; i++ {
		items[i] = MessageItem{
			Message: a.messages[i],
			Receipt: &trackingReceipt{
				id: a.messages[i].MessageId,
			},
		}
	}

	// Remove consumed messages
	a.messages = a.messages[numToReturn:]
	if len(a.messages) == 0 {
		a.consumed = true
	}

	return &ConsumeResult{Items: items}, nil
}

type trackingReceipt struct {
	id     string
	acked  bool
	nacked bool
	mu     sync.Mutex
}

func (r *trackingReceipt) Ack(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acked = true
	return nil
}

func (r *trackingReceipt) Nack(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nacked = true
	return nil
}
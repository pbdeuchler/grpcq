package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/pbdeuchler/grpcq/go/proto"
)

func TestProducerValidation(t *testing.T) {
	adapter := &mockAdapter{}
	producer := NewProducer(adapter, "test-producer")

	tests := []struct {
		name      string
		queueName string
		topic     string
		action    string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "valid inputs",
			queueName: "valid-queue",
			topic:     "test.Service",
			action:    "TestAction",
			wantErr:   false,
		},
		{
			name:      "empty queue name",
			queueName: "",
			topic:     "test.Service",
			action:    "TestAction",
			wantErr:   true,
			errMsg:    "queue name cannot be empty",
		},
		{
			name:      "queue name with spaces",
			queueName: " queue-name ",
			topic:     "test.Service",
			action:    "TestAction",
			wantErr:   true,
			errMsg:    "leading or trailing whitespace",
		},
		{
			name:      "queue name with invalid chars",
			queueName: "queue@name",
			topic:     "test.Service",
			action:    "TestAction",
			wantErr:   true,
			errMsg:    "invalid character",
		},
		{
			name:      "empty topic",
			queueName: "valid-queue",
			topic:     "",
			action:    "TestAction",
			wantErr:   true,
			errMsg:    "topic cannot be empty",
		},
		{
			name:      "empty action",
			queueName: "valid-queue",
			topic:     "test.Service",
			action:    "",
			wantErr:   true,
			errMsg:    "action cannot be empty",
		},
		{
			name:      "topic with spaces",
			queueName: "valid-queue",
			topic:     " test.Service ",
			action:    "TestAction",
			wantErr:   true,
			errMsg:    "leading or trailing whitespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := producer.Send(
				context.Background(),
				tt.queueName,
				tt.topic,
				tt.action,
				&pb.Message{},
				nil,
			)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Expected error containing '%s', got nil", tt.errMsg)
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("Expected error containing '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestProducerMessageSizeValidation(t *testing.T) {
	adapter := &mockAdapter{}
	producer := NewProducer(adapter, "test-producer")

	// Create a message that will exceed the size limit when serialized
	largeData := make([]byte, MaxMessageSize+1)
	largeMsg := &pb.Message{
		Payload: largeData,
	}

	err := producer.Send(
		context.Background(),
		"valid-queue",
		"test.Service",
		"TestAction",
		largeMsg,
		nil,
	)

	if err == nil {
		t.Fatal("Expected error for oversized message, got nil")
	}

	if !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("Expected error about maximum size, got: %v", err)
	}
}

func TestProducerConcurrentSends(t *testing.T) {
	adapter := &threadSafeAdapter{
		messages: make([]*pb.Message, 0),
	}
	producer := NewProducer(adapter, "test-producer")

	numGoroutines := 100
	messagesPerGoroutine := 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerGoroutine; j++ {
				err := producer.Send(
					context.Background(),
					"test-queue",
					"test.Service",
					"TestAction",
					&pb.Message{Topic: "test"},
					map[string]string{"goroutine": string(rune(id))},
				)
				if err != nil {
					t.Errorf("Send failed: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	expectedMessages := numGoroutines * messagesPerGoroutine
	if len(adapter.messages) != expectedMessages {
		t.Fatalf("Expected %d messages, got %d", expectedMessages, len(adapter.messages))
	}

	// Verify all messages have unique IDs
	messageIDs := make(map[string]bool)
	for _, msg := range adapter.messages {
		if messageIDs[msg.MessageId] {
			t.Fatalf("Duplicate message ID found: %s", msg.MessageId)
		}
		messageIDs[msg.MessageId] = true
	}
}

// threadSafeAdapter is a thread-safe mock adapter for concurrent testing
type threadSafeAdapter struct {
	mu       sync.Mutex
	messages []*pb.Message
}

func (a *threadSafeAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append(a.messages, messages...)
	return nil
}

func (a *threadSafeAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	return &ConsumeResult{}, nil
}

func TestProducerAdapterFailure(t *testing.T) {
	adapter := &failingAdapter{
		failAfter: 2,
	}
	producer := NewProducer(adapter, "test-producer")

	// First two sends should succeed
	for i := 0; i < 2; i++ {
		err := producer.Send(
			context.Background(),
			"test-queue",
			"test.Service",
			"TestAction",
			&pb.Message{},
			nil,
		)
		if err != nil {
			t.Fatalf("Send %d should have succeeded, got error: %v", i+1, err)
		}
	}

	// Third send should fail
	err := producer.Send(
		context.Background(),
		"test-queue",
		"test.Service",
		"TestAction",
		&pb.Message{},
		nil,
	)
	if err == nil {
		t.Fatal("Expected adapter failure, got nil")
	}
	if !strings.Contains(err.Error(), "adapter failure") {
		t.Fatalf("Expected adapter failure error, got: %v", err)
	}
}

// failingAdapter fails after a certain number of publishes
type failingAdapter struct {
	failAfter int
	count     int
	mu        sync.Mutex
}

func (a *failingAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.count++
	if a.count > a.failAfter {
		return errors.New("adapter failure")
	}
	return nil
}

func (a *failingAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	return &ConsumeResult{}, nil
}

func TestProducerContextCancellation(t *testing.T) {
	adapter := &slowAdapter{
		delay: 100 * time.Millisecond,
	}
	producer := NewProducer(adapter, "test-producer")

	ctx, cancel := context.WithCancel(context.Background())

	// Start a send in the background
	errCh := make(chan error, 1)
	go func() {
		errCh <- producer.Send(
			ctx,
			"test-queue",
			"test.Service",
			"TestAction",
			&pb.Message{},
			nil,
		)
	}()

	// Cancel the context quickly
	time.Sleep(10 * time.Millisecond)
	cancel()

	// Check the error
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Expected context cancellation error, got nil")
		}
		// The error should be context.Canceled, but could be wrapped
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("Expected context cancellation error, got: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Send did not return within expected time")
	}
}

// slowAdapter simulates a slow network operation
type slowAdapter struct {
	delay time.Duration
}

func (a *slowAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	select {
	case <-time.After(a.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *slowAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error) {
	return &ConsumeResult{}, nil
}

func TestProducerSendBatchValidation(t *testing.T) {
	adapter := &mockAdapter{}
	producer := NewProducer(adapter, "test-producer")

	specs := []MessageSpec{
		{
			Topic:        "test.Service",
			Action:       "Action1",
			ProtoMessage: &pb.Message{},
		},
		{
			Topic:        "", // Invalid: empty topic
			Action:       "Action2",
			ProtoMessage: &pb.Message{},
		},
	}

	err := producer.SendBatch(context.Background(), "test-queue", specs)
	if err == nil {
		t.Fatal("Expected validation error for empty topic, got nil")
	}
	if !strings.Contains(err.Error(), "topic cannot be empty") {
		t.Fatalf("Expected error about empty topic, got: %v", err)
	}
}

func TestProducerEdgeCases(t *testing.T) {
	adapter := &mockAdapter{}
	producer := NewProducer(adapter, "test-producer")

	t.Run("nil metadata", func(t *testing.T) {
		err := producer.Send(
			context.Background(),
			"test-queue",
			"test.Service",
			"TestAction",
			&pb.Message{},
			nil, // nil metadata should be handled gracefully
		)
		if err != nil {
			t.Fatalf("Failed to handle nil metadata: %v", err)
		}
	})

	t.Run("empty batch", func(t *testing.T) {
		err := producer.SendBatch(context.Background(), "test-queue", []MessageSpec{})
		if err != nil {
			t.Fatalf("Failed to handle empty batch: %v", err)
		}
		if len(adapter.published) != 0 {
			t.Fatal("Expected no messages published for empty batch")
		}
	})

	t.Run("special characters in metadata", func(t *testing.T) {
		metadata := map[string]string{
			"special-chars": "!@#$%^&*(){}[]|\\/\"'<>?,.",
			"unicode":       "你好世界🌍",
			"empty-value":   "",
		}
		err := producer.Send(
			context.Background(),
			"test-queue",
			"test.Service",
			"TestAction",
			&pb.Message{},
			metadata,
		)
		if err != nil {
			t.Fatalf("Failed to handle special characters in metadata: %v", err)
		}

		// Verify metadata was preserved correctly
		published := adapter.published[len(adapter.published)-1]
		for key, value := range metadata {
			if published.Metadata[key] != value {
				t.Errorf("Metadata mismatch for key '%s': expected '%s', got '%s'",
					key, value, published.Metadata[key])
			}
		}
	})
}
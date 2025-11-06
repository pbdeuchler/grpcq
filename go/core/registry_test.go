package core

import (
	"context"
	"errors"
	"testing"

	pb "github.com/pbdeuchler/grpcq/proto/grpcq"
)

func TestRegistryRegisterAndHandle(t *testing.T) {
	registry := NewRegistry()

	called := false
	handler := func(ctx context.Context, msg *pb.Message) error {
		called = true
		return nil
	}

	// Register handler
	registry.Register("test.Service", "TestAction", handler)

	// Verify registration
	if !registry.IsRegistered("test.Service", "TestAction") {
		t.Error("Expected handler to be registered")
	}

	// Handle message
	msg := &pb.Message{
		Topic:  "test.Service",
		Action: "TestAction",
	}

	if err := registry.Handle(context.Background(), msg); err != nil {
		t.Errorf("Handle failed: %v", err)
	}

	if !called {
		t.Error("Handler was not called")
	}
}

func TestRegistryUnknownTopic(t *testing.T) {
	registry := NewRegistry()

	msg := &pb.Message{
		Topic:  "unknown.Service",
		Action: "TestAction",
	}

	err := registry.Handle(context.Background(), msg)
	if err == nil {
		t.Error("Expected error for unknown topic")
	}

	if err.Error() != "no handlers registered for topic: unknown.Service" {
		t.Errorf("Unexpected error message: %v", err)
	}
}

func TestRegistryUnknownAction(t *testing.T) {
	registry := NewRegistry()

	registry.Register("test.Service", "KnownAction", func(ctx context.Context, msg *pb.Message) error {
		return nil
	})

	msg := &pb.Message{
		Topic:  "test.Service",
		Action: "UnknownAction",
	}

	err := registry.Handle(context.Background(), msg)
	if err == nil {
		t.Error("Expected error for unknown action")
	}
}

func TestRegistryHandlerError(t *testing.T) {
	registry := NewRegistry()

	expectedErr := errors.New("handler error")
	registry.Register("test.Service", "TestAction", func(ctx context.Context, msg *pb.Message) error {
		return expectedErr
	})

	msg := &pb.Message{
		Topic:  "test.Service",
		Action: "TestAction",
	}

	err := registry.Handle(context.Background(), msg)
	if err != expectedErr {
		t.Errorf("Expected error %v, got %v", expectedErr, err)
	}
}

func TestRegistryTopicsAndActions(t *testing.T) {
	registry := NewRegistry()

	registry.Register("service1", "action1", nil)
	registry.Register("service1", "action2", nil)
	registry.Register("service2", "action1", nil)

	topics := registry.Topics()
	if len(topics) != 2 {
		t.Errorf("Expected 2 topics, got %d", len(topics))
	}

	actions := registry.Actions("service1")
	if len(actions) != 2 {
		t.Errorf("Expected 2 actions for service1, got %d", len(actions))
	}

	actions = registry.Actions("service2")
	if len(actions) != 1 {
		t.Errorf("Expected 1 action for service2, got %d", len(actions))
	}

	actions = registry.Actions("unknown")
	if actions != nil {
		t.Errorf("Expected nil for unknown topic, got %v", actions)
	}
}

func TestRegistryConcurrency(t *testing.T) {
	registry := NewRegistry()

	// Register handlers concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			registry.Register("test.Service", "TestAction", func(ctx context.Context, msg *pb.Message) error {
				return nil
			})
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify handler is registered
	if !registry.IsRegistered("test.Service", "TestAction") {
		t.Error("Expected handler to be registered")
	}
}

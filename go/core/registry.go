package core

import (
	"context"
	"fmt"
	"sync"

	pb "github.com/pbdeuchler/grpcq/proto/grpcq"
)

// Registry maintains a mapping of topic/action pairs to handler functions.
// It is thread-safe and allows concurrent registration and lookup.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]map[string]Handler
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]map[string]Handler),
	}
}

// Register associates a handler with a specific topic and action.
// If a handler is already registered for this topic/action pair, it will be overwritten.
func (r *Registry) Register(topic, action string, handler Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.handlers[topic] == nil {
		r.handlers[topic] = make(map[string]Handler)
	}
	r.handlers[topic][action] = handler
}

// Handle processes a message by looking up and invoking the appropriate handler.
// Returns an error if no handler is registered for the message's topic/action,
// or if the handler returns an error.
func (r *Registry) Handle(ctx context.Context, msg *pb.Message) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	topicHandlers, topicExists := r.handlers[msg.Topic]
	if !topicExists {
		return fmt.Errorf("no handlers registered for topic: %s", msg.Topic)
	}

	handler, actionExists := topicHandlers[msg.Action]
	if !actionExists {
		return fmt.Errorf("no handler registered for topic: %s, action: %s", msg.Topic, msg.Action)
	}

	return handler(ctx, msg)
}

// IsRegistered checks if a handler exists for the given topic and action.
func (r *Registry) IsRegistered(topic, action string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	topicHandlers, topicExists := r.handlers[topic]
	if !topicExists {
		return false
	}

	_, actionExists := topicHandlers[action]
	return actionExists
}

// Topics returns a list of all registered topics.
func (r *Registry) Topics() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	topics := make([]string, 0, len(r.handlers))
	for topic := range r.handlers {
		topics = append(topics, topic)
	}
	return topics
}

// Actions returns a list of all registered actions for a given topic.
func (r *Registry) Actions(topic string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	topicHandlers, exists := r.handlers[topic]
	if !exists {
		return nil
	}

	actions := make([]string, 0, len(topicHandlers))
	for action := range topicHandlers {
		actions = append(actions, action)
	}
	return actions
}

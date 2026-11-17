// Package grpcq provides the runtime support for grpcq generated code.
// It handles server and client implementations for queue-based RPC.
package grpcq

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/pbdeuchler/grpcq/go/core"
	pb "github.com/pbdeuchler/grpcq/go/proto"
	"google.golang.org/protobuf/proto"
)

// QueueAdapter is the interface that queue implementations must satisfy.
// This is re-exported from core for convenience in generated code.
type QueueAdapter = core.QueueAdapter

// Server represents a grpcq server that processes messages from a queue.
type Server struct {
	registry *core.Registry
	worker   *core.Worker
	started  atomic.Bool
}

// ServerConfig contains configuration for a grpcq server.
type ServerConfig struct {
	// QueueName is the name of the queue to consume from
	QueueName string

	// Concurrency is the maximum number of messages to process concurrently
	Concurrency int

	// MaxBatch is the maximum number of messages to fetch in one consume operation
	MaxBatch int

	// PollIntervalMs is how long to wait between poll attempts when no messages are available
	PollIntervalMs int
}

// ServerOption configures a Server.
type ServerOption func(*ServerConfig)

// WithQueueName sets the queue name for the server.
func WithQueueName(name string) ServerOption {
	return func(c *ServerConfig) {
		c.QueueName = name
	}
}

// WithConcurrency sets the concurrency level for message processing.
func WithConcurrency(n int) ServerOption {
	return func(c *ServerConfig) {
		c.Concurrency = n
	}
}

// WithMaxBatch sets the maximum batch size for consuming messages.
func WithMaxBatch(n int) ServerOption {
	return func(c *ServerConfig) {
		c.MaxBatch = n
	}
}

// WithPollInterval sets the polling interval in milliseconds.
func WithPollInterval(ms int) ServerOption {
	return func(c *ServerConfig) {
		c.PollIntervalMs = ms
	}
}

// NewServer creates a new grpcq server.
func NewServer(adapter QueueAdapter, opts ...ServerOption) *Server {
	config := &ServerConfig{
		QueueName:      "default-queue",
		Concurrency:    10,
		MaxBatch:       10,
		PollIntervalMs: 1000,
	}

	for _, opt := range opts {
		opt(config)
	}

	registry := core.NewRegistry()
	return &Server{
		registry: registry,
		worker: core.NewWorker(adapter, registry, core.WorkerConfig{
			QueueName:      config.QueueName,
			Concurrency:    config.Concurrency,
			MaxBatch:       config.MaxBatch,
			PollIntervalMs: config.PollIntervalMs,
		}),
	}
}

// RegisterMethod registers a method handler with the server.
// This is called by generated code.
func (s *Server) RegisterMethod(
	serviceName string,
	methodName string,
	handler func(ctx context.Context, req interface{}) (interface{}, error),
	newRequest func() interface{},
) {
	// Wrap the typed handler to work with grpcq messages
	wrappedHandler := func(ctx context.Context, msg *pb.Message) error {
		// Create new request instance
		req := newRequest()

		// Unmarshal payload
		if err := proto.Unmarshal(msg.Payload, req.(proto.Message)); err != nil {
			return fmt.Errorf("failed to unmarshal request: %w", err)
		}

		// Call handler
		_, err := handler(ctx, req)
		return err
	}

	s.registry.Register(serviceName, methodName, wrappedHandler)
}

// Start starts the server and begins processing messages.
// This blocks until the context is cancelled, Stop is called, or an error occurs.
func (s *Server) Start(ctx context.Context) error {
	s.started.Store(true)
	return s.worker.Start(ctx)
}

// Stop gracefully stops the server.
// It stops intake and waits for received work and receipt operations to finish.
func (s *Server) Stop() error {
	if !s.started.Load() {
		return fmt.Errorf("server not started")
	}

	return s.worker.Stop(context.Background())
}

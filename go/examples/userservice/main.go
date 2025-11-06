package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pbdeuchler/grpcq/go/adapters/memory"
	"github.com/pbdeuchler/grpcq/go/core"
	"github.com/pbdeuchler/grpcq/go/examples/userservice"
	pb "github.com/pbdeuchler/grpcq/proto/grpcq"
	"google.golang.org/protobuf/proto"
)

const (
	queueName = "user-service-queue"
	topic     = "userservice.UserService"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Create an in-memory adapter for this example
	adapter := memory.NewAdapter(1000)

	// Start the worker in a goroutine
	go runWorker(ctx, adapter)

	// Give worker time to start
	time.Sleep(100 * time.Millisecond)

	// Run the publisher
	if err := runPublisher(ctx, adapter); err != nil {
		log.Fatalf("Publisher error: %v", err)
	}

	// Wait for completion or signal
	select {
	case <-sigCh:
		log.Println("Received shutdown signal")
		cancel()
	case <-time.After(5 * time.Second):
		log.Println("Example completed")
		cancel()
	}

	// Give worker time to drain
	time.Sleep(1 * time.Second)
}

// runWorker sets up and runs a worker that processes user service messages.
func runWorker(ctx context.Context, adapter core.QueueAdapter) {
	// Create a registry and register handlers
	registry := core.NewRegistry()

	// Register CreateUser handler
	registry.Register(topic, "CreateUser", handleCreateUser)

	// Create and start the worker
	config := core.DefaultWorkerConfig(queueName)
	config.Concurrency = 5
	config.PollIntervalMs = 100 // Poll faster for demo purposes

	worker := core.NewWorker(adapter, registry, config)

	log.Println("Worker started, waiting for messages...")
	if err := worker.Start(ctx); err != nil && err != context.Canceled {
		log.Printf("Worker error: %v", err)
	}
	log.Println("Worker stopped")
}

// handleCreateUser is the handler for CreateUser messages.
func handleCreateUser(ctx context.Context, msg *pb.Message) error {
	// Deserialize the payload
	var req userservice.CreateUserRequest
	if err := proto.Unmarshal(msg.Payload, &req); err != nil {
		return fmt.Errorf("failed to unmarshal CreateUserRequest: %w", err)
	}

	log.Printf("Processing CreateUser: name=%s email=%s", req.Name, req.Email)

	// Simulate some processing
	time.Sleep(100 * time.Millisecond)

	// In a real application, you would:
	// 1. Validate the request
	// 2. Create the user in the database
	// 3. Publish a response or event

	log.Printf("Successfully created user: %s", req.Name)
	return nil
}

// runPublisher demonstrates publishing messages to the queue.
func runPublisher(ctx context.Context, adapter core.QueueAdapter) error {
	// Create a publisher
	publisher := core.NewPublisher(adapter, "example-publisher")

	log.Println("Publishing user creation requests...")

	// Publish several CreateUser requests
	users := []struct {
		name  string
		email string
	}{
		{"Alice Johnson", "alice@example.com"},
		{"Bob Smith", "bob@example.com"},
		{"Charlie Brown", "charlie@example.com"},
	}

	for _, user := range users {
		req := &userservice.CreateUserRequest{
			Name:  user.name,
			Email: user.email,
		}

		metadata := map[string]string{
			"trace_id": fmt.Sprintf("trace-%d", time.Now().UnixNano()),
		}

		err := publisher.Send(
			ctx,
			queueName,
			topic,
			"CreateUser",
			req,
			metadata,
		)

		if err != nil {
			return fmt.Errorf("failed to publish message: %w", err)
		}

		log.Printf("Published CreateUser request for: %s", user.name)
		time.Sleep(200 * time.Millisecond)
	}

	log.Println("All messages published")
	return nil
}

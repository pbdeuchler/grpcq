package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pbdeuchler/grpcq/go/adapters/memory"
	"github.com/pbdeuchler/grpcq/go/core"
	grpcadapter "github.com/pbdeuchler/grpcq/go/grpc"
	userpb "github.com/pbdeuchler/grpcq/go/examples/userservice/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	queueName   = "user-service-queue"
	serviceName = "userservice.UserService"
	grpcAddr    = "localhost:50051"
)

var (
	mode = flag.String("mode", "demo", "Mode to run: sync-server, sync-client, async-worker, async-client, or demo (runs worker+client together)")
)

func main() {
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	switch *mode {
	case "sync-server":
		log.Println("Starting in SYNC SERVER mode (traditional gRPC)")
		runSyncServer(ctx, sigCh)
	case "sync-client":
		log.Println("Starting in SYNC CLIENT mode (traditional gRPC)")
		runSyncClient(ctx)
	case "async-worker":
		log.Println("Starting in ASYNC WORKER mode (grpcq)")
		runAsyncWorker(ctx, sigCh)
	case "async-client":
		log.Println("Starting in ASYNC CLIENT mode (grpcq)")
		runAsyncClient(ctx)
	case "demo":
		log.Println("Starting in DEMO mode (async worker + client)")
		runDemo(ctx, sigCh)
	default:
		log.Fatalf("Unknown mode: %s. Use: sync-server, sync-client, async-worker, async-client, or demo", *mode)
	}
}

// runSyncServer starts a traditional gRPC server.
func runSyncServer(ctx context.Context, sigCh chan os.Signal) {
	// Create service implementation
	svc := NewUserService()

	// Create gRPC server
	grpcServer := grpc.NewServer()
	userpb.RegisterUserServiceServer(grpcServer, svc)

	// Start listening
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	log.Printf("gRPC server listening on %s", grpcAddr)

	// Start server in goroutine
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-sigCh
	log.Println("Shutting down gRPC server...")
	grpcServer.GracefulStop()
}

// runSyncClient calls the traditional gRPC server.
func runSyncClient(ctx context.Context) {
	// Connect to gRPC server
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	client := userpb.NewUserServiceClient(conn)

	log.Println("Calling CreateUser via traditional gRPC...")

	// Make synchronous calls
	users := []struct {
		name  string
		email string
	}{
		{"Alice Johnson", "alice@example.com"},
		{"Bob Smith", "bob@example.com"},
		{"Charlie Brown", "charlie@example.com"},
	}

	for _, user := range users {
		resp, err := client.CreateUser(ctx, &userpb.CreateUserRequest{
			Name:  user.name,
			Email: user.email,
		})
		if err != nil {
			log.Printf("CreateUser failed: %v", err)
			continue
		}
		log.Printf("Created user: id=%s name=%s email=%s", resp.UserId, resp.Name, resp.Email)

		// Also test GetUser
		getResp, err := client.GetUser(ctx, &userpb.GetUserRequest{
			UserId: resp.UserId,
		})
		if err != nil {
			log.Printf("GetUser failed: %v", err)
			continue
		}
		log.Printf("Retrieved user: id=%s name=%s email=%s", getResp.UserId, getResp.Name, getResp.Email)
	}

	log.Println("Sync client completed")
}

// runAsyncWorker starts a grpcq worker that processes messages.
func runAsyncWorker(ctx context.Context, sigCh chan os.Signal) {
	// Create adapter
	adapter := memory.NewAdapter(1000)

	// Create service implementation (same as gRPC!)
	svc := NewUserService()

	// Create registry
	registry := core.NewRegistry()

	// Register handlers using the grpc adapter
	// This bridges the gRPC server implementation to grpcq
	registry.Register(serviceName, "CreateUser", grpcadapter.WrapUnaryMethod(
		svc.CreateUser,
		func() *userpb.CreateUserRequest { return &userpb.CreateUserRequest{} },
	))

	registry.Register(serviceName, "GetUser", grpcadapter.WrapUnaryMethod(
		svc.GetUser,
		func() *userpb.GetUserRequest { return &userpb.GetUserRequest{} },
	))

	// Create and start worker
	config := core.DefaultWorkerConfig(queueName)
	config.Concurrency = 5
	config.PollIntervalMs = 100

	worker := core.NewWorker(adapter, registry, config)

	log.Println("Worker started, waiting for messages...")

	// Start worker in goroutine
	go func() {
		if err := worker.Start(ctx); err != nil && err != context.Canceled {
			log.Printf("Worker error: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-sigCh
	log.Println("Shutting down worker...")
}

// runAsyncClient publishes messages using the gRPC client interface.
func runAsyncClient(ctx context.Context) {
	// Create adapter and publisher
	adapter := memory.NewAdapter(1000)
	publisher := core.NewPublisher(adapter, "example-client")

	// Create async client connection
	clientAdapter := grpcadapter.NewClientAdapter(publisher, queueName)

	// Create gRPC client using the async connection
	// The client has the same interface as the synchronous version!
	client := userpb.NewUserServiceClient(clientAdapter.Conn())

	log.Println("Publishing CreateUser requests via gRPC client interface...")

	// Make calls - they look identical to sync calls but are async!
	users := []struct {
		name  string
		email string
	}{
		{"Alice Johnson", "alice@example.com"},
		{"Bob Smith", "bob@example.com"},
		{"Charlie Brown", "charlie@example.com"},
	}

	for _, user := range users {
		// This looks like a sync call but publishes to queue!
		_, err := client.CreateUser(ctx, &userpb.CreateUserRequest{
			Name:  user.name,
			Email: user.email,
		})
		if err != nil {
			log.Printf("Failed to publish CreateUser: %v", err)
			continue
		}
		log.Printf("Published CreateUser request for: %s", user.name)
		time.Sleep(200 * time.Millisecond)
	}

	log.Println("Async client completed")
}

// runDemo runs both worker and client together (original example behavior).
func runDemo(ctx context.Context, sigCh chan os.Signal) {
	// Create adapter (shared between worker and publisher)
	adapter := memory.NewAdapter(1000)

	// Start worker
	go func() {
		svc := NewUserService()
		registry := core.NewRegistry()

		// Register with the new adapter
		registry.Register(serviceName, "CreateUser", grpcadapter.WrapUnaryMethod(
			svc.CreateUser,
			func() *userpb.CreateUserRequest { return &userpb.CreateUserRequest{} },
		))

		registry.Register(serviceName, "GetUser", grpcadapter.WrapUnaryMethod(
			svc.GetUser,
			func() *userpb.GetUserRequest { return &userpb.GetUserRequest{} },
		))

		config := core.DefaultWorkerConfig(queueName)
		config.Concurrency = 5
		config.PollIntervalMs = 100

		worker := core.NewWorker(adapter, registry, config)
		log.Println("Worker started, waiting for messages...")

		if err := worker.Start(ctx); err != nil && err != context.Canceled {
			log.Printf("Worker error: %v", err)
		}
		log.Println("Worker stopped")
	}()

	// Give worker time to start
	time.Sleep(100 * time.Millisecond)

	// Run publisher using gRPC client interface
	publisher := core.NewPublisher(adapter, "demo-client")
	clientAdapter := grpcadapter.NewClientAdapter(publisher, queueName)
	client := userpb.NewUserServiceClient(clientAdapter.Conn())

	log.Println("Publishing user creation requests...")

	users := []struct {
		name  string
		email string
	}{
		{"Alice Johnson", "alice@example.com"},
		{"Bob Smith", "bob@example.com"},
		{"Charlie Brown", "charlie@example.com"},
	}

	for _, user := range users {
		_, err := client.CreateUser(ctx, &userpb.CreateUserRequest{
			Name:  user.name,
			Email: user.email,
		})
		if err != nil {
			log.Printf("Failed to publish: %v", err)
			continue
		}
		log.Printf("Published CreateUser request for: %s", user.name)
		time.Sleep(200 * time.Millisecond)
	}

	log.Println("All messages published")

	// Wait for completion or signal
	select {
	case <-sigCh:
		log.Println("Received shutdown signal")
	case <-time.After(2 * time.Second):
		log.Println("Demo completed")
	}

	// Give worker time to process remaining messages
	time.Sleep(1 * time.Second)
}

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
	"github.com/pbdeuchler/grpcq/go/grpcq"
	userpb "github.com/pbdeuchler/grpcq/go/examples/userservice/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	queueName = "user-service-queue"
	grpcAddr  = "localhost:50051"
)

var (
	mode = flag.String("mode", "demo", "Mode to run: sync-server, sync-client, async-server, async-client, or demo")
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
	case "async-server":
		log.Println("Starting in ASYNC SERVER mode (grpcq)")
		runAsyncServer(ctx, sigCh)
	case "async-client":
		log.Println("Starting in ASYNC CLIENT mode (grpcq)")
		runAsyncClient(ctx)
	case "demo":
		log.Println("Starting in DEMO mode (async server + client)")
		runDemo(ctx, sigCh)
	default:
		log.Fatalf("Unknown mode: %s", *mode)
	}
}

// runSyncServer starts a traditional gRPC server.
func runSyncServer(ctx context.Context, sigCh chan os.Signal) {
	// Create service implementation
	svc := NewUserService()

	// Create gRPC server - uses standard gRPC registration
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
	// Connect to gRPC server - uses standard gRPC client
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
	}

	log.Println("Sync client completed")
}

// runAsyncServer starts a grpcq server that processes messages.
// Notice how similar this is to the sync version!
func runAsyncServer(ctx context.Context, sigCh chan os.Signal) {
	// Create adapter
	adapter := memory.NewAdapter(1000)

	// Create service implementation (same code as gRPC!)
	svc := NewUserService()

	// Register service with grpcq - uses generated registration function
	server := userpb.RegisterUserServiceQServer(
		adapter,
		svc,
		grpcq.WithQueueName(queueName),
		grpcq.WithConcurrency(5),
		grpcq.WithPollInterval(100),
	)

	log.Println("grpcq server started, waiting for messages...")

	// Start server in goroutine
	go func() {
		if err := server.Start(ctx); err != nil && err != context.Canceled {
			log.Printf("Server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-sigCh
	log.Println("Shutting down grpcq server...")
}

// runAsyncClient publishes messages using the grpcq client.
// Notice how similar this is to the sync version!
func runAsyncClient(ctx context.Context) {
	// Create adapter
	adapter := memory.NewAdapter(1000)

	// Create grpcq client - uses generated client constructor
	client := userpb.NewUserServiceQClient(
		adapter,
		grpcq.WithClientQueueName(queueName),
		grpcq.WithOriginator("example-client"),
	)

	log.Println("Publishing CreateUser requests via grpcq...")

	// Make calls - same interface as gRPC client!
	users := []struct {
		name  string
		email string
	}{
		{"Alice Johnson", "alice@example.com"},
		{"Bob Smith", "bob@example.com"},
		{"Charlie Brown", "charlie@example.com"},
	}

	for _, user := range users {
		// Same method signature as gRPC!
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

	log.Println("Async client completed")
}

// runDemo runs both server and client together.
func runDemo(ctx context.Context, sigCh chan os.Signal) {
	// Create shared adapter
	adapter := memory.NewAdapter(1000)

	// Start async server
	go func() {
		svc := NewUserService()
		server := userpb.RegisterUserServiceQServer(
			adapter,
			svc,
			grpcq.WithQueueName(queueName),
			grpcq.WithConcurrency(5),
			grpcq.WithPollInterval(100),
		)

		log.Println("Server started, waiting for messages...")
		if err := server.Start(ctx); err != nil && err != context.Canceled {
			log.Printf("Server error: %v", err)
		}
		log.Println("Server stopped")
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Create client and publish messages
	client := userpb.NewUserServiceQClient(
		adapter,
		grpcq.WithClientQueueName(queueName),
		grpcq.WithOriginator("demo-client"),
	)

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

	// Give server time to process remaining messages
	time.Sleep(1 * time.Second)
}

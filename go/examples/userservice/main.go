package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pbdeuchler/grpcq/go/adapters/memory"
	userpb "github.com/pbdeuchler/grpcq/go/examples/userservice/proto"
	"github.com/pbdeuchler/grpcq/go/grpcq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	queueName = "user-service-queue"
	grpcAddr  = "localhost:50051"
)

var (
	mode            = flag.String("mode", "demo", "Mode to run: sync-server, sync-client, async-server, async-client, or demo")
	queueListenAddr = flag.String("queue_listen", "127.0.0.1:8081", "Address for the async queue publish HTTP server (used by async server/demo)")
	queueEndpoint   = flag.String("queue_endpoint", "http://127.0.0.1:8081", "Endpoint for the async queue publish HTTP server (used by async client/demo)")
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
	connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		connCtx,
		grpcAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
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
	adapter := memory.NewAdapter(1000)
	queueServer, queueErrCh := startQueuePublishServer(*queueListenAddr, adapter)

	svc := NewUserService()
	server := userpb.RegisterUserServiceQServer(
		adapter,
		svc,
		grpcq.WithQueueName(queueName),
		grpcq.WithConcurrency(5),
		grpcq.WithPollInterval(100),
	)

	log.Println("grpcq server started, waiting for messages...")

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.Start(ctx)
	}()

	var (
		serverErr     error
		serverRunning = true
	)

	select {
	case <-sigCh:
		log.Println("Shutdown signal received")
	case err := <-queueErrCh:
		if err != nil {
			log.Printf("Queue publish server error: %v", err)
		}
	case err := <-serverErrCh:
		serverErr = err
		serverRunning = false
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("grpcq server exited: %v", err)
		}
	}

	if serverRunning {
		if err := server.Stop(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Error stopping server: %v", err)
		}

		serverErr = <-serverErrCh
		if serverErr != nil && !errors.Is(serverErr, context.Canceled) {
			log.Printf("grpcq server exited: %v", serverErr)
		}
	}

	shutdownHTTPServer(queueServer)

	select {
	case err := <-queueErrCh:
		if err != nil {
			log.Printf("Queue publish server error: %v", err)
		}
	default:
	}
}

// runAsyncClient publishes messages using the grpcq client.
// Notice how similar this is to the sync version!
func runAsyncClient(ctx context.Context) {
	adapter := memory.NewRemoteAdapter(*queueEndpoint)

	client := userpb.NewUserServiceQClient(
		adapter,
		grpcq.WithClientQueueName(queueName),
		grpcq.WithOriginator("example-client"),
	)

	log.Printf("Publishing CreateUser requests via grpcq (endpoint: %s)...", *queueEndpoint)

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
	adapter := memory.NewAdapter(1000)
	queueServer, queueErrCh := startQueuePublishServer(*queueListenAddr, adapter)

	svc := NewUserService()
	server := userpb.RegisterUserServiceQServer(
		adapter,
		svc,
		grpcq.WithQueueName(queueName),
		grpcq.WithConcurrency(5),
		grpcq.WithPollInterval(100),
	)

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.Start(ctx)
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	client := userpb.NewUserServiceQClient(
		memory.NewRemoteAdapter(*queueEndpoint),
		grpcq.WithClientQueueName(queueName),
		grpcq.WithOriginator("demo-client"),
	)

	log.Printf("Publishing user creation requests (endpoint: %s)...", *queueEndpoint)

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

	var (
		serverErr     error
		serverRunning = true
	)

	select {
	case <-sigCh:
		log.Println("Received shutdown signal")
	case err := <-queueErrCh:
		if err != nil {
			log.Printf("Queue publish server error: %v", err)
		}
	case err := <-serverErrCh:
		serverErr = err
		serverRunning = false
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("grpcq server exited: %v", err)
		}
	case <-time.After(2 * time.Second):
		log.Println("Demo completed")
	}

	// Give server time to process remaining messages
	time.Sleep(1 * time.Second)

	if serverRunning {
		if err := server.Stop(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("Error stopping server: %v", err)
		}

		serverErr = <-serverErrCh
		if serverErr != nil && !errors.Is(serverErr, context.Canceled) {
			log.Printf("grpcq server exited: %v", serverErr)
		}
	}

	shutdownHTTPServer(queueServer)

	select {
	case err := <-queueErrCh:
		if err != nil {
			log.Printf("Queue publish server error: %v", err)
		}
	default:
	}
}

func startQueuePublishServer(addr string, adapter *memory.Adapter) (*http.Server, chan error) {
	server := &http.Server{
		Addr:    addr,
		Handler: memory.NewPublishHandler(adapter),
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Queue publish HTTP server listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	return server, errCh
}

func shutdownHTTPServer(server *http.Server) {
	if server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("Queue publish server shutdown error: %v", err)
	}
}

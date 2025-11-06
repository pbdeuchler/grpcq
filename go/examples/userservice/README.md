# User Service Example

This example demonstrates how to use grpcq to convert a traditional gRPC service to an asynchronous queue-based architecture.

## What This Example Shows

1. **Publisher Pattern**: How to publish gRPC method calls as messages to a queue
2. **Worker Pattern**: How to consume and process messages using registered handlers
3. **Handler Registration**: How to map topic/action pairs to handler functions
4. **Message Processing**: Complete lifecycle from publish to acknowledgment

## Running the Example

```bash
cd go/examples/userservice
go run main.go
```

## Expected Output

```
Worker started, waiting for messages...
Publishing user creation requests...
Published CreateUser request for: Alice Johnson
Processing CreateUser: name=Alice Johnson email=alice@example.com
Successfully created user: Alice Johnson
Published CreateUser request for: Bob Smith
Processing CreateUser: name=Bob Smith email=bob@example.com
Successfully created user: Bob Smith
Published CreateUser request for: Charlie Brown
Processing CreateUser: name=Charlie Brown email=charlie@example.com
Successfully created user: Charlie Brown
All messages published
Example completed
Worker stopped
```

## Code Walkthrough

### 1. Setting Up the Adapter

```go
// Create an in-memory adapter for this example
adapter := memory.NewAdapter(1000)
```

For production, you would use the SQS adapter:

```go
import (
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/sqs"
    sqsadapter "github.com/pbdeuchler/grpcq/go/adapters/sqs"
)

cfg, _ := config.LoadDefaultConfig(ctx)
client := sqs.NewFromConfig(cfg)

adapter, err := sqsadapter.NewAdapter(sqsadapter.Config{
    QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789/my-queue",
    Client:   client,
})
```

### 2. Registering Handlers (Worker Side)

```go
registry := core.NewRegistry()
registry.Register(topic, "CreateUser", handleCreateUser)
```

The handler function signature:

```go
func handleCreateUser(ctx context.Context, msg *pb.Message) error {
    // Deserialize the payload
    var req userservice.CreateUserRequest
    proto.Unmarshal(msg.Payload, &req)

    // Process the request
    // ...

    return nil // or return error to nack
}
```

### 3. Starting the Worker

```go
config := core.DefaultWorkerConfig(queueName)
worker := core.NewWorker(adapter, registry, config)
worker.Start(ctx)
```

### 4. Publishing Messages (Client Side)

```go
publisher := core.NewPublisher(adapter, "my-service")

req := &userservice.CreateUserRequest{
    Name:  "Alice",
    Email: "alice@example.com",
}

publisher.Send(ctx, queueName, topic, "CreateUser", req, metadata)
```

## Migration from gRPC

### Before (Traditional gRPC)

**Server:**
```go
type userServiceServer struct {
    pb.UnimplementedUserServiceServer
}

func (s *userServiceServer) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
    // Implementation
}

// Start gRPC server
grpcServer := grpc.NewServer()
pb.RegisterUserServiceServer(grpcServer, &userServiceServer{})
```

**Client:**
```go
conn, _ := grpc.Dial("localhost:50051")
client := pb.NewUserServiceClient(conn)
resp, err := client.CreateUser(ctx, &CreateUserRequest{...})
```

### After (grpcq)

**Worker (replaces Server):**
```go
registry := core.NewRegistry()
registry.Register("userservice.UserService", "CreateUser", func(ctx context.Context, msg *pb.Message) error {
    var req CreateUserRequest
    proto.Unmarshal(msg.Payload, &req)
    // Same implementation as before
    return nil
})

worker := core.NewWorker(adapter, registry, config)
worker.Start(ctx)
```

**Publisher (replaces Client):**
```go
publisher := core.NewPublisher(adapter, "my-service")
err := publisher.Send(ctx, queueName, "userservice.UserService", "CreateUser", &CreateUserRequest{...}, nil)
// Note: No immediate response (async)
```

## Key Differences

| Aspect | gRPC | grpcq |
|--------|------|-------|
| Communication | Synchronous | Asynchronous |
| Response | Immediate | None (fire-and-forget) |
| Retry | Client-side | Queue-level |
| Scaling | Load balancer | Queue consumers |
| Backpressure | Connection limits | Queue depth |

## When to Use grpcq

✅ **Good fit:**
- Fire-and-forget operations
- High-volume background tasks
- Services that need to scale independently
- Operations with built-in retry logic
- Cross-language service communication

❌ **Not recommended:**
- Request-response patterns requiring immediate answers
- Real-time interactive APIs
- Operations requiring streaming

## Next Steps

1. Try modifying the example to add more handlers
2. Experiment with the SQS adapter
3. Add error handling and retries
4. Implement dead-letter queue handling
5. Add metrics and observability

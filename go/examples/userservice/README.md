# User Service Example

This example demonstrates grpcq's gRPC interoperability - how to write a service implementation once and run it in both **synchronous (traditional gRPC)** and **asynchronous (queue-based)** modes.

## Key Concept

The same service implementation code works for both:
- Traditional gRPC server/client (synchronous, request-response)
- grpcq worker/publisher (asynchronous, queue-based)

This enables gradual migration, flexible testing, and deployment options without code changes.

## Running the Example

The example supports multiple modes:

### Demo Mode (Default)
Runs both async worker and client together:
```bash
cd go/examples/userservice
go run . -mode demo
```

### Synchronous gRPC

**Start the gRPC server:**
```bash
go run . -mode sync-server
```

**In another terminal, run the client:**
```bash
go run . -mode sync-client
```

### Asynchronous Queue-Based

The async modes share work through an HTTP publish endpoint that fronts the in-memory adapter. Run the worker with a listener address (defaults to `127.0.0.1:8081`):

**Start the worker:**
```bash
go run . -mode async-server -queue_listen 127.0.0.1:8081
```

**In another terminal, run the publisher pointing at the same endpoint:**
```bash
go run . -mode async-client -queue_endpoint http://127.0.0.1:8081
```

You can pick any address/port pair as long as both processes agree. For example, to expose the demo on all interfaces use `-queue_listen 0.0.0.0:9000` on the server and `-queue_endpoint http://localhost:9000` on the client.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                    UserService Implementation                │
│          (Write once, use in both sync and async)           │
└──────────────────┬──────────────────────┬───────────────────┘
                   │                      │
         ┌─────────▼─────────┐  ┌─────────▼──────────┐
         │  Synchronous      │  │   Asynchronous     │
         │   (gRPC)          │  │     (grpcq)        │
         └─────────┬─────────┘  └─────────┬──────────┘
                   │                      │
         ┌─────────▼─────────┐  ┌─────────▼──────────┐
         │  gRPC Server      │  │  grpcq Worker      │
         │  grpc.NewServer() │  │  WrapUnaryMethod() │
         └───────────────────┘  └────────────────────┘
```

## Code Walkthrough

### 1. Service Implementation (service.go)

The service implements the standard gRPC server interface:

```go
type UserService struct {
    userpb.UnimplementedUserServiceServer
    // ... fields ...
}

func (s *UserService) CreateUser(ctx context.Context, req *userpb.CreateUserRequest) (*userpb.CreateUserResponse, error) {
    // Your business logic here
    // This code is the same for both sync and async!
    return &userpb.CreateUserResponse{...}, nil
}
```

**Key Point**: This is standard gRPC code. No queue-specific logic needed.

### 2. Synchronous Mode (Traditional gRPC)

**Server:**
```go
svc := NewUserService()

// Standard gRPC server setup
grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, svc)
grpcServer.Serve(listener)
```

**Client:**
```go
conn, _ := grpc.Dial("localhost:50051")
client := userpb.NewUserServiceClient(conn)

// Synchronous call - waits for response
resp, err := client.CreateUser(ctx, &userpb.CreateUserRequest{
    Name: "Alice",
    Email: "alice@example.com",
})
// resp is available immediately
```

### 3. Asynchronous Mode (grpcq)

**Worker (replaces gRPC server):**
```go
svc := NewUserService()  // Same service!
adapter := memory.NewAdapter(1000)

// Use generated registration function
server := userpb.RegisterUserServiceQServer(
    adapter,
    svc,
    grpcq.WithQueueName("user-queue"),
    grpcq.WithConcurrency(10),
)

server.Start(ctx)
```

**Publisher (replaces gRPC client):**
```go
adapter := memory.NewAdapter(1000)

// Use generated client constructor
client := userpb.NewUserServiceQClient(
    adapter,
    grpcq.WithClientQueueName("user-queue"),
    grpcq.WithOriginator("my-service"),
)

// Same interface as gRPC client!
resp, err := client.CreateUser(ctx, &userpb.CreateUserRequest{
    Name: "Alice",
    Email: "alice@example.com",
})
// Returns immediately (fire-and-forget)
```

## Comparison

### Synchronous (gRPC)

**Server:**
```go
grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, svc)
grpcServer.Serve(lis)
```

**Client:**
```go
conn, _ := grpc.Dial("localhost:50051")
client := userpb.NewUserServiceClient(conn)
resp, err := client.CreateUser(ctx, req)  // Waits for response
```

### Asynchronous (grpcq)

**Worker:**
```go
server := userpb.RegisterUserServiceQServer(
    adapter,
    svc,
    grpcq.WithQueueName("user-queue"),
)
server.Start(ctx)
```

**Publisher:**
```go
client := userpb.NewUserServiceQClient(
    adapter,
    grpcq.WithClientQueueName("user-queue"),
)
client.CreateUser(ctx, req)  // Fire-and-forget
```

## Key Differences

| Aspect | Synchronous (gRPC) | Asynchronous (grpcq) |
|--------|-------------------|---------------------|
| Communication | Direct connection | Via message queue |
| Response | Immediate | None (fire-and-forget) |
| Retry | Client-side | Queue-level |
| Scaling | Load balancer | Queue workers |
| Backpressure | Connection limits | Queue depth |
| Implementation | Standard gRPC | Same code + adapters |

## Use Cases

### When to Use Synchronous (gRPC)
- Need immediate responses
- Interactive APIs (REST, GraphQL gateways)
- Real-time operations
- Client needs to react to result

### When to Use Asynchronous (grpcq)
- Fire-and-forget operations
- Background processing
- High-volume data ingestion
- Decouple service scaling
- Built-in retry and DLQ
- Services in different regions/networks

## Benefits of Interoperability

1. **Write Once, Deploy Anywhere**
   - Single service implementation
   - Choose deployment mode at runtime
   - No code changes needed

2. **Gradual Migration**
   - Start with gRPC in dev
   - Migrate to queues in production
   - Or vice versa
   - Mix and match per environment

3. **Testing Flexibility**
   - Test with fast sync calls
   - Deploy with reliable async queues
   - Same code, different modes

4. **Standard Tooling**
   - Use protoc for code generation
   - Standard gRPC patterns
   - Type-safe interfaces

## Production Deployment

### Using AWS SQS

Replace the memory adapter with SQS:

```go
import (
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/sqs"
    sqsadapter "github.com/pbdeuchler/grpcq/go/adapters/sqs"
)

cfg, _ := config.LoadDefaultConfig(ctx)
sqsClient := sqs.NewFromConfig(cfg)

adapter, _ := sqsadapter.NewAdapter(sqsadapter.Config{
    QueueURLs: map[string]string{
        "user-queue": "https://sqs.us-east-1.amazonaws.com/123456789/user-queue",
    },
    Client: sqsClient,
})

// Use adapter with worker or publisher
// Everything else stays the same!
```

### Hybrid Deployment

You can even run both modes simultaneously:

```go
// Serve sync requests on gRPC
grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, svc)
go grpcServer.Serve(lis)

// Also process async requests from queue
server := userpb.RegisterUserServiceQServer(
    adapter,
    svc,
    grpcq.WithQueueName("user-queue"),
)
go server.Start(ctx)

// Same service, serving both sync and async!
```

## Next Steps

1. Modify the service to add more methods
2. Try deploying with SQS or another queue adapter
3. Experiment with different worker configurations
4. Add response handling (publish to response topics)
5. Implement request-response patterns
6. Add metrics and observability

## Further Reading

- [Main README](../../../README.md) - Full grpcq documentation
- [Protocol Specification](../../../docs/protocol.md) - Message format details
- [Core Package](../../core/) - Core abstractions and interfaces
- [gRPC Adapters](../../grpc/) - Interoperability layer implementation

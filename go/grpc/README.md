# gRPC Interoperability Package

This package provides seamless interoperability between standard gRPC services and grpcq's queue-based architecture. It allows you to write service implementations once and deploy them in either synchronous (gRPC) or asynchronous (grpcq) mode.

## Overview

The `grpc` package contains adapters that bridge the gap between:
- gRPC server implementations ↔ grpcq workers
- gRPC client interfaces ↔ grpcq publishers

This enables a "write once, deploy anywhere" pattern for microservices.

## Key Components

### Server Adapters

**`WrapUnaryMethod`**: Wraps a gRPC unary method to work with grpcq messages.

```go
func WrapUnaryMethod[TReq proto.Message, TResp proto.Message](
    methodFunc func(context.Context, TReq) (TResp, error),
    newRequest func() TReq,
) func(context.Context, *pb.Message) error
```

### Client Adapters

**`AsyncClientConn`**: Implements `grpc.ClientConnInterface` but routes calls through a grpcq publisher.

**`ClientAdapter`**: Provides a convenient wrapper for creating async client connections.

```go
func NewClientAdapter(publisher *core.Publisher, queueName string) *ClientAdapter
```

## Usage

### Server Side: Adapting gRPC Services

#### Step 1: Implement the gRPC Service Interface

Write your service using standard gRPC patterns:

```go
import (
    userpb "your/proto/package"
)

type UserService struct {
    userpb.UnimplementedUserServiceServer
    // ... your fields ...
}

func (s *UserService) CreateUser(ctx context.Context, req *userpb.CreateUserRequest) (*userpb.CreateUserResponse, error) {
    // Your business logic
    return &userpb.CreateUserResponse{
        UserId: "123",
        Name: req.Name,
        Email: req.Email,
    }, nil
}
```

#### Step 2a: Deploy as Traditional gRPC Server

```go
svc := &UserService{}

grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, svc)

listener, _ := net.Listen("tcp", ":50051")
grpcServer.Serve(listener)
```

#### Step 2b: Deploy as grpcq Worker

```go
import grpcadapter "github.com/pbdeuchler/grpcq/go/grpc"

svc := &UserService{}

// Create registry
registry := core.NewRegistry()

// Wrap each method
registry.Register("userservice.UserService", "CreateUser",
    grpcadapter.WrapUnaryMethod(
        svc.CreateUser,
        func() *userpb.CreateUserRequest { return &userpb.CreateUserRequest{} },
    ))

// Create and start worker
config := core.DefaultWorkerConfig("user-queue")
worker := core.NewWorker(adapter, registry, config)
worker.Start(ctx)
```

### Client Side: Using gRPC Clients with Queues

#### Traditional gRPC Client

```go
conn, _ := grpc.Dial("localhost:50051", grpc.WithInsecure())
client := userpb.NewUserServiceClient(conn)

// Synchronous call
resp, err := client.CreateUser(ctx, &userpb.CreateUserRequest{
    Name: "Alice",
    Email: "alice@example.com",
})
// resp is available immediately
```

#### Queue-Based Client (Same Interface!)

```go
import grpcadapter "github.com/pbdeuchler/grpcq/go/grpc"

// Create publisher
publisher := core.NewPublisher(adapter, "my-service")

// Create client adapter
clientAdapter := grpcadapter.NewClientAdapter(publisher, "user-queue")

// Use the same client interface!
client := userpb.NewUserServiceClient(clientAdapter.Conn())

// Looks identical but publishes to queue
resp, err := client.CreateUser(ctx, &userpb.CreateUserRequest{
    Name: "Alice",
    Email: "alice@example.com",
})
// Returns immediately (fire-and-forget)
// resp will be zero value since this is async
```

## Advanced Usage

### Wrapping Multiple Methods

```go
registry := core.NewRegistry()

// Register all methods for a service
registry.Register("userservice.UserService", "CreateUser",
    grpcadapter.WrapUnaryMethod(
        svc.CreateUser,
        func() *userpb.CreateUserRequest { return &userpb.CreateUserRequest{} },
    ))

registry.Register("userservice.UserService", "GetUser",
    grpcadapter.WrapUnaryMethod(
        svc.GetUser,
        func() *userpb.GetUserRequest { return &userpb.GetUserRequest{} },
    ))

registry.Register("userservice.UserService", "UpdateUser",
    grpcadapter.WrapUnaryMethod(
        svc.UpdateUser,
        func() *userpb.UpdateUserRequest { return &userpb.UpdateUserRequest{} },
    ))
```

### Hybrid Deployment

Run both sync and async modes simultaneously:

```go
svc := &UserService{}

// Start gRPC server for synchronous requests
grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, svc)
go grpcServer.Serve(listener)

// Start worker for asynchronous requests
registry := core.NewRegistry()
registry.Register("userservice.UserService", "CreateUser",
    grpcadapter.WrapUnaryMethod(svc.CreateUser, newRequest))

worker := core.NewWorker(adapter, registry, config)
go worker.Start(ctx)

// Service now handles both sync and async traffic!
```

### Service Name and Method Name Conventions

The adapter expects service and method names to follow gRPC conventions:

**Service Name**: Full proto package path + service name
- Format: `package.ServiceName`
- Example: `userservice.UserService`

**Method Name**: The RPC method name as defined in proto
- Example: `CreateUser`, `GetUser`, `DeleteUser`

These map to the full gRPC method path: `/package.ServiceName/MethodName`

## How It Works

### Server Side

1. **WrapUnaryMethod** creates a closure that:
   - Accepts a grpcq `Message`
   - Unmarshals the payload to the request type
   - Calls your gRPC method implementation
   - Handles the response (currently discards it for fire-and-forget)
   - Returns any errors for nack behavior

2. The wrapped function matches the `core.Handler` signature:
   ```go
   func(ctx context.Context, msg *pb.Message) error
   ```

3. The registry routes incoming messages to the appropriate handler based on topic and action.

### Client Side

1. **AsyncClientConn** implements `grpc.ClientConnInterface`

2. When a gRPC client method is called:
   - The `Invoke` method intercepts the call
   - Extracts service name and method name from the full method path
   - Marshals the request proto
   - Publishes to the queue via grpcq publisher

3. Since it's async:
   - The method returns immediately
   - The response parameter is not populated
   - Errors only reflect publishing failures, not processing failures

## Limitations

### Current Limitations

1. **No Responses**: Fire-and-forget only. Response values are not returned to callers.

2. **Streaming Not Supported**: Only unary RPCs are supported. Streaming methods will return errors.

3. **No Interceptors**: gRPC interceptors are not invoked in async mode.

4. **Metadata Handling**: Call options and metadata are not fully preserved in async mode.

### Future Enhancements

- **Request-Response Pattern**: Support for async request-response using response topics
- **Streaming Support**: Enable streaming over queues with chunking
- **Full Metadata**: Preserve all gRPC metadata in queue messages
- **Interceptor Support**: Call interceptors even in async mode
- **Timeout Handling**: Proper context deadline propagation

## Type Safety

The package uses Go generics for type-safe method wrapping:

```go
func WrapUnaryMethod[TReq proto.Message, TResp proto.Message](
    methodFunc func(context.Context, TReq) (TResp, error),
    newRequest func() TReq,
) func(context.Context, *pb.Message) error
```

This ensures:
- Compile-time type checking
- No runtime type assertions needed
- Clear API for method signatures

## Error Handling

### Server Side

Errors returned from your gRPC method implementation are handled as follows:

- **nil**: Message is acked (successfully processed)
- **error**: Message is nacked (will be retried according to queue policy)

gRPC status codes are preserved in the error message.

### Client Side

Errors from `AsyncClientConn.Invoke` indicate:
- Publishing failures (queue unavailable, message too large, etc.)
- Invalid method names
- Marshaling errors

Errors do NOT indicate processing failures (since that happens later, asynchronously).

## Testing

### Testing Synchronous Mode

Use standard gRPC testing patterns:

```go
import "google.golang.org/grpc/test/bufconn"

// Create in-memory gRPC connection
listener := bufconn.Listen(1024 * 1024)
grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, svc)

go grpcServer.Serve(listener)

// Connect client
conn, _ := grpc.DialContext(ctx, "", grpc.WithContextDialer(
    func(context.Context, string) (net.Conn, error) {
        return listener.Dial()
    }))

client := userpb.NewUserServiceClient(conn)
// Test client calls
```

### Testing Asynchronous Mode

Use the memory adapter:

```go
import "github.com/pbdeuchler/grpcq/go/adapters/memory"

// Create in-memory queue
adapter := memory.NewAdapter(100)

// Setup worker
registry := core.NewRegistry()
registry.Register("service", "Method",
    grpcadapter.WrapUnaryMethod(svc.Method, newRequest))
worker := core.NewWorker(adapter, registry, config)
go worker.Start(ctx)

// Setup client
publisher := core.NewPublisher(adapter, "test")
clientAdapter := grpcadapter.NewClientAdapter(publisher, "queue")
client := userpb.NewUserServiceClient(clientAdapter.Conn())

// Test async calls
// Use channels or sleeps to wait for processing
```

## Examples

See the [User Service Example](../examples/userservice/) for a complete working example demonstrating:
- Service implementation
- Synchronous gRPC server
- Synchronous gRPC client
- Asynchronous worker
- Asynchronous publisher
- Multiple modes in one binary

## Migration Guide

### From gRPC to grpcq

1. **No Code Changes**: Your service implementation stays the same

2. **Change Server Setup**:
   ```go
   // Before
   grpcServer := grpc.NewServer()
   userpb.RegisterUserServiceServer(grpcServer, svc)
   grpcServer.Serve(lis)

   // After
   registry.Register("service", "Method",
       grpcadapter.WrapUnaryMethod(svc.Method, newRequest))
   worker := core.NewWorker(adapter, registry, config)
   worker.Start(ctx)
   ```

3. **Change Client Setup**:
   ```go
   // Before
   conn, _ := grpc.Dial("host:port")
   client := userpb.NewUserServiceClient(conn)

   // After
   clientAdapter := grpcadapter.NewClientAdapter(publisher, "queue")
   client := userpb.NewUserServiceClient(clientAdapter.Conn())
   ```

4. **Handle Response Changes**: Update caller code to handle fire-and-forget semantics

### Gradual Rollout

1. **Phase 1**: Deploy both sync and async versions
2. **Phase 2**: Migrate non-critical traffic to async
3. **Phase 3**: Monitor and tune worker configuration
4. **Phase 4**: Migrate remaining traffic
5. **Phase 5**: Decommission sync endpoints

## Best Practices

1. **Service Names**: Use the full proto package path for service names

2. **Error Handling**: Always check errors from client calls (publishing failures)

3. **Idempotency**: Make handlers idempotent since messages may be retried

4. **Context**: Respect context cancellation in your implementations

5. **Testing**: Test both modes to ensure behavioral consistency

6. **Observability**: Add logging and metrics to track processing

7. **Configuration**: Use environment variables to switch between modes

## API Reference

### Types

- `AsyncClientConn`: Implements `grpc.ClientConnInterface` for async calls
- `ClientAdapter`: Wrapper for convenient client creation
- `MethodHandler`: (deprecated) Legacy method wrapper type
- `ServerAdapter`: (deprecated) Legacy server adapter type

### Functions

**`WrapUnaryMethod[TReq, TResp](methodFunc, newRequest) func`**
- Wraps a gRPC unary method for use with grpcq
- Returns a function compatible with `core.Handler`

**`NewClientAdapter(publisher, queueName) *ClientAdapter`**
- Creates a new client adapter
- Returns adapter that can create gRPC-compatible connections

**`NewAsyncClientConn(publisher, queueName) *AsyncClientConn`**
- Creates a new async client connection
- Returns a `grpc.ClientConnInterface` implementation

**`(c *ClientAdapter) Conn() grpc.ClientConnInterface`**
- Returns a connection for use with gRPC client constructors

**`(c *AsyncClientConn) Invoke(ctx, method, args, reply, opts...) error`**
- Implements gRPC Invoke for unary calls
- Publishes message and returns immediately

**`(c *AsyncClientConn) NewStream(...) (grpc.ClientStream, error)`**
- Not supported, returns error

## Contributing

Contributions welcome! Areas for improvement:

- Request-response pattern support
- Better metadata handling
- Streaming support
- Performance optimizations
- Additional testing utilities

## License

MIT

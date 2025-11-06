# protoc-gen-grpcq

A protoc plugin that generates grpcq client and server stubs from Protocol Buffer service definitions.

## Overview

`protoc-gen-grpcq` generates Go code for services defined in `.proto` files, enabling queue-based asynchronous RPC using the same service interfaces as standard gRPC. The generated code provides:

- Server interfaces and registration functions
- Client interfaces and constructors
- Full compatibility with existing gRPC service implementations

## Installation

```bash
cd cmd/protoc-gen-grpcq
go install
```

Or build directly:

```bash
go build -o $GOPATH/bin/protoc-gen-grpcq
```

## Usage

### Basic Usage

Generate grpcq code alongside your regular gRPC code:

```bash
# Generate gRPC code
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       your_service.proto

# Generate grpcq code
protoc --grpcq_out=. --grpcq_opt=paths=source_relative \
       your_service.proto
```

### Makefile Example

```makefile
.PHONY: proto
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       --grpcq_out=. --grpcq_opt=paths=source_relative \
	       proto/*.proto
```

## Generated Code

For a service defined as:

```protobuf
syntax = "proto3";

package userservice;

service UserService {
  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse);
  rpc GetUser(GetUserRequest) returns (GetUserResponse);
}
```

The plugin generates:

### Server Side

```go
// Interface (compatible with gRPC interface)
type UserServiceQServer interface {
    CreateUser(context.Context, *CreateUserRequest) (*CreateUserResponse, error)
    GetUser(context.Context, *GetUserRequest) (*GetUserResponse, error)
}

// Registration function
func RegisterUserServiceQServer(
    adapter grpcq.QueueAdapter,
    srv UserServiceQServer,
    opts ...grpcq.ServerOption,
) *grpcq.Server
```

### Client Side

```go
// Interface
type UserServiceQClient interface {
    CreateUser(ctx context.Context, in *CreateUserRequest, opts ...grpcq.CallOption) (*CreateUserResponse, error)
    GetUser(ctx context.Context, in *GetUserRequest, opts ...grpcq.CallOption) (*GetUserResponse, error)
}

// Constructor
func NewUserServiceQClient(
    adapter grpcq.QueueAdapter,
    opts ...grpcq.ClientOption,
) UserServiceQClient
```

## Using Generated Code

### Server Implementation

Your service implementation works with both gRPC and grpcq:

```go
type UserService struct {
    // Embed gRPC unimplemented server for compatibility
    userpb.UnimplementedUserServiceServer
    // ... your fields ...
}

func (s *UserService) CreateUser(ctx context.Context, req *userpb.CreateUserRequest) (*userpb.CreateUserResponse, error) {
    // Your implementation
    return &userpb.CreateUserResponse{...}, nil
}
```

### Running as gRPC Server (Synchronous)

```go
svc := &UserService{}

// Standard gRPC registration
grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, svc)
grpcServer.Serve(listener)
```

### Running as grpcq Server (Asynchronous)

```go
svc := &UserService{}
adapter := memory.NewAdapter(1000)

// grpcq registration - uses generated function
server := userpb.RegisterUserServiceQServer(
    adapter,
    svc,
    grpcq.WithQueueName("user-queue"),
    grpcq.WithConcurrency(10),
)

server.Start(ctx)
```

### Client Usage

#### gRPC Client (Synchronous)

```go
conn, _ := grpc.Dial("localhost:50051")
client := userpb.NewUserServiceClient(conn)

// Synchronous call
resp, err := client.CreateUser(ctx, &userpb.CreateUserRequest{...})
```

#### grpcq Client (Asynchronous)

```go
adapter := memory.NewAdapter(1000)

// Uses generated constructor
client := userpb.NewUserServiceQClient(
    adapter,
    grpcq.WithClientQueueName("user-queue"),
    grpcq.WithOriginator("my-service"),
)

// Same interface, but async!
resp, err := client.CreateUser(ctx, &userpb.CreateUserRequest{...})
// Returns immediately (fire-and-forget)
```

## Naming Conventions

To avoid conflicts with standard gRPC generated code, grpcq generated types use a "Q" suffix:

| gRPC Generated | grpcq Generated |
|----------------|-----------------|
| `UserServiceServer` | `UserServiceQServer` |
| `UserServiceClient` | `UserServiceQClient` |
| `RegisterUserServiceServer()` | `RegisterUserServiceQServer()` |
| `NewUserServiceClient()` | `NewUserServiceQClient()` |

This allows both gRPC and grpcq code to coexist in the same package.

## Options

The plugin supports the same options as `protoc-gen-go-grpc`:

### paths

Specifies how import paths are generated:

```bash
# Use source-relative paths (recommended)
protoc --grpcq_out=. --grpcq_opt=paths=source_relative your.proto

# Use import paths
protoc --grpcq_out=. --grpcq_opt=paths=import your.proto
```

### require_unimplemented_servers

Controls whether unimplemented server stubs are generated (not used in grpcq):

```bash
protoc --grpcq_out=. --grpcq_opt=require_unimplemented_servers=false your.proto
```

## Complete Example

See [go/examples/userservice](../../go/examples/userservice/) for a complete working example.

## Requirements

- Go 1.18 or later (for generics support)
- `protoc` compiler
- `google.golang.org/protobuf/compiler/protogen`

## How It Works

1. The plugin reads Protocol Buffer service definitions via `protoc`
2. For each service, it generates:
   - A server interface matching the gRPC interface
   - A registration function that wraps methods for queue processing
   - A client interface with the same methods as gRPC
   - A client implementation that publishes to queues

3. The generated code uses the `grpcq` package for runtime support:
   - Server registration and message routing
   - Client publishing and call options
   - Queue adapter interface

## Limitations

Current limitations of the generated code:

1. **Fire-and-forget only**: Client calls don't wait for responses
2. **Unary RPCs only**: Streaming RPCs are not supported
3. **No interceptors**: gRPC interceptors don't apply to queue calls
4. **Limited metadata**: Some gRPC metadata may not be preserved

Future enhancements will address these limitations.

## Development

### Building the Plugin

```bash
go build -o protoc-gen-grpcq
```

### Testing

Generate code for test proto files:

```bash
protoc --grpcq_out=testdata --grpcq_opt=paths=source_relative testdata/test.proto
```

## Contributing

Contributions are welcome! Areas for improvement:

- Support for streaming RPCs
- Request-response patterns
- Better error messages
- Code generation tests
- Support for more protobuf features

## License

MIT

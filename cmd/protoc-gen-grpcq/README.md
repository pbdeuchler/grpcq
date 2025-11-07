# protoc-gen-grpcq

Protoc plugin that generates grpcq client and server code from `.proto` files.

## Installation

```bash
go install github.com/pbdeuchler/grpcq/cmd/protoc-gen-grpcq@latest
```

## Usage

```bash
protoc --go_out=. --go-grpc_out=. --grpcq_out=. your_service.proto
```

## Generated Code

For a service like:
```protobuf
service UserService {
  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse);
}
```

The plugin generates:

### Server (Consumer)
```go
// Interface compatible with gRPC
type UserServiceConsumer interface {
    CreateUser(context.Context, *CreateUserRequest) (*CreateUserResponse, error)
}

// Registration function
func RegisterUserServiceConsumer(adapter, srv, opts...) *grpcq.Server
```

### Client (Producer)
```go
// Interface
type UserServiceProducer interface {
    CreateUser(ctx, req, opts...) (*CreateUserResponse, error)
}

// Constructor
func NewUserServiceProducer(adapter, opts...) UserServiceProducer
```

## Using Generated Code

### Same Service Implementation
```go
type UserService struct {
    userpb.UnimplementedUserServiceServer
}

func (s *UserService) CreateUser(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
    // Your business logic here
    return &CreateUserResponse{UserId: "123"}, nil
}
```

### Run as gRPC
```go
grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, &UserService{})
grpcServer.Serve(listener)
```

### Run as Queue Consumer
```go
adapter := sqs.NewAdapter(client, queueURL)
server := userpb.RegisterUserServiceConsumer(adapter, &UserService{})
server.Start(ctx)
```

## Naming Convention

| gRPC | grpcq | Role |
|------|-------|------|
| `UserServiceServer` | `UserServiceConsumer` | Processes messages |
| `UserServiceClient` | `UserServiceProducer` | Sends messages |

## Options

- `paths=source_relative` - Use source-relative import paths (recommended)
- `paths=import` - Use Go import paths

## Complete Example

See [go/examples/userservice](../../go/examples/userservice/)

## Limitations

- Fire-and-forget only (no request-response yet)
- Unary RPCs only (no streaming)
- No interceptor support
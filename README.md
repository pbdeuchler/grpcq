# grpcq

Convert gRPC services to async queue-based architectures with minimal code changes.

## Overview

grpcq lets you use the same service implementation for both synchronous gRPC and asynchronous queue-based communication. Write your service once, deploy it either way.

> [!NOTE]
> This project was mostly built with LLMs to test the new Claude Code Web product. The code here has not been tested in production or rigorously reviewed (yet). Use at your own risk.

## Features

- **Protocol Buffer code generation** via `protoc-gen-grpcq`
- **Multiple queue backends** (SQS, Kafka, RabbitMQ via adapters)
- **Same service code** works for both gRPC and queue modes
- **Built-in validation**, retry logic, and graceful shutdown
- **In-memory adapter** for testing

## Installation

```bash
go get github.com/pbdeuchler/grpcq
go install github.com/pbdeuchler/grpcq/cmd/protoc-gen-grpcq@latest
```

## Usage

### 1. Generate Code

```bash
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       --grpcq_out=. --grpcq_opt=paths=source_relative \
       your_service.proto
```

### 2. Implement Your Service

```go
type UserService struct {
    userpb.UnimplementedUserServiceServer
}

func (s *UserService) CreateUser(ctx context.Context, req *userpb.CreateUserRequest) (*userpb.CreateUserResponse, error) {
    // Your business logic here
    return &userpb.CreateUserResponse{UserId: "123"}, nil
}
```

### 3. Run as gRPC or Queue Mode

**Traditional gRPC:**

```go
grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, &UserService{})
grpcServer.Serve(listener)
```

**Queue Mode (async):**

```go
adapter := sqs.NewAdapter(sqsClient, queueURL)  // or memory.NewAdapter() for testing
server := userpb.RegisterUserServiceConsumer(adapter, &UserService{})
server.Start(ctx)
```

### 4. Client Usage

**gRPC Client:**

```go
client := userpb.NewUserServiceClient(grpcConn)
resp, _ := client.CreateUser(ctx, req)
```

**Queue Producer:**

```go
producer := userpb.NewUserServiceProducer(adapter)
producer.CreateUser(ctx, req)  // Fire-and-forget
```

## Queue Adapters

### AWS SQS

```go
import sqsadapter "github.com/pbdeuchler/grpcq/go/adapters/sqs"

cfg, _ := config.LoadDefaultConfig(ctx)
adapter := sqsadapter.NewAdapter(sqs.NewFromConfig(cfg), queueURL)
```

### In-Memory (Testing)

```go
import "github.com/pbdeuchler/grpcq/go/adapters/memory"

adapter := memory.NewAdapter(1000)  // buffer size
```

### Custom Adapter

Implement the `QueueAdapter` interface:

```go
type QueueAdapter interface {
    Publish(ctx context.Context, queueName string, messages ...*pb.Message) error
    Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error)
}
```

## Examples

- [User Service](go/examples/userservice/) - Complete working example

```bash
cd go/examples/userservice
go run main.go  # Runs async demo with in-memory queue
```

## Project Structure

```
grpcq/
├── cmd/protoc-gen-grpcq/   # Protoc plugin
├── go/
│   ├── core/               # Core types (Producer, Consumer, Registry)
│   ├── grpcq/              # Runtime for generated code
│   ├── adapters/           # Queue adapters (memory, SQS, etc.)
│   └── examples/           # Example services
└── proto/                  # Protocol definitions
```

## Development

```bash
make proto  # Generate protobuf code
make test   # Run tests
make build  # Build all packages
```

## License

MIT. See [LICENSE](LICENSE) file for details.

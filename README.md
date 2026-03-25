# grpcq

grpcq turns gRPC service definitions into async, queue-driven architectures. You define your service with Protocol Buffers, implement it once, and deploy it as either a traditional gRPC server or an async queue consumer -- same business logic, no code changes.

A protoc plugin (`protoc-gen-grpcq`) generates typed producer and consumer stubs from your `.proto` files. Messages are serialized into a language-agnostic protobuf envelope and routed through any queue backend that implements the `QueueAdapter` interface. The runtime handles concurrent message processing, batching, graceful shutdown, and dead-letter semantics (ack/nack).

Runtimes exist for **Go**, **Rust**, and **TypeScript**. The wire format (a `grpcq.Message` proto) is shared across all three, so a Go producer can publish to a queue consumed by a Rust worker.

> **Status:** This project is experimental. It has not been tested in production or rigorously reviewed. Use at your own risk.

## How It Works

```
                  ┌──────────────────┐
  Proto file ───> │ protoc-gen-grpcq │ ───> Generated Producer + Consumer stubs
                  └──────────────────┘

  Producer ──publish──> [ Queue ] ──consume──> Consumer ──> Your service handler
  (fire-and-forget)     SQS / Memory / ...     (Worker)
```

Each gRPC method becomes a queue action. The generated `Producer` serializes the request proto into a `grpcq.Message` envelope (originator, topic, action, payload, metadata) and publishes it to a queue. On the other side, a `Worker` consumes messages, looks up the handler by topic+action in a `Registry`, deserializes the payload, and calls your service method.

Responses are not routed back -- this is a fire-and-forget model suited for commands, events, and async task dispatch.

## Installation

### Go

```bash
go get github.com/pbdeuchler/grpcq
go install github.com/pbdeuchler/grpcq/cmd/protoc-gen-grpcq@latest
```

### Rust

```toml
# Cargo.toml
[dependencies]
grpcq = { git = "https://github.com/pbdeuchler/grpcq", path = "rust" }

# Enable the SQS adapter:
grpcq = { git = "https://github.com/pbdeuchler/grpcq", path = "rust", features = ["sqs"] }
```

### TypeScript

```bash
npm install grpcq   # or reference the local typescript/ directory
```

## Quick Start (Go)

### 1. Define your service

```protobuf
// user.proto
syntax = "proto3";
package userservice;

service UserService {
  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse);
  rpc GetUser(GetUserRequest) returns (GetUserResponse);
}

message CreateUserRequest {
  string name = 1;
  string email = 2;
}

message CreateUserResponse {
  string user_id = 1;
  string name = 2;
  string email = 3;
}
```

### 2. Generate code

```bash
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       --grpcq_out=. --grpcq_opt=paths=source_relative \
       user.proto
```

This produces `user_grpcq.pb.go` alongside the standard gRPC stubs. It contains a `RegisterUserServiceConsumer` function (for consuming) and a `NewUserServiceProducer` function (for publishing).

### 3. Implement your service (once)

```go
type UserService struct {
    userpb.UnimplementedUserServiceServer
}

func (s *UserService) CreateUser(ctx context.Context, req *userpb.CreateUserRequest) (*userpb.CreateUserResponse, error) {
    userID := generateID()
    return &userpb.CreateUserResponse{UserId: userID, Name: req.Name, Email: req.Email}, nil
}
```

This implementation satisfies both the gRPC `UserServiceServer` interface and the grpcq `UserServiceConsumer` interface.

### 4a. Deploy as a traditional gRPC server

```go
grpcServer := grpc.NewServer()
userpb.RegisterUserServiceServer(grpcServer, &UserService{})
grpcServer.Serve(listener)
```

### 4b. Deploy as an async queue consumer

```go
adapter := memory.NewAdapter(1000) // or sqsadapter.NewAdapter(...)

server := userpb.RegisterUserServiceConsumer(
    adapter,
    &UserService{},
    grpcq.WithQueueName("user-queue"),
    grpcq.WithConcurrency(5),
    grpcq.WithPollInterval(100),
)

server.Start(ctx) // blocks until ctx is cancelled or server.Stop() is called
```

### 5. Publish messages

```go
producer := userpb.NewUserServiceProducer(
    adapter,
    grpcq.WithClientQueueName("user-queue"),
    grpcq.WithOriginator("api-gateway"),
)

err := producer.CreateUser(ctx, &userpb.CreateUserRequest{
    Name:  "Alice",
    Email: "alice@example.com",
})
```

The producer has the same method signatures as a gRPC client, minus the response return value.

## Queue Adapters

All adapters implement a two-method interface:

```go
type QueueAdapter interface {
    Publish(ctx context.Context, queueName string, messages ...*pb.Message) error
    Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error)
}
```

### AWS SQS

```go
import sqsadapter "github.com/pbdeuchler/grpcq/go/adapters/sqs"

cfg, _ := config.LoadDefaultConfig(ctx)
adapter, _ := sqsadapter.NewAdapter(sqsadapter.Config{
    Client: sqs.NewFromConfig(cfg),
    QueueURLs: map[string]string{
        "user-queue": "https://sqs.us-east-1.amazonaws.com/123456789/user-queue",
    },
})
```

SQS messages are base64-encoded protobuf. The adapter handles batching (up to 10 per SQS API call), long polling (20s), and visibility timeout management for nack.

### In-Memory

```go
import "github.com/pbdeuchler/grpcq/go/adapters/memory"

adapter := memory.NewAdapter(1000) // channel buffer size per queue
```

Channel-backed, suitable for tests and local development. Supports `QueueDepth()` and `Clear()` for test assertions.

### Custom

Implement `QueueAdapter`. The `Receipt` returned with each consumed message must support `Ack()` (delete from queue) and `Nack()` (requeue for retry):

```go
type Receipt interface {
    Ack(ctx context.Context) error
    Nack(ctx context.Context) error
}
```

## Rust

The Rust runtime mirrors Go's architecture. It is async-runtime-agnostic (uses `futures` traits, no Tokio dependency).

```rust
use grpcq::{Producer, Worker, WorkerConfig, Registry, CancellationToken};
use grpcq::adapters::memory::MemoryAdapter;
use std::sync::Arc;

// Produce
let adapter = Arc::new(MemoryAdapter::new(1000));
let producer = Producer::new(adapter.clone(), "my-service");
producer.send("user-queue", "userservice.UserService", "CreateUser", &request, Default::default()).await?;

// Consume
let registry = Registry::new();
registry.register("userservice.UserService", "CreateUser", |msg| async move {
    // handle message
    Ok(())
});

let config = WorkerConfig::new("user-queue").with_concurrency(5);
let worker = Worker::new(adapter, registry, config);
let token = CancellationToken::new();
worker.start(token).await?;
```

Enable the `sqs` feature for AWS SQS support.

## TypeScript

```typescript
import { Producer, Worker, Registry, Server } from "grpcq";
import { MemoryAdapter } from "grpcq/adapters";

// Produce
const adapter = new MemoryAdapter(1000);
const producer = new Producer(adapter, "my-service");
await producer.send("user-queue", "userservice.UserService", "CreateUser", payload);

// Consume
const registry = new Registry();
registry.register("userservice.UserService", "CreateUser", async (msg) => {
    // handle message
});

const server = new Server({ adapter, registry, queueName: "user-queue", concurrency: 5 });
await server.start();
```

## Worker Pools

For horizontal scaling within a single process, use `WorkerPool` to run multiple concurrent consumers:

```go
pool := core.NewWorkerPool(adapter, registry, config, 4) // 4 workers
pool.Start(ctx)
```

Each worker independently polls, processes messages concurrently (up to the configured concurrency limit), and drains in-flight work on shutdown.

## Message Format

All messages use the `grpcq.Message` protobuf envelope:

```protobuf
message Message {
  string originator = 1;   // sender identity (service name, instance ID)
  string topic = 2;        // gRPC service name, e.g. "userservice.UserService"
  string action = 3;       // gRPC method name, e.g. "CreateUser"
  bytes payload = 4;       // serialized request proto
  string message_id = 5;   // UUID
  int64 timestamp_ms = 6;  // creation time (Unix ms)
  map<string, string> metadata = 7;  // tracing, correlation IDs, etc.
}
```

Payload size is capped at 256KB (SQS compatibility). The topic+action pair is used for handler routing.

## Project Structure

```
grpcq/
├── cmd/protoc-gen-grpcq/   # protoc plugin (Go codegen)
├── proto/                   # grpcq.Message wire format definition
├── go/
│   ├── core/               # QueueAdapter, Producer, Worker, Registry, WorkerPool
│   ├── grpcq/              # Server/Client runtime for generated code
│   ├── adapters/           # memory, SQS
│   └── examples/           # userservice demo
├── rust/                   # Rust runtime + adapters
└── typescript/             # TypeScript runtime + adapters
```

## Development

```bash
make proto          # regenerate grpcq.Message proto
make proto-example  # regenerate userservice example proto
make test           # run Go tests
make example        # build the example binary
make run-example    # build and run the demo
```

## License

MIT

# grpcq

grpcq turns gRPC service definitions into async, queue-driven architectures. You define your service with Protocol Buffers, implement it once, and deploy it as either a traditional gRPC server or an async queue consumer -- same business logic, no code changes.

A protoc plugin (`protoc-gen-grpcq`) generates typed producer and consumer stubs from your `.proto` files. Messages are serialized into a language-agnostic protobuf envelope and routed through any queue backend that implements the `QueueAdapter` interface. The runtime handles concurrent message processing, batching, graceful shutdown, and acknowledgement.

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

A successful producer call confirms queue acceptance. Handler execution and acknowledgement happen later; their failures are handled by the consumer.

## Installation

### Go

```bash
go get github.com/pbdeuchler/grpcq
go install github.com/pbdeuchler/grpcq/cmd/protoc-gen-grpcq@latest
```

### Rust

```toml
# The Rust crates currently live in this repository's `rust/` workspace.
# The examples and tests consume them as path dependencies from a checkout.
[dependencies]
grpcq = { path = "../path/to/grpcq/rust" }
prost = "0.13"

[build-dependencies]
grpcq-build = { path = "../path/to/grpcq/rust/grpcq-build" }
```

Add `features = ["sqs"]` to `grpcq` for the AWS SQS adapter. Add `features = ["tonic"]` to both `grpcq` and `grpcq-build` to generate a tonic server and queue consumer that share one service trait.

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

adapter := memory.NewAdapter(1000) // unacknowledged message capacity per queue
```

Suitable for tests and local development. Capacity includes queued and in-flight messages until acknowledgement, so a failed message always has room to return to its queue. `QueueDepth()` counts available messages. `Clear()` discards available messages while preserving outstanding receipts and their capacity reservations. Rust and TypeScript memory adapters use the same rules.

### Custom

Implement `QueueAdapter`. The `Receipt` returned with each consumed message must support `Ack()` (delete from queue) and `Nack()` (requeue for retry):

```go
type Receipt interface {
    Ack(ctx context.Context) error
    Nack(ctx context.Context) error
}
```

## Rust

The Rust codebase currently has two layers:

- `grpcq`: the runtime crate. `Client` and `Server` are the higher-level entry points; `Producer`, `Registry`, `Worker`, and `WorkerPool` are still exposed for lower-level wiring.
- `grpcq-build`: a build-time crate that wraps `prost-build` and generates typed `{service}_consumer` and `{service}_producer` modules from unary RPC services.

The core runtime is futures-based and does not require Tokio unless you opt into the `tonic` feature.

### Rust Typed Codegen

`grpcq-build` is the main typed Rust story in the repo today. It generates:

- a `{service}_consumer` module with an async trait and a `*Consumer` wrapper that implements `grpcq::ServiceRegistrar`
- a `{service}_producer` module with typed fire-and-forget producer methods

```rust
// build.rs
fn main() -> std::io::Result<()> {
    grpcq_build::compile_protos(&["proto/greeter.proto"], &["proto"])?;
    Ok(())
}
```

```rust
// src/lib.rs
pub mod generated {
    include!(concat!(env!("OUT_DIR"), "/examples.greeter.rs"));
}
```

```rust
use std::{sync::Arc, thread, time::Duration};

use futures::executor::block_on;
use grpcq::{
    adapters::memory, CancellationToken, ClientConfig, Server, ServerConfig, SharedAdapter,
};

pub mod generated {
    include!(concat!(env!("OUT_DIR"), "/examples.greeter.rs"));
}

use generated::{
    greeter_consumer::{Greeter, GreeterConsumer},
    greeter_producer::GreeterProducer,
    HelloReply, HelloRequest,
};

struct GreeterService;

#[grpcq::async_trait]
impl Greeter for GreeterService {
    async fn say_hello(&self, req: HelloRequest) -> grpcq::Result<HelloReply> {
        Ok(HelloReply {
            message: format!("hello {}", req.name),
        })
    }
}

fn main() -> grpcq::Result<()> {
    let adapter: SharedAdapter = Arc::new(memory::Adapter::new(16));
    let cancellation = CancellationToken::new();
    let server_token = cancellation.clone();

    let server = Server::builder(
        adapter.clone(),
        ServerConfig::default()
            .with_queue_name("examples")
            .with_poll_interval(Duration::from_millis(10)),
    )
    .add_service(GreeterConsumer::new(GreeterService));

    let handle = thread::spawn(move || block_on(server.serve(server_token)));

    let producer = GreeterProducer::new(
        adapter,
        ClientConfig::default()
            .with_queue_name("examples")
            .with_originator("typed-greeter"),
    );

    block_on(producer.say_hello(HelloRequest {
        name: "world".to_string(),
    }))?;

    cancellation.cancel();
    let _ = handle.join();
    Ok(())
}
```

This is the same pattern used by `rust/examples/typed-greeter` and by the `grpcq-build` integration fixtures when they are run separately.

### What The Generated API Does

- Generated producer methods are fire-and-forget and return `grpcq::Result<()>`.
- Generated consumer traits preserve the proto response type so a service implementation can match a gRPC-style signature, but queue delivery does not send that response anywhere.
- `grpcq-build` only supports unary RPC methods today. Streaming RPCs are rejected at codegen time.

### Lower-Level Runtime API

If you do not want code generation, the lower-level runtime is still available:

- `Client::invoke` and `CallOptions` for typed request publishing against explicit service and method names
- `Producer::send` and `Producer::send_batch` for manual envelope publishing via `MessageSpec`
- `Server::register_method` for decoding a typed request and handling it with access to `HandlerContext`
- `Registry`, `Worker`, and `WorkerPool` for manual routing and worker orchestration

The in-memory adapter is `grpcq::adapters::memory::Adapter`. The AWS SQS adapter lives behind the `sqs` feature as `grpcq::adapters::sqs::Adapter`.

### tonic Compatibility

Enable the `tonic` feature on both `grpcq` and `grpcq-build` to generate tonic's `{service}_server` module alongside the queue producer and consumer. Implement the tonic server trait once: the consumer re-exports that same trait and accepts the same implementation. The build script still makes just one `grpcq_build::compile_protos` call.

Generated tonic code also requires a direct `tonic` dependency:

```toml
[dependencies]
grpcq = { path = "../path/to/grpcq/rust", features = ["tonic"] }
tonic = { version = "0.12", default-features = false, features = ["codegen", "prost", "transport"] }

[build-dependencies]
grpcq-build = { path = "../path/to/grpcq/rust/grpcq-build", features = ["tonic"] }
```

For example, one `Arc<GreeterService>` can be passed to both `greeter_server::GreeterServer::from_arc` and `greeter_consumer::GreeterConsumer::from_arc`. The integration fixture in `rust/grpcq-build/tests/fixtures/tonic-fixture` exercises both paths, including tonic's codecs and generated server routing.

Applications that previously implemented both the grpcq and tonic traits should keep only the tonic implementation and remove the separate `tonic_build` invocation. Queue producer methods still return only queue acceptance; they do not return the service response.

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

Stopping a worker or cancelling its run stops intake. Every message in an already received batch finishes processing, and the worker waits for its ack or nack attempt before returning. In Go, handler contexts retain their values during this drain; cancelling the run does not cancel handlers or receipt settlement. A deadline on `Stop(ctx)` bounds the caller's wait while draining continues. `Server.Stop()` waits for the drain.

TypeScript adapters receive an optional `AbortSignal` in `consume` and must use it to interrupt pending I/O. A returned batch remains the worker's responsibility even if that signal is aborted. Rust callers must keep polling the worker or pool's start future through shutdown; dropping the future does not perform a graceful drain.

Handler and receipt failures stop intake. A failed handler is nacked, the received batch drains, and the run returns an error with the message ID and route. If nack also fails, the error retains both causes. Processing failures remain visible during cancellation. Concurrent failures return the first observed error; an already observed receive error takes precedence.

The runtime does not retry failed handlers within the same run or delete poison messages. Applications decide when to restart consumers; retry limits and dead-letter routing belong to the queue backend. SQS receipts forward settlement attempts to SQS without caching local success, so failed operations can be attempted again. A failed acknowledgement can leave completed work eligible for redelivery; handlers must tolerate duplicates.

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
├── rust/                   # Rust runtime, adapters, codegen crate, examples
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

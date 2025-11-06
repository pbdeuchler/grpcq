# grpcq

An async gRPC queue abstraction library for building scalable, queue-based microservices across multiple languages.

## Overview

grpcq enables you to convert traditional synchronous gRPC services into asynchronous, queue-based architectures. It provides a simple abstraction layer that works with multiple message queue systems (SQS, Kafka, RabbitMQ, etc.) and supports multiple programming languages (Go, Python, Rust, TypeScript).

### Key Features

- **Queue Agnostic**: Pluggable adapters for SQS, Kafka, RabbitMQ, Redis Streams, and more
- **Multi-Language**: Shared protocol enables cross-language communication
- **Type-Safe**: Uses Protocol Buffers for message serialization
- **Production-Ready**: Built-in retry logic, error handling, and graceful shutdown
- **Testable**: Includes in-memory adapter for testing

### When to Use grpcq

✅ **Good Fit:**

- Fire-and-forget operations
- High-volume background processing
- Services that need independent scaling
- Cross-language microservice communication
- Systems requiring queue-level retry and DLQ handling

❌ **Not Recommended:**

- Request-response patterns requiring immediate answers
- Real-time interactive APIs
- Streaming operations

## Quick Start

### Installation

```bash
go get github.com/pbdeuchler/grpcq
```

### Run the Example

```bash
cd go/examples/userservice
go run main.go
```

## Usage

### Publisher (Client Side)

```go
package main

import (
    "context"
    "github.com/pbdeuchler/grpcq/go/core"
    "github.com/pbdeuchler/grpcq/go/adapters/memory"
)

func main() {
    // Create adapter (in-memory for testing)
    adapter := memory.NewAdapter(1000)

    // Create publisher
    publisher := core.NewPublisher(adapter, "my-service")

    // Publish a message
    req := &YourProtoRequest{
        Field: "value",
    }

    metadata := map[string]string{
        "trace_id": "trace-12345",
    }

    err := publisher.Send(
        context.Background(),
        "queue-name",
        "service.ServiceName",
        "MethodName",
        req,
        metadata,
    )
}
```

### Worker (Server Side)

```go
package main

import (
    "context"
    "github.com/pbdeuchler/grpcq/go/core"
    "github.com/pbdeuchler/grpcq/go/adapters/memory"
    pb "github.com/pbdeuchler/grpcq/proto/grpcq"
    "google.golang.org/protobuf/proto"
)

func main() {
    // Create adapter
    adapter := memory.NewAdapter(1000)

    // Create registry and register handlers
    registry := core.NewRegistry()
    registry.Register("service.ServiceName", "MethodName", handleMethod)

    // Create and start worker
    config := core.DefaultWorkerConfig("queue-name")
    worker := core.NewWorker(adapter, registry, config)

    worker.Start(context.Background())
}

func handleMethod(ctx context.Context, msg *pb.Message) error {
    // Deserialize payload
    var req YourProtoRequest
    if err := proto.Unmarshal(msg.Payload, &req); err != nil {
        return err
    }

    // Process the request
    // ...

    return nil // or return error to nack
}
```

### Using SQS Adapter

```go
import (
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/sqs"
    sqsadapter "github.com/pbdeuchler/grpcq/go/adapters/sqs"
)

// Load AWS config
cfg, err := config.LoadDefaultConfig(ctx)
if err != nil {
    log.Fatal(err)
}

// Create SQS client
client := sqs.NewFromConfig(cfg)

// Create SQS adapter
adapter, err := sqsadapter.NewAdapter(sqsadapter.Config{
    QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789/my-queue",
    Client:   client,
})

// Use adapter with publisher/worker
publisher := core.NewPublisher(adapter, "my-service")
```

## Architecture

### Components

```
┌─────────────┐
│  Publisher  │  Sends messages to queue
└──────┬──────┘
       │
       ▼
┌─────────────┐
│   Adapter   │  Queue implementation (SQS, Kafka, etc.)
└──────┬──────┘
       │
       ▼
┌─────────────┐
│   Worker    │  Consumes and processes messages
└──────┬──────┘
       │
       ▼
┌─────────────┐
│  Registry   │  Routes messages to handlers
└──────┬──────┘
       │
       ▼
┌─────────────┐
│   Handler   │  Processes individual messages
└─────────────┘
```

### Message Flow

1. **Publisher** serializes proto message and creates envelope
2. **Adapter** sends message to queue system
3. **Worker** polls queue and receives messages
4. **Registry** looks up handler for topic/action
5. **Handler** processes message and returns result
6. **Worker** acks (success) or nacks (failure) message

## Protocol

All messages use a shared protobuf definition that enables cross-language communication:

```protobuf
message Message {
  string originator = 1;      // sender identification
  string topic = 2;            // maps to gRPC service
  string action = 3;           // maps to gRPC method
  bytes payload = 4;           // serialized proto request
  string message_id = 5;       // unique identifier
  int64 timestamp_ms = 6;      // creation timestamp
  map<string, string> metadata = 7;  // headers, trace context
}
```

See [docs/protocol.md](docs/protocol.md) for detailed protocol specification.

## Development

### Build

```bash
make build
```

### Test

Note: Tests require proto files to be generated first.

```bash
make proto
make test
```

### Format Code

```bash
make fmt
```

### Clean

```bash
make clean
```

## Project Structure

```
grpcq/
├── proto/                      # Shared protocol definitions
│   ├── message.proto           # Core message proto
│   └── grpcq/                  # Generated Go code
├── go/                         # Go implementation
│   ├── core/                   # Core abstractions
│   │   ├── types.go            # Interfaces and types
│   │   ├── registry.go         # Handler registry
│   │   ├── publisher.go        # Message publisher
│   │   └── worker.go           # Message worker
│   ├── adapters/               # Queue adapters
│   │   ├── memory/             # In-memory (testing)
│   │   ├── sqs/                # AWS SQS
│   │   └── kafka/              # Apache Kafka (future)
│   └── examples/               # Example applications
│       └── userservice/        # User service example
├── python/                     # Python implementation (future)
├── rust/                       # Rust implementation (future)
├── docs/                       # Documentation
│   └── protocol.md             # Protocol specification
└── Makefile                    # Build tasks
```

## Adapters

### Available Adapters

| Adapter            | Status     | Package                |
| ------------------ | ---------- | ---------------------- |
| Memory (In-memory) | ✅ Ready   | `go/adapters/memory`   |
| AWS SQS            | ✅ Ready   | `go/adapters/sqs`      |
| Apache Kafka       | 🚧 Planned | `go/adapters/kafka`    |
| RabbitMQ           | 🚧 Planned | `go/adapters/rabbitmq` |
| Redis Streams      | 🚧 Planned | `go/adapters/redis`    |

### Creating a Custom Adapter

Implement the `QueueAdapter` interface:

```go
type QueueAdapter interface {
    Publish(ctx context.Context, queueName string, messages ...*pb.Message) error
    Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error)
}
```

See existing adapters for examples.

## Language Implementations

### Status

| Language   | Status     | Package       |
| ---------- | ---------- | ------------- |
| Go         | ✅ Ready   | `go/`         |
| Python     | 🚧 Planned | `python/`     |
| Rust       | 🚧 Planned | `rust/`       |
| TypeScript | 🚧 Planned | `typescript/` |

### Adding a New Language

1. Import generated proto code
2. Implement core interfaces (Registry, Publisher, Worker)
3. Create adapter for at least one queue system
4. Follow language-specific conventions
5. Ensure cross-language compatibility tests pass

See [implementation.md](implementation.md) for detailed implementation guide.

## Examples

- [User Service](go/examples/userservice/) - Complete example with publisher and worker

## Roadmap

### v1.0 (Current)

- [x] Go core implementation
- [x] Memory adapter
- [x] SQS adapter
- [x] Protocol documentation
- [x] Basic examples

### v1.1 (Planned)

- [ ] Kafka adapter
- [ ] Enhanced observability (metrics, tracing)
- [ ] Dead letter queue handling

### v2.0 (Future)

- [ ] Python implementation
- [ ] Rust implementation
- [ ] TypeScript implementation
- [ ] Request-response patterns
- [ ] Message routing and filtering

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Submit a pull request

## License

MIT. See [LICENSE](LICENSE) file for details.

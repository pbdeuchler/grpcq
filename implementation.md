# grpcq

## Async gRPC Queue Abstraction Implementation Plan

## Overview

Build a multi-language library that intercepts gRPC service calls and routes them through message queues for asynchronous processing. The design prioritizes simplicity, maintainability, and clear API boundaries while supporting multiple languages and queue implementations.

**Initial Implementation**: Go with SQS adapter  
**Future Implementations**: Python, Rust, TypeScript with adapters for Kafka, RabbitMQ, Redis Streams, etc.

## Repository Structure

```
/
├── proto/
│   └── message.proto          # Shared proto definition
├── go/
│   ├── core/                  # Core abstractions
│   │   ├── types.go
│   │   ├── registry.go
│   │   ├── worker.go
│   │   └── publisher.go
│   ├── adapters/              # Queue implementations
│   │   ├── sqs/
│   │   │   └── adapter.go
│   │   ├── memory/            # For testing
│   │   │   └── adapter.go
│   │   └── kafka/             # Future
│   │       └── adapter.go
│   └── examples/
├── python/                     # Future
│   ├── core/
│   └── adapters/
│       └── sqs/
├── rust/                       # Future
│   ├── core/
│   └── adapters/
│       └── sqs/
└── docs/
    └── protocol.md            # Language-agnostic protocol spec
```

## Core Design Principles

### 1. Language-Agnostic Protocol

The Message proto serves as the contract between all implementations. Any language that can serialize/deserialize this proto can participate.

### 2. Separation of Concerns

- **Protocol Layer**: Shared proto definition
- **Core Layer**: Language-specific routing and worker logic
- **Adapter Layer**: Queue-specific implementations
- **Application Layer**: Business logic handlers

## Shared Data Model

Define a single proto message that all languages use:

```proto
// proto/message.proto
syntax = "proto3";
package asyncgrpc;

message Message {
  string originator = 1;      // sender identification
  string topic = 2;            // maps to gRPC service
  string action = 3;           // maps to gRPC method
  bytes payload = 4;           // serialized proto request
  string message_id = 5;       // unique identifier
  int64 timestamp_ms = 6;      // creation timestamp
  map<string, string> metadata = 7;  // headers, trace context, etc.
}
```

## Go Implementation Design

### Package Structure

```
github.com/yourorg/asyncgrpc/go
├── core                       # Main package
├── adapters/sqs              # SQS adapter package
└── adapters/memory           # In-memory adapter for testing
```

### Core Interfaces (go/core)

#### Queue Adapter Interface

```
type QueueAdapter interface {
    // Publish sends messages to a queue
    Publish(ctx, queueName, ...*Message) error

    // Consume returns a batch of messages with their receipts
    Consume(ctx, queueName, maxBatch) (*ConsumeResult, error)
}

type ConsumeResult struct {
    Items []MessageItem
}

type MessageItem struct {
    Message *Message
    Receipt Receipt
}

type Receipt interface {
    Ack(ctx) error
    Nack(ctx) error
}
```

#### Service Registry

```
type Registry struct {
    // internal: map[topic][action]handler
}

Registry.Register(topic, action, handler)
Registry.Handle(ctx, *Message) error
```

### Publisher API

```
type Publisher struct {
    adapter QueueAdapter
    originator string
}

Publisher.Send(ctx, queueName, topic, action, protoMessage) error
```

### Worker API

```
type Worker struct {
    adapter QueueAdapter
    registry *Registry
    config WorkerConfig
}

Worker.Start(ctx) error
Worker.Stop(ctx) error
```

## Implementation Phases

### Phase 1: Shared Protocol

1. Define `proto/message.proto`
2. Create build scripts to generate code for each language
3. Document the protocol semantics in `docs/protocol.md`

### Phase 2: Go Core Implementation

1. **Package Setup**: Create `go/core` package structure
2. **Generated Code**: Import generated proto code
3. **Core Types**: Define QueueAdapter, Receipt, Registry interfaces
4. **Registry Implementation**:
   - Two-level map: topic → action → handler
   - Thread-safe registration
   - Clear error messages for missing handlers
5. **Worker Implementation**:
   - Polling loop with graceful shutdown
   - Concurrent message processing with semaphore
   - Proper context propagation

### Phase 3: Go Memory Adapter

Create `go/adapters/memory`:

- In-memory queue for testing
- Implements QueueAdapter interface
- Useful for unit tests and local development

### Phase 4: Go SQS Adapter

Create `go/adapters/sqs`:

1. **Dependencies**: AWS SDK v2 for Go
2. **Publish Implementation**:
   - SendMessageBatch with 10-message chunks
   - Message attributes for topic/action (for filtering)
3. **Consume Implementation**:
   - ReceiveMessage with long polling
   - Return SQSReceipt wrapper
4. **Receipt Implementation**:
   - Ack: DeleteMessage
   - Nack: ChangeMessageVisibility to 0
5. **Configuration**:
   - Let SQS handle retries, DLQ, visibility timeouts
   - Expose only SQS-specific settings (region, endpoint)

### Phase 5: Python Implementation (Future)

Mirror the Go structure in `python/`:

1. Core abstractions using Python protocols/ABCs
2. SQS adapter using boto3
3. Pythonic API following language conventions

### Phase 6: Additional Queue Adapters

For each new queue system:

1. Create new package/module in appropriate language
2. Implement the adapter interface
3. Add integration tests
4. Document queue-specific configuration

## Testing Strategy

### Protocol Tests

- Verify proto serialization/deserialization across languages
- Test message compatibility between publishers and consumers in different languages

### Language-Specific Tests

For each language implementation:

- Unit tests for core logic
- Integration tests with memory adapter
- Contract tests for each queue adapter

### Cross-Language Tests

- Go publisher → Python consumer
- Python publisher → Go consumer
- Mixed language processing of same queue

## Migration Path

To convert an existing gRPC service:

1. **Choose Implementation Language**: Select based on existing service language
2. **Install Library**: Import appropriate package/module
3. **Server Side**:
   - Replace gRPC server with worker
   - Register existing service methods as handlers
   - Configure queue adapter
4. **Client Side**:
   - Replace gRPC client with publisher
   - Handle async nature (no immediate response)

## Design Decisions

### Why Namespace by Language?

- Each language has its own idioms and package management
- Developers only need to import their language's implementation
- Allows language-specific optimizations while maintaining protocol compatibility

### Why Separate Adapters Package?

- Queue adapters have external dependencies (AWS SDK, Kafka client)
- Core remains dependency-free
- Users only import adapters they need

### Why Not Abstract Everything?

- Each queue has unique features (SQS attributes, Kafka partitions)
- Adapters can expose queue-specific options
- Protocol compatibility is the only hard requirement

## Anti-patterns to Avoid

1. **Don't create language wrappers**: Each implementation should feel native to its language
2. **Don't force identical APIs**: Go uses contexts, Python uses kwargs, respect the differences
3. **Don't hide queue features**: Adapters can expose queue-specific functionality
4. **Don't centralize configuration**: Each adapter manages its own settings
5. **Don't couple implementations**: Languages communicate only through the proto contract

## Success Metrics

The library succeeds if:

1. Adding a new language implementation takes < 1 week
2. Adding a new queue adapter takes < 1 day
3. Cross-language communication "just works"
4. Each implementation feels native to its language
5. The protocol remains stable across versions

## Next Steps

1. Create repository with proposed structure
2. Define and document the proto
3. Implement Go core package
4. Add memory adapter with comprehensive tests
5. Implement SQS adapter
6. Create example service demonstrating migration
7. Plan Python implementation based on Go learnings

Remember: The protocol is the contract. Everything else should feel natural in its respective language. The Go implementation sets the pattern but shouldn't constrain future language implementations.

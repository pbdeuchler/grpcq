# grpcq Protocol Specification

Version: 1.0.0

## Overview

The grpcq protocol defines how gRPC service calls are serialized, transmitted through message queues, and deserialized for async processing. This document describes the language-agnostic protocol that all implementations must follow to ensure interoperability.

## Core Concepts

### Message Flow

1. **Publishing**: A client wants to call a gRPC method asynchronously
   - Client serializes the request proto to bytes
   - Client creates a Message proto with metadata
   - Client publishes the Message to a queue

2. **Processing**: A worker receives and processes the message
   - Worker receives Message from queue
   - Worker looks up handler based on topic/action
   - Worker deserializes payload and invokes handler
   - Worker acknowledges or rejects the message

### Protocol Contract

All implementations MUST:
- Use the Message proto defined in `proto/message.proto`
- Preserve all fields during transmission
- Handle unknown metadata keys gracefully
- Generate unique message_id values
- Set timestamp_ms to Unix milliseconds

## Message Structure

### Field Semantics

#### originator (string)
- **Purpose**: Identifies the sender of the message
- **Format**: Arbitrary string, no validation enforced
- **Examples**:
  - Service name: "order-service"
  - Instance ID: "order-service-pod-abc123"
  - Environment + service: "prod-payment-processor"
- **Usage**: Debugging, monitoring, message attribution

#### topic (string)
- **Purpose**: Maps to gRPC service name
- **Format**: Fully qualified service name from proto definition
- **Examples**:
  - "user.UserService"
  - "payment.PaymentProcessor"
  - "inventory.InventoryManager"
- **Usage**: First-level routing key for handlers

#### action (string)
- **Purpose**: Maps to gRPC method name
- **Format**: Method name from proto definition
- **Examples**:
  - "CreateUser"
  - "ProcessPayment"
  - "UpdateInventory"
- **Usage**: Second-level routing key for handlers

#### payload (bytes)
- **Purpose**: Serialized protobuf request message
- **Format**: Binary protobuf encoding
- **Size**: Queue adapter dependent (SQS: 256KB, Kafka: configurable)
- **Usage**: Contains the actual gRPC request parameters

#### message_id (string)
- **Purpose**: Unique identifier for deduplication and tracing
- **Format**: UUID v4 recommended, any unique string accepted
- **Examples**:
  - "550e8400-e29b-41d4-a716-446655440000"
  - "msg-20240101-123456-abc"
- **Usage**: Deduplication, correlation, debugging

#### timestamp_ms (int64)
- **Purpose**: Message creation time for ordering and TTL
- **Format**: Unix timestamp in milliseconds
- **Range**: 0 to 9223372036854775807 (max int64)
- **Usage**: Age calculation, expiration, ordering

#### metadata (map<string, string>)
- **Purpose**: Extensible key-value storage for cross-cutting concerns
- **Format**: String keys and string values only
- **Common Keys**:
  - `trace_id`: Distributed tracing identifier
  - `span_id`: Trace span identifier
  - `correlation_id`: Request correlation across services
  - `user_id`: Authenticated user identifier
  - `tenant_id`: Multi-tenancy isolation
  - `priority`: Message priority hint
  - `deadline_ms`: Processing deadline (Unix milliseconds)
- **Usage**: Context propagation, tracing, authorization

## Handler Registration

### Topic and Action Mapping

Handlers are registered using a two-level map:

```
Registry: topic -> action -> handler function
```

**Registration Rules**:
1. Each topic/action pair maps to exactly one handler
2. Duplicate registrations should error or overwrite based on implementation
3. Missing handlers should return clear error messages
4. Handler signature should accept the deserialized request proto

### Error Handling

**Message Processing Failures**:
- **Unknown topic/action**: Nack message (dead letter after retries)
- **Deserialization error**: Nack message (likely corrupted)
- **Handler error**: Nack message (may succeed on retry)
- **Handler success**: Ack message

## Queue Adapter Contract

### Interface Requirements

All queue adapters must implement:

1. **Publish**: Send one or more messages to a queue
   - Batch publishing recommended for efficiency
   - Return error if any message fails
   - No partial success (all or nothing)

2. **Consume**: Receive a batch of messages
   - Return messages with receipt handles
   - Support configurable batch size
   - Block or timeout based on implementation

3. **Acknowledge**: Confirm successful processing
   - Remove message from queue
   - Idempotent operation

4. **Negative Acknowledge**: Indicate processing failure
   - Return message to queue for retry
   - Respect queue's retry/DLQ policy

### Queue-Specific Considerations

**SQS**:
- Use message attributes for topic/action (enables filtering)
- Respect 256KB message size limit
- Use SendMessageBatch (max 10 messages)
- ReceiveMessage with long polling

**Kafka**:
- Use topic as Kafka topic, action as message key
- Partitioning by action enables ordered processing
- Consumer groups for parallel processing
- Offset management for acknowledgment

**RabbitMQ**:
- Exchange: topic, Routing key: action
- Use confirms for publish reliability
- Ack/Nack for consumption

## Cross-Language Compatibility

### Protocol Buffers Version

- **Minimum**: proto3 syntax
- **Features**: Basic types, maps, enums only
- **Avoid**: Any, oneof, extensions (for maximum compatibility)

### Serialization

All implementations must:
1. Use standard protobuf binary encoding
2. Preserve unknown fields during deserialization
3. Handle missing optional fields gracefully
4. Not rely on field ordering

### Testing Cross-Language Compatibility

Implementations should verify:
1. Go publisher -> Python consumer works
2. Python publisher -> Go consumer works
3. Messages contain all expected fields
4. Metadata is preserved accurately

## Versioning Strategy

### Protocol Evolution

**Breaking Changes** (require major version bump):
- Removing or renaming Message fields
- Changing field types
- Changing field numbers

**Non-Breaking Changes** (minor version):
- Adding new Message fields (with defaults)
- Adding new metadata keys
- Documenting new conventions

### Backward Compatibility

Implementations must:
1. Ignore unknown Message fields
2. Ignore unknown metadata keys
3. Provide defaults for missing optional fields
4. Support messages from older protocol versions

## Security Considerations

### Data in Transit

- Protocol defines message structure only
- Transport encryption depends on queue adapter:
  - SQS: TLS by default
  - Kafka: SASL/TLS configuration
  - RabbitMQ: TLS configuration

### Data at Rest

- Queue-level encryption (e.g., SQS SSE-KMS)
- Application-level encryption of payload field
- Metadata is typically unencrypted for routing

### Authentication and Authorization

- Not part of protocol specification
- Implement at adapter level:
  - SQS: IAM policies
  - Kafka: ACLs
  - RabbitMQ: User permissions

### Sensitive Data

**Recommendations**:
- Avoid PII in metadata keys/values
- Encrypt sensitive payloads before serialization
- Use topic/action naming that doesn't leak information
- Implement message TTL to limit exposure window

## Performance Considerations

### Message Size

- Keep messages small (< 64KB recommended)
- Large payloads impact queue performance
- Consider reference patterns for large data

### Batching

- Batch publish when possible (reduces API calls)
- Batch consume for higher throughput
- Balance batch size with latency requirements

### Metadata Size

- Each metadata entry adds overhead
- Limit to essential keys only
- Consider compressed encodings for large trace contexts

## Monitoring and Observability

### Recommended Metrics

**Publisher Metrics**:
- Messages published (count)
- Publish errors (count)
- Publish latency (histogram)
- Message size (histogram)

**Worker Metrics**:
- Messages received (count)
- Messages processed (count by topic/action)
- Processing latency (histogram)
- Handler errors (count by topic/action)
- Unknown handlers (count)

### Tracing Integration

- Extract trace context from metadata
- Start new span for message processing
- Link spans across service boundaries
- Include message_id in span attributes

### Logging

**Include in Logs**:
- message_id: For correlation
- topic/action: For filtering
- originator: For debugging
- timestamp_ms: For age calculation

**Avoid in Logs**:
- Raw payload bytes: Too verbose, may contain PII
- Full metadata map: May contain sensitive data

## Best Practices

### Publisher Best Practices

1. Set meaningful originator (service name + instance)
2. Generate UUIDs for message_id
3. Use current time for timestamp_ms
4. Include trace context in metadata
5. Handle publish errors with retries
6. Validate payload size before publishing

### Worker Best Practices

1. Register all handlers before starting
2. Use graceful shutdown (drain in-flight messages)
3. Implement handler timeouts
4. Log all message processing errors
5. Monitor unknown topic/action occurrences
6. Use worker pools for concurrency

### Handler Best Practices

1. Keep handlers idempotent
2. Use context for cancellation
3. Return errors clearly
4. Avoid long-running operations
5. Propagate trace context to downstream calls

## Example Message

```json
{
  "originator": "order-service-prod-pod-123",
  "topic": "payment.PaymentService",
  "action": "ProcessPayment",
  "payload": "<binary protobuf bytes>",
  "message_id": "550e8400-e29b-41d4-a716-446655440000",
  "timestamp_ms": 1704067200000,
  "metadata": {
    "trace_id": "1234567890abcdef",
    "span_id": "fedcba0987654321",
    "user_id": "user-12345",
    "correlation_id": "order-67890"
  }
}
```

## Implementation Checklist

When implementing grpcq for a new language:

- [ ] Import generated Message proto
- [ ] Implement QueueAdapter interface
- [ ] Implement Registry with two-level map
- [ ] Implement Publisher with message serialization
- [ ] Implement Worker with polling loop
- [ ] Generate unique message IDs
- [ ] Set current timestamp in milliseconds
- [ ] Preserve all metadata during processing
- [ ] Handle unknown topic/action gracefully
- [ ] Test cross-language compatibility
- [ ] Document language-specific conventions

## Compliance

An implementation is protocol-compliant if:

1. It can deserialize any valid Message proto
2. It preserves all Message fields during processing
3. It follows the topic/action handler routing
4. It generates valid message_id values
5. It sets timestamp_ms correctly
6. It can interoperate with other language implementations

## References

- Protocol Buffer Language Guide: https://protobuf.dev/programming-guides/proto3/
- Message Queue Patterns: https://www.enterpriseintegrationpatterns.com/
- Distributed Tracing: https://opentelemetry.io/

---

For implementation-specific details, refer to the documentation in each language directory.

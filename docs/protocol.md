# grpcq Protocol Specification

Version: 1.0.0

## Message Structure

All grpcq messages use this protobuf format:

```protobuf
message Message {
  string originator = 1;           // Sender identification
  string topic = 2;                // gRPC service name (e.g., "user.UserService")
  string action = 3;               // gRPC method name (e.g., "CreateUser")
  bytes payload = 4;               // Serialized protobuf request
  string message_id = 5;           // Unique identifier (UUID recommended)
  int64 timestamp_ms = 6;          // Unix timestamp in milliseconds
  map<string, string> metadata = 7;  // Headers, trace context, etc.
}
```

## Field Requirements

### Required Fields
- `topic` - Must match the proto service name
- `action` - Must match the proto method name
- `payload` - Serialized request protobuf
- `message_id` - Must be unique (UUID v4 recommended)
- `timestamp_ms` - Message creation time

### Optional Fields
- `originator` - Sender identification (service name, instance ID)
- `metadata` - Key-value pairs for context propagation

### Common Metadata Keys
- `trace_id` - Distributed tracing identifier
- `span_id` - Trace span identifier
- `correlation_id` - Request correlation ID
- `user_id` - Authenticated user
- `tenant_id` - Multi-tenancy identifier

## Size Limits

- **Maximum message size**: 256KB (SQS compatibility)
- **Queue/Topic names**: Alphanumeric, hyphens, underscores, dots only
- **Metadata**: String keys and values only

## Message Flow

1. **Producer** creates Message with serialized request
2. **Adapter** publishes to queue
3. **Consumer** receives and deserializes
4. **Registry** routes to handler by topic/action
5. **Handler** processes and returns success/error
6. **Consumer** acks (success) or nacks (error)

## Queue Adapter Contract

Adapters must implement:

```go
type QueueAdapter interface {
    // Publish messages to queue (atomic - all or nothing)
    Publish(ctx context.Context, queueName string, messages ...*pb.Message) error

    // Consume messages from queue with receipts for ack/nack
    Consume(ctx context.Context, queueName string, maxBatch int) (*ConsumeResult, error)
}
```

## Error Handling

- **Unknown topic/action**: Nack → DLQ after retries
- **Deserialization error**: Nack → DLQ (corrupted)
- **Handler error**: Nack → Retry based on queue config
- **Handler success**: Ack → Remove from queue

## Cross-Language Compatibility

- Use proto3 syntax only
- Preserve unknown fields during deserialization
- Handle missing optional fields gracefully
- All implementations must use the same Message proto

## Security Considerations

- **Transport**: Use TLS (queue-specific: SQS, Kafka SASL/TLS)
- **At rest**: Queue-level encryption (SQS SSE-KMS)
- **Sensitive data**: Avoid PII in metadata, encrypt payloads if needed
- **Authentication**: Implement at adapter level (IAM, ACLs)

## Example Message (JSON representation)

```json
{
  "originator": "order-service-prod-123",
  "topic": "payment.PaymentService",
  "action": "ProcessPayment",
  "payload": "<base64-encoded-protobuf>",
  "message_id": "550e8400-e29b-41d4-a716-446655440000",
  "timestamp_ms": 1704067200000,
  "metadata": {
    "trace_id": "abc123",
    "user_id": "user-456"
  }
}
```
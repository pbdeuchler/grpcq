# User Service Example

This example demonstrates grpcq's gRPC interoperability - how to write a service implementation once and run it in both **synchronous (traditional gRPC)** and **asynchronous (queue-based)** modes.

## Key Concept

The same service implementation code works for both:

- Traditional gRPC server/client (synchronous, request-response)
- grpcq worker/publisher (asynchronous, queue-based)

This enables gradual migration, flexible testing, and deployment options without code changes.

## Running the Example

The example supports multiple modes:

### Demo Mode (Default)

Runs both async worker and client together:

```bash
cd go/examples/userservice
go run . -mode demo
```

### Synchronous gRPC

**Start the gRPC server:**

```bash
go run . -mode sync-server
```

**In another terminal, run the client:**

```bash
go run . -mode sync-client
```

### Asynchronous Queue-Based

The async modes share work through an HTTP publish endpoint that fronts the in-memory adapter. Run the worker with a listener address (defaults to `127.0.0.1:8081`):

**Start the worker:**

```bash
go run . -mode async-server -queue_listen 127.0.0.1:8081
```

**In another terminal, run the publisher pointing at the same endpoint:**

```bash
go run . -mode async-client -queue_endpoint http://127.0.0.1:8081
```

You can pick any address/port pair as long as both processes agree. For example, to expose the demo on all interfaces use `-queue_listen 0.0.0.0:9000` on the server and `-queue_endpoint http://localhost:9000` on the client.

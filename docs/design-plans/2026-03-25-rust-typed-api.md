# Rust Typed API Design

## Summary

grpcq turns gRPC service definitions into async, queue-driven message pipelines. Rather than replacing gRPC, it lets you reuse the same `.proto` definitions and service implementations to publish and consume messages through a queue backend (e.g., AWS SQS, an in-memory channel) without modifying business logic. The wire format is a protobuf envelope (`grpcq.Message`) carrying a serialized request payload alongside routing fields (topic, action, originator), making the system language-agnostic.

This document describes a typed code generation layer for the Rust runtime. Today the Rust API is untyped at the user boundary: handlers are registered with string-based topic/action identifiers and the caller is responsible for correct serialization. The new design introduces a `grpcq-build` crate that reads `.proto` files at compile time (via `build.rs`) and emits typed Rust modules -- one per proto service -- containing an async consumer trait, a consumer wrapper struct, and a typed producer struct. The generated code connects to the existing `Registry`/`Worker` infrastructure under the hood, exposing a tonic-familiar API (`Server::builder().add_service(...)`) at the surface. An optional `tonic` feature flag makes the generated traits signature-compatible with tonic's server traits, so a single struct can serve both gRPC and queue traffic.

## Definition of Done

1. A `grpcq-build` crate (analogous to `tonic-build`) reads `.proto` files in `build.rs` and generates typed consumer traits, consumer wrappers, and producer structs -- mirroring tonic's codegen ergonomics.
2. For each proto service, codegen produces: (a) an async `XxxService` trait with one method per RPC, (b) an `XxxConsumer` wrapper that takes `impl XxxService` and registers handlers with a grpcq `Server`, (c) an `XxxProducer` struct with typed fire-and-forget methods per RPC.
3. The core `grpcq` crate remains prost-first with no tonic dependency. An optional `tonic` feature flag on `grpcq-build` enables generating traits compatible with tonic's generated server traits, so one impl serves both gRPC and queue consumption.
4. Users get compile-time type safety for both producing and consuming -- no string-based topic/action routing at the user level.
5. The existing untyped `Producer`/`Server` APIs continue to work for users who don't want codegen.
6. Existing tests pass; new tests cover the codegen-generated code paths.
7. The user-facing API mirrors tonic's patterns: `Server::builder().add_service(XxxConsumer::new(impl))` for consuming, `XxxProducer::new(adapter, originator)` for producing.

**Out of scope:**
- Bidirectional/response routing (remains fire-and-forget).
- Streaming RPC methods (unary only for initial version).

## Acceptance Criteria

### rust-typed-api.AC1: grpcq-build generates code from .proto files
- **rust-typed-api.AC1.1 Success:** `grpcq_build::compile_protos("service.proto")` in build.rs produces a `{service}_consumer` module with an async trait containing one method per unary RPC
- **rust-typed-api.AC1.2 Success:** Generated consumer trait methods use snake_case names derived from proto method names (e.g., `SayHello` -> `say_hello`)
- **rust-typed-api.AC1.3 Success:** `grpcq_build::compile_protos` produces a `{service}_producer` module with a struct containing typed fire-and-forget methods
- **rust-typed-api.AC1.4 Failure:** Proto service with streaming RPC methods produces a compile-time error or warning from grpcq-build
- **rust-typed-api.AC1.5 Edge:** Proto file with multiple services generates separate consumer/producer modules for each

### rust-typed-api.AC2: Generated code provides compile-time type safety
- **rust-typed-api.AC2.1 Success:** Generated consumer trait methods accept the exact prost request type and return `Result<ResponseType>`
- **rust-typed-api.AC2.2 Success:** Passing the wrong request type to a consumer method is a compile error
- **rust-typed-api.AC2.3 Success:** Generated producer methods accept the exact prost request type -- passing the wrong type is a compile error

### rust-typed-api.AC3: Tonic compatibility via feature flag
- **rust-typed-api.AC3.1 Success:** With `tonic` feature enabled, generated consumer trait uses `tonic::Request<T>` / `tonic::Response<T>` / `tonic::Status` signatures identical to tonic-generated server traits
- **rust-typed-api.AC3.2 Success:** A single struct implementing the tonic-compatible trait can be registered with both `tonic::Server` and `grpcq::ServerBuilder`

### rust-typed-api.AC4: End-to-end typed pipeline works
- **rust-typed-api.AC4.1 Success:** Consumer receives a message, deserializes it to the correct prost type, and dispatches to the correct trait method
- **rust-typed-api.AC4.2 Failure:** Message with malformed payload (fails prost decode) results in `Error::RequestDecode` and nack
- **rust-typed-api.AC4.3 Success:** Producer serializes request, sets correct topic+action strings, and publishes via adapter

### rust-typed-api.AC5: Backward compatibility
- **rust-typed-api.AC5.1 Success:** All existing tests in `rust/tests/runtime.rs` pass without modification
- **rust-typed-api.AC5.2 Success:** Existing untyped `Producer::send` and `Server::register_method` APIs remain functional and unchanged

### rust-typed-api.AC6: Test coverage
- **rust-typed-api.AC6.1 Success:** Integration test exercises the full pipeline: generated producer sends typed message -> queue -> generated consumer dispatches to trait impl -> handler receives correctly typed request

### rust-typed-api.AC7: Server builder pattern
- **rust-typed-api.AC7.1 Success:** `Server::builder(adapter, config).add_service(XxxConsumer::new(impl)).serve(token).await` starts and processes messages
- **rust-typed-api.AC7.2 Success:** Multiple services can be registered via chained `add_service()` calls on the same builder

## Glossary

- **prost / prost-build**: The de facto Rust library for Protocol Buffers. `prost` handles encoding/decoding; `prost-build` compiles `.proto` files from `build.rs` and exposes a `ServiceGenerator` trait for plugins to generate additional code alongside message structs.
- **tonic / tonic-build**: A Rust gRPC framework built on prost. Generates server traits and client stubs from `.proto` files. grpcq's optional `tonic` feature flag makes generated consumer traits signature-compatible with tonic's server traits.
- **`ServiceGenerator` (prost-build trait)**: A plugin interface in `prost-build` that receives parsed service definitions and writes additional Rust source code. `grpcq-build` implements this trait to emit consumer and producer modules.
- **fire-and-forget**: A messaging pattern where the sender publishes a message and does not wait for a response. All grpcq producers operate this way.
- **`QueueAdapter`**: The grpcq abstraction over a queue backend. Implementations exist for in-memory channels and AWS SQS; users can supply their own.
- **`Registry`**: grpcq's internal handler lookup table. Maps `(topic, action)` string pairs to handler closures. Generated consumer wrappers call into it; user code does not interact with it directly when using the typed API.
- **`ServiceRegistrar` (new)**: The extension point added in this design. Allows generated consumer structs to register their handlers into a `Registry` when added to a `ServerBuilder`.
- **ack / nack**: Queue acknowledgement signals. `ack` removes a message (success); `nack` returns it for retry (failure).
- **topic / action**: The two routing fields in `grpcq.Message`. Topic = fully-qualified proto service name; action = RPC method name. Together they form the handler lookup key.

## Architecture

Two new crates are introduced alongside the existing `grpcq` runtime crate:

**`grpcq-build`** — a build-time crate (used in downstream `build.rs`) that implements `prost_build::ServiceGenerator`. It reads `.proto` service definitions and generates typed Rust modules for each service. It depends on `prost-build` and optionally on `tonic-build` (behind a `tonic` feature flag). It produces no runtime code itself — all generated code depends only on `grpcq` and `prost`.

**`grpcq` (existing, modified)** — the async runtime crate. Gains a `ServiceRegistrar` trait and a `ServerBuilder` that supports `add_service()`, enabling generated consumer wrappers to register themselves with the server. Existing untyped APIs (`Producer::send`, `Server::register_method`) remain unchanged.

### Data Flow

```
.proto file
    |
    v
build.rs (grpcq_build::compile_protos)
    |
    v  prost_build::ServiceGenerator
    |
    +---> {service}_consumer module
    |       - trait {Service} (async methods: req -> Result<resp>)
    |       - struct {Service}Consumer<T> (wraps impl, registers with Server)
    |
    +---> {service}_producer module
            - struct {Service}Producer (typed fire-and-forget methods)

Runtime:
    Producer side:  {Service}Producer --> Client --> QueueAdapter --> Queue
    Consumer side:  Queue --> Worker --> Registry --> {Service}Consumer<T> --> user impl
```

### Generated Code Contracts

For a proto service:
```proto
service Greeter {
  rpc SayHello (HelloRequest) returns (HelloReply);
}
```

**Consumer module** (`greeter_consumer`):

```rust
pub mod greeter_consumer {
    use super::*;

    #[grpcq::async_trait]
    pub trait Greeter: Send + Sync + 'static {
        async fn say_hello(&self, req: HelloRequest) -> grpcq::Result<HelloReply>;
    }

    pub struct GreeterConsumer<T: Greeter> {
        inner: std::sync::Arc<T>,
    }

    impl<T: Greeter> GreeterConsumer<T> {
        pub fn new(inner: T) -> Self;
        pub fn from_arc(inner: std::sync::Arc<T>) -> Self;
    }

    impl<T: Greeter> grpcq::ServiceRegistrar for GreeterConsumer<T> {
        fn register(&self, registry: &grpcq::Registry);
        fn service_name(&self) -> &'static str; // "helloworld.Greeter"
    }
}
```

**Producer module** (`greeter_producer`):

```rust
pub mod greeter_producer {
    use super::*;

    pub struct GreeterProducer {
        client: grpcq::Client,
    }

    impl GreeterProducer {
        pub fn new(
            adapter: grpcq::SharedAdapter,
            config: grpcq::ClientConfig,
        ) -> Self;

        pub async fn say_hello(
            &self,
            req: HelloRequest,
        ) -> grpcq::Result<()>;

        pub async fn say_hello_with_options(
            &self,
            req: HelloRequest,
            options: grpcq::CallOptions,
        ) -> grpcq::Result<()>;
    }
}
```

**New runtime types** (in `grpcq` crate):

```rust
pub trait ServiceRegistrar: Send + Sync {
    fn register(&self, registry: &Registry);
    fn service_name(&self) -> &'static str;
}

pub struct ServerBuilder {
    adapter: SharedAdapter,
    config: ServerConfig,
    registrars: Vec<Box<dyn ServiceRegistrar>>,
}

impl ServerBuilder {
    pub fn add_service<S: ServiceRegistrar + 'static>(mut self, svc: S) -> Self;
    pub async fn serve(self, cancellation: CancellationToken) -> Result<()>;
}

impl Server {
    pub fn builder(adapter: SharedAdapter, config: ServerConfig) -> ServerBuilder;
}
```

### Tonic Compatibility (feature flag)

With `grpcq-build`'s `tonic` feature enabled, the generated consumer trait uses tonic's type wrappers:

```rust
// With tonic feature
#[tonic::async_trait]
pub trait Greeter: Send + Sync + 'static {
    async fn say_hello(
        &self,
        req: tonic::Request<HelloRequest>,
    ) -> std::result::Result<tonic::Response<HelloReply>, tonic::Status>;
}
```

The `GreeterConsumer` wrapper unwraps `tonic::Response` and converts `tonic::Status` errors to `grpcq::Error` before passing to the registry. This allows a single struct to implement both tonic's `Greeter` trait (for gRPC serving) and grpcq's `Greeter` trait (for queue consumption) since the signatures are identical.

## Existing Patterns

The Rust crate follows a composition-based pattern: `Server` composes `Registry` + `Worker`, and `Client` composes `Producer`. Handler registration uses generic closures (`Fn(HandlerContext, TReq) -> Fut`), not trait objects. This is the right foundation — the generated code calls `Server::register_method` internally, adding a typed layer on top.

The Go codegen plugin (`cmd/protoc-gen-grpcq/main.go`) establishes the naming convention: `{Service}Consumer` for server-side, `{Service}Producer` for client-side. The Rust codegen follows this convention.

The existing `Server::register_method` takes `HandlerContext` as the first argument. The generated consumer trait omits `HandlerContext` from user-facing methods (per design decision) — the `GreeterConsumer` wrapper constructs and discards it internally. This diverges from the raw API but simplifies the generated trait. Users who need message metadata can use the untyped API directly.

<!-- START_PHASE_1 -->
## Implementation Phases

### Phase 1: Cargo Workspace and grpcq-build Scaffold
**Goal:** Establish multi-crate workspace and minimal grpcq-build crate that compiles

**Components:**
- Root `Cargo.toml` workspace manifest including `rust/` (existing) and `rust/grpcq-build/` (new)
- `rust/grpcq-build/Cargo.toml` with dependencies on `prost-build`, `quote`, `syn`, `proc-macro2`
- `rust/grpcq-build/src/lib.rs` with public `compile_protos()` function that delegates to `prost_build::Config`
- Empty `ServiceGenerator` impl that generates no service code yet (messages still generated by prost)

**Dependencies:** None (first phase)

**Done when:** `cargo build --all-features` succeeds for the workspace, `grpcq-build::compile_protos()` compiles a `.proto` file and produces prost message types
<!-- END_PHASE_1 -->

<!-- START_PHASE_2 -->
### Phase 2: ServiceRegistrar Trait and ServerBuilder
**Goal:** Add the runtime extension point that generated code will target

**Components:**
- `ServiceRegistrar` trait in `rust/src/core.rs` or a new `rust/src/service.rs`
- `ServerBuilder` struct in `rust/src/server.rs` with `add_service()` and `serve()` methods
- `Server::builder()` constructor

**Dependencies:** Phase 1 (workspace structure)

**Covers:** `rust-typed-api.AC7.1`, `rust-typed-api.AC7.2`

**Done when:** `ServerBuilder` can accept `ServiceRegistrar` impls, construct a `Server` with a populated `Registry`, and start it. Tests verify a hand-written `ServiceRegistrar` impl registers handlers correctly and processes messages.
<!-- END_PHASE_2 -->

<!-- START_PHASE_3 -->
### Phase 3: Consumer Codegen
**Goal:** Generate typed consumer traits and consumer wrapper structs from `.proto` files

**Components:**
- `ServiceGenerator` impl in `rust/grpcq-build/src/lib.rs` — generates `{service}_consumer` module per proto service
- Generated async trait with one method per unary RPC (snake_case method name, bare prost request/response types)
- Generated `{Service}Consumer<T>` struct implementing `ServiceRegistrar`
- Method name mapping: proto `SayHello` -> Rust `say_hello`

**Dependencies:** Phase 2 (ServiceRegistrar trait)

**Covers:** `rust-typed-api.AC1.1`, `rust-typed-api.AC1.2`, `rust-typed-api.AC2.1`, `rust-typed-api.AC2.2`, `rust-typed-api.AC4.1`, `rust-typed-api.AC4.2`

**Done when:** A test proto file generates consumer code that compiles, a hand-written trait impl can be registered with `ServerBuilder`, and messages are deserialized and dispatched to the correct handler method with type safety.
<!-- END_PHASE_3 -->

<!-- START_PHASE_4 -->
### Phase 4: Producer Codegen
**Goal:** Generate typed producer structs with fire-and-forget methods

**Components:**
- Producer module generation in the `ServiceGenerator` impl
- Generated `{Service}Producer` struct wrapping `grpcq::Client`
- Typed methods per RPC returning `Result<()>`
- `_with_options` variants for per-call metadata/queue overrides

**Dependencies:** Phase 3 (codegen infrastructure)

**Covers:** `rust-typed-api.AC1.3`, `rust-typed-api.AC2.3`, `rust-typed-api.AC4.3`

**Done when:** Generated producer compiles, serializes requests correctly, and routes to the right topic+action. Integration test: producer sends typed message, consumer receives and deserializes it through the full pipeline.
<!-- END_PHASE_4 -->

<!-- START_PHASE_5 -->
### Phase 5: Tonic Feature Flag
**Goal:** Optional tonic compatibility for consumer traits

**Components:**
- `tonic` feature flag on `grpcq-build` crate
- Conditional codegen: when enabled, consumer trait uses `tonic::Request<T>` / `tonic::Response<T>` / `tonic::Status`
- `GreeterConsumer` wrapper adapts tonic types to grpcq internals (unwrap Request, discard Response)
- `grpcq` runtime gains optional `tonic` dependency for the wrapper conversion code

**Dependencies:** Phase 3 (consumer codegen)

**Covers:** `rust-typed-api.AC3.1`, `rust-typed-api.AC3.2`

**Done when:** With `tonic` feature enabled, a single struct implementing the generated trait can be used with both `tonic::Server` (via `GreeterServer::new()`) and `grpcq::ServerBuilder` (via `GreeterConsumer::new()`). Tests verify both paths.
<!-- END_PHASE_5 -->

<!-- START_PHASE_6 -->
### Phase 6: Backward Compatibility and Documentation
**Goal:** Ensure existing untyped APIs work unchanged and document the new codegen workflow

**Components:**
- Verify all existing tests in `rust/tests/runtime.rs` pass without modification
- README update with Rust codegen quick start (build.rs setup, consumer impl, producer usage)
- Example proto + build.rs + consumer/producer in `rust/examples/`

**Dependencies:** Phase 4, Phase 5

**Covers:** `rust-typed-api.AC5.1`, `rust-typed-api.AC5.2`, `rust-typed-api.AC6.1`

**Done when:** All existing tests pass, example compiles and runs, README documents the workflow.
<!-- END_PHASE_6 -->

## Additional Considerations

**Streaming RPCs:** The initial design handles unary RPCs only. Proto services with streaming methods should generate a compile-time error or warning from `grpcq-build`, not silently skip them. A future design can add streaming support if needed.

**Method name collision:** If a proto service has methods whose snake_case forms collide (unlikely but possible), `grpcq-build` should emit a compile error. No runtime handling needed.

**async_trait dependency:** The generated code uses `async_trait` for trait method signatures. When Rust stabilizes native async fn in traits with dyn dispatch, the codegen can be updated to drop this dependency. The generated trait signature is the public contract — the async_trait expansion is an implementation detail.

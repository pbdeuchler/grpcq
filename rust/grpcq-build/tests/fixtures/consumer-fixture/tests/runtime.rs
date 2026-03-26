use std::{
    sync::{mpsc, Mutex},
    thread,
    time::Duration,
};

use futures::{executor::block_on, FutureExt};
use grpcq::{
    adapters::memory, CallOptions, CancellationToken, ClientConfig, ConsumeResult, Error, Message,
    QueueAdapter, Registry, Result, Server, ServerConfig, ServiceRegistrar, SharedAdapter,
};
use grpcq_build_consumer_fixture::generated::{
    greeter_consumer::{Greeter, GreeterConsumer},
    greeter_producer::GreeterProducer,
    HelloReply, HelloRequest,
};
use prost::Message as ProstMessage;

struct GreeterService {
    processed_tx: mpsc::Sender<String>,
}

#[grpcq::async_trait]
impl Greeter for GreeterService {
    async fn say_hello(&self, req: HelloRequest) -> grpcq::Result<HelloReply> {
        self.processed_tx
            .send(req.name.clone())
            .expect("processed request should be recorded");
        Ok(HelloReply {
            message: format!("hello {}", req.name),
        })
    }
}

#[derive(Default)]
struct RecordingAdapter {
    queue_name: Mutex<Option<String>>,
    messages: Mutex<Vec<Message>>,
}

impl RecordingAdapter {
    fn take_queue_name(&self) -> Option<String> {
        self.queue_name.lock().expect("queue lock poisoned").clone()
    }

    fn take_messages(&self) -> Vec<Message> {
        self.messages
            .lock()
            .expect("messages lock poisoned")
            .clone()
    }
}

impl QueueAdapter for RecordingAdapter {
    fn publish<'a>(
        &'a self,
        queue_name: &'a str,
        messages: &'a [Message],
    ) -> futures::future::BoxFuture<'a, Result<()>> {
        async move {
            *self.queue_name.lock().expect("queue lock poisoned") = Some(queue_name.to_string());
            self.messages
                .lock()
                .expect("messages lock poisoned")
                .extend(messages.iter().cloned());
            Ok(())
        }
        .boxed()
    }

    fn consume<'a>(
        &'a self,
        _queue_name: &'a str,
        _max_batch: usize,
    ) -> futures::future::BoxFuture<'a, Result<ConsumeResult>> {
        async { Ok(ConsumeResult::default()) }.boxed()
    }
}

#[test]
fn generated_producer_and_consumer_round_trip_through_server_builder() {
    let adapter = std::sync::Arc::new(memory::Adapter::new(16));
    let shared: SharedAdapter = adapter.clone();
    let (processed_tx, processed_rx) = mpsc::channel();

    let server = Server::builder(
        shared.clone(),
        ServerConfig::default()
            .with_queue_name("queue")
            .with_poll_interval(Duration::from_millis(10)),
    )
    .add_service(GreeterConsumer::new(GreeterService { processed_tx }));

    let cancellation = CancellationToken::new();
    let cancellation_for_thread = cancellation.clone();
    let handle = thread::spawn(move || block_on(server.serve(cancellation_for_thread)));

    let producer = GreeterProducer::new(
        shared,
        ClientConfig::default()
            .with_queue_name("queue")
            .with_originator("origin"),
    );
    block_on(producer.say_hello(HelloRequest {
        name: "alice".to_string(),
    }))
    .expect("send should succeed");

    let processed = processed_rx
        .recv_timeout(Duration::from_secs(2))
        .expect("generated consumer should receive the typed request");
    assert_eq!(processed, "alice");

    cancellation.cancel();

    let outcome = handle.join().expect("server thread should join");
    assert!(matches!(outcome, Err(Error::Cancelled)));
}

#[test]
fn generated_producer_uses_typed_routing_and_call_options() {
    let adapter = std::sync::Arc::new(RecordingAdapter::default());
    let shared: SharedAdapter = adapter.clone();
    let producer = GreeterProducer::new(
        shared,
        ClientConfig::default()
            .with_queue_name("default-queue")
            .with_originator("origin"),
    );

    block_on(
        producer.say_hello_with_options(
            HelloRequest {
                name: "alice".to_string(),
            },
            CallOptions::default()
                .with_queue_name("override-queue")
                .with_metadata([("trace-id", "123")]),
        ),
    )
    .expect("generated producer should publish");

    assert_eq!(adapter.take_queue_name().as_deref(), Some("override-queue"));
    let messages = adapter.take_messages();
    assert_eq!(messages.len(), 1);
    assert_eq!(messages[0].originator, "origin");
    assert_eq!(messages[0].topic, "grpcq.test.Greeter");
    assert_eq!(messages[0].action, "SayHello");
    assert_eq!(
        messages[0].metadata.get("trace-id"),
        Some(&"123".to_string())
    );

    let decoded = HelloRequest::decode(messages[0].payload.as_slice())
        .expect("generated producer should encode the request payload");
    assert_eq!(decoded.name, "alice");
}

#[test]
fn generated_consumer_returns_request_decode_errors() {
    let registry = Registry::new();
    let (processed_tx, _processed_rx) = mpsc::channel();
    let consumer = GreeterConsumer::new(GreeterService { processed_tx });

    assert_eq!(consumer.service_name(), "grpcq.test.Greeter");
    consumer.register(&registry);

    let err = block_on(registry.handle(Message {
        topic: "grpcq.test.Greeter".to_string(),
        action: "SayHello".to_string(),
        payload: vec![0xff],
        ..Message::default()
    }))
    .expect_err("malformed payload should fail");

    assert!(matches!(
        err,
        Error::RequestDecode {
            ref service,
            ref method,
            ..
        } if service == "grpcq.test.Greeter" && method == "SayHello"
    ));
}

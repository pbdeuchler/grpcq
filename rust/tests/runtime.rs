use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc, Arc, Mutex,
    },
    thread,
    time::Duration,
};

use futures::{executor::block_on, future::BoxFuture, FutureExt};
use grpcq::{
    adapters::memory, CallOptions, CancellationToken, Client, ClientConfig, ConsumeResult, Error,
    Message, MessageItem, Producer, QueueAdapter, Receipt, Registry, Result, Server, ServerConfig,
    ServiceRegistrar, SharedAdapter, SharedReceipt, Worker, WorkerConfig, MAX_MESSAGE_SIZE,
};
use prost::Message as ProstMessage;

#[derive(Clone, PartialEq, prost::Message)]
struct TestRequest {
    #[prost(string, tag = "1")]
    name: String,
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
    ) -> BoxFuture<'a, Result<()>> {
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
    ) -> BoxFuture<'a, Result<ConsumeResult>> {
        async { Ok(ConsumeResult::default()) }.boxed()
    }
}

#[derive(Default)]
struct TestReceipt {
    acked: AtomicBool,
    nacked: AtomicBool,
    ack_tx: Mutex<Option<mpsc::Sender<()>>>,
}

impl TestReceipt {
    fn new(ack_tx: mpsc::Sender<()>) -> Self {
        Self {
            acked: AtomicBool::new(false),
            nacked: AtomicBool::new(false),
            ack_tx: Mutex::new(Some(ack_tx)),
        }
    }
}

impl Receipt for TestReceipt {
    fn ack(&self) -> BoxFuture<'_, Result<()>> {
        async move {
            self.acked.store(true, Ordering::SeqCst);
            if let Some(tx) = self.ack_tx.lock().expect("ack lock poisoned").take() {
                let _ = tx.send(());
            }
            Ok(())
        }
        .boxed()
    }

    fn nack(&self) -> BoxFuture<'_, Result<()>> {
        async move {
            self.nacked.store(true, Ordering::SeqCst);
            Ok(())
        }
        .boxed()
    }
}

struct StubAdapter {
    item: Mutex<Option<MessageItem>>,
}

impl StubAdapter {
    fn new(item: MessageItem) -> Self {
        Self {
            item: Mutex::new(Some(item)),
        }
    }
}

impl QueueAdapter for StubAdapter {
    fn publish<'a>(
        &'a self,
        _queue_name: &'a str,
        _messages: &'a [Message],
    ) -> BoxFuture<'a, Result<()>> {
        async { Ok(()) }.boxed()
    }

    fn consume<'a>(
        &'a self,
        _queue_name: &'a str,
        _max_batch: usize,
    ) -> BoxFuture<'a, Result<ConsumeResult>> {
        async move {
            let item = self.item.lock().expect("item lock poisoned").take();
            Ok(ConsumeResult {
                items: item.into_iter().collect(),
            })
        }
        .boxed()
    }
}

struct RecordingRegistrar {
    service_name: &'static str,
    method_name: &'static str,
    processed_tx: mpsc::Sender<(&'static str, &'static str, String)>,
}

impl RecordingRegistrar {
    fn new(
        service_name: &'static str,
        method_name: &'static str,
        processed_tx: mpsc::Sender<(&'static str, &'static str, String)>,
    ) -> Self {
        Self {
            service_name,
            method_name,
            processed_tx,
        }
    }
}

impl ServiceRegistrar for RecordingRegistrar {
    fn register(&self, registry: &Registry) {
        let processed_tx = self.processed_tx.clone();
        let service_name = self.service_name;
        let method_name = self.method_name;

        registry.register(service_name, method_name, move |message| {
            let processed_tx = processed_tx.clone();

            async move {
                let request = TestRequest::decode(message.payload.as_slice())
                    .expect("registered test payload should decode");
                processed_tx
                    .send((service_name, method_name, request.name))
                    .expect("processed message should be recorded");
                Ok(())
            }
        });
    }

    fn service_name(&self) -> &'static str {
        self.service_name
    }
}

#[test]
fn producer_send_populates_the_message_envelope() {
    let adapter = Arc::new(RecordingAdapter::default());
    let shared: SharedAdapter = adapter.clone();
    let producer = Producer::new(shared, "test-producer");

    let mut metadata = HashMap::new();
    metadata.insert("trace-id".to_string(), "123".to_string());

    block_on(producer.send(
        "test-queue",
        "svc.Service",
        "DoThing",
        &TestRequest {
            name: "alice".to_string(),
        },
        metadata,
    ))
    .expect("send should succeed");

    let messages = adapter.take_messages();
    assert_eq!(messages.len(), 1);

    let message = &messages[0];
    assert_eq!(message.originator, "test-producer");
    assert_eq!(message.topic, "svc.Service");
    assert_eq!(message.action, "DoThing");
    assert!(!message.message_id.is_empty());
    assert!(message.timestamp_ms > 0);
    assert_eq!(message.metadata.get("trace-id"), Some(&"123".to_string()));

    let decoded = TestRequest::decode(message.payload.as_slice()).expect("payload should decode");
    assert_eq!(decoded.name, "alice");
}

#[test]
fn producer_validates_inputs() {
    let adapter = Arc::new(RecordingAdapter::default());
    let shared: SharedAdapter = adapter;
    let producer = Producer::new(shared, "origin");

    let err = block_on(producer.send(
        "",
        "svc.Service",
        "DoThing",
        &TestRequest::default(),
        HashMap::new(),
    ))
    .expect_err("empty queue name should fail");
    assert!(matches!(err, Error::EmptyQueueName));

    let err = block_on(producer.send(
        "queue",
        "",
        "DoThing",
        &TestRequest::default(),
        HashMap::new(),
    ))
    .expect_err("empty topic should fail");
    assert!(matches!(err, Error::EmptyTopic));

    let oversized = Message {
        payload: vec![0_u8; MAX_MESSAGE_SIZE + 1],
        ..Message::default()
    };
    let err = block_on(producer.send(
        "queue",
        "svc.Service",
        "DoThing",
        &oversized,
        HashMap::new(),
    ))
    .expect_err("oversized message should fail");
    assert!(matches!(err, Error::MessageTooLarge { .. }));
}

#[test]
fn registry_routes_messages_and_reports_missing_handlers() {
    let registry = Registry::new();
    let called = Arc::new(AtomicBool::new(false));
    let called_in_handler = called.clone();

    registry.register("svc.Service", "DoThing", move |_message| {
        let called_in_handler = called_in_handler.clone();
        async move {
            called_in_handler.store(true, Ordering::SeqCst);
            Ok(())
        }
    });

    block_on(registry.handle(Message {
        topic: "svc.Service".to_string(),
        action: "DoThing".to_string(),
        ..Message::default()
    }))
    .expect("registered handler should be called");

    assert!(called.load(Ordering::SeqCst));

    let err = block_on(registry.handle(Message {
        topic: "unknown.Service".to_string(),
        action: "DoThing".to_string(),
        ..Message::default()
    }))
    .expect_err("unknown topic should fail");
    assert!(matches!(err, Error::UnknownTopic { .. }));
}

#[test]
fn memory_adapter_supports_ack_nack_and_all_or_nothing_publish() {
    let adapter = memory::Adapter::new(2);

    block_on(adapter.publish(
        "queue",
        &[Message {
            message_id: "msg-1".to_string(),
            ..Message::default()
        }],
    ))
    .expect("initial publish should succeed");

    let err = block_on(adapter.publish(
        "queue",
        &[
            Message {
                message_id: "msg-2".to_string(),
                ..Message::default()
            },
            Message {
                message_id: "msg-3".to_string(),
                ..Message::default()
            },
        ],
    ))
    .expect_err("batch publish should fail when the queue lacks capacity");
    assert!(matches!(err, Error::QueueFull { .. }));
    assert_eq!(adapter.queue_depth("queue"), 1);

    let result = block_on(adapter.consume("queue", 10)).expect("consume should succeed");
    assert_eq!(result.items.len(), 1);

    block_on(result.items[0].receipt.nack()).expect("nack should requeue the message");
    assert_eq!(adapter.queue_depth("queue"), 1);

    let result = block_on(adapter.consume("queue", 10)).expect("consume should succeed");
    block_on(result.items[0].receipt.ack()).expect("ack should succeed");
    assert_eq!(adapter.queue_depth("queue"), 0);
}

#[test]
fn worker_waits_for_in_flight_work_before_returning_on_cancellation() {
    let (ack_tx, ack_rx) = mpsc::channel();
    let receipt: SharedReceipt = Arc::new(TestReceipt::new(ack_tx));
    let adapter = Arc::new(StubAdapter::new(MessageItem {
        message: Message {
            topic: "svc".to_string(),
            action: "action".to_string(),
            message_id: "1".to_string(),
            ..Message::default()
        },
        receipt,
    }));
    let shared_adapter: SharedAdapter = adapter;

    let registry = Registry::new();
    let (started_tx, started_rx) = mpsc::channel();
    let (release_tx, release_rx) = mpsc::channel::<()>();
    let release_rx = Arc::new(Mutex::new(release_rx));

    registry.register("svc", "action", move |_message| {
        let started_tx = started_tx.clone();
        let release_rx = release_rx.clone();
        async move {
            let _ = started_tx.send(());
            release_rx
                .lock()
                .expect("release lock poisoned")
                .recv()
                .expect("release signal should be sent");
            Ok(())
        }
    });

    let worker = Arc::new(Worker::new(
        shared_adapter,
        registry,
        WorkerConfig::new("queue")
            .with_concurrency(1)
            .with_max_batch(1)
            .with_poll_interval(Duration::from_millis(10)),
    ));

    let cancellation = CancellationToken::new();
    let worker_for_thread = worker.clone();
    let cancellation_for_thread = cancellation.clone();
    let handle = thread::spawn(move || block_on(worker_for_thread.start(cancellation_for_thread)));

    started_rx
        .recv_timeout(Duration::from_secs(2))
        .expect("handler should start");

    cancellation.cancel();

    assert!(
        ack_rx.recv_timeout(Duration::from_millis(100)).is_err(),
        "ack should not complete before the handler finishes"
    );

    release_tx.send(()).expect("release signal should be sent");

    ack_rx
        .recv_timeout(Duration::from_secs(2))
        .expect("ack should complete after the handler finishes");

    let outcome = handle.join().expect("worker thread should join");
    assert!(matches!(outcome, Err(Error::Cancelled)));
}

#[test]
fn client_invoke_uses_queue_override_and_metadata() {
    let adapter = Arc::new(RecordingAdapter::default());
    let shared: SharedAdapter = adapter.clone();
    let client = Client::new(
        shared,
        ClientConfig::default()
            .with_queue_name("default-queue")
            .with_originator("producer"),
    );

    block_on(
        client.invoke(
            "svc.Service",
            "DoThing",
            &TestRequest::default(),
            CallOptions::default()
                .with_queue_name("override-queue")
                .with_metadata([("trace-id", "123")]),
        ),
    )
    .expect("invoke should succeed");

    assert_eq!(adapter.take_queue_name().as_deref(), Some("override-queue"));

    let messages = adapter.take_messages();
    assert_eq!(messages.len(), 1);
    assert_eq!(messages[0].originator, "producer");
    assert_eq!(
        messages[0].metadata.get("trace-id"),
        Some(&"123".to_string())
    );
}

#[test]
fn server_processes_typed_requests() {
    let adapter = Arc::new(memory::Adapter::new(16));
    let shared: SharedAdapter = adapter.clone();
    let server = Arc::new(Server::new(
        shared.clone(),
        ServerConfig::default()
            .with_queue_name("queue")
            .with_poll_interval(Duration::from_millis(10)),
    ));

    let (processed_tx, processed_rx) = mpsc::channel();
    server.register_method::<TestRequest, (), _, _>(
        "svc.Service",
        "CreateUser",
        move |ctx, req| {
            let processed_tx = processed_tx.clone();
            async move {
                processed_tx
                    .send((ctx.message.topic, ctx.message.action, req.name))
                    .expect("processed message should be recorded");
                Ok(())
            }
        },
    );

    let server_for_thread = server.clone();
    let cancellation = CancellationToken::new();
    let cancellation_for_thread = cancellation.clone();
    let handle = thread::spawn(move || block_on(server_for_thread.start(cancellation_for_thread)));

    let producer = Producer::new(shared, "origin");
    block_on(producer.send(
        "queue",
        "svc.Service",
        "CreateUser",
        &TestRequest {
            name: "alice".to_string(),
        },
        HashMap::new(),
    ))
    .expect("send should succeed");

    let processed = processed_rx
        .recv_timeout(Duration::from_secs(2))
        .expect("server should process the request");
    assert_eq!(processed.0, "svc.Service");
    assert_eq!(processed.1, "CreateUser");
    assert_eq!(processed.2, "alice");

    block_on(server.stop()).expect("server stop should succeed");

    let outcome = handle.join().expect("server thread should join");
    assert!(outcome.is_ok(), "server should exit cleanly: {outcome:?}");
}

#[test]
fn server_builder_serves_messages_from_registered_services() {
    let adapter = Arc::new(memory::Adapter::new(16));
    let shared: SharedAdapter = adapter.clone();
    let (processed_tx, processed_rx) = mpsc::channel();

    let server = Server::builder(
        shared.clone(),
        ServerConfig::default()
            .with_queue_name("queue")
            .with_poll_interval(Duration::from_millis(10)),
    )
    .add_service(RecordingRegistrar::new(
        "svc.Service",
        "CreateUser",
        processed_tx,
    ));

    let cancellation = CancellationToken::new();
    let cancellation_for_thread = cancellation.clone();
    let handle = thread::spawn(move || block_on(server.serve(cancellation_for_thread)));

    let producer = Producer::new(shared, "origin");
    block_on(producer.send(
        "queue",
        "svc.Service",
        "CreateUser",
        &TestRequest {
            name: "alice".to_string(),
        },
        HashMap::new(),
    ))
    .expect("send should succeed");

    let processed = processed_rx
        .recv_timeout(Duration::from_secs(2))
        .expect("server should process the request");
    assert_eq!(processed.0, "svc.Service");
    assert_eq!(processed.1, "CreateUser");
    assert_eq!(processed.2, "alice");

    cancellation.cancel();

    let outcome = handle.join().expect("server thread should join");
    assert!(
        matches!(outcome, Err(Error::Cancelled)),
        "builder-backed server should stop on cancellation: {outcome:?}"
    );
}

#[test]
fn server_builder_supports_chaining_multiple_services() {
    let adapter = Arc::new(memory::Adapter::new(16));
    let shared: SharedAdapter = adapter.clone();
    let (processed_tx, processed_rx) = mpsc::channel();

    let server = Server::builder(
        shared.clone(),
        ServerConfig::default()
            .with_queue_name("queue")
            .with_poll_interval(Duration::from_millis(10)),
    )
    .add_service(RecordingRegistrar::new(
        "svc.Users",
        "CreateUser",
        processed_tx.clone(),
    ))
    .add_service(RecordingRegistrar::new(
        "svc.Teams",
        "CreateTeam",
        processed_tx,
    ));

    let cancellation = CancellationToken::new();
    let cancellation_for_thread = cancellation.clone();
    let handle = thread::spawn(move || block_on(server.serve(cancellation_for_thread)));

    let producer = Producer::new(shared, "origin");
    block_on(producer.send(
        "queue",
        "svc.Users",
        "CreateUser",
        &TestRequest {
            name: "alice".to_string(),
        },
        HashMap::new(),
    ))
    .expect("user message should succeed");
    block_on(producer.send(
        "queue",
        "svc.Teams",
        "CreateTeam",
        &TestRequest {
            name: "eng".to_string(),
        },
        HashMap::new(),
    ))
    .expect("team message should succeed");

    let first = processed_rx
        .recv_timeout(Duration::from_secs(2))
        .expect("first service should process");
    let second = processed_rx
        .recv_timeout(Duration::from_secs(2))
        .expect("second service should process");
    let mut processed = [first, second];
    processed.sort_unstable();

    assert_eq!(
        processed,
        [
            ("svc.Teams", "CreateTeam", "eng".to_string()),
            ("svc.Users", "CreateUser", "alice".to_string()),
        ]
    );

    cancellation.cancel();

    let outcome = handle.join().expect("server thread should join");
    assert!(
        matches!(outcome, Err(Error::Cancelled)),
        "builder-backed server should stop on cancellation: {outcome:?}"
    );
}

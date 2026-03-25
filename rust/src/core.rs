use std::{
    collections::HashMap,
    future::Future,
    sync::{
        atomic::{AtomicU8, Ordering},
        Arc, RwLock,
    },
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use futures::{
    future::BoxFuture,
    pin_mut, select_biased,
    stream::{FuturesUnordered, StreamExt},
    FutureExt,
};
use futures_timer::Delay;
use prost::Message as ProstMessage;
use uuid::Uuid;

use crate::{
    error::{Error, Result},
    proto::Message,
    signal::Signal,
};

pub const MAX_MESSAGE_SIZE: usize = 256 * 1024;

pub type SharedAdapter = Arc<dyn QueueAdapter>;
pub type SharedReceipt = Arc<dyn Receipt>;

pub trait QueueAdapter: Send + Sync {
    fn publish<'a>(
        &'a self,
        queue_name: &'a str,
        messages: &'a [Message],
    ) -> BoxFuture<'a, Result<()>>;
    fn consume<'a>(
        &'a self,
        queue_name: &'a str,
        max_batch: usize,
    ) -> BoxFuture<'a, Result<ConsumeResult>>;
}

pub trait Receipt: Send + Sync {
    fn ack(&self) -> BoxFuture<'_, Result<()>>;
    fn nack(&self) -> BoxFuture<'_, Result<()>>;
}

#[derive(Clone, Debug, Default)]
pub struct CancellationToken {
    signal: Signal,
}

impl CancellationToken {
    pub fn new() -> Self {
        Self {
            signal: Signal::new(),
        }
    }

    pub fn cancel(&self) {
        self.signal.set();
    }

    pub fn is_cancelled(&self) -> bool {
        self.signal.is_set()
    }

    pub async fn cancelled(&self) {
        self.signal.wait().await;
    }
}

#[derive(Clone)]
pub struct MessageItem {
    pub message: Message,
    pub receipt: SharedReceipt,
}

#[derive(Clone, Default)]
pub struct ConsumeResult {
    pub items: Vec<MessageItem>,
}

#[derive(Clone, Debug)]
pub struct MessageSpec {
    pub topic: String,
    pub action: String,
    pub payload: Vec<u8>,
    pub metadata: HashMap<String, String>,
}

impl MessageSpec {
    pub fn new<T>(topic: impl Into<String>, action: impl Into<String>, request: &T) -> Self
    where
        T: ProstMessage,
    {
        Self {
            topic: topic.into(),
            action: action.into(),
            payload: request.encode_to_vec(),
            metadata: HashMap::new(),
        }
    }

    pub fn with_metadata<K, V, I>(mut self, metadata: I) -> Self
    where
        K: Into<String>,
        V: Into<String>,
        I: IntoIterator<Item = (K, V)>,
    {
        self.metadata
            .extend(metadata.into_iter().map(|(k, v)| (k.into(), v.into())));
        self
    }
}

pub struct Producer {
    adapter: SharedAdapter,
    originator: String,
}

impl std::fmt::Debug for Producer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Producer")
            .field("originator", &self.originator)
            .finish_non_exhaustive()
    }
}

impl Producer {
    pub fn new(adapter: SharedAdapter, originator: impl Into<String>) -> Self {
        Self {
            adapter,
            originator: originator.into(),
        }
    }

    pub async fn send<T>(
        &self,
        queue_name: &str,
        topic: &str,
        action: &str,
        proto_message: &T,
        metadata: HashMap<String, String>,
    ) -> Result<()>
    where
        T: ProstMessage,
    {
        validate_queue_name(queue_name)?;
        validate_topic_action(topic, action)?;

        let payload = proto_message.encode_to_vec();
        if payload.len() > MAX_MESSAGE_SIZE {
            return Err(Error::MessageTooLarge {
                topic: topic.to_string(),
                action: action.to_string(),
                limit: MAX_MESSAGE_SIZE,
                actual: payload.len(),
            });
        }

        let message = Message {
            originator: self.originator.clone(),
            topic: topic.to_string(),
            action: action.to_string(),
            payload,
            message_id: Uuid::new_v4().to_string(),
            timestamp_ms: timestamp_ms(),
            metadata,
        };

        self.adapter.publish(queue_name, &[message]).await
    }

    pub async fn send_batch(&self, queue_name: &str, specs: &[MessageSpec]) -> Result<()> {
        validate_queue_name(queue_name)?;

        let mut messages = Vec::with_capacity(specs.len());
        for spec in specs {
            validate_topic_action(&spec.topic, &spec.action)?;

            if spec.payload.len() > MAX_MESSAGE_SIZE {
                return Err(Error::MessageTooLarge {
                    topic: spec.topic.clone(),
                    action: spec.action.clone(),
                    limit: MAX_MESSAGE_SIZE,
                    actual: spec.payload.len(),
                });
            }

            messages.push(Message {
                originator: self.originator.clone(),
                topic: spec.topic.clone(),
                action: spec.action.clone(),
                payload: spec.payload.clone(),
                message_id: Uuid::new_v4().to_string(),
                timestamp_ms: timestamp_ms(),
                metadata: spec.metadata.clone(),
            });
        }

        self.adapter.publish(queue_name, &messages).await
    }
}

type Handler = dyn Fn(Message) -> BoxFuture<'static, Result<()>> + Send + Sync;

#[derive(Clone, Default)]
pub struct Registry {
    handlers: Arc<RwLock<HashMap<String, HashMap<String, Arc<Handler>>>>>,
}

impl Registry {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn register<F, Fut>(&self, topic: impl Into<String>, action: impl Into<String>, handler: F)
    where
        F: Fn(Message) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = Result<()>> + Send + 'static,
    {
        let topic = topic.into();
        let action = action.into();
        let wrapped: Arc<Handler> =
            Arc::new(move |message: Message| -> BoxFuture<'static, Result<()>> {
                handler(message).boxed()
            });

        let mut handlers = self.handlers.write().expect("registry lock poisoned");
        handlers.entry(topic).or_default().insert(action, wrapped);
    }

    pub async fn handle(&self, message: Message) -> Result<()> {
        let handler = {
            let handlers = self.handlers.read().expect("registry lock poisoned");
            let topic_handlers =
                handlers
                    .get(&message.topic)
                    .ok_or_else(|| Error::UnknownTopic {
                        topic: message.topic.clone(),
                    })?;

            topic_handlers
                .get(&message.action)
                .cloned()
                .ok_or_else(|| Error::UnknownAction {
                    topic: message.topic.clone(),
                    action: message.action.clone(),
                })?
        };

        handler(message).await
    }

    pub fn is_registered(&self, topic: &str, action: &str) -> bool {
        let handlers = self.handlers.read().expect("registry lock poisoned");
        handlers
            .get(topic)
            .and_then(|topic_handlers| topic_handlers.get(action))
            .is_some()
    }

    pub fn topics(&self) -> Vec<String> {
        let handlers = self.handlers.read().expect("registry lock poisoned");
        handlers.keys().cloned().collect()
    }

    pub fn actions(&self, topic: &str) -> Option<Vec<String>> {
        let handlers = self.handlers.read().expect("registry lock poisoned");
        handlers
            .get(topic)
            .map(|topic_handlers| topic_handlers.keys().cloned().collect())
    }
}

#[derive(Clone, Debug)]
pub struct WorkerConfig {
    queue_name: String,
    max_batch: usize,
    concurrency: usize,
    poll_interval: Duration,
}

impl WorkerConfig {
    pub fn new(queue_name: impl Into<String>) -> Self {
        Self {
            queue_name: queue_name.into(),
            max_batch: 10,
            concurrency: 10,
            poll_interval: Duration::from_secs(1),
        }
    }

    pub fn queue_name(&self) -> &str {
        &self.queue_name
    }

    pub fn max_batch(&self) -> usize {
        self.max_batch
    }

    pub fn concurrency(&self) -> usize {
        self.concurrency
    }

    pub fn poll_interval(&self) -> Duration {
        self.poll_interval
    }

    pub fn with_max_batch(mut self, max_batch: usize) -> Self {
        self.max_batch = max_batch.max(1);
        self
    }

    pub fn with_concurrency(mut self, concurrency: usize) -> Self {
        self.concurrency = concurrency.max(1);
        self
    }

    pub fn with_poll_interval(mut self, poll_interval: Duration) -> Self {
        self.poll_interval = poll_interval;
        self
    }
}

#[repr(u8)]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum WorkerState {
    Idle = 0,
    Running = 1,
    Stopped = 2,
}

pub struct Worker {
    adapter: SharedAdapter,
    registry: Registry,
    config: WorkerConfig,
    stop: CancellationToken,
    finished: CancellationToken,
    state: AtomicU8,
}

impl Worker {
    pub fn new(adapter: SharedAdapter, registry: Registry, config: WorkerConfig) -> Self {
        Self {
            adapter,
            registry,
            config,
            stop: CancellationToken::new(),
            finished: CancellationToken::new(),
            state: AtomicU8::new(WorkerState::Idle as u8),
        }
    }

    pub async fn start(&self, cancellation: CancellationToken) -> Result<()> {
        if self
            .state
            .compare_exchange(
                WorkerState::Idle as u8,
                WorkerState::Running as u8,
                Ordering::SeqCst,
                Ordering::SeqCst,
            )
            .is_err()
        {
            return Err(Error::WorkerAlreadyStarted);
        }

        let outcome = self.run(cancellation).await;
        self.state
            .store(WorkerState::Stopped as u8, Ordering::SeqCst);
        self.finished.cancel();
        outcome
    }

    pub async fn stop(&self) -> Result<()> {
        self.stop.cancel();

        if self.state.load(Ordering::SeqCst) == WorkerState::Idle as u8 {
            return Ok(());
        }

        self.finished.cancelled().await;
        Ok(())
    }

    async fn run(&self, cancellation: CancellationToken) -> Result<()> {
        let mut in_flight = FuturesUnordered::new();

        let outcome = loop {
            while in_flight.len() >= self.config.concurrency {
                match self
                    .await_with_signals(&cancellation, in_flight.next())
                    .await?
                {
                    Some(_) => {}
                    None => break,
                }
            }

            if self.stop.is_cancelled() {
                break Ok(());
            }

            if cancellation.is_cancelled() {
                break Err(Error::Cancelled);
            }

            let consume_result = match self
                .await_with_signals(
                    &cancellation,
                    self.adapter
                        .consume(&self.config.queue_name, self.config.max_batch),
                )
                .await?
            {
                Some(result) => result,
                None => break Ok(()),
            };

            let consume_result = match consume_result {
                Ok(result) => result,
                Err(err) => break Err(err),
            };

            if consume_result.items.is_empty() {
                let should_continue = if in_flight.is_empty() {
                    self.wait_for_next_poll(&cancellation).await?
                } else {
                    self.wait_for_inflight_or_poll(&cancellation, &mut in_flight)
                        .await?
                };

                if !should_continue {
                    break Ok(());
                }
                continue;
            }

            for item in consume_result.items {
                while in_flight.len() >= self.config.concurrency {
                    match self
                        .await_with_signals(&cancellation, in_flight.next())
                        .await?
                    {
                        Some(_) => {}
                        None => break,
                    }
                }

                if self.stop.is_cancelled() {
                    break;
                }

                if cancellation.is_cancelled() {
                    break;
                }

                let registry = self.registry.clone();
                in_flight.push(
                    async move {
                        process_message(registry, item).await;
                    }
                    .boxed(),
                );
            }
        };

        while in_flight.next().await.is_some() {}
        outcome
    }

    async fn await_with_signals<T, F>(
        &self,
        cancellation: &CancellationToken,
        future: F,
    ) -> Result<Option<T>>
    where
        F: Future<Output = T>,
    {
        let stop = self.stop.cancelled().fuse();
        let cancelled = cancellation.cancelled().fuse();
        let future = future.fuse();

        pin_mut!(stop, cancelled, future);

        select_biased! {
            _ = stop => Ok(None),
            _ = cancelled => Err(Error::Cancelled),
            value = future => Ok(Some(value)),
        }
    }

    async fn wait_for_next_poll(&self, cancellation: &CancellationToken) -> Result<bool> {
        if self.config.poll_interval.is_zero() {
            return Ok(true);
        }

        Ok(self
            .await_with_signals(cancellation, Delay::new(self.config.poll_interval))
            .await?
            .is_some())
    }

    async fn wait_for_inflight_or_poll(
        &self,
        cancellation: &CancellationToken,
        in_flight: &mut FuturesUnordered<BoxFuture<'static, ()>>,
    ) -> Result<bool> {
        if in_flight.is_empty() {
            return self.wait_for_next_poll(cancellation).await;
        }

        if self.config.poll_interval.is_zero() {
            return Ok(self
                .await_with_signals(cancellation, in_flight.next())
                .await?
                .is_some());
        }

        let stop = self.stop.cancelled().fuse();
        let cancelled = cancellation.cancelled().fuse();
        let next = in_flight.next().fuse();
        let delay = Delay::new(self.config.poll_interval).fuse();

        pin_mut!(stop, cancelled, next, delay);

        select_biased! {
            _ = stop => Ok(false),
            _ = cancelled => Err(Error::Cancelled),
            _ = next => Ok(true),
            _ = delay => Ok(true),
        }
    }
}

pub struct WorkerPool {
    workers: Vec<Arc<Worker>>,
    stop_requested: CancellationToken,
    finished: CancellationToken,
    state: AtomicU8,
}

impl WorkerPool {
    pub fn new(
        adapter: SharedAdapter,
        registry: Registry,
        config: WorkerConfig,
        num_workers: usize,
    ) -> Self {
        let worker_count = num_workers.max(1);
        let workers = (0..worker_count)
            .map(|_| {
                Arc::new(Worker::new(
                    adapter.clone(),
                    registry.clone(),
                    config.clone(),
                ))
            })
            .collect();

        Self {
            workers,
            stop_requested: CancellationToken::new(),
            finished: CancellationToken::new(),
            state: AtomicU8::new(WorkerState::Idle as u8),
        }
    }

    pub async fn start(&self, cancellation: CancellationToken) -> Result<()> {
        if self
            .state
            .compare_exchange(
                WorkerState::Idle as u8,
                WorkerState::Running as u8,
                Ordering::SeqCst,
                Ordering::SeqCst,
            )
            .is_err()
        {
            return Err(Error::WorkerPoolAlreadyStarted);
        }

        let mut tasks = FuturesUnordered::new();
        for worker in &self.workers {
            let worker = Arc::clone(worker);
            let cancellation = cancellation.clone();
            tasks.push(async move { worker.start(cancellation).await }.boxed());
        }

        let outcome = loop {
            let stop = self.stop_requested.cancelled().fuse();
            let cancelled = cancellation.cancelled().fuse();
            let next = tasks.next().fuse();

            pin_mut!(stop, cancelled, next);

            select_biased! {
                _ = stop => {
                    self.stop_all().await;
                    break Ok(());
                }
                _ = cancelled => {
                    self.stop_all().await;
                    break Err(Error::Cancelled);
                }
                task = next => {
                    match task {
                        Some(Ok(())) => {
                            if tasks.is_empty() {
                                break Ok(());
                            }
                        }
                        Some(Err(err)) => {
                            self.stop_all().await;
                            break Err(err);
                        }
                        None => break Ok(()),
                    }
                }
            }
        };

        while tasks.next().await.is_some() {}

        self.state
            .store(WorkerState::Stopped as u8, Ordering::SeqCst);
        self.finished.cancel();
        outcome
    }

    pub async fn stop(&self) -> Result<()> {
        self.stop_requested.cancel();
        self.stop_all().await;

        if self.state.load(Ordering::SeqCst) == WorkerState::Idle as u8 {
            return Ok(());
        }

        self.finished.cancelled().await;
        Ok(())
    }

    async fn stop_all(&self) {
        for worker in &self.workers {
            let _ = worker.stop().await;
        }
    }
}

async fn process_message(registry: Registry, item: MessageItem) {
    if registry.handle(item.message).await.is_err() {
        let _ = item.receipt.nack().await;
        return;
    }

    let _ = item.receipt.ack().await;
}

fn timestamp_ms() -> i64 {
    i64::try_from(
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_millis(),
    )
    .unwrap_or(i64::MAX)
}

fn validate_queue_name(queue_name: &str) -> Result<()> {
    if queue_name.is_empty() {
        return Err(Error::EmptyQueueName);
    }

    if queue_name.trim() != queue_name {
        return Err(Error::QueueNameWhitespace);
    }

    for ch in queue_name.chars() {
        if ch.is_ascii_alphanumeric() || matches!(ch, '-' | '_' | '.') {
            continue;
        }

        return Err(Error::InvalidQueueNameCharacter(ch));
    }

    Ok(())
}

fn validate_topic_action(topic: &str, action: &str) -> Result<()> {
    if topic.is_empty() {
        return Err(Error::EmptyTopic);
    }

    if action.is_empty() {
        return Err(Error::EmptyAction);
    }

    if topic.trim() != topic {
        return Err(Error::TopicWhitespace);
    }

    if action.trim() != action {
        return Err(Error::ActionWhitespace);
    }

    Ok(())
}

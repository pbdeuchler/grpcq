use std::{
    fmt,
    future::Future,
    sync::atomic::{AtomicBool, Ordering},
};

use prost::Message as ProstMessage;

use crate::{
    core::{CancellationToken, Registry, SharedAdapter, Worker, WorkerConfig},
    proto::Message,
    Error, Result,
};

#[derive(Clone, Debug)]
pub struct HandlerContext {
    pub message: Message,
}

#[derive(Clone, Debug)]
pub struct ServerConfig {
    pub queue_name: String,
    pub concurrency: usize,
    pub max_batch: usize,
    pub poll_interval: std::time::Duration,
}

impl Default for ServerConfig {
    fn default() -> Self {
        Self {
            queue_name: "default-queue".to_string(),
            concurrency: 10,
            max_batch: 10,
            poll_interval: std::time::Duration::from_secs(1),
        }
    }
}

impl ServerConfig {
    pub fn with_queue_name(mut self, queue_name: impl Into<String>) -> Self {
        self.queue_name = queue_name.into();
        self
    }

    pub fn with_concurrency(mut self, concurrency: usize) -> Self {
        self.concurrency = concurrency;
        self
    }

    pub fn with_max_batch(mut self, max_batch: usize) -> Self {
        self.max_batch = max_batch;
        self
    }

    pub fn with_poll_interval(mut self, poll_interval: std::time::Duration) -> Self {
        self.poll_interval = poll_interval;
        self
    }
}

pub struct Server {
    registry: Registry,
    worker: Worker,
    started: AtomicBool,
}

impl fmt::Debug for Server {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Server")
            .field("started", &self.started.load(Ordering::Relaxed))
            .finish_non_exhaustive()
    }
}

impl Server {
    pub fn new(adapter: SharedAdapter, config: ServerConfig) -> Self {
        let registry = Registry::new();
        let worker = Worker::new(
            adapter,
            registry.clone(),
            WorkerConfig::new(config.queue_name)
                .with_concurrency(config.concurrency)
                .with_max_batch(config.max_batch)
                .with_poll_interval(config.poll_interval),
        );

        Self {
            registry,
            worker,
            started: AtomicBool::new(false),
        }
    }

    pub fn register_method<TReq, TResp, F, Fut>(
        &self,
        service_name: impl Into<String>,
        method_name: impl Into<String>,
        handler: F,
    ) where
        TReq: ProstMessage + Default + Send + 'static,
        TResp: Send + 'static,
        F: Fn(HandlerContext, TReq) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = Result<TResp>> + Send + 'static,
    {
        let service_name = service_name.into();
        let method_name = method_name.into();
        let handler = std::sync::Arc::new(handler);

        self.registry.register(
            service_name.clone(),
            method_name.clone(),
            move |message: Message| {
                let handler = handler.clone();
                let service_name = service_name.clone();
                let method_name = method_name.clone();

                async move {
                    let request = TReq::decode(message.payload.as_slice()).map_err(|source| {
                        Error::RequestDecode {
                            service: service_name.clone(),
                            method: method_name.clone(),
                            source,
                        }
                    })?;

                    let context = HandlerContext { message };
                    let _ = handler(context, request).await?;
                    Ok(())
                }
            },
        );
    }

    pub async fn start(&self, cancellation: CancellationToken) -> Result<()> {
        self.started.store(true, Ordering::SeqCst);
        self.worker.start(cancellation).await
    }

    pub async fn stop(&self) -> Result<()> {
        if !self.started.load(Ordering::SeqCst) {
            return Err(Error::ServerNotStarted);
        }

        self.worker.stop().await
    }
}

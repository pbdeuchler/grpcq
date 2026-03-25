use std::collections::HashMap;

use prost::Message as ProstMessage;

use crate::{
    core::{MessageSpec, Producer, SharedAdapter},
    Result,
};

#[derive(Clone, Debug)]
pub struct ClientConfig {
    pub queue_name: String,
    pub originator: String,
}

impl Default for ClientConfig {
    fn default() -> Self {
        Self {
            queue_name: "default-queue".to_string(),
            originator: "grpcq-client".to_string(),
        }
    }
}

impl ClientConfig {
    pub fn with_queue_name(mut self, queue_name: impl Into<String>) -> Self {
        self.queue_name = queue_name.into();
        self
    }

    pub fn with_originator(mut self, originator: impl Into<String>) -> Self {
        self.originator = originator.into();
        self
    }
}

#[derive(Clone, Debug, Default)]
pub struct CallOptions {
    queue_name: Option<String>,
    metadata: HashMap<String, String>,
}

impl CallOptions {
    pub fn with_queue_name(mut self, queue_name: impl Into<String>) -> Self {
        self.queue_name = Some(queue_name.into());
        self
    }

    pub fn with_metadata<K, V, I>(mut self, metadata: I) -> Self
    where
        K: Into<String>,
        V: Into<String>,
        I: IntoIterator<Item = (K, V)>,
    {
        self.metadata.extend(
            metadata
                .into_iter()
                .map(|(key, value)| (key.into(), value.into())),
        );
        self
    }
}

pub struct Client {
    producer: Producer,
    config: ClientConfig,
}

impl std::fmt::Debug for Client {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Client")
            .field("config", &self.config)
            .finish_non_exhaustive()
    }
}

impl Client {
    pub fn new(adapter: SharedAdapter, config: ClientConfig) -> Self {
        let producer = Producer::new(adapter, config.originator.clone());
        Self { producer, config }
    }

    pub async fn invoke<T>(
        &self,
        service_name: &str,
        method_name: &str,
        request: &T,
        options: CallOptions,
    ) -> Result<()>
    where
        T: ProstMessage,
    {
        let queue_name = options
            .queue_name
            .as_deref()
            .unwrap_or(&self.config.queue_name);

        self.producer
            .send(
                queue_name,
                service_name,
                method_name,
                request,
                options.metadata,
            )
            .await
    }

    pub async fn invoke_spec(&self, queue_name: &str, spec: &MessageSpec) -> Result<()> {
        self.producer
            .send_batch(queue_name, std::slice::from_ref(spec))
            .await
    }
}

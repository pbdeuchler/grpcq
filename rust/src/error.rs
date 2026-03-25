use thiserror::Error;

pub type Result<T> = std::result::Result<T, Error>;

#[derive(Debug, Error)]
pub enum Error {
    #[error("queue name cannot be empty")]
    EmptyQueueName,
    #[error("queue name cannot have leading or trailing whitespace")]
    QueueNameWhitespace,
    #[error("queue name contains invalid character: {0}")]
    InvalidQueueNameCharacter(char),
    #[error("topic cannot be empty")]
    EmptyTopic,
    #[error("action cannot be empty")]
    EmptyAction,
    #[error("topic cannot have leading or trailing whitespace")]
    TopicWhitespace,
    #[error("action cannot have leading or trailing whitespace")]
    ActionWhitespace,
    #[error(
        "message payload for {topic}.{action} exceeds maximum size of {limit} bytes (got {actual} bytes)"
    )]
    MessageTooLarge {
        topic: String,
        action: String,
        limit: usize,
        actual: usize,
    },
    #[error("no handlers registered for topic: {topic}")]
    UnknownTopic { topic: String },
    #[error("no handler registered for topic: {topic}, action: {action}")]
    UnknownAction { topic: String, action: String },
    #[error("failed to decode request for {service}.{method}: {source}")]
    RequestDecode {
        service: String,
        method: String,
        #[source]
        source: prost::DecodeError,
    },
    #[error("message already acknowledged")]
    AlreadyAcknowledged,
    #[error("message already nacked")]
    AlreadyNacked,
    #[error("queue {queue_name} is full")]
    QueueFull { queue_name: String },
    #[error("consume is not supported by this adapter")]
    ConsumeNotSupported,
    #[error("server not started")]
    ServerNotStarted,
    #[error("worker has already been started")]
    WorkerAlreadyStarted,
    #[error("worker pool has already been started")]
    WorkerPoolAlreadyStarted,
    #[error("operation cancelled")]
    Cancelled,
    #[error("{0}")]
    Other(String),
}

impl Error {
    pub fn other(message: impl Into<String>) -> Self {
        Self::Other(message.into())
    }
}

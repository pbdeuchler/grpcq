pub use async_trait::async_trait;
#[cfg(feature = "tonic")]
pub use tonic;

mod client;
mod core;
mod error;
mod server;
mod signal;

pub mod adapters;
pub mod proto;

pub use client::{CallOptions, Client, ClientConfig};
pub use core::{
    CancellationToken, ConsumeResult, MessageItem, MessageSpec, Producer, QueueAdapter, Receipt,
    Registry, SharedAdapter, SharedReceipt, Worker, WorkerConfig, WorkerPool, MAX_MESSAGE_SIZE,
};
pub use error::{Error, Result};
pub use proto::Message;
pub use server::{HandlerContext, Server, ServerBuilder, ServerConfig, ServiceRegistrar};

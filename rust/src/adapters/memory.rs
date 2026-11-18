use std::{
    collections::{HashMap, VecDeque},
    sync::{Arc, Mutex},
};

use futures::{future::BoxFuture, FutureExt};

use crate::{
    core::{ConsumeResult, MessageItem, QueueAdapter, Receipt},
    Error, Message, Result, SharedReceipt,
};

#[derive(Clone)]
pub struct Adapter {
    inner: Arc<Inner>,
}

struct Inner {
    queues: Mutex<HashMap<String, Queue>>,
    buffer_size: usize,
}

#[derive(Default)]
struct Queue {
    messages: VecDeque<Message>,
    pending: usize,
}

impl Adapter {
    /// Capacity includes queued and in-flight messages until acknowledgement.
    pub fn new(buffer_size: usize) -> Self {
        Self {
            inner: Arc::new(Inner {
                queues: Mutex::new(HashMap::new()),
                buffer_size: if buffer_size == 0 { 1000 } else { buffer_size },
            }),
        }
    }

    pub fn queue_depth(&self, queue_name: &str) -> usize {
        let queues = self
            .inner
            .queues
            .lock()
            .expect("memory adapter lock poisoned");
        queues
            .get(queue_name)
            .map_or(0, |queue| queue.messages.len())
    }

    /// Discard queued messages, preserving outstanding receipts and their capacity.
    pub fn clear(&self) {
        let mut queues = self
            .inner
            .queues
            .lock()
            .expect("memory adapter lock poisoned");
        for queue in queues.values_mut() {
            queue.pending -= queue.messages.len();
            queue.messages.clear();
        }
    }
}

impl QueueAdapter for Adapter {
    fn publish<'a>(
        &'a self,
        queue_name: &'a str,
        messages: &'a [Message],
    ) -> BoxFuture<'a, Result<()>> {
        async move {
            if messages.is_empty() {
                return Ok(());
            }

            let mut queues = self
                .inner
                .queues
                .lock()
                .expect("memory adapter lock poisoned");
            let queue = queues.entry(queue_name.to_string()).or_default();

            if messages.len() > self.inner.buffer_size - queue.pending {
                return Err(Error::QueueFull {
                    queue_name: queue_name.to_string(),
                });
            }

            queue.messages.extend(messages.iter().cloned());
            queue.pending += messages.len();
            Ok(())
        }
        .boxed()
    }

    fn consume<'a>(
        &'a self,
        queue_name: &'a str,
        max_batch: usize,
    ) -> BoxFuture<'a, Result<ConsumeResult>> {
        async move {
            let drained = {
                let mut queues = self
                    .inner
                    .queues
                    .lock()
                    .expect("memory adapter lock poisoned");
                let queue = queues.entry(queue_name.to_string()).or_default();
                let limit = max_batch.max(1).min(queue.messages.len());
                let mut drained = Vec::with_capacity(limit);

                for _ in 0..limit {
                    match queue.messages.pop_front() {
                        Some(message) => drained.push(message),
                        None => break,
                    }
                }

                drained
            };

            let items = drained
                .into_iter()
                .map(|message| MessageItem {
                    receipt: memory_receipt(self.inner.clone(), queue_name, message.clone()),
                    message,
                })
                .collect();

            Ok(ConsumeResult { items })
        }
        .boxed()
    }
}

fn memory_receipt(inner: Arc<Inner>, queue_name: &str, message: Message) -> SharedReceipt {
    Arc::new(MemoryReceipt {
        state: Mutex::new(ReceiptState::Pending),
        inner,
        queue_name: queue_name.to_string(),
        message,
    })
}

struct MemoryReceipt {
    state: Mutex<ReceiptState>,
    inner: Arc<Inner>,
    queue_name: String,
    message: Message,
}

enum ReceiptState {
    Pending,
    Acked,
    Nacked,
}

impl Receipt for MemoryReceipt {
    fn ack(&self) -> BoxFuture<'_, Result<()>> {
        async move {
            let mut state = self.state.lock().expect("memory receipt lock poisoned");
            match *state {
                ReceiptState::Pending => {
                    let mut queues = self
                        .inner
                        .queues
                        .lock()
                        .expect("memory adapter lock poisoned");
                    queues
                        .get_mut(&self.queue_name)
                        .expect("receipt queue exists")
                        .pending -= 1;
                    *state = ReceiptState::Acked;
                    Ok(())
                }
                ReceiptState::Acked => Err(Error::AlreadyAcknowledged),
                ReceiptState::Nacked => Err(Error::AlreadyNacked),
            }
        }
        .boxed()
    }

    fn nack(&self) -> BoxFuture<'_, Result<()>> {
        async move {
            let mut state = self.state.lock().expect("memory receipt lock poisoned");
            match *state {
                ReceiptState::Pending => {}
                ReceiptState::Acked => return Err(Error::AlreadyAcknowledged),
                ReceiptState::Nacked => return Err(Error::AlreadyNacked),
            }

            let mut queues = self
                .inner
                .queues
                .lock()
                .expect("memory adapter lock poisoned");
            let queue = queues
                .get_mut(&self.queue_name)
                .expect("receipt queue exists");
            queue.messages.push_back(self.message.clone());
            *state = ReceiptState::Nacked;

            Ok(())
        }
        .boxed()
    }
}

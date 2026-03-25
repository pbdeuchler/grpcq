// pattern: Imperative Shell (adapter I/O) + Functional Core (encode/decode)

use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};

use aws_sdk_sqs::{
    types::{MessageAttributeValue, SendMessageBatchRequestEntry},
    Client as SqsClient,
};
use base64::{engine::general_purpose::STANDARD as BASE64, Engine as _};
use futures::{future::BoxFuture, FutureExt};
use prost::Message as ProstMessage;

use crate::{
    core::{ConsumeResult, MessageItem, QueueAdapter, Receipt},
    Error, Message, Result, SharedReceipt,
};

const MAX_BATCH_SIZE: usize = 10;
const LONG_POLL_SECONDS: i32 = 20;

pub struct Config {
    pub client: SqsClient,
    pub queue_urls: HashMap<String, String>,
}

#[derive(Clone)]
pub struct Adapter {
    client: SqsClient,
    queue_urls: HashMap<String, String>,
}

impl Adapter {
    pub fn new(config: Config) -> Result<Self> {
        if config.queue_urls.is_empty() {
            return Err(Error::other("at least one queue URL is required"));
        }

        Ok(Self {
            client: config.client,
            queue_urls: config.queue_urls,
        })
    }

    fn resolve_url(&self, queue_name: &str) -> Result<&str> {
        self.queue_urls
            .get(queue_name)
            .map(String::as_str)
            .ok_or_else(|| Error::other(format!("queue name {queue_name} not configured")))
    }

    async fn send_batch(&self, queue_url: &str, messages: &[Message]) -> Result<()> {
        let entries = messages
            .iter()
            .map(build_batch_entry)
            .collect::<Result<Vec<_>>>()?;

        let output = self
            .client
            .send_message_batch()
            .queue_url(queue_url)
            .set_entries(Some(entries))
            .send()
            .await
            .map_err(|e| Error::other(format!("failed to send batch to SQS: {e}")))?;

        let failed = output.failed();
        if !failed.is_empty() {
            let msg = failed[0].message().unwrap_or("unknown error");
            return Err(Error::other(format!(
                "failed to send {} message(s): {msg}",
                failed.len()
            )));
        }

        Ok(())
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

            let queue_url = self.resolve_url(queue_name)?;

            for chunk in messages.chunks(MAX_BATCH_SIZE) {
                self.send_batch(queue_url, chunk).await?;
            }

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
            let queue_url = self.resolve_url(queue_name)?;
            let clamped = max_batch.clamp(1, MAX_BATCH_SIZE) as i32;

            let output = self
                .client
                .receive_message()
                .queue_url(queue_url)
                .max_number_of_messages(clamped)
                .wait_time_seconds(LONG_POLL_SECONDS)
                .message_attribute_names("All")
                .send()
                .await
                .map_err(|e| Error::other(format!("failed to receive from SQS: {e}")))?;

            let items = output
                .messages()
                .iter()
                .map(|sqs_msg| {
                    let message = decode_message_body(sqs_msg.body().unwrap_or(""))?;

                    let receipt: SharedReceipt = Arc::new(SqsReceipt {
                        state: Mutex::new(ReceiptState::Pending),
                        client: self.client.clone(),
                        queue_url: queue_url.to_string(),
                        receipt_handle: sqs_msg.receipt_handle().unwrap_or("").to_string(),
                    });

                    Ok(MessageItem { message, receipt })
                })
                .collect::<Result<Vec<_>>>()?;

            Ok(ConsumeResult { items })
        }
        .boxed()
    }
}

// -- Functional Core: encode/decode --

fn encode_message_body(msg: &Message) -> String {
    BASE64.encode(msg.encode_to_vec())
}

fn decode_message_body(body: &str) -> Result<Message> {
    if let Ok(data) = BASE64.decode(body) {
        if let Ok(msg) = Message::decode(data.as_slice()) {
            return Ok(msg);
        }
    }

    // Fallback: raw protobuf (legacy compat with Go producer)
    Message::decode(body.as_bytes())
        .map_err(|e| Error::other(format!("failed to decode message body: {e}")))
}

fn build_batch_entry(msg: &Message) -> Result<SendMessageBatchRequestEntry> {
    let body = encode_message_body(msg);

    let string_attr = |value: &str| -> Result<MessageAttributeValue> {
        MessageAttributeValue::builder()
            .data_type("String")
            .string_value(value)
            .build()
            .map_err(|e| Error::other(format!("failed to build message attribute: {e}")))
    };

    SendMessageBatchRequestEntry::builder()
        .id(&msg.message_id)
        .message_body(body)
        .message_attributes("topic", string_attr(&msg.topic)?)
        .message_attributes("action", string_attr(&msg.action)?)
        .message_attributes("originator", string_attr(&msg.originator)?)
        .build()
        .map_err(|e| Error::other(format!("failed to build batch entry: {e}")))
}

// -- Receipt --

enum ReceiptState {
    Pending,
    Acked,
    Nacked,
}

struct SqsReceipt {
    state: Mutex<ReceiptState>,
    client: SqsClient,
    queue_url: String,
    receipt_handle: String,
}

impl Receipt for SqsReceipt {
    fn ack(&self) -> BoxFuture<'_, Result<()>> {
        async move {
            {
                let mut state = self.state.lock().expect("sqs receipt lock poisoned");
                match *state {
                    ReceiptState::Pending => *state = ReceiptState::Acked,
                    ReceiptState::Acked => return Err(Error::AlreadyAcknowledged),
                    ReceiptState::Nacked => return Err(Error::AlreadyNacked),
                }
            }

            self.client
                .delete_message()
                .queue_url(&self.queue_url)
                .receipt_handle(&self.receipt_handle)
                .send()
                .await
                .map_err(|e| Error::other(format!("failed to delete message from SQS: {e}")))?;

            Ok(())
        }
        .boxed()
    }

    fn nack(&self) -> BoxFuture<'_, Result<()>> {
        async move {
            {
                let mut state = self.state.lock().expect("sqs receipt lock poisoned");
                match *state {
                    ReceiptState::Pending => *state = ReceiptState::Nacked,
                    ReceiptState::Acked => return Err(Error::AlreadyAcknowledged),
                    ReceiptState::Nacked => return Err(Error::AlreadyNacked),
                }
            }

            self.client
                .change_message_visibility()
                .queue_url(&self.queue_url)
                .receipt_handle(&self.receipt_handle)
                .visibility_timeout(0)
                .send()
                .await
                .map_err(|e| Error::other(format!("failed to change visibility in SQS: {e}")))?;

            Ok(())
        }
        .boxed()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn encode_decode_round_trip() {
        let original = Message {
            originator: "svc".into(),
            topic: "topic".into(),
            action: "action".into(),
            message_id: "id-1".into(),
            payload: vec![0x00, 0xff, 0x10, 0x80],
            ..Default::default()
        };

        let body = encode_message_body(&original);
        let decoded = decode_message_body(&body).unwrap();

        assert_eq!(decoded.originator, original.originator);
        assert_eq!(decoded.topic, original.topic);
        assert_eq!(decoded.action, original.action);
        assert_eq!(decoded.message_id, original.message_id);
        assert_eq!(decoded.payload, original.payload);
    }

    #[test]
    fn encode_produces_valid_base64() {
        let msg = Message {
            originator: "svc".into(),
            topic: "t".into(),
            action: "a".into(),
            message_id: "id".into(),
            payload: vec![0x00, 0xff],
            ..Default::default()
        };

        let body = encode_message_body(&msg);
        assert!(BASE64.decode(&body).is_ok());
    }

    #[test]
    fn decode_invalid_body_returns_error() {
        let result = decode_message_body("not-valid-base64-or-protobuf!!!");
        assert!(result.is_err());
    }

    #[test]
    fn decode_empty_body_returns_default_message() {
        // Empty string base64-decodes to empty bytes, which prost decodes as default Message
        let result = decode_message_body("");
        assert!(result.is_ok());
        let msg = result.unwrap();
        assert!(msg.message_id.is_empty());
    }

    #[test]
    fn round_trip_preserves_metadata() {
        let mut metadata = HashMap::new();
        metadata.insert("key".to_string(), "value".to_string());
        metadata.insert("trace-id".to_string(), "abc-123".to_string());

        let original = Message {
            originator: "svc".into(),
            topic: "topic".into(),
            action: "action".into(),
            message_id: "id-meta".into(),
            payload: vec![1, 2, 3],
            metadata,
            ..Default::default()
        };

        let body = encode_message_body(&original);
        let decoded = decode_message_body(&body).unwrap();

        assert_eq!(decoded.metadata, original.metadata);
    }

    #[test]
    fn round_trip_preserves_timestamp() {
        let original = Message {
            originator: "svc".into(),
            topic: "topic".into(),
            action: "action".into(),
            message_id: "id-ts".into(),
            timestamp_ms: 1711234567890,
            ..Default::default()
        };

        let body = encode_message_body(&original);
        let decoded = decode_message_body(&body).unwrap();

        assert_eq!(decoded.timestamp_ms, original.timestamp_ms);
    }
}

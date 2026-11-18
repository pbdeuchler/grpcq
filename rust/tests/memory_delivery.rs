use futures::executor::block_on;
use grpcq::{adapters::memory::Adapter, Error, Message, QueueAdapter};

fn message(id: &str) -> Message {
    Message {
        message_id: id.into(),
        ..Message::default()
    }
}

#[test]
fn capacity_includes_unacknowledged_messages() {
    block_on(async {
        let adapter = Adapter::new(1);
        adapter
            .publish("queue", &[message("original")])
            .await
            .unwrap();
        let delivery = adapter.consume("queue", 1).await.unwrap();
        assert!(matches!(
            adapter.publish("queue", &[message("replacement")]).await,
            Err(Error::QueueFull { .. })
        ));
        delivery.items[0].receipt.nack().await.unwrap();
        let retry = adapter.consume("queue", 1).await.unwrap();
        assert_eq!(retry.items.len(), 1);
        assert_eq!(retry.items[0].message.message_id, "original");
        retry.items[0].receipt.ack().await.unwrap();
        adapter
            .publish("queue", &[message("replacement")])
            .await
            .unwrap();
    });
}

#[test]
fn receipts_settle_exactly_once() {
    block_on(async {
        for first_ack in [true, false] {
            for second_ack in [true, false] {
                let adapter = Adapter::new(1);
                adapter
                    .publish("queue", &[message("original")])
                    .await
                    .unwrap();
                let delivery = adapter.consume("queue", 1).await.unwrap();
                let receipt = &delivery.items[0].receipt;
                if first_ack {
                    receipt.ack().await
                } else {
                    receipt.nack().await
                }
                .unwrap();
                let error = if second_ack {
                    receipt.ack().await
                } else {
                    receipt.nack().await
                }
                .unwrap_err();
                assert!(matches!(
                    (&error, first_ack),
                    (Error::AlreadyAcknowledged, true) | (Error::AlreadyNacked, false)
                ));
                assert_eq!(adapter.queue_depth("queue"), usize::from(!first_ack));
                assert_eq!(
                    adapter.publish("queue", &[message("next")]).await.is_ok(),
                    first_ack
                );
            }
        }
    });
}

#[test]
fn clear_preserves_outstanding_receipts() {
    block_on(async {
        for ack in [true, false] {
            let adapter = Adapter::new(2);
            adapter
                .publish("queue", &[message("in-flight"), message("queued")])
                .await
                .unwrap();
            let delivery = adapter.consume("queue", 1).await.unwrap();
            adapter.clear();
            adapter.publish("queue", &[message("new")]).await.unwrap();
            assert!(matches!(
                adapter.publish("queue", &[message("overflow")]).await,
                Err(Error::QueueFull { .. })
            ));
            if ack {
                delivery.items[0].receipt.ack().await.unwrap();
                adapter
                    .publish("queue", &[message("after-ack")])
                    .await
                    .unwrap();
            } else {
                delivery.items[0].receipt.nack().await.unwrap();
                let retried = adapter.consume("queue", 2).await.unwrap();
                assert_eq!(retried.items.len(), 2);
                assert_eq!(retried.items[1].message.message_id, "in-flight");
            }
        }
    });
}

#[test]
fn concurrent_receipt_settlement_succeeds_once() {
    block_on(async {
        let adapter = Adapter::new(1);
        adapter
            .publish("queue", &[message("original")])
            .await
            .unwrap();
        let delivery = adapter.consume("queue", 1).await.unwrap();
        let ack_receipt = delivery.items[0].receipt.clone();
        let nack_receipt = delivery.items[0].receipt.clone();
        let (ack, nack) = std::thread::scope(|scope| {
            let ack = scope.spawn(|| block_on(ack_receipt.ack()));
            let nack = scope.spawn(|| block_on(nack_receipt.nack()));
            (ack.join().unwrap(), nack.join().unwrap())
        });
        assert_ne!(ack.is_ok(), nack.is_ok());
        assert_eq!(adapter.queue_depth("queue"), usize::from(nack.is_ok()));
        assert_eq!(
            adapter.publish("queue", &[message("next")]).await.is_ok(),
            ack.is_ok()
        );
    });
}

#[test]
fn batches_respect_inflight_capacity_and_queue_isolation() {
    block_on(async {
        let adapter = Adapter::new(3);
        adapter
            .publish("queue", &[message("a"), message("b")])
            .await
            .unwrap();
        let delivery = adapter.consume("queue", 2).await.unwrap();
        assert!(matches!(
            adapter
                .publish("queue", &[message("c"), message("d")])
                .await,
            Err(Error::QueueFull { .. })
        ));
        assert_eq!(adapter.queue_depth("queue"), 0);
        adapter.publish("other", &[message("other")]).await.unwrap();
        adapter.publish("queue", &[message("c")]).await.unwrap();
        for item in delivery.items {
            item.receipt.nack().await.unwrap();
        }
        assert_eq!(adapter.queue_depth("queue"), 3);
        assert_eq!(adapter.queue_depth("other"), 1);
    });
}

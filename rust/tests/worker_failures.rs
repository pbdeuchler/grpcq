use futures::{
    channel::oneshot,
    executor::block_on,
    future::{select, BoxFuture, Either},
    FutureExt,
};
use grpcq::{
    adapters::memory::Adapter, CancellationToken, ConsumeResult, Error, Message, MessageItem,
    QueueAdapter, Receipt, Registry, Result, Worker, WorkerConfig, WorkerPool,
};
use std::{
    sync::{
        atomic::{AtomicUsize, Ordering},
        Arc, Mutex,
    },
    time::Duration,
};

fn run<T>(future: impl std::future::Future<Output = T>) -> T {
    block_on(async {
        match select(
            Box::pin(future),
            Box::pin(futures_timer::Delay::new(Duration::from_secs(2))),
        )
        .await
        {
            Either::Left((value, _)) => value,
            Either::Right(_) => panic!("worker failure was not surfaced"),
        }
    })
}

struct TestReceipt {
    ack_fails: bool,
    nack_fails: bool,
    acks: AtomicUsize,
    nacks: AtomicUsize,
    cancel: CancellationToken,
}
impl Receipt for TestReceipt {
    fn ack(&self) -> BoxFuture<'_, Result<()>> {
        async move {
            self.acks.fetch_add(1, Ordering::SeqCst);
            self.cancel.cancel();
            if self.ack_fails {
                Err(Error::other("delete failed"))
            } else {
                Ok(())
            }
        }
        .boxed()
    }
    fn nack(&self) -> BoxFuture<'_, Result<()>> {
        async move {
            self.nacks.fetch_add(1, Ordering::SeqCst);
            self.cancel.cancel();
            if self.nack_fails {
                Err(Error::other("requeue failed"))
            } else {
                Ok(())
            }
        }
        .boxed()
    }
}
struct SingleDelivery(Mutex<Option<MessageItem>>);
impl QueueAdapter for SingleDelivery {
    fn publish<'a>(&'a self, _: &'a str, _: &'a [Message]) -> BoxFuture<'a, Result<()>> {
        async { Ok(()) }.boxed()
    }
    fn consume<'a>(&'a self, _: &'a str, _: usize) -> BoxFuture<'a, Result<ConsumeResult>> {
        async move {
            let item = self.0.lock().unwrap().take();
            match item {
                Some(item) => Ok(ConsumeResult { items: vec![item] }),
                None => futures::future::pending().await,
            }
        }
        .boxed()
    }
}
fn message(id: &str) -> Message {
    Message {
        message_id: id.into(),
        topic: "svc".into(),
        action: "call".into(),
        ..Default::default()
    }
}

#[test]
fn processing_and_settlement_failures_survive_cancellation() {
    for pooled in [false, true] {
        for (handler_fails, ack_fails, nack_fails) in [
            (false, false, false),
            (true, false, false),
            (false, true, false),
            (true, false, true),
        ] {
            run(async {
                let cancel = CancellationToken::new();
                let receipt = Arc::new(TestReceipt {
                    ack_fails,
                    nack_fails,
                    acks: AtomicUsize::new(0),
                    nacks: AtomicUsize::new(0),
                    cancel: cancel.clone(),
                });
                let adapter = Arc::new(SingleDelivery(Mutex::new(Some(MessageItem {
                    message: message("message-1"),
                    receipt: receipt.clone(),
                }))));
                let registry = Registry::new();
                registry.register("svc", "call", move |_| async move {
                    if handler_fails {
                        Err(Error::other("handler failed"))
                    } else {
                        Ok(())
                    }
                });
                let config = WorkerConfig::new("q").with_concurrency(1);
                let error = if pooled {
                    WorkerPool::new(adapter, registry, config, 2)
                        .start(cancel)
                        .await
                } else {
                    Worker::new(adapter, registry, config).start(cancel).await
                }
                .unwrap_err();
                if !handler_fails && !ack_fails {
                    assert!(matches!(error, Error::Cancelled));
                } else {
                    let diagnostic = error.to_string();
                    for detail in ["message-1", "svc/call"] {
                        assert!(diagnostic.contains(detail), "{diagnostic}");
                    }
                    if handler_fails {
                        assert!(diagnostic.contains("handler failed"), "{diagnostic}");
                    }
                    if ack_fails {
                        assert!(diagnostic.contains("delete failed"), "{diagnostic}");
                    }
                    if nack_fails {
                        assert!(diagnostic.contains("requeue failed"), "{diagnostic}");
                    }
                    assert!(matches!(error, Error::Delivery { .. }));
                }
                assert_eq!(
                    receipt.acks.load(Ordering::SeqCst),
                    usize::from(!handler_fails)
                );
                assert_eq!(
                    receipt.nacks.load(Ordering::SeqCst),
                    usize::from(handler_fails)
                );
            });
        }
    }
}

#[test]
fn poison_messages_stop_intake_and_preserve_the_received_batch() {
    for registered in [false, true] {
        run(async {
            let adapter = Arc::new(Adapter::new(2));
            adapter
                .publish("q", &[message("bad"), message("good")])
                .await
                .unwrap();
            let calls = Arc::new(AtomicUsize::new(0));
            let count = calls.clone();
            let registry = Registry::new();
            if registered {
                registry.register("svc", "call", move |message| {
                    count.fetch_add(1, Ordering::SeqCst);
                    async move {
                        if message.message_id == "bad" {
                            Err(Error::other("permanent failure"))
                        } else {
                            Ok(())
                        }
                    }
                });
            }
            let worker = Worker::new(
                adapter.clone(),
                registry,
                WorkerConfig::new("q").with_concurrency(1).with_max_batch(2),
            );
            assert!(matches!(
                worker.start(CancellationToken::new()).await,
                Err(Error::Delivery { .. })
            ));
            assert_eq!(calls.load(Ordering::SeqCst), if registered { 2 } else { 0 });
            assert_eq!(adapter.queue_depth("q"), if registered { 1 } else { 2 });
        });
    }
}

#[test]
fn processing_failure_stops_other_pool_workers() {
    run(async {
        let adapter = Arc::new(Adapter::new(1));
        adapter.publish("q", &[message("poison")]).await.unwrap();
        let pool = WorkerPool::new(adapter.clone(), Registry::new(), WorkerConfig::new("q"), 2);
        assert!(matches!(
            pool.start(CancellationToken::new()).await,
            Err(Error::Delivery { .. })
        ));
        assert_eq!(adapter.queue_depth("q"), 1);
    });
}

#[test]
fn pool_returns_handler_failures_that_complete_during_shutdown() {
    for cancel in [false, true] {
        run(async {
            let adapter = Arc::new(Adapter::new(1));
            adapter.publish("q", &[message("suspended")]).await.unwrap();
            let (release, blocked) = oneshot::channel::<()>();
            let blocked = Mutex::new(Some(blocked));
            let registry = Registry::new();
            registry.register("svc", "call", move |_| {
                let blocked = blocked.lock().unwrap().take().unwrap();
                async move {
                    blocked.await.unwrap();
                    Err(Error::other("handler failed after shutdown"))
                }
            });
            let pool = WorkerPool::new(adapter.clone(), registry, WorkerConfig::new("q"), 2);
            let token = CancellationToken::new();
            let mut running = Box::pin(pool.start(token.clone()));
            assert!(futures::poll!(running.as_mut()).is_pending());
            let mut stopping = Box::pin(pool.stop());
            if cancel {
                token.cancel();
            } else {
                assert!(futures::poll!(stopping.as_mut()).is_pending());
            }
            assert!(futures::poll!(running.as_mut()).is_pending());
            release.send(()).unwrap();
            assert!(matches!(running.await, Err(Error::Delivery { .. })));
            assert_eq!(adapter.queue_depth("q"), 1);
            if !cancel {
                stopping.await.unwrap();
            }
        });
    }
}

#[test]
fn typed_request_decode_failure_is_returned_without_calling_the_handler() {
    run(async {
        let adapter = Arc::new(Adapter::new(1));
        let mut invalid = message("invalid-request");
        invalid.payload = vec![0xff];
        adapter.publish("q", &[invalid]).await.unwrap();
        let calls = Arc::new(AtomicUsize::new(0));
        let count = calls.clone();
        let server = grpcq::Server::new(
            adapter.clone(),
            grpcq::ServerConfig::default().with_queue_name("q"),
        );
        server.register_method::<Message, (), _, _>("svc", "call", move |_, _| {
            count.fetch_add(1, Ordering::SeqCst);
            async { Ok(()) }
        });
        assert!(
            matches!(server.start(CancellationToken::new()).await, Err(Error::Delivery { source, .. }) if matches!(*source, Error::RequestDecode { .. }))
        );
        assert_eq!(calls.load(Ordering::SeqCst), 0);
        assert_eq!(adapter.queue_depth("q"), 1);
    });
}

use futures::{
    channel::oneshot,
    executor::block_on,
    future::{select, BoxFuture, Either},
    FutureExt,
};
use grpcq::{
    adapters::memory::Adapter, CancellationToken, ConsumeResult, Error, Message, QueueAdapter,
    Registry, Result, Worker, WorkerConfig, WorkerPool,
};
use std::{
    future::Future,
    sync::{
        atomic::{AtomicBool, AtomicUsize, Ordering},
        Arc,
    },
    time::Duration,
};

fn run<T>(future: impl Future<Output = T>) -> T {
    block_on(async {
        match select(
            Box::pin(future),
            Box::pin(futures_timer::Delay::new(Duration::from_secs(2))),
        )
        .await
        {
            Either::Left((result, _)) => result,
            Either::Right(_) => panic!("worker lifecycle operation timed out"),
        }
    })
}

fn message() -> Message {
    Message {
        topic: "svc".into(),
        action: "call".into(),
        ..Default::default()
    }
}

#[test]
fn cancellation_drains_suspended_handlers() {
    run(async {
        let adapter = Arc::new(Adapter::new(1));
        adapter.publish("q", &[message()]).await.unwrap();
        let (release, blocked) = oneshot::channel::<()>();
        let blocked = Arc::new(std::sync::Mutex::new(Some(blocked)));
        let started = Arc::new(AtomicBool::new(false));
        let handler_started = started.clone();
        let registry = Registry::new();
        registry.register("svc", "call", move |_| {
            let blocked = blocked.lock().unwrap().take().unwrap();
            handler_started.store(true, Ordering::SeqCst);
            async move {
                blocked.await.unwrap();
                Ok(())
            }
        });
        let token = CancellationToken::new();
        let worker = Worker::new(
            adapter.clone(),
            registry,
            WorkerConfig::new("q").with_concurrency(1),
        );
        let mut running = Box::pin(worker.start(token.clone()));
        assert!(futures::poll!(running.as_mut()).is_pending());
        assert!(started.load(Ordering::SeqCst));
        token.cancel();
        assert!(
            futures::poll!(running.as_mut()).is_pending(),
            "cancellation dropped the handler instead of draining it"
        );
        release.send(()).unwrap();
        assert!(matches!(running.await, Err(Error::Cancelled)));
        adapter
            .publish("q", &[message()])
            .await
            .expect("drained handler must acknowledge its receipt");
    });
}

#[test]
fn cancellation_drains_the_entire_received_batch() {
    run(async {
        let adapter = Arc::new(Adapter::new(2));
        adapter.publish("q", &[message(), message()]).await.unwrap();
        let token = CancellationToken::new();
        let cancel = token.clone();
        let calls = Arc::new(AtomicUsize::new(0));
        let count = calls.clone();
        let registry = Registry::new();
        registry.register("svc", "call", move |_| {
            count.fetch_add(1, Ordering::SeqCst);
            cancel.cancel();
            async { Ok(()) }
        });
        let worker = Worker::new(
            adapter.clone(),
            registry,
            WorkerConfig::new("q").with_concurrency(1).with_max_batch(2),
        );
        assert!(matches!(worker.start(token).await, Err(Error::Cancelled)));
        assert_eq!(calls.load(Ordering::SeqCst), 2);
        adapter
            .publish("q", &[message(), message()])
            .await
            .expect("every received message must be settled");
    });
}

struct LongPoll {
    first: Arc<Adapter>,
    polls: AtomicUsize,
    fail_after_delivery: bool,
}
impl QueueAdapter for LongPoll {
    fn publish<'a>(&'a self, _: &'a str, _: &'a [Message]) -> BoxFuture<'a, Result<()>> {
        async { Ok(()) }.boxed()
    }
    fn consume<'a>(&'a self, _: &'a str, _: usize) -> BoxFuture<'a, Result<ConsumeResult>> {
        async move {
            if self.polls.fetch_add(1, Ordering::SeqCst) == 0 {
                self.first.consume("q", 1).await
            } else if self.fail_after_delivery {
                Err(Error::other("receive failed"))
            } else {
                futures::future::pending().await
            }
        }
        .boxed()
    }
}

#[test]
fn handlers_progress_while_the_next_receive_is_pending() {
    run(async {
        let memory = Arc::new(Adapter::new(1));
        memory.publish("q", &[message()]).await.unwrap();
        let adapter = Arc::new(LongPoll {
            first: memory.clone(),
            polls: AtomicUsize::new(0),
            fail_after_delivery: false,
        });
        let token = CancellationToken::new();
        let cancel = token.clone();
        let registry = Registry::new();
        registry.register("svc", "call", move |_| {
            cancel.cancel();
            async { Ok(()) }
        });
        let worker = Worker::new(adapter, registry, WorkerConfig::new("q"));
        assert!(matches!(worker.start(token).await, Err(Error::Cancelled)));
        memory.publish("q", &[message()]).await.unwrap();
    });
}

#[test]
fn receive_failure_still_drains_received_work() {
    run(async {
        let memory = Arc::new(Adapter::new(1));
        memory.publish("q", &[message()]).await.unwrap();
        let adapter = Arc::new(LongPoll {
            first: memory.clone(),
            polls: AtomicUsize::new(0),
            fail_after_delivery: true,
        });
        let (release, blocked) = oneshot::channel::<()>();
        let blocked = Arc::new(std::sync::Mutex::new(Some(blocked)));
        let registry = Registry::new();
        registry.register("svc", "call", move |_| {
            let blocked = blocked.lock().unwrap().take().unwrap();
            async move {
                blocked.await.unwrap();
                Ok(())
            }
        });
        let worker = Worker::new(adapter, registry, WorkerConfig::new("q"));
        let mut running = Box::pin(worker.start(CancellationToken::new()));
        assert!(futures::poll!(running.as_mut()).is_pending());
        release.send(()).unwrap();
        assert!(matches!(running.await, Err(Error::Other(message)) if message == "receive failed"));
        memory.publish("q", &[message()]).await.unwrap();
    });
}

#[test]
fn pool_cancellation_and_stop_complete() {
    for cancel in [true, false] {
        run(async {
            let pool = WorkerPool::new(
                Arc::new(Adapter::new(1)),
                Registry::new(),
                WorkerConfig::new("q"),
                2,
            );
            let token = CancellationToken::new();
            let mut running = Box::pin(pool.start(token.clone()));
            assert!(futures::poll!(running.as_mut()).is_pending());
            if cancel {
                token.cancel();
                assert!(matches!(running.await, Err(Error::Cancelled)));
            } else {
                let (result, stopped) = futures::join!(running, pool.stop());
                result.unwrap();
                stopped.unwrap();
            }
        });
    }
}

#[test]
fn pool_failure_stops_other_running_workers() {
    run(async {
        struct FailingPoll(AtomicUsize);
        impl QueueAdapter for FailingPoll {
            fn publish<'a>(&'a self, _: &'a str, _: &'a [Message]) -> BoxFuture<'a, Result<()>> {
                async { Ok(()) }.boxed()
            }
            fn consume<'a>(&'a self, _: &'a str, _: usize) -> BoxFuture<'a, Result<ConsumeResult>> {
                async move {
                    let call = self.0.fetch_add(1, Ordering::SeqCst);
                    if call == 0 {
                        futures::future::pending().await
                    } else {
                        Err(Error::other("receive failed"))
                    }
                }
                .boxed()
            }
        }
        let pool = WorkerPool::new(
            Arc::new(FailingPoll(AtomicUsize::new(0))),
            Registry::new(),
            WorkerConfig::new("q"),
            2,
        );
        assert!(
            matches!(pool.start(CancellationToken::new()).await, Err(Error::Other(message)) if message == "receive failed")
        );
    });
}

#[test]
fn failed_received_messages_are_nacked_during_shutdown() {
    run(async {
        let memory = Arc::new(Adapter::new(2));
        memory.publish("q", &[message(), message()]).await.unwrap();
        let token = CancellationToken::new();
        let cancel = token.clone();
        let registry = Registry::new();
        registry.register("svc", "call", move |_| {
            cancel.cancel();
            async { Err(Error::other("handler failed")) }
        });
        let worker = Worker::new(
            memory.clone(),
            registry,
            WorkerConfig::new("q").with_concurrency(1).with_max_batch(2),
        );
        assert!(matches!(
            worker.start(token).await,
            Err(Error::Delivery {
                operation: "handle",
                ..
            })
        ));
        assert_eq!(memory.queue_depth("q"), 2);
    });
}

#[test]
fn stopped_worker_drains_suspended_handler() {
    run(async {
        let memory = Arc::new(Adapter::new(1));
        memory.publish("q", &[message()]).await.unwrap();
        let (release, blocked) = oneshot::channel::<()>();
        let blocked = Arc::new(std::sync::Mutex::new(Some(blocked)));
        let registry = Registry::new();
        registry.register("svc", "call", move |_| {
            let blocked = blocked.lock().unwrap().take().unwrap();
            async move {
                blocked.await.unwrap();
                Ok(())
            }
        });
        let worker = Worker::new(
            memory.clone(),
            registry,
            WorkerConfig::new("q").with_concurrency(1),
        );
        let mut running = Box::pin(worker.start(CancellationToken::new()));
        assert!(futures::poll!(running.as_mut()).is_pending());
        let mut stopping = Box::pin(worker.stop());
        assert!(futures::poll!(stopping.as_mut()).is_pending());
        assert!(futures::poll!(running.as_mut()).is_pending());
        release.send(()).unwrap();
        running.await.unwrap();
        stopping.await.unwrap();
        memory.publish("q", &[message()]).await.unwrap();
    });
}

#[test]
fn zero_poll_interval_still_drives_handlers() {
    run(async {
        let memory = Arc::new(Adapter::new(1));
        memory.publish("q", &[message()]).await.unwrap();
        let token = CancellationToken::new();
        let cancel = token.clone();
        let registry = Registry::new();
        registry.register("svc", "call", move |_| {
            cancel.cancel();
            async { Ok(()) }
        });
        let worker = Worker::new(
            memory.clone(),
            registry,
            WorkerConfig::new("q").with_poll_interval(Duration::ZERO),
        );
        assert!(matches!(worker.start(token).await, Err(Error::Cancelled)));
        memory.publish("q", &[message()]).await.unwrap();
    });
}

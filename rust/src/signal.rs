use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc,
};

use event_listener::Event;

#[derive(Clone, Debug, Default)]
pub struct Signal {
    inner: Arc<Inner>,
}

#[derive(Debug, Default)]
struct Inner {
    set: AtomicBool,
    event: Event,
}

impl Signal {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn is_set(&self) -> bool {
        self.inner.set.load(Ordering::Acquire)
    }

    pub fn set(&self) {
        if !self.inner.set.swap(true, Ordering::Release) {
            self.inner.event.notify(usize::MAX);
        }
    }

    pub async fn wait(&self) {
        if self.inner.set.load(Ordering::Acquire) {
            return;
        }

        let listener = self.inner.event.listen();
        if self.inner.set.load(Ordering::Acquire) {
            return;
        }

        listener.await;
    }
}

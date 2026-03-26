use std::{sync::Arc, thread, time::Duration};

use futures::executor::block_on;
use grpcq::{
    adapters::memory, CancellationToken, ClientConfig, Server, ServerConfig, SharedAdapter,
};

pub mod generated {
    include!(concat!(env!("OUT_DIR"), "/examples.greeter.rs"));
}

use generated::{
    greeter_consumer::{Greeter, GreeterConsumer},
    greeter_producer::GreeterProducer,
    HelloReply, HelloRequest,
};

struct GreeterService;

#[grpcq::async_trait]
impl Greeter for GreeterService {
    async fn say_hello(&self, req: HelloRequest) -> grpcq::Result<HelloReply> {
        Ok(HelloReply {
            message: format!("hello {}", req.name),
        })
    }
}

fn main() -> grpcq::Result<()> {
    let adapter: SharedAdapter = Arc::new(memory::Adapter::new(16));
    let token = CancellationToken::new();
    let token_for_thread = token.clone();

    let server = Server::builder(
        adapter.clone(),
        ServerConfig::default()
            .with_queue_name("examples")
            .with_poll_interval(Duration::from_millis(10)),
    )
    .add_service(GreeterConsumer::new(GreeterService));

    let handle = thread::spawn(move || block_on(server.serve(token_for_thread)));

    let producer = GreeterProducer::new(
        adapter,
        ClientConfig::default()
            .with_queue_name("examples")
            .with_originator("typed-greeter"),
    );

    block_on(producer.say_hello(HelloRequest {
        name: "world".to_string(),
    }))?;

    token.cancel();
    let _ = handle.join();
    Ok(())
}

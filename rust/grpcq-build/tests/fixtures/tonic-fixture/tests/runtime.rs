use std::{
    sync::{mpsc, Arc},
    thread,
    time::Duration,
};

use futures::executor::block_on;
use grpcq::{
    adapters::memory, CancellationToken, ClientConfig, Error, Server, ServerConfig, SharedAdapter,
};
use grpcq_build_tonic_fixture::{
    grpcq_generated::{
        greeter_consumer::{Greeter as GrpcqGreeter, GreeterConsumer},
        greeter_producer::GreeterProducer,
        HelloReply, HelloRequest,
    },
    tonic_generated::greeter_server::{Greeter as TonicGreeter, GreeterServer},
};

struct GreeterService {
    processed_tx: mpsc::Sender<String>,
}

impl GreeterService {
    fn handle(&self, req: HelloRequest) -> Result<HelloReply, grpcq::tonic::Status> {
        self.processed_tx
            .send(req.name.clone())
            .expect("processed request should be recorded");
        Ok(HelloReply {
            message: format!("hello {}", req.name),
        })
    }
}

#[grpcq::tonic::async_trait]
impl GrpcqGreeter for GreeterService {
    async fn say_hello(
        &self,
        req: grpcq::tonic::Request<HelloRequest>,
    ) -> std::result::Result<grpcq::tonic::Response<HelloReply>, grpcq::tonic::Status> {
        Ok(grpcq::tonic::Response::new(self.handle(req.into_inner())?))
    }
}

#[grpcq::tonic::async_trait]
impl TonicGreeter for GreeterService {
    async fn say_hello(
        &self,
        req: grpcq::tonic::Request<HelloRequest>,
    ) -> std::result::Result<grpcq::tonic::Response<HelloReply>, grpcq::tonic::Status> {
        Ok(grpcq::tonic::Response::new(self.handle(req.into_inner())?))
    }
}

#[test]
fn tonic_compatible_service_type_registers_with_both_servers() {
    let adapter = Arc::new(memory::Adapter::new(16));
    let shared: SharedAdapter = adapter.clone();
    let (processed_tx, processed_rx) = mpsc::channel();

    let server = Server::builder(
        shared.clone(),
        ServerConfig::default()
            .with_queue_name("queue")
            .with_poll_interval(Duration::from_millis(10)),
    )
    .add_service(GreeterConsumer::new(GreeterService { processed_tx }));

    let cancellation = CancellationToken::new();
    let cancellation_for_thread = cancellation.clone();
    let handle = thread::spawn(move || block_on(server.serve(cancellation_for_thread)));

    let producer = GreeterProducer::new(
        shared,
        ClientConfig::default()
            .with_queue_name("queue")
            .with_originator("origin"),
    );
    block_on(producer.say_hello(HelloRequest {
        name: "alice".to_string(),
    }))
    .expect("generated producer should publish");

    let processed = processed_rx
        .recv_timeout(Duration::from_secs(2))
        .expect("grpcq consumer should process the typed request");
    assert_eq!(processed, "alice");

    let (discard_tx, _discard_rx) = mpsc::channel();
    let tonic_service = GreeterServer::new(GreeterService {
        processed_tx: discard_tx,
    });
    let _ = grpcq::tonic::transport::Server::builder().add_service(tonic_service);

    cancellation.cancel();

    let outcome = handle.join().expect("server thread should join");
    assert!(matches!(outcome, Err(Error::Cancelled)));
}

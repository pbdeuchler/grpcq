use std::sync::{Arc, Mutex};

use futures::executor::block_on;
use grpcq::{
    adapters::memory, CancellationToken, ClientConfig, Error, Message, QueueAdapter, Server,
    ServerConfig,
};
use grpcq_build_tonic_fixture::grpcq_generated::{
    greeter_consumer::GreeterConsumer,
    greeter_producer::GreeterProducer,
    greeter_server::{Greeter, GreeterServer},
    HelloReply, HelloRequest,
};

#[derive(Default)]
struct GreeterService {
    calls: Mutex<Vec<String>>,
    cancellation: CancellationToken,
}

// The ordinary tonic implementation is also the queue consumer implementation.
#[grpcq::tonic::async_trait]
impl Greeter for GreeterService {
    async fn say_hello(
        &self,
        req: grpcq::tonic::Request<HelloRequest>,
    ) -> Result<grpcq::tonic::Response<HelloReply>, grpcq::tonic::Status> {
        let name = req.into_inner().name;
        self.calls.lock().unwrap().push(name.clone());
        if name == "reject" {
            return Err(grpcq::tonic::Status::invalid_argument("name rejected"));
        }
        self.cancellation.cancel();
        Ok(grpcq::tonic::Response::new(HelloReply {
            message: format!("hello {name}"),
        }))
    }
}

async fn grpc_call(
    service: Arc<GreeterService>,
    name: &str,
) -> Result<HelloReply, grpcq::tonic::Status> {
    // Exercise tonic's real codecs, HTTP routing and generated server in process.
    let mut client = grpcq::tonic::client::Grpc::new(GreeterServer::from_arc(service));
    let response = client
        .unary(
            grpcq::tonic::Request::new(HelloRequest { name: name.into() }),
            grpcq::tonic::codegen::http::uri::PathAndQuery::from_static(
                "/grpcq.test.Greeter/SayHello",
            ),
            grpcq::tonic::codec::ProstCodec::<HelloRequest, HelloReply>::default(),
        )
        .await?;
    Ok(response.into_inner())
}

#[test]
fn one_tonic_implementation_serves_grpc_and_queued_requests() {
    block_on(async {
        let adapter = Arc::new(memory::Adapter::new(1));
        let service = Arc::new(GreeterService::default());
        let producer = GreeterProducer::new(
            adapter.clone(),
            ClientConfig::default().with_queue_name("queue"),
        );
        producer
            .say_hello(HelloRequest {
                name: "queued".into(),
            })
            .await
            .unwrap();
        assert!(
            service.calls.lock().unwrap().is_empty(),
            "publish only confirms acceptance"
        );
        assert_eq!(adapter.queue_depth("queue"), 1);

        let server = Server::builder(
            adapter.clone(),
            ServerConfig::default().with_queue_name("queue"),
        )
        .add_service(GreeterConsumer::from_arc(service.clone()));
        assert!(matches!(
            server.serve(service.cancellation.clone()).await,
            Err(Error::Cancelled)
        ));
        assert_eq!(adapter.queue_depth("queue"), 0);

        assert_eq!(
            grpc_call(service.clone(), "grpc").await.unwrap().message,
            "hello grpc"
        );
        assert_eq!(*service.calls.lock().unwrap(), ["queued", "grpc"]);
    });
}

#[test]
fn tonic_status_is_returned_through_grpc() {
    block_on(async {
        let service = Arc::new(GreeterService::default());
        let status = grpc_call(service.clone(), "reject").await.unwrap_err();
        assert_eq!(status.code(), grpcq::tonic::Code::InvalidArgument);
        assert_eq!(status.message(), "name rejected");
        assert_eq!(*service.calls.lock().unwrap(), ["reject"]);
    });
}

#[test]
fn generated_queue_consumer_preserves_decode_and_handler_failures() {
    for malformed in [false, true] {
        block_on(async {
            let adapter = Arc::new(memory::Adapter::new(1));
            let service = Arc::new(GreeterService::default());
            if malformed {
                adapter
                    .publish(
                        "queue",
                        &[Message {
                            topic: "grpcq.test.Greeter".into(),
                            action: "SayHello".into(),
                            payload: vec![0xff],
                            ..Default::default()
                        }],
                    )
                    .await
                    .unwrap();
            } else {
                GreeterProducer::new(
                    adapter.clone(),
                    ClientConfig::default().with_queue_name("queue"),
                )
                .say_hello(HelloRequest {
                    name: "reject".into(),
                })
                .await
                .unwrap();
            }
            let server = Server::builder(
                adapter.clone(),
                ServerConfig::default().with_queue_name("queue"),
            )
            .add_service(GreeterConsumer::from_arc(service.clone()));
            let error = server.serve(CancellationToken::new()).await.unwrap_err();
            match error {
                Error::Delivery {
                    operation: "handle",
                    source,
                    ..
                } => {
                    if malformed {
                        assert!(matches!(*source, Error::RequestDecode { .. }));
                    } else {
                        assert!(source.to_string().contains("name rejected"));
                    }
                }
                other => panic!("unexpected delivery error: {other}"),
            }
            assert_eq!(service.calls.lock().unwrap().len(), usize::from(!malformed));
            assert_eq!(adapter.queue_depth("queue"), 1);
        });
    }
}

#[test]
fn generated_producer_returns_queue_rejection() {
    block_on(async {
        let adapter = Arc::new(memory::Adapter::new(1));
        let producer = GreeterProducer::new(
            adapter.clone(),
            ClientConfig::default().with_queue_name("queue"),
        );
        producer
            .say_hello(HelloRequest {
                name: "first".into(),
            })
            .await
            .unwrap();
        assert!(matches!(
            producer
                .say_hello(HelloRequest {
                    name: "second".into()
                })
                .await,
            Err(Error::QueueFull { .. })
        ));
        assert_eq!(adapter.queue_depth("queue"), 1);
    });
}

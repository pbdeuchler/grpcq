pub mod grpcq_generated {
    include!(concat!(env!("OUT_DIR"), "/grpcq/grpcq.test.rs"));
}

pub mod tonic_generated {
    include!(concat!(env!("OUT_DIR"), "/tonic/grpcq.test.rs"));
}

fn main() {
    grpcq_build::compile_protos(&["proto/greeter.proto"], &["proto"])
        .expect("consumer fixture proto compilation should succeed");
}

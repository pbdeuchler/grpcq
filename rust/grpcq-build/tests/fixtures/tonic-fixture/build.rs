use std::{fs, path::PathBuf};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let out_dir = PathBuf::from(std::env::var("OUT_DIR")?);
    let grpcq_dir = out_dir.join("grpcq");
    let tonic_dir = out_dir.join("tonic");
    fs::create_dir_all(&grpcq_dir)?;
    fs::create_dir_all(&tonic_dir)?;

    let mut grpcq_config = grpcq_build::Config::new();
    grpcq_config.out_dir(&grpcq_dir);
    grpcq_config.compile_protos(&["proto/greeter.proto"], &["proto"])?;

    tonic_build::configure()
        .build_client(false)
        .build_server(true)
        .out_dir(&tonic_dir)
        .extern_path(
            ".grpcq.test.HelloRequest",
            "crate::grpcq_generated::HelloRequest",
        )
        .extern_path(
            ".grpcq.test.HelloReply",
            "crate::grpcq_generated::HelloReply",
        )
        .compile_protos(&["proto/greeter.proto"], &["proto"])?;

    Ok(())
}

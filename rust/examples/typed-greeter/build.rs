fn main() -> std::io::Result<()> {
    grpcq_build::compile_protos(&["proto/greeter.proto"], &["proto"])?;
    Ok(())
}

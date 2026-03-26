use std::{
    fs,
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

#[test]
fn compile_protos_generates_consumer_and_producer_code() {
    let fixture = fixture_path("greeter.proto");
    let out_dir = unique_temp_dir();

    let previous_out_dir = std::env::var_os("OUT_DIR");
    std::env::set_var("OUT_DIR", &out_dir);
    grpcq_build::compile_protos(
        &[fixture.as_path()],
        &[fixture.parent().expect("fixture parent")],
    )
    .expect("proto compilation should succeed");
    restore_out_dir(previous_out_dir);

    let generated = out_dir.join("grpcq.test.rs");
    let contents = fs::read_to_string(&generated).expect("generated file should exist");

    assert!(
        contents.contains("pub struct HelloRequest"),
        "expected prost message output in {generated:?}"
    );
    assert!(
        contents.contains("pub mod greeter_consumer"),
        "expected a generated consumer module in {generated:?}"
    );
    assert!(
        contents.contains("async fn say_hello"),
        "expected snake_case consumer trait methods in {generated:?}"
    );
    assert!(
        contents.contains("pub struct GreeterConsumer"),
        "expected a generated consumer wrapper in {generated:?}"
    );
    assert!(
        contents.contains("grpcq::ServiceRegistrar"),
        "expected generated registrar impls in {generated:?}"
    );
    assert!(
        contents.contains("pub mod greeter_producer"),
        "expected a generated producer module in {generated:?}"
    );
    assert!(
        contents.contains("pub struct GreeterProducer"),
        "expected a generated producer wrapper in {generated:?}"
    );
    assert!(
        contents.contains("say_hello_with_options"),
        "expected generated _with_options methods in {generated:?}"
    );
}

fn fixture_path(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests")
        .join("fixtures")
        .join(name)
}

fn unique_temp_dir() -> PathBuf {
    let suffix = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("system clock should be after unix epoch")
        .as_nanos();
    let path = std::env::temp_dir().join(format!("grpcq-build-test-{suffix}"));
    fs::create_dir_all(&path).expect("temp directory should be created");
    path
}

fn restore_out_dir(previous: Option<std::ffi::OsString>) {
    match previous {
        Some(value) => std::env::set_var("OUT_DIR", value),
        None => std::env::remove_var("OUT_DIR"),
    }
}

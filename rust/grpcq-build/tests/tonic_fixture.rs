use std::{
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

#[test]
fn generated_tonic_fixture_compiles_and_runs() {
    let manifest_path = fixture_manifest("tonic-fixture");
    let target_dir = unique_temp_dir();

    let output = Command::new("cargo")
        .arg("test")
        .arg("--manifest-path")
        .arg(&manifest_path)
        .env("CARGO_TARGET_DIR", &target_dir)
        .output()
        .expect("tonic fixture cargo test should launch");

    if !output.status.success() {
        panic!(
            "tonic fixture cargo test failed\nstdout:\n{}\nstderr:\n{}",
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
    }
}

fn fixture_manifest(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests")
        .join("fixtures")
        .join(name)
        .join("Cargo.toml")
}

fn unique_temp_dir() -> PathBuf {
    let suffix = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("system clock should be after unix epoch")
        .as_nanos();
    let path = std::env::temp_dir().join(format!("grpcq-build-tonic-target-{suffix}"));
    fs::create_dir_all(&path).expect("temp directory should be created");
    path
}

# Protocol Buffer Generation

This project uses Protocol Buffers for message serialization.

**Important:** The core message proto (`go/proto/message.pb.go`) is checked into version control. You only need to regenerate proto files if:
- You modify `proto/message.proto`
- You're working on the example service (`go/examples/userservice/user.proto`)

## Prerequisites

### Install protoc

**Linux (x86_64):**
```bash
make install-protoc
```

Or manually:
```bash
cd /tmp
curl -LO https://github.com/protocolbuffers/protobuf/releases/download/v25.1/protoc-25.1-linux-x86_64.zip
unzip protoc-25.1-linux-x86_64.zip
sudo cp bin/protoc /usr/local/bin/
sudo chmod +x /usr/local/bin/protoc
```

**macOS:**
```bash
brew install protobuf
```

**Windows:**
Download from: https://github.com/protocolbuffers/protobuf/releases

### Install protoc-gen-go

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
```

Or using make:
```bash
make deps
```

## Generate Proto Files

### Generate All

```bash
make proto
make proto-example
```

### Generate Manually

**Core message proto:**
```bash
protoc --go_out=. --go_opt=paths=source_relative proto/message.proto
```

**Example user service proto:**
```bash
protoc --go_out=. --go_opt=paths=source_relative go/examples/userservice/user.proto
```

## Verify Generation

After generating, verify the files exist:
```bash
ls -l proto/grpcq/message.pb.go
ls -l go/examples/userservice/user.pb.go
```

## Build and Test

After generating proto files:

```bash
# Build all packages
make build

# Run tests
make test

# Run example
make run-example
```

## Troubleshooting

### "protoc: command not found"
- Install protoc following the instructions above
- Ensure `/usr/local/bin` is in your PATH

### "protoc-gen-go: program not found or is not executable"
- Run `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`
- Ensure `$GOPATH/bin` is in your PATH (usually `~/go/bin`)

### Import errors
- Make sure you've run `go mod download`
- Check that the `go_package` option in proto files matches your module path

### Generated files are gitignored
- This is intentional. Generated files should not be committed
- Each developer generates them locally
- CI/CD should generate them during the build process

## CI/CD Integration

In your CI/CD pipeline, ensure proto generation happens before build:

```yaml
# GitHub Actions example
- name: Install protoc
  run: make install-protoc

- name: Install protoc-gen-go
  run: make deps

- name: Generate proto files
  run: make proto && make proto-example

- name: Build
  run: make build

- name: Test
  run: make test
```

# Proto File Regeneration Required

**IMPORTANT:** The proto files checked into this repository (`go/proto/message.pb.go` and `go/examples/userservice/proto/user.pb.go`) were manually created and need to be regenerated with the actual `protoc` compiler for full functionality.

## Why?

The proto files were created during initial development without access to the `protoc` compiler. While the basic structure is correct, the binary descriptor data may not be fully accurate.

## How to Regenerate

### Prerequisites

Install protoc and protoc-gen-go:

```bash
# Install protoc-gen-go
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest

# Install protoc (choose your platform)

# Linux
cd /tmp
curl -LO https://github.com/protocolbuffers/protobuf/releases/download/v25.1/protoc-25.1-linux-x86_64.zip
unzip protoc-25.1-linux-x86_64.zip
sudo cp bin/protoc /usr/local/bin/
sudo chmod +x /usr/local/bin/protoc

# macOS
brew install protobuf

# Windows
# Download from: https://github.com/protocolbuffers/protobuf/releases
```

### Regenerate Proto Files

```bash
# Regenerate core message proto
make proto

# Regenerate example proto
make proto-example
```

### Verify

After regeneration, verify the build and tests work:

```bash
# Build all packages
go build ./go/core
go build ./go/adapters/...
go build ./go/examples/userservice

# Run tests
go test ./go/core/...
go test ./go/adapters/...
```

### Commit

After regenerating and verifying, commit the updated proto files:

```bash
git add go/proto/message.pb.go go/examples/userservice/proto/user.pb.go
git commit -m "Regenerate proto files with protoc"
```

## What Works Without Regeneration

The following should work even with the manually created proto files:
- Building all packages (as long as descriptor data isn't accessed)
- Running the example (basic functionality)
- Using the library in simple scenarios

## What Requires Regeneration

The following will fail without proper proto regeneration:
- Unit tests that use proto reflection
- Advanced protobuf features
- Proto descriptor introspection
- Full protobuf wire format compliance

## First-Time Contributors

If you're setting up this project for the first time, please regenerate the proto files immediately after cloning:

```bash
git clone https://github.com/pbdeuchler/grpcq.git
cd grpcq
make deps
make install-protoc  # or install protoc manually
make proto
make proto-example
go test ./...  # Verify everything works
```

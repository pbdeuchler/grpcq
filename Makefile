.PHONY: proto test build clean example

# Generate protobuf code
# Note: go/proto/message.pb.go is checked into version control
# Only regenerate if you modify proto/message.proto
proto:
	@echo "Generating protobuf code to go/proto/..."
	protoc --go_out=go/proto --go_opt=paths=source_relative --proto_path=proto/ proto/*.proto

# Generate example proto
# Note: go/examples/userservice/proto/user.pb.go is also checked in
proto-example:
	@echo "Generating example protobuf code to go/examples/userservice/proto/..."
	protoc --go_out=plugins=grpc:go/examples/userservice/proto --proto_path=go/examples/userservice go/examples/userservice/user.proto

# Run all tests
test:
	go test --race -v ./go/...

# Build the example
example:
	go build -o bin/userservice-example ./go/examples/userservice

# Run the example
run-example: example
	./bin/userservice-example

# Clean generated files (keeps checked-in proto packages)
clean:
	rm -rf bin/
	find . -name "*.pb.go" ! -path "./go/proto/*" ! -path "./go/examples/*/proto/*" -delete

# Format code
fmt:
	go fmt ./...

# Run linter
lint:
	golangci-lint run ./...

# Help
help:
	@echo "Available targets:"
	@echo "  proto          - Generate protobuf code from proto files"
	@echo "  proto-example  - Generate example protobuf code"
	@echo "  test           - Run all tests"
	@echo "  example        - Build the example service"
	@echo "  run-example    - Build and run the example service"
	@echo "  clean          - Remove generated files and binaries"
	@echo "  fmt            - Format Go code"
	@echo "  lint           - Run linter"
	@echo "  help           - Show this help message"

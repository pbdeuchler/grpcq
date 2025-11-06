.PHONY: proto test build clean example

# Generate protobuf code
proto:
	@echo "Generating protobuf code..."
	protoc --go_out=. --go_opt=paths=source_relative \
		proto/message.proto

# Generate example proto
proto-example:
	@echo "Generating example protobuf code..."
	protoc --go_out=. --go_opt=paths=source_relative \
		go/examples/userservice/user.proto

# Run all tests
test:
	go test -v ./go/core/...
	go test -v ./go/adapters/...

# Build the example
example:
	go build -o bin/userservice-example ./go/examples/userservice

# Run the example
run-example: example
	./bin/userservice-example

# Clean generated files
clean:
	rm -rf bin/
	find . -name "*.pb.go" -delete

# Install dependencies
deps:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go mod download

# Format code
fmt:
	go fmt ./...

# Run linter
lint:
	golangci-lint run ./...

# Build all
build:
	go build ./go/core
	go build ./go/adapters/memory
	go build ./go/adapters/sqs

# Download and install protoc (Linux x86_64)
install-protoc:
	@echo "Installing protoc..."
	@cd /tmp && \
		curl -LO https://github.com/protocolbuffers/protobuf/releases/download/v25.1/protoc-25.1-linux-x86_64.zip && \
		unzip -q protoc-25.1-linux-x86_64.zip && \
		sudo cp bin/protoc /usr/local/bin/ && \
		sudo chmod +x /usr/local/bin/protoc && \
		rm -rf protoc-25.1-linux-x86_64.zip bin include
	@echo "protoc installed successfully"

# Run mod tidy
tidy:
	go mod tidy

# Help
help:
	@echo "Available targets:"
	@echo "  proto          - Generate protobuf code from proto files"
	@echo "  proto-example  - Generate example protobuf code"
	@echo "  test           - Run all tests"
	@echo "  example        - Build the example service"
	@echo "  run-example    - Build and run the example service"
	@echo "  clean          - Remove generated files and binaries"
	@echo "  deps           - Install Go dependencies"
	@echo "  fmt            - Format Go code"
	@echo "  lint           - Run linter"
	@echo "  build          - Build all packages"
	@echo "  install-protoc - Download and install protoc compiler"
	@echo "  tidy           - Run go mod tidy"
	@echo "  help           - Show this help message"

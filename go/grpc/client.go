package grpc

import (
	"context"
	"fmt"

	"github.com/pbdeuchler/grpcq/go/core"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// AsyncClientConn implements grpc.ClientConnInterface but routes calls through
// a grpcq publisher instead of making synchronous RPC calls.
//
// This allows generated gRPC client stubs to work with queue-based async messaging.
// Note: Since queue-based messaging is fire-and-forget, responses are not returned.
// The response will be a zero value of the response type.
type AsyncClientConn struct {
	publisher *core.Publisher
	queueName string
}

// NewAsyncClientConn creates a new AsyncClientConn that routes gRPC calls
// through the specified queue using the given publisher.
func NewAsyncClientConn(publisher *core.Publisher, queueName string) *AsyncClientConn {
	return &AsyncClientConn{
		publisher: publisher,
		queueName: queueName,
	}
}

// Invoke implements grpc.ClientConnInterface.Invoke for unary RPCs.
// It publishes the request to the queue and returns immediately.
// The 'reply' parameter will not be populated since this is async.
func (c *AsyncClientConn) Invoke(ctx context.Context, method string, args interface{}, reply interface{}, opts ...grpc.CallOption) error {
	// Extract service and method name from full method path
	// Format: "/package.Service/Method"
	serviceName, methodName, err := parseMethodName(method)
	if err != nil {
		return err
	}

	// Marshal the request
	req, ok := args.(proto.Message)
	if !ok {
		return fmt.Errorf("args must be a proto.Message")
	}

	// Extract metadata from call options if any
	metadata := extractMetadata(opts)

	// Publish the message
	if err := c.publisher.Send(ctx, c.queueName, serviceName, methodName, req, metadata); err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}

	// Note: reply is not populated since this is async fire-and-forget
	return nil
}

// NewStream implements grpc.ClientConnInterface.NewStream for streaming RPCs.
// Streaming is not supported in async queue-based mode.
func (c *AsyncClientConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, fmt.Errorf("streaming RPCs are not supported in async queue mode")
}

// parseMethodName extracts the service and method name from a full method path.
// Input format: "/package.Service/Method"
// Returns: "package.Service", "Method", error
func parseMethodName(fullMethod string) (string, string, error) {
	if len(fullMethod) == 0 || fullMethod[0] != '/' {
		return "", "", fmt.Errorf("invalid method name: %s", fullMethod)
	}

	// Remove leading slash
	fullMethod = fullMethod[1:]

	// Find the last slash
	lastSlash := -1
	for i := len(fullMethod) - 1; i >= 0; i-- {
		if fullMethod[i] == '/' {
			lastSlash = i
			break
		}
	}

	if lastSlash == -1 {
		return "", "", fmt.Errorf("invalid method name format: %s", fullMethod)
	}

	serviceName := fullMethod[:lastSlash]
	methodName := fullMethod[lastSlash+1:]

	return serviceName, methodName, nil
}

// extractMetadata extracts metadata from grpc.CallOptions.
// This is a simplified version - in a full implementation, this would handle
// all the standard gRPC metadata and headers.
func extractMetadata(opts []grpc.CallOption) map[string]string {
	// TODO: Extract metadata from call options
	// For now, return empty map
	return make(map[string]string)
}

// ClientAdapter wraps a grpcq publisher to provide a gRPC-like client interface.
type ClientAdapter struct {
	publisher *core.Publisher
	queueName string
}

// NewClientAdapter creates a new ClientAdapter.
func NewClientAdapter(publisher *core.Publisher, queueName string) *ClientAdapter {
	return &ClientAdapter{
		publisher: publisher,
		queueName: queueName,
	}
}

// Conn returns a grpc.ClientConnInterface that can be used with generated
// gRPC client constructors.
//
// Example:
//   adapter := grpc.NewClientAdapter(publisher, "user-queue")
//   client := userpb.NewUserServiceClient(adapter.Conn())
//   // Now client methods will publish to the queue instead of making RPC calls
func (c *ClientAdapter) Conn() grpc.ClientConnInterface {
	return NewAsyncClientConn(c.publisher, c.queueName)
}

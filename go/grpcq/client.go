package grpcq

import (
	"context"
	"fmt"

	"github.com/pbdeuchler/grpcq/go/core"
	"google.golang.org/protobuf/proto"
)

// Client represents a grpcq client that publishes messages to a queue.
type Client struct {
	publisher *core.Publisher
	config    *ClientConfig
}

// ClientConfig contains configuration for a grpcq client.
type ClientConfig struct {
	// QueueName is the name of the queue to publish to
	QueueName string

	// Originator identifies the sender (typically service name or instance ID)
	Originator string
}

// CallOption configures a client call.
type CallOption interface {
	apply(*callOptions)
}

type callOptions struct {
	queueName string
	metadata  map[string]string
}

type funcCallOption struct {
	f func(*callOptions)
}

func (fco *funcCallOption) apply(co *callOptions) {
	fco.f(co)
}

func newFuncCallOption(f func(*callOptions)) *funcCallOption {
	return &funcCallOption{f: f}
}

// WithQueueName returns a CallOption that sets the queue name for the call.
func WithQueueNameOption(name string) CallOption {
	return newFuncCallOption(func(o *callOptions) {
		o.queueName = name
	})
}

// WithMetadata returns a CallOption that sets metadata for the call.
func WithMetadata(metadata map[string]string) CallOption {
	return newFuncCallOption(func(o *callOptions) {
		if o.metadata == nil {
			o.metadata = make(map[string]string)
		}
		for k, v := range metadata {
			o.metadata[k] = v
		}
	})
}

// ClientOption configures a Client.
type ClientOption func(*ClientConfig)

// WithClientQueueName sets the default queue name for the client.
func WithClientQueueName(name string) ClientOption {
	return func(c *ClientConfig) {
		c.QueueName = name
	}
}

// WithOriginator sets the originator identifier for the client.
func WithOriginator(originator string) ClientOption {
	return func(c *ClientConfig) {
		c.Originator = originator
	}
}

// NewClient creates a new grpcq client.
func NewClient(adapter QueueAdapter, opts ...ClientOption) *Client {
	config := &ClientConfig{
		QueueName:  "default-queue",
		Originator: "grpcq-client",
	}

	for _, opt := range opts {
		opt(config)
	}

	return &Client{
		publisher: core.NewPublisher(adapter, config.Originator),
		config:    config,
	}
}

// Invoke performs a unary RPC call by publishing a message to the queue.
// This is called by generated client code.
//
// serviceName: Full service name (e.g., "userservice.UserService")
// methodName: Method name (e.g., "CreateUser")
// req: Request proto message
// resp: Response proto message (will be zero value for async fire-and-forget)
// opts: Call options
func (c *Client) Invoke(
	ctx context.Context,
	serviceName string,
	methodName string,
	req interface{},
	resp interface{},
	opts ...CallOption,
) error {
	// Apply call options
	callOpts := &callOptions{
		queueName: c.config.QueueName,
		metadata:  make(map[string]string),
	}
	for _, opt := range opts {
		opt.apply(callOpts)
	}

	// Marshal request
	reqProto, ok := req.(proto.Message)
	if !ok {
		return fmt.Errorf("request must be a proto.Message")
	}

	// Publish message
	err := c.publisher.Send(
		ctx,
		callOpts.queueName,
		serviceName,
		methodName,
		reqProto,
		callOpts.metadata,
	)
	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}

	// Note: resp is not populated since this is async fire-and-forget
	// In the future, we can implement request-response patterns
	return nil
}

// Close closes the client and releases resources.
func (c *Client) Close() error {
	// Nothing to close for now
	return nil
}

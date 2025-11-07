// Package grpc provides interoperability between standard gRPC services and grpcq.
// It enables using the same service implementation for both synchronous gRPC
// and asynchronous queue-based communication.
package grpc

import (
	"context"
	"fmt"
	"reflect"

	pb "github.com/pbdeuchler/grpcq/go/proto"
	"google.golang.org/protobuf/proto"
)

// MethodHandler wraps a gRPC method to work with grpcq's message-based system.
// It handles deserialization of the request, invocation of the gRPC method,
// and optional handling of the response.
type MethodHandler struct {
	// ServiceName is the fully qualified gRPC service name (e.g., "userservice.UserService")
	ServiceName string

	// MethodName is the gRPC method name (e.g., "CreateUser")
	MethodName string

	// Handler is the function that processes the message
	Handler func(ctx context.Context, msg *pb.Message) error
}

// UnaryServerAdapter wraps a unary gRPC method to work with grpcq.
// The methodFunc should have signature: func(context.Context, RequestType) (ResponseType, error)
// where RequestType and ResponseType are proto.Message types.
//
// Example:
//
//	adapter := grpc.UnaryServerAdapter(
//	    "userservice.UserService",
//	    "CreateUser",
//	    func(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
//	        // Your implementation
//	    },
//	    func() proto.Message { return &CreateUserRequest{} },
//	)
func UnaryServerAdapter(
	serviceName string,
	methodName string,
	methodFunc interface{},
	newRequest func() proto.Message,
) MethodHandler {
	return MethodHandler{
		ServiceName: serviceName,
		MethodName:  methodName,
		Handler: func(ctx context.Context, msg *pb.Message) error {
			// Create new request instance
			req := newRequest()

			// Unmarshal the payload
			if err := proto.Unmarshal(msg.Payload, req); err != nil {
				return fmt.Errorf("failed to unmarshal request: %w", err)
			}

			resp, err := invokeUnaryMethod(methodFunc, ctx, req)
			if err != nil {
				return fmt.Errorf("method %s failed: %w", methodName, err)
			}

			// For async processing, we don't return the response directly
			// In a full implementation, this could publish a response message
			_ = resp
			return nil
		},
	}
}

// ServerAdapter provides a convenient way to register all methods of a gRPC
// service implementation with a grpcq registry.
type ServerAdapter struct {
	serviceName string
	handlers    []MethodHandler
}

// NewServerAdapter creates a new ServerAdapter for the specified service.
func NewServerAdapter(serviceName string) *ServerAdapter {
	return &ServerAdapter{
		serviceName: serviceName,
		handlers:    make([]MethodHandler, 0),
	}
}

// RegisterMethod registers a gRPC method with the adapter.
// The handler should unmarshal the request, call the implementation, and handle the response.
func (s *ServerAdapter) RegisterMethod(methodName string, handler func(ctx context.Context, msg *pb.Message) error) {
	s.handlers = append(s.handlers, MethodHandler{
		ServiceName: s.serviceName,
		MethodName:  methodName,
		Handler:     handler,
	})
}

// RegisterUnary is a helper that registers a unary method with automatic marshaling.
// TReq and TResp should be proto.Message types.
//
// The methodFunc should have signature: func(ctx context.Context, req TReq) (TResp, error)
//
// Example:
//
//	adapter.RegisterUnary("CreateUser",
//	    func(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
//	        return svc.CreateUser(ctx, req)
//	    },
//	    func() proto.Message { return &CreateUserRequest{} },
//	)
func (s *ServerAdapter) RegisterUnary(
	methodName string,
	methodFunc func(context.Context, proto.Message) (proto.Message, error),
	newRequest func() proto.Message,
) {
	handler := func(ctx context.Context, msg *pb.Message) error {
		req := newRequest()
		if err := proto.Unmarshal(msg.Payload, req); err != nil {
			return fmt.Errorf("failed to unmarshal request: %w", err)
		}

		resp, err := methodFunc(ctx, req)
		if err != nil {
			return fmt.Errorf("method %s failed: %w", methodName, err)
		}

		// In async mode, we typically don't send responses back
		// But we could optionally publish to a response queue here
		_ = resp
		return nil
	}

	s.RegisterMethod(methodName, handler)
}

// GetHandlers returns all registered method handlers.
func (s *ServerAdapter) GetHandlers() []MethodHandler {
	return s.handlers
}

// WrapUnaryMethod wraps a gRPC unary method to work with grpcq messages.
// This is a helper function for creating handlers without using ServerAdapter.
// It returns a function compatible with core.Handler.
//
// Example:
//
//	handler := grpc.WrapUnaryMethod(
//	    func(ctx context.Context, req *CreateUserRequest) (*CreateUserResponse, error) {
//	        return &CreateUserResponse{UserId: "123", Name: req.Name, Email: req.Email}, nil
//	    },
//	    func() proto.Message { return &CreateUserRequest{} },
//	)
func WrapUnaryMethod[TReq proto.Message, TResp proto.Message](
	methodFunc func(context.Context, TReq) (TResp, error),
	newRequest func() TReq,
) func(context.Context, *pb.Message) error {
	return func(ctx context.Context, msg *pb.Message) error {
		req := newRequest()
		if err := proto.Unmarshal(msg.Payload, req); err != nil {
			return fmt.Errorf("failed to unmarshal request: %w", err)
		}

		resp, err := methodFunc(ctx, req)
		if err != nil {
			return err
		}

		// For async, we don't return the response
		// In a complete implementation, this could publish to a response topic
		_ = resp
		return nil
	}
}

var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()

func invokeUnaryMethod(methodFunc interface{}, ctx context.Context, req proto.Message) (proto.Message, error) {
	switch fn := methodFunc.(type) {
	case func(context.Context, proto.Message) (proto.Message, error):
		return fn(ctx, req)
	case func(context.Context, proto.Message) error:
		return nil, fn(ctx, req)
	}

	fnValue := reflect.ValueOf(methodFunc)
	if fnValue.Kind() != reflect.Func {
		return nil, fmt.Errorf("methodFunc must be a function")
	}

	fnType := fnValue.Type()
	if fnType.NumIn() != 2 || !fnType.In(0).AssignableTo(contextType) {
		return nil, fmt.Errorf("unsupported method signature")
	}

	reqValue := reflect.ValueOf(req)
	if !reqValue.Type().AssignableTo(fnType.In(1)) {
		return nil, fmt.Errorf("unsupported request type: got %s, want %s", reqValue.Type(), fnType.In(1))
	}

	if fnType.NumOut() != 2 {
		return nil, fmt.Errorf("unsupported method return signature")
	}

	results := fnValue.Call([]reflect.Value{reflect.ValueOf(ctx), reqValue})
	respVal := results[0]
	errVal := results[1]

	if !errVal.IsNil() {
		err, ok := errVal.Interface().(error)
		if !ok {
			return nil, fmt.Errorf("method returned non-error type")
		}
		return nil, err
	}

	if respVal.IsNil() {
		return nil, nil
	}

	resp, ok := respVal.Interface().(proto.Message)
	if !ok {
		return nil, fmt.Errorf("response does not implement proto.Message")
	}
	return resp, nil
}

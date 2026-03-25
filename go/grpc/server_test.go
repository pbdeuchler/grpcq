package grpc

import (
	"context"
	"testing"

	pb "github.com/pbdeuchler/grpcq/go/proto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestUnaryServerAdapterSupportsTypedFunc(t *testing.T) {
	handler := UnaryServerAdapter(
		"service",
		"Method",
		func(ctx context.Context, req *emptypb.Empty) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		},
		func() proto.Message { return &emptypb.Empty{} },
	).Handler

	payload, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	msg := &pb.Message{
		Payload: payload,
		Topic:   "service",
		Action:  "Method",
	}

	if err := handler(context.Background(), msg); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
}

func TestUnaryServerAdapterInvalidSignature(t *testing.T) {
	handler := UnaryServerAdapter(
		"service",
		"Method",
		func(req *emptypb.Empty) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		},
		func() proto.Message { return &emptypb.Empty{} },
	).Handler

	payload, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	msg := &pb.Message{
		Payload: payload,
		Topic:   "service",
		Action:  "Method",
	}

	if err := handler(context.Background(), msg); err == nil {
		t.Fatal("expected error for invalid method signature")
	}
}

func TestUnaryServerAdapterInvalidErrorReturnDoesNotPanic(t *testing.T) {
	handler := UnaryServerAdapter(
		"service",
		"Method",
		func(ctx context.Context, req *emptypb.Empty) (*emptypb.Empty, string) {
			return &emptypb.Empty{}, ""
		},
		func() proto.Message { return &emptypb.Empty{} },
	).Handler

	payload, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	msg := &pb.Message{Payload: payload}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handler panicked: %v", r)
		}
	}()

	if err := handler(context.Background(), msg); err == nil {
		t.Fatal("expected validation error for invalid error return type")
	}
}

func TestUnaryServerAdapterSupportsErrorOnlyMethod(t *testing.T) {
	handler := UnaryServerAdapter(
		"service",
		"Method",
		func(ctx context.Context, req *emptypb.Empty) error {
			return nil
		},
		func() proto.Message { return &emptypb.Empty{} },
	).Handler

	payload, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	msg := &pb.Message{Payload: payload}
	if err := handler(context.Background(), msg); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
}

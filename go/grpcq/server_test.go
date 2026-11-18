package grpcq_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pbdeuchler/grpcq/go/adapters/memory"
	"github.com/pbdeuchler/grpcq/go/grpcq"
	pb "github.com/pbdeuchler/grpcq/go/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestServerStopDrainsHandlerAndAcknowledgement(t *testing.T) {
	adapter := memory.NewAdapter(1)
	server := grpcq.NewServer(adapter, grpcq.WithQueueName("q"), grpcq.WithConcurrency(1))
	started := make(chan struct{})
	release := make(chan struct{})
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "retained")
	server.RegisterMethod("svc", "call", func(handlerCtx context.Context, _ interface{}) (interface{}, error) {
		close(started)
		<-release
		if err := handlerCtx.Err(); err != nil {
			t.Errorf("handler cancelled during drain: %v", err)
		}
		if handlerCtx.Value(contextKey{}) != "retained" {
			t.Error("handler lost context values")
		}
		return &emptypb.Empty{}, nil
	}, func() interface{} { return &emptypb.Empty{} })
	message := &pb.Message{Topic: "svc", Action: "call"}
	if err := adapter.Publish(ctx, "q", message); err != nil {
		t.Fatal(err)
	}
	running := make(chan error, 1)
	go func() { running <- server.Start(ctx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Stop() }()
	// The handler must finish before Stop can complete.
	select {
	case err := <-stopped:
		t.Fatalf("stop returned before handler completion: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for _, done := range []<-chan error{running, stopped} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("server shutdown timed out")
		}
	}
	if err := adapter.Publish(ctx, "q", message); err != nil {
		t.Fatalf("stop returned without acknowledging received work: %v", err)
	}
	if err := server.Start(ctx); err == nil {
		t.Fatal("server must reject repeated starts")
	}
}

func TestServerStopBeforeStart(t *testing.T) {
	server := grpcq.NewServer(memory.NewAdapter(1))
	if err := server.Stop(); err == nil {
		t.Fatal("stopping an unstarted server must fail")
	}
}

func TestServerReturnsRequestDecodeFailure(t *testing.T) {
	adapter := memory.NewAdapter(1)
	server := grpcq.NewServer(adapter, grpcq.WithQueueName("q"))
	calls := 0
	server.RegisterMethod("svc", "call", func(context.Context, interface{}) (interface{}, error) { calls++; return &emptypb.Empty{}, nil }, func() interface{} { return &emptypb.Empty{} })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := adapter.Publish(ctx, "q", &pb.Message{MessageId: "invalid-request", Topic: "svc", Action: "call", Payload: []byte{0xff}}); err != nil {
		t.Fatal(err)
	}
	err := server.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "failed to unmarshal request") {
		t.Fatalf("decode failure was hidden: %v", err)
	}
	if calls != 0 {
		t.Fatal("malformed request reached the handler")
	}
	if adapter.QueueDepth("q") != 1 {
		t.Fatal("malformed request was lost")
	}
}

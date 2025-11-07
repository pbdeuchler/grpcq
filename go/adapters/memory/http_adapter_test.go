package memory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	pb "github.com/pbdeuchler/grpcq/go/proto"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRemoteAdapterPublish(t *testing.T) {
	adapter := NewAdapter(100)
	handler := NewPublishHandler(adapter)

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			return rec.Result(), nil
		}),
	}

	remote := NewRemoteAdapter("http://queue", WithHTTPClient(client))

	msg := &pb.Message{MessageId: "id-1"}
	if err := remote.Publish(context.Background(), "test-queue", msg); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	result, err := adapter.Consume(context.Background(), "test-queue", 10)
	if err != nil {
		t.Fatalf("Consume failed: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Items))
	}

	if result.Items[0].Message.MessageId != "id-1" {
		t.Fatalf("unexpected message id: %s", result.Items[0].Message.MessageId)
	}
}

func TestRemoteAdapterConsumeNotSupported(t *testing.T) {
	remote := NewRemoteAdapter("http://example.com")

	if _, err := remote.Consume(context.Background(), "queue", 1); err == nil {
		t.Fatal("expected error for unsupported consume")
	}
}

package memory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pbdeuchler/grpcq/go/core"
	pb "github.com/pbdeuchler/grpcq/go/proto"
	"google.golang.org/protobuf/proto"
)

// publishRequest is the payload used by the HTTP publish endpoint.
type publishRequest struct {
	Messages []string `json:"messages"`
}

// NewPublishHandler returns an http.Handler that accepts publish requests and forwards
// them to the provided adapter. The handler expects POST requests to paths of the form
// /queues/{queue}/messages with a JSON body containing base64 encoded protobuf payloads.
func NewPublishHandler(adapter *Adapter) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/queues/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		queueName, ok := strings.CutPrefix(r.URL.Path, "/queues/")
		if !ok || !strings.HasSuffix(queueName, "/messages") {
			http.NotFound(w, r)
			return
		}
		queueName = strings.TrimSuffix(queueName, "/messages")

		queueName, err := url.PathUnescape(queueName)
		if err != nil {
			http.Error(w, "invalid queue name", http.StatusBadRequest)
			return
		}

		var req publishRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if len(req.Messages) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		messages := make([]*pb.Message, 0, len(req.Messages))
		for i, encoded := range req.Messages {
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid message %d payload", i), http.StatusBadRequest)
				return
			}

			msg := &pb.Message{}
			if err := proto.Unmarshal(data, msg); err != nil {
				http.Error(w, fmt.Sprintf("invalid message %d", i), http.StatusBadRequest)
				return
			}
			messages = append(messages, msg)
		}

		if err := adapter.Publish(r.Context(), queueName, messages...); err != nil {
			http.Error(w, fmt.Sprintf("failed to publish: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// RemoteAdapter implements core.QueueAdapter by forwarding publish requests to a remote
// publish endpoint exposed via NewPublishHandler.
type RemoteAdapter struct {
	baseURL string
	client  *http.Client
}

// RemoteOption customises the behaviour of RemoteAdapter.
type RemoteOption func(*RemoteAdapter)

// WithHTTPClient sets a custom http.Client for the remote adapter.
func WithHTTPClient(client *http.Client) RemoteOption {
	return func(a *RemoteAdapter) {
		if client != nil {
			a.client = client
		}
	}
}

// NewRemoteAdapter creates a new adapter that forwards publish calls to the given baseURL.
// The baseURL should include the scheme, e.g. http://localhost:8081.
func NewRemoteAdapter(baseURL string, opts ...RemoteOption) *RemoteAdapter {
	adapter := &RemoteAdapter{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	for _, opt := range opts {
		opt(adapter)
	}

	return adapter
}

// Publish sends the messages to the remote HTTP endpoint.
func (a *RemoteAdapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	if len(messages) == 0 {
		return nil
	}

	encoded := make([]string, 0, len(messages))
	for i, msg := range messages {
		if msg == nil {
			return fmt.Errorf("message %d is nil", i)
		}

		data, err := proto.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to marshal message %d: %w", i, err)
		}

		encoded = append(encoded, base64.StdEncoding.EncodeToString(data))
	}

	reqBody, err := json.Marshal(publishRequest{Messages: encoded})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	u := fmt.Sprintf("%s/queues/%s/messages", a.baseURL, url.PathEscape(queueName))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("publish request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("publish request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

// Consume is not supported by the RemoteAdapter because it is intended for client-side publishing only.
func (a *RemoteAdapter) Consume(ctx context.Context, queueName string, maxBatch int) (*core.ConsumeResult, error) {
	return nil, fmt.Errorf("consume is not supported by remote adapter")
}

package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

func TestStreamNormalizesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		var requestBody requestPayload
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(requestBody.Messages) != 1 || !strings.Contains(requestBody.Messages[0].Content, "<untrusted_conversation_content>") {
			t.Fatalf("request did not mark user content as untrusted: %#v", requestBody)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("test-secret"))
	stream, err := client.Stream(context.Background(), model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	textEvent, err := stream.Recv()
	if err != nil || textEvent.Text != "你好" {
		t.Fatalf("text event = %#v, err=%v", textEvent, err)
	}
	usageEvent, err := stream.Recv()
	if err != nil || usageEvent.Usage == nil || usageEvent.Usage.InputTokens != 3 {
		t.Fatalf("usage event = %#v, err=%v", usageEvent, err)
	}
	doneEvent, err := stream.Recv()
	if err != nil || doneEvent.Type != model.EventDone || doneEvent.FinishReason != "stop" {
		t.Fatalf("done event = %#v, err=%v", doneEvent, err)
	}
}

func TestStreamRetriesBeforeContent(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewWithHTTPClient(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture"}, nil, &http.Client{Timeout: 3 * time.Second})
	stream, err := client.Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	stream.Close()
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestAuthErrorIsClassifiedAndNotRetried(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { attempts.Add(1); w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	_, err := client.Stream(context.Background(), model.ChatRequest{})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_AUTH_FAILED" || appErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestStreamAcceptsFinishReasonWithoutDoneSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	stream, err := client.Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if event, err := stream.Recv(); err != nil || event.Text != "ok" {
		t.Fatalf("first Recv() = %#v, %v", event, err)
	}
	if event, err := stream.Recv(); err != nil || event.Type != model.EventDone || event.FinishReason != "stop" {
		t.Fatalf("second Recv() = %#v, %v", event, err)
	}
}

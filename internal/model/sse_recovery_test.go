package model_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
)

func TestCapturedTruncatedResponsesEvent(t *testing.T) {
	dir := os.Getenv("SCIAIDE_STREAM_REPLAY_DIR")
	if dir == "" {
		t.Skip("no captured truncated event supplied")
	}
	payload, err := os.ReadFile(filepath.Join(dir, "provider-event.txt"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s", payload)
	}))
	defer server.Close()
	stream, err := responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_STREAM_INTERRUPTED" || !appErr.Retryable || !strings.Contains(appErr.Details, string(payload)) {
		t.Fatalf("captured half JSON must be discarded and retried: %#v", err)
	}
	t.Log("captured half JSON classified as interrupted stream, not accepted output; no live model/DB")
}

func TestProtocolsFrameAndBoundSSEEvents(t *testing.T) {
	for _, protocol := range []struct {
		name, delta, terminal string
		client                func(modelprofile.Profile) model.ChatModel
	}{
		{"responses", `{"type":"response.output_text.delta","delta":"exact text"}`, `{"type":"response.completed","response":{"status":"completed"}}`, func(p modelprofile.Profile) model.ChatModel { return responses.New(p, nil) }},
		{"chat", `{"choices":[{"delta":{"content":"exact text"}}]}`, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, func(p modelprofile.Profile) model.ChatModel { return openai.New(p, nil) }},
		{"messages", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"exact text"}}`, `{"type":"message_stop"}`, func(p modelprofile.Profile) model.ChatModel { return anthropic.New(p, nil) }},
	} {
		for _, mode := range []string{"multiline", "truncated", "invalid", "oversized-line", "oversized-event"} {
			t.Run(protocol.name+"/"+mode, func(t *testing.T) {
				wire := ""
				switch mode {
				case "multiline":
					wire = ": heartbeat\r\nevent: fixture\r\ndata: {\r\ndata: " + protocol.delta[1:] + "\r\n\r\n"
					if protocol.name == "messages" {
						wire = "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" + wire
						wire += "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
					}
					wire += "data: " + protocol.terminal + "\n\n"
				case "truncated":
					wire = `data: {"delta":"`
				case "invalid":
					wire = `data: {"delta":broken}` + "\n\n"
				case "oversized-line":
					wire = "data: " + strings.Repeat("x", 1<<20) + "\n\n"
				case "oversized-event":
					wire = "data: " + strings.Repeat("x", 600000) + "\ndata: " + strings.Repeat("y", 600000) + "\n\n"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, wire)
				}))
				defer server.Close()
				stream, err := protocol.client(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}).Stream(context.Background(), model.ChatRequest{})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				event, err := stream.Recv()
				if mode == "multiline" {
					if err != nil || event.Text != "exact text" {
						t.Fatalf("multiline event: %+v %v", event, err)
					}
					for {
						event, err = stream.Recv()
						if err != nil {
							t.Fatalf("terminal event: %+v %v", event, err)
						}
						if event.Type == model.EventDone {
							break
						}
					}
					return
				}
				var appErr *apperr.Error
				if !errors.As(err, &appErr) || appErr.Retryable != (mode == "truncated") {
					t.Fatalf("invalid transport classification: %+v %v", event, err)
				}
				wantCode := "MODEL_STREAM_INVALID"
				if mode == "truncated" {
					wantCode = "MODEL_STREAM_INTERRUPTED"
				}
				if appErr.Code != wantCode {
					t.Fatalf("classification=%s want=%s", appErr.Code, wantCode)
				}
			})
		}
	}
}

func TestResponsesMultilineSSEDataIsOneEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": heartbeat\r\nevent: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"exact text\"}\r\n\r\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	stream, err := responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if event, err := stream.Recv(); err != nil || event.Text != "exact text" {
		t.Fatalf("multiline data rejected as standalone JSON fragments: %+v %v", event, err)
	}
	if event, err := stream.Recv(); err != nil || event.Type != model.EventDone {
		t.Fatalf("terminal event: %+v %v", event, err)
	}
}

func TestChatDoneSentinelRetainsWhitespaceCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"exact text\"}}]}\n\ndata:   [DONE] \t\n\n")
	}))
	defer server.Close()
	stream, err := openai.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if event, err := stream.Recv(); err != nil || event.Text != "exact text" {
		t.Fatalf("text: %+v %v", event, err)
	}
	if event, err := stream.Recv(); err != nil || event.Type != model.EventDone {
		t.Fatalf("sentinel whitespace broke existing completion: %+v %v", event, err)
	}
}

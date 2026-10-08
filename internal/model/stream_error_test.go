package model_test

import (
	"bytes"
	"context"
	"encoding/json"
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

// Replays the captured provider event, never calls a real provider or user DB.
func TestCapturedReviewConcurrencyEvent(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_CONCURRENCY_REPLAY_DIR")
	if dir == "" {
		t.Skip("no captured review concurrency event")
	}
	payload, err := os.ReadFile(filepath.Join(dir, "provider-event.json"))
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", compact.Bytes())
	}))
	defer server.Close()
	stream, err := responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || !appErr.Retryable || appErr.Code != "MODEL_RATE_LIMITED" || !strings.Contains(appErr.Details, "gateway_concurrency_limit") || strings.Contains(appErr.Details, "HTTP status:") {
		t.Fatalf("captured event did not enter bounded retry path: %#v", err)
	}
	t.Log("exact captured response.failed event classified as temporary account concurrency limit; no model output or scientific validation involved")
}

func TestCapturedProviderUnavailableEvent(t *testing.T) {
	dir := os.Getenv("SCIAIDE_PROVIDER_REPLAY_DIR")
	if dir == "" {
		t.Skip("no captured provider failure supplied")
	}
	payload, err := os.ReadFile(filepath.Join(dir, "provider-event.json"))
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", compact.Bytes())
	}))
	defer server.Close()
	stream, err := responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || !appErr.Retryable || appErr.Code != "MODEL_UNAVAILABLE" || !strings.Contains(appErr.Details, "upstream_error") || !strings.Contains(appErr.Details, "temporarily unavailable") || strings.Contains(appErr.Details, "HTTP status:") {
		t.Fatalf("observed temporary outage was treated as permanent or lost diagnostics: %#v", err)
	}
}

func TestProviderHTTPRejectionDoesNotRetryTemporaryMessage(t *testing.T) {
	for _, protocol := range []struct {
		name   string
		client func(modelprofile.Profile) model.ChatModel
	}{
		{"responses", func(p modelprofile.Profile) model.ChatModel { return responses.New(p, nil) }},
		{"chat", func(p modelprofile.Profile) model.ChatModel { return openai.New(p, nil) }},
		{"messages", func(p modelprofile.Profile) model.ChatModel { return anthropic.New(p, nil) }},
	} {
		for _, status := range []int{400, 401, 403, 404, 422} {
			t.Run(fmt.Sprintf("%s/%d", protocol.name, status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"type":"upstream_error","code":"upstream_error","message":"The service is temporarily unavailable. Please retry later."}}`)
				}))
				defer server.Close()
				_, err := protocol.client(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}).Stream(context.Background(), model.ChatRequest{})
				var appErr *apperr.Error
				if !errors.As(err, &appErr) || appErr.Retryable || appErr.HTTPStatus != status || !strings.Contains(appErr.Details, "temporarily unavailable") {
					t.Fatalf("HTTP rejection was overridden or lost diagnostics: %#v", err)
				}
			})
		}
	}
}

func TestProtocolsClassifyUpstreamStreamErrors(t *testing.T) {
	for _, protocol := range []struct {
		name   string
		client func(modelprofile.Profile) model.ChatModel
	}{
		{"responses", func(p modelprofile.Profile) model.ChatModel { return responses.New(p, nil) }},
		{"chat_completions", func(p modelprofile.Profile) model.ChatModel { return openai.New(p, nil) }},
		{"messages", func(p modelprofile.Profile) model.ChatModel { return anthropic.New(p, nil) }},
	} {
		for _, test := range []struct {
			code      string
			retryable bool
		}{
			{"stream_read_error", true},
			{"gateway_concurrency_limit", true},
			{"invalid_api_key", false},
			{"context_length_exceeded", false},
			{"invalid_request_error", false},
			{"unknown_error", false},
		} {
			t.Run(protocol.name+"/"+test.code, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"error\":{\"code\":%q,\"message\":%q,\"type\":\"upstream_error\"},\"sequence_number\":0,\"type\":\"error\"}\n\n", test.code, test.code)
				}))
				defer server.Close()
				stream, err := protocol.client(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}).Stream(context.Background(), model.ChatRequest{})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				_, err = stream.Recv()
				wantCode := "MODEL_REQUEST_REJECTED"
				if test.retryable {
					wantCode = "MODEL_UNAVAILABLE"
				}
				wantMessage := test.code
				if test.code == "gateway_concurrency_limit" {
					wantCode, wantMessage = "MODEL_RATE_LIMITED", "模型服务账户并发已达上限，请稍后重试。"
				}
				var appErr *apperr.Error
				if !errors.As(err, &appErr) || appErr.Code != wantCode || appErr.Retryable != test.retryable || appErr.UserMessage != wantMessage {
					t.Fatalf("Recv() error = %#v", err)
				}
				if !strings.Contains(appErr.Details, test.code) || !strings.Contains(appErr.Details, "upstream_error") || strings.Contains(appErr.Details, "HTTP status:") {
					t.Fatalf("lost SSE details or invented HTTP status: %s", appErr.Details)
				}
			})
		}
	}
}

func TestAllProviderEnvelopesRecoverGenericTemporaryOutage(t *testing.T) {
	for _, protocol := range []struct {
		name   string
		client func(modelprofile.Profile) model.ChatModel
	}{
		{"responses", func(p modelprofile.Profile) model.ChatModel { return responses.New(p, nil) }},
		{"chat", func(p modelprofile.Profile) model.ChatModel { return openai.New(p, nil) }},
		{"messages", func(p modelprofile.Profile) model.ChatModel { return anthropic.New(p, nil) }},
	} {
		for _, envelope := range []string{"error", "response.failed", "response.error", "buffered"} {
			if protocol.name != "responses" && strings.HasPrefix(envelope, "response.") || envelope == "buffered" && protocol.name != "chat" {
				continue
			}
			for _, permanent := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/permanent=%v", protocol.name, envelope, permanent), func(t *testing.T) {
					code := "upstream_error"
					if permanent {
						code = "invalid_request_error"
					}
					failure := map[string]any{"type": "upstream_error", "code": code, "message": "The service is temporarily unavailable. Please retry later."}
					value := map[string]any{"type": envelope, "error": failure}
					if strings.HasPrefix(envelope, "response.") {
						delete(value, "error")
						value["response"] = map[string]any{"status": "failed", "error": failure}
					}
					payload, _ := json.Marshal(value)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						if envelope == "buffered" {
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write(payload)
						} else {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
						}
					}))
					defer server.Close()
					stream, err := protocol.client(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}).Stream(context.Background(), model.ChatRequest{DisableStreaming: envelope == "buffered"})
					if err == nil {
						defer stream.Close()
						_, err = stream.Recv()
					}
					var appErr *apperr.Error
					if !errors.As(err, &appErr) || appErr.Retryable == permanent || !strings.Contains(appErr.Details, code) || !strings.Contains(appErr.Details, "temporarily unavailable") {
						t.Fatalf("protocol branch has divergent recovery: %#v", err)
					}
				})
			}
		}
	}
}

func TestResponsesNestedStreamReadErrorAfterPartialText(t *testing.T) {
	for _, eventType := range []string{"response.failed", "response.error"} {
		t.Run(eventType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
				_, _ = fmt.Fprintf(w, "data: {\"type\":%q,\"response\":{\"error\":{\"code\":\"stream_read_error\",\"message\":\"stream_read_error\",\"type\":\"upstream_error\"}}}\n\n", eventType)
			}))
			defer server.Close()
			stream, err := responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if event, err := stream.Recv(); err != nil || event.Type != model.EventTextDelta || event.Text != "partial" {
				t.Fatalf("partial event = %#v, %v", event, err)
			}
			_, err = stream.Recv()
			var appErr *apperr.Error
			if !errors.As(err, &appErr) || !appErr.Retryable || appErr.Code != "MODEL_UNAVAILABLE" || !strings.Contains(appErr.Details, eventType) {
				t.Fatalf("Recv() error = %#v", err)
			}
		})
	}
}

func TestBufferedChatConcurrencyErrorUsesSameClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"code":"gateway_concurrency_limit","message":"fixture account busy"}}`)
	}))
	defer server.Close()
	_, err := openai.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{DisableStreaming: true})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_RATE_LIMITED" || !appErr.Retryable || !strings.Contains(appErr.Details, "gateway_concurrency_limit") {
		t.Fatalf("buffered provider error bypassed temporary-limit classification: %#v", err)
	}
}

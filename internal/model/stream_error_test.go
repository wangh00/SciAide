package model_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
)

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
				var appErr *apperr.Error
				if !errors.As(err, &appErr) || appErr.Code != wantCode || appErr.Retryable != test.retryable || appErr.UserMessage != test.code {
					t.Fatalf("Recv() error = %#v", err)
				}
				if !strings.Contains(appErr.Details, test.code) || !strings.Contains(appErr.Details, "upstream_error") || strings.Contains(appErr.Details, "HTTP status:") {
					t.Fatalf("lost SSE details or invented HTTP status: %s", appErr.Details)
				}
			})
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

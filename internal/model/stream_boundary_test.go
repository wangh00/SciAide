package model_test

import (
	"context"
	"crypto/sha256"
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

func TestProtocolEventBoundaryControlsIncompleteJSONRecovery(t *testing.T) {
	for name, client := range map[string]func(modelprofile.Profile) model.ChatModel{
		"responses": func(p modelprofile.Profile) model.ChatModel { return responses.New(p, nil) },
		"chat":      func(p modelprofile.Profile) model.ChatModel { return openai.New(p, nil) },
		"messages":  func(p modelprofile.Profile) model.ChatModel { return anthropic.New(p, nil) },
	} {
		for _, closed := range []bool{false, true} {
			t.Run(name+map[bool]string{true: "/closed", false: "/transport_EOF"}[closed], func(t *testing.T) {
				wire := `data: {"delta":"PRIVATE_SENTINEL`
				if closed {
					wire += "\n\n"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, wire)
				}))
				defer server.Close()
				stream, err := client(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}).Stream(context.Background(), model.ChatRequest{})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				_, err = stream.Recv()
				var classified *apperr.Error
				want := "MODEL_STREAM_INTERRUPTED"
				if closed {
					want = "MODEL_STREAM_INVALID"
				}
				if !errors.As(err, &classified) || classified.Code != want || classified.Retryable == closed || strings.Contains(classified.Details, "PRIVATE_SENTINEL") {
					t.Fatalf("closed=%t classification or privacy invalid: %#v", closed, err)
				}
			})
		}
	}
}

func TestInvalidNativeResponseNeverLeaksViaCauseOrBufferedDiagnostics(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", buffered), func(t *testing.T) {
			body := `{"type":"response.output_item.done","output_index":0,"item":{"type":"PRIVATE_SENTINEL","content":[{"text":"PRIVATE_SENTINEL"}]}}`
			if buffered {
				body = `{"choices":{"content":"PRIVATE_SENTINEL","arguments":"PRIVATE_SENTINEL"},"message":"PRIVATE_SENTINEL"}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if buffered {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, body)
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
				}
			}))
			defer server.Close()
			profile := modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}
			var client model.ChatModel = responses.New(profile, nil)
			if buffered {
				client = openai.New(profile, nil)
			}
			stream, err := client.Stream(context.Background(), model.ChatRequest{DisableStreaming: buffered})
			if err == nil {
				defer stream.Close()
				_, err = stream.Recv()
			}
			if err == nil {
				t.Fatal("invalid native body accepted")
			}
			public := apperr.Public(err)
			if public.Retryable || strings.Contains(public.Message+public.Details+err.Error(), "PRIVATE_SENTINEL") || !strings.Contains(public.Details, fmt.Sprintf("%x", sha256.Sum256([]byte(body)))) {
				t.Fatal("native content escaped error boundary", public, err)
			}
			if buffered && !strings.Contains(public.Details, `"jsonError": "type_mismatch"`) {
				t.Fatal("buffered decode location lost", public.Details)
			}
		})
	}
}

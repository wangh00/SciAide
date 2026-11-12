package model_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPolicyRejectionAcrossProtocolsPreservesProviderFailure(t *testing.T) {
	for _, p := range []struct {
		name   string
		client func(modelprofile.Profile) model.ChatModel
	}{{"chat", func(p modelprofile.Profile) model.ChatModel { return openai.New(p, nil) }}, {"responses", func(p modelprofile.Profile) model.ChatModel { return responses.New(p, nil) }}, {"anthropic", func(p modelprofile.Profile) model.ChatModel { return anthropic.New(p, nil) }}} {
		for _, mode := range []string{"http", "stream"} {
			t.Run(p.name+"/"+mode, func(t *testing.T) {
				payload := `{"error":{"type":"invalid_request_error","code":"invalid_prompt","message":"Invalid prompt: your prompt was flagged as potentially violating our usage policy."}}`
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if mode == "http" {
						w.WriteHeader(400)
						fmt.Fprint(w, payload)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if p.name == "responses" {
						fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":%s}\n\n", payload)
					} else {
						fmt.Fprintf(w, "data: {\"type\":\"error\",%s\n\n", payload[1:])
					}
				}))
				defer server.Close()
				stream, e := p.client(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}).Stream(context.Background(), model.ChatRequest{})
				if e == nil {
					defer stream.Close()
					_, e = stream.Recv()
				}
				var a *apperr.Error
				if !errors.As(e, &a) || a.Code != "MODEL_PROMPT_REJECTED" || a.Retryable || !strings.Contains(a.Details, "invalid_prompt") {
					t.Fatal(e)
				}
			})
		}
	}
}

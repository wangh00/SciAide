package model_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestHTTPGatewayIdentityCannotTriggerProtocolNegotiation(t *testing.T) {
	for name, create := range map[string]func(modelprofile.Profile) model.ChatModel{
		"chat":      func(p modelprofile.Profile) model.ChatModel { return openai.New(p, nil) },
		"responses": func(p modelprofile.Profile) model.ChatModel { return responses.New(p, nil) },
		"messages":  func(p modelprofile.Profile) model.ChatModel { return anthropic.New(p, nil) },
	} {
		for _, status := range []int{400, 422, 429, 503} {
			for _, parameter := range []string{"reasoning_effort", "prompt_cache_key", "cache_control"} {
				t.Run(fmt.Sprintf("%s/%d/%s", name, status, parameter), func(t *testing.T) {
					body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "no_userid", "message": "PRIVATE_SENTINEL unsupported parameter " + parameter}})
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						_, _ = io.Copy(io.Discard, r.Body)
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Retry-After", "1")
						w.WriteHeader(status)
						_, _ = w.Write(body)
					}))
					defer server.Close()
					client := create(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5})
					stream, err := client.Stream(context.Background(), model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "Plan a bounded design."}}, PromptCacheKey: "identity-case", RequestedReasoningLevel: modelcap.ReasoningHigh, ResolvedReasoningLevel: modelcap.ReasoningHigh})
					if stream != nil {
						_ = stream.Close()
					}
					var p *apperr.Error
					if !errors.As(err, &p) || p.Code != "MODEL_REQUEST_REJECTED" || p.Retryable || p.HTTPStatus != status || calls.Load() != 1 || !strings.Contains(p.Details, fmt.Sprintf("%x", sha256.Sum256(body))) {
						t.Fatal("HTTP identity error triggered negotiation or lost diagnosis", calls.Load(), err)
					}
					if strings.Contains(p.Details+p.UserMessage, "PRIVATE_SENTINEL") {
						t.Fatal("raw HTTP identity body leaked", p)
					}
				})
			}
		}
	}
}

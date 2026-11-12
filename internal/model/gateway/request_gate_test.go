package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestModelRequestGateCountsNegotiationAndPreservesPermanentDenial(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, limit := range []int{1, 3} {
			t.Run(fmt.Sprint(protocol, "/", limit), func(t *testing.T) {
				var received, used atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := received.Add(1)
					if n == 1 {
						w.WriteHeader(400)
						message := "Unsupported parameter: reasoning_effort"
						if protocol == modelprofile.ProtocolOpenAIResponses {
							message = "reasoning.effort is unsupported"
						}
						if protocol == modelprofile.ProtocolAnthropic {
							message = "thinking is not supported by this model"
						}
						fmt.Fprintf(w, `{"error":{"message":%q}}`, message)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					switch protocol {
					case modelprofile.ProtocolOpenAIChat:
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					case modelprofile.ProtocolOpenAIResponses:
						io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
					case modelprofile.ProtocolAnthropic:
						io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
					}
				}))
				defer server.Close()
				profile := modelprofile.Profile{ID: "fixture", Enabled: true, APIProtocol: protocol, BaseURL: server.URL, ModelID: "custom", Models: []modelprofile.ProfileModel{{ID: "custom", Enabled: true}}}
				gate := func(context.Context) error {
					if int(used.Load()) >= limit {
						return &apperr.Error{Code: "DEBUG_MODEL_BUDGET_EXHAUSTED", UserMessage: "fixture cap"}
					}
					used.Add(1)
					return nil
				}
				resolved, e := NewResolver(loader{profile}, gate).Resolve(context.Background(), "fixture", "custom")
				if e != nil {
					t.Fatal(e)
				}
				stream, e := resolved.Model.Stream(context.Background(), model.ChatRequest{ResolvedReasoningLevel: modelcap.ReasoningHigh})
				if limit == 1 {
					p := apperr.Public(e)
					if e == nil || p.Code != "DEBUG_MODEL_BUDGET_EXHAUSTED" || p.Retryable || received.Load() != 1 || used.Load() != 1 {
						t.Fatal("negotiation escaped cap or denial became retryable", e, received.Load(), used.Load())
					}
				} else {
					if e != nil || received.Load() != 2 || used.Load() != 2 {
						t.Fatal("fixture did not negotiate through two HTTP attempts", e, received.Load(), used.Load())
					}
					_ = stream.Close()
				}
			})
		}
	}
}

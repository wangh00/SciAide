package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestWorkflowResponsesConcurrencyRetryPreservesLogicalTurn(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(fmt.Sprintf("permanent=%v", permanent), func(t *testing.T) {
			var mu sync.Mutex
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				requests++
				count := requests
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				if count <= 2 {
					code := "gateway_concurrency_limit"
					if permanent {
						code = "invalid_api_key"
					}
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"sequence_number\":0,\"response\":{\"status\":\"failed\",\"error\":{\"code\":%q,\"message\":\"Concurrency limit exceeded for account, please retry later\"}}}\n\n", code)
					return
				}
				answer, _ := json.Marshal(`{"items":["reviewed"],"state":"ready"}`)
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%s}\n\n", answer)
				_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":11,\"output_tokens\":3}}}\n\n")
			}))
			defer server.Close()
			loop, state, _ := newLoopFixture(t, nil)
			bindSubmissionRepair(loop, state)
			loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
			loop.retryDelay = func(int) time.Duration { return 0 }
			observer := &retryCaptureObserver{retrying: make(chan RetryStatus, 6)}
			loop.observer = observer
			got := loop.Run(context.Background(), state.run.ID)
			mu.Lock()
			count := requests
			mu.Unlock()
			if permanent {
				if got != OutcomeFailed || count != 1 || len(observer.retrying) != 0 || state.run.ErrorCode != "MODEL_REQUEST_REJECTED" {
					t.Fatal("permanent code or provider message incorrectly triggered retries")
				}
				return
			}
			if got != OutcomeCompleted || count != 3 || state.run.ModelTurns != 1 || len(observer.retrying) != 2 || len(state.calls) != 0 || len(state.requestUsage) != 1 {
				t.Fatalf("retry changed logical turn or repeated tools: outcome=%s requests=%d run=%+v", got, count, state.run)
			}
			for range 2 {
				status := <-observer.retrying
				if status.Phase != "stream" || status.Reason != "rate_limit" || status.MaxAttempts != 5 || !strings.Contains(status.Message, "限流") {
					t.Fatalf("retry feedback: %+v", status)
				}
			}
			if state.run.InputTokens != 11 || state.run.OutputTokens != 3 || !strings.Contains(state.messages[1].Parts[0].Text, "reviewed") {
				t.Fatal("failed stream polluted accepted result or usage")
			}
		})
	}
}

func TestWorkflowResponsesConcurrencyStopsAfterExistingRetryBudget(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"gateway_concurrency_limit\",\"message\":\"fixture busy\"}}}\n\n")
	}))
	defer server.Close()
	loop, state, _ := newLoopFixture(t, nil)
	bindSubmissionRepair(loop, state)
	loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
	loop.retryDelay = func(int) time.Duration { return 0 }
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || requests.Load() != 6 || state.run.ModelTurns != 1 || state.run.ErrorCode != "MODEL_RATE_LIMITED" || state.messages[1].Parts[0].Text != "" {
		t.Fatalf("unbounded/accepted concurrency failure: requests=%d run=%+v", requests.Load(), state.run)
	}
	if !strings.Contains(state.run.ErrorDetails, "gateway_concurrency_limit") {
		t.Fatal("provider diagnostic lost on exhaustion")
	}
}

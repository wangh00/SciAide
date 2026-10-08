package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestProviderHTTPOutageStopsAtRequestBudget(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"The service is temporarily unavailable. Please retry later."}}`)
	}))
	defer server.Close()
	loop, state, _ := newLoopFixture(t, nil)
	bindSubmissionRepair(loop, state)
	loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
	loop.retryDelay = func(int) time.Duration { return 0 }
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || requests.Load() != maxRequestRetries+1 || state.run.ModelTurns != 1 || state.run.ErrorCode != "MODEL_UNAVAILABLE" {
		t.Fatalf("request exhaustion: %s, requests=%d, run=%+v", got, requests.Load(), state.run)
	}
	if !strings.Contains(state.run.ErrorMessage, "已停止") || !strings.Contains(state.run.ErrorDetails, "Recovery stopped after 5 request retries") || !strings.Contains(state.run.ErrorDetails, "HTTP status: 503") {
		t.Fatal("request exhaustion hid stopping condition or HTTP diagnostic")
	}
}

func TestRetryExhaustionPreservesIdentityWithoutMutatingOriginal(t *testing.T) {
	for _, phase := range []string{"request", "stream"} {
		original := &apperr.Error{Code: "MODEL_TIMEOUT", UserMessage: "automatic retry", Details: "original payload", Retryable: true, HTTPStatus: 408, RetryAfter: time.Second, CorrelationID: "fixture", Cause: context.DeadlineExceeded}
		err := retryExhaustedError(original, phase, 5)
		var exhausted *apperr.Error
		if !errors.As(err, &exhausted) || exhausted == original || !errors.Is(err, original) || !errors.Is(err, context.DeadlineExceeded) || exhausted.Code != original.Code || exhausted.HTTPStatus != original.HTTPStatus || exhausted.RetryAfter != original.RetryAfter || exhausted.CorrelationID != original.CorrelationID || !exhausted.Retryable {
			t.Fatal("retry exhaustion changed error identity")
		}
		if original.UserMessage != "automatic retry" || original.Details != "original payload" || !strings.Contains(exhausted.UserMessage, "已停止") || !strings.HasPrefix(exhausted.Details, "original payload\n") {
			t.Fatal("original diagnostic mutated or exhaustion not visible")
		}
	}
}

func TestProviderTimeoutExhaustionKeepsDiagnosticAtRunBoundary(t *testing.T) {
	provider := &openingFailureModel{inner: fake.New(), remaining: 20, err: &apperr.Error{Code: "MODEL_TIMEOUT", UserMessage: "will retry", Details: "provider connection diagnostic", Retryable: true, Cause: context.DeadlineExceeded}}
	loop, state, _ := newLoopFixture(t, nil)
	loop.models = fakeResolver{model: provider}
	loop.retryDelay = func(int) time.Duration { return 0 }
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || provider.Attempts() != 6 || state.run.ErrorCode != "MODEL_TIMEOUT" || !strings.Contains(state.run.ErrorMessage, "已停止") || !strings.Contains(state.run.ErrorDetails, "provider connection diagnostic") || !strings.Contains(state.run.ErrorDetails, "Recovery stopped after 5 request retries") {
		t.Fatalf("run boundary overwrote provider timeout/exhaustion: %s %+v", got, state.run)
	}
	// A real run-context deadline still uses the existing stopped-request path.
	provider = &openingFailureModel{inner: fake.New(), remaining: 1, err: context.DeadlineExceeded}
	loop, state, _ = newLoopFixture(t, nil)
	loop.models = fakeResolver{model: provider}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "MODEL_REQUEST_TIMEOUT" || !strings.Contains(state.run.ErrorMessage, "已停止") {
		t.Fatal("context deadline no longer stops the run")
	}
}

func TestWorkflowProviderOutageRecoveryKeepsTurnAndCompletedTools(t *testing.T) {
	for _, mode := range []string{"recover", "exhaust", "cancel", "permanent"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var requests []string
			answer := `{"items":["verified"],"state":"ready"}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				requests = append(requests, string(body))
				count := len(requests)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				if count == 1 {
					responsesFixtureCall(w, "accepted-call")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":11,\"output_tokens\":3}}}\n\n")
					return
				}
				if mode != "recover" || count < 4 {
					// Even complete-looking JSON and a proposed tool from a failed
					// stream are drafts, not accepted scientific output or execution.
					responsesFixtureEvent(w, map[string]any{"type": "response.output_text.delta", "delta": answer})
					responsesFixtureCall(w, "discarded-call")
					code := "upstream_error"
					if mode == "permanent" {
						code = "invalid_api_key"
					}
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"upstream_error\",\"code\":%q,\"message\":\"The service is temporarily unavailable. Please retry later.\"}}\n\n", code)
					return
				}
				text, _ := json.Marshal(answer)
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%s}\n\n", text)
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":13,\"output_tokens\":5}}}\n\n")
			}))
			defer server.Close()
			loop, state, _ := newLoopFixture(t, nil)
			bindSubmissionRepair(loop, state)
			loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
			loop.retryDelay = func(int) time.Duration { return 0 }
			if mode == "cancel" {
				loop.retryDelay = func(int) time.Duration { return time.Hour }
			}
			observer := &retryCaptureObserver{retrying: make(chan RetryStatus, 6)}
			loop.observer = observer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan Outcome, 1)
			go func() { done <- loop.Run(ctx, state.run.ID) }()
			if mode == "cancel" {
				select {
				case status := <-observer.retrying:
					if status.Reason != "service_unavailable" {
						t.Fatal("temporary outage mislabeled in UI")
					}
					cancel()
				case <-time.After(5 * time.Second):
					t.Fatal("retry wait did not begin")
				}
			}
			var got Outcome
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("bounded recovery did not stop")
			}
			mu.Lock()
			defer mu.Unlock()
			for _, request := range requests[1:] {
				if request != requests[1] || !strings.Contains(request, "function_call_output") || strings.Contains(request, "discarded-call") {
					t.Fatal("completed tool context lost or failed proposal replayed")
				}
			}
			if len(state.calls) != 1 || len(state.providerTurns) != 1 || state.run.ModelTurns != 2 {
				t.Fatal("outage replayed completed tools or persisted failed stream provider state")
			}
			wantRequests, wantOutcome, wantCode := 4, OutcomeCompleted, ""
			if mode == "exhaust" {
				wantRequests, wantOutcome, wantCode = maxStreamRetries+2, OutcomeFailed, "MODEL_UNAVAILABLE"
				if !strings.Contains(state.run.ErrorMessage, "已停止") || !strings.Contains(state.run.ErrorDetails, "Recovery stopped after 5 stream retries") || !strings.Contains(state.run.ErrorDetails, "upstream_error") {
					t.Fatal("retry exhaustion hid stopping condition or provider diagnostics")
				}
			} else if mode == "cancel" {
				wantRequests, wantOutcome, wantCode = 2, OutcomeCancelled, "RUN_CANCELLED"
			} else if mode == "permanent" {
				wantRequests, wantOutcome, wantCode = 2, OutcomeFailed, "MODEL_REQUEST_REJECTED"
			}
			if got != wantOutcome || len(requests) != wantRequests || state.run.ErrorCode != wantCode {
				t.Fatalf("requests=%d outcome=%s run=%+v", len(requests), got, state.run)
			}
			if mode == "recover" {
				if state.journalDrafts[2] != answer || state.messages[1].Parts[0].Text != answer || state.run.InputTokens != 24 || state.run.OutputTokens != 8 || len(observer.retrying) != 2 {
					t.Fatal("failed stream entered accepted result, accounting or correction budget")
				}
			} else if state.messages[1].Parts[0].Text != "" || state.run.InputTokens != 11 || state.run.OutputTokens != 3 {
				t.Fatal("complete-looking failed draft accepted or accounted")
			}
		})
	}
}

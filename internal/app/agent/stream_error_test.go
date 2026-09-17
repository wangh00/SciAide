package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type countingStreamRetryExecutor struct {
	ToolExecutor
	calls int
}

func (e *countingStreamRetryExecutor) Execute(ctx context.Context, projectID, callID string) (tool.Execution, error) {
	e.calls++
	return e.ToolExecutor.Execute(ctx, projectID, callID)
}

func TestAgentLoopRetriesResponsesStreamReadErrorWithoutRepeatingTools(t *testing.T) {
	for _, test := range []struct {
		name       string
		failures   int
		wantWaits  int
		wantCalls  int
		wantResult Outcome
	}{
		{"recovers after one retry", 1, 1, 3, OutcomeCompleted},
		{"recovers on fifth retry", 5, 5, 7, OutcomeCompleted},
		{"stops after five retries", 6, 5, 7, OutcomeFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				mu.Lock()
				requests = append(requests, string(body))
				n := len(requests)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				if n == 1 {
					_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"fixture","arguments":"{\"query\":\"paper\"}"}}`+"\n\n")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
					return
				}
				if n <= test.failures+1 {
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"discarded partial %d\"}\n\n", n)
					_, _ = io.WriteString(w, `data: {"error":{"code":"stream_read_error","message":"stream_read_error","type":"upstream_error"},"sequence_number":0,"type":"error"}`+"\n\n")
					return
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"recovered answer\"}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
			}))
			defer server.Close()
			loop, state, _ := newLoopFixture(t, nil)
			loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
			executor := &countingStreamRetryExecutor{ToolExecutor: loop.executor}
			loop.executor = executor
			waits := 0
			loop.sleep = func(context.Context, time.Duration) error { waits++; return nil }
			if outcome := loop.Run(context.Background(), "run"); outcome != test.wantResult {
				t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != test.wantCalls || waits != test.wantWaits {
				t.Fatalf("requests = %d, waits = %d", len(requests), waits)
			}
			if executor.calls != 1 || len(state.calls) != 1 || state.run.ModelTurns != 2 {
				t.Fatalf("executions = %d, calls = %d, logical turns = %d", executor.calls, len(state.calls), state.run.ModelTurns)
			}
			for _, call := range state.calls {
				if call.Status != tool.CallCompleted {
					t.Fatalf("completed tool changed: %#v", call)
				}
			}
			for _, request := range requests[1:] {
				if request != requests[1] || !strings.Contains(request, "function_call_output") || strings.Contains(request, "discarded partial") {
					t.Fatal("retry changed the request, lost completed tool results, or replayed a partial draft")
				}
			}
			wantText := "recovered answer"
			if test.wantResult == OutcomeFailed {
				wantText = ""
				if state.run.ErrorCode != "MODEL_UNAVAILABLE" || !strings.Contains(state.run.ErrorDetails, "stream_read_error") {
					t.Fatalf("exhausted error = %#v", state.run)
				}
			}
			if text := state.messages[1].Parts[0].Text; text != wantText {
				t.Fatalf("assistant text = %q, want %q", text, wantText)
			}
		})
	}
}

package agent

import (
	"context"
	"encoding/json"
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
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func responsesFixtureEvent(w io.Writer, value any) {
	data, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

func responsesFixtureCall(w io.Writer, id string) {
	responsesFixtureEvent(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{
		"type": "function_call", "id": "item-" + id, "call_id": id, "name": "fixture", "arguments": `{"query":"paper"}`,
	}})
}

func TestWorkflowResponsesInterruptedJSONKeepsCompletedToolsAndContext(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	answer := `{"items":["verified"],"state":"ready"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, string(body))
		count := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch count {
		case 1:
			responsesFixtureCall(w, "accepted-call")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":11,\"output_tokens\":3}}}\n\n")
		case 2:
			responsesFixtureCall(w, "discarded-call")
			responsesFixtureEvent(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "id": "discarded-message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "discarded draft"}},
			}})
			_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"`)
		case 3:
			responsesFixtureEvent(w, map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "id": "accepted-message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": answer}},
			}})
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":13,\"output_tokens\":5}}}\n\n")
		default:
			t.Error("interruption restarted more than the unfinished logical turn")
		}
	}))
	defer server.Close()
	loop, state, _ := newLoopFixture(t, nil)
	bindSubmissionRepair(loop, state)
	loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
	loop.retryDelay = func(int) time.Duration { return 0 }
	observer := &retryCaptureObserver{retrying: make(chan RetryStatus, 6)}
	loop.observer = observer
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatalf("outcome=%s run=%+v", got, state.run)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 || requests[1] != requests[2] || !strings.Contains(requests[2], "function_call_output") || strings.Contains(requests[2], "discarded") {
		t.Fatal("reconnect lost completed tool context or included failed stream state")
	}
	if len(state.calls) != 1 || state.run.ModelTurns != 2 || len(observer.retrying) != 1 {
		t.Fatalf("tool proposal leaked or logical turn repeated: calls=%+v run=%+v", state.calls, state.run)
	}
	if len(state.providerTurns) != 2 || len(state.requestUsage) != 2 || state.run.InputTokens != 24 || state.run.OutputTokens != 8 {
		t.Fatal("interrupted state or usage entered accepted logical turns")
	}
	if state.journalDrafts[2] != answer || state.messages[1].Parts[0].Text != answer {
		t.Fatal("half draft entered journal candidate or accepted stage result")
	}
}

func TestWorkflowResponsesInvalidJSONRetryBudgetAndCancellation(t *testing.T) {
	for _, mode := range []string{"truncated", "invalid", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"discarded\"}\n\n")
				if mode == "invalid" {
					_, _ = io.WriteString(w, `data: {"type":broken}`)
				} else {
					_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"`)
				}
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
				case <-observer.retrying:
					cancel()
				case <-time.After(5 * time.Second):
					t.Fatal("stream retry wait did not begin")
				}
			}
			var got Outcome
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("bounded retries or cancellation did not finish")
			}
			wantCode, wantRequests, wantOutcome := "MODEL_STREAM_INTERRUPTED", int32(maxStreamRetries+1), OutcomeFailed
			if mode == "invalid" {
				wantCode, wantRequests = "MODEL_STREAM_INVALID", 1
			} else if mode == "cancel" {
				wantCode, wantRequests, wantOutcome = "RUN_CANCELLED", 1, OutcomeCancelled
			}
			if got != wantOutcome || requests.Load() != wantRequests || state.run.ErrorCode != wantCode || state.run.ModelTurns != 1 || len(state.calls) != 0 || state.messages[1].Parts[0].Text != "" {
				t.Fatalf("unbounded retry or accepted invalid draft: requests=%d outcome=%s run=%+v", requests.Load(), got, state.run)
			}
		})
	}
}

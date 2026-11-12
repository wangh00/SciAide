package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestFirstFailedLongGenerationGetsOneBoundedRecovery(t *testing.T) {
	var requests atomic.Int32
	answer := `{"items":["verified"],"state":"ready"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			responsesFixtureCall(w, "accepted-call")
			io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
			return
		}
		if n == 2 {
			// Productive reasoning runs longer than the total recovery allowance.
			// An upstream failure loses the entire response, not just its idle tail.
			for i := 0; i < 12; i++ {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
				responsesFixtureEvent(w, map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "working"})
				w.(http.Flusher).Flush()
			}
			// Even a completed call item in the lost response must never run.
			responsesFixtureCall(w, "discarded-long-response-call")
			responsesFixtureEvent(w, map[string]any{"type": "error", "code": "upstream_http2_stream_error", "message": "Upstream HTTP/2 stream failed"})
			return
		}
		responsesFixtureEvent(w, map[string]any{"type": "response.output_text.delta", "delta": answer})
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	loop, state, _ := newLoopFixture(t, nil)
	bindSubmissionRepair(loop, state)
	loop.modelRecoveryBudget = 150 * time.Millisecond
	loop.retryDelay = func(int) time.Duration { return 0 }
	loop.models = protocolResolver{model: watchdogModel{responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), 100 * time.Millisecond}, protocol: modelcap.ProtocolOpenAIResponses}
	o := &watchdogObserver{}
	loop.observer = o
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || requests.Load() != 3 || len(state.calls) != 1 || state.messages[1].Parts[0].Text != answer {
		t.Fatal("first long response did not recover safely", got, state.run, requests.Load(), len(state.calls))
	}
	if len(o.retries) != 1 || o.retries[0].Reason != "bounded_recovery" || o.retries[0].MaxAttempts != 1 {
		t.Fatal("recovery did not expose its one-attempt boundary", o.events, o.retries)
	}
}

type progressTimestampStream struct {
	model.Stream
	at time.Time
}

func (s progressTimestampStream) LastProgressAt() time.Time { return s.at }

func TestProgressClockRetainedThroughWrappers(t *testing.T) {
	started := time.Now()
	ended := started.Add(16 * time.Minute)
	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{"no_progress", time.Time{}},
		{"productive_generation", ended.Add(-20 * time.Second)},
		{"stale_clock", started.Add(-time.Second)},
		{"invalid_future_clock", ended.Add(time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &cancelOnCloseStream{Stream: model.WithReasoningResolution(progressTimestampStream{at: tc.at}, "high", "high")}
			if got := model.LastStreamProgress(stream); got != tc.at {
				t.Fatal(got, tc.at)
			}
		})
	}
}

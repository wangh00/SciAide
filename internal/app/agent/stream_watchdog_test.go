package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type watchdogModel struct {
	model.ChatModel
	idle time.Duration
}

func (m watchdogModel) Stream(ctx context.Context, r model.ChatRequest) (model.Stream, error) {
	if m.idle > 0 && (r.StreamInactivityTimeout <= 0 || r.StreamInactivityTimeout > m.idle) {
		r.StreamInactivityTimeout = m.idle
	}
	return m.ChatModel.Stream(ctx, r)
}

type watchdogObserver struct {
	NopObserver
	mu        sync.Mutex
	events    []string
	retries   []RetryStatus
	receiving []RetryStatus
}

func (o *watchdogObserver) Retrying(_ chat.Run, r RetryStatus) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, "wait")
	o.retries = append(o.retries, r)
}
func (o *watchdogObserver) RetryReceiving(_ chat.Run, r RetryStatus) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, "receiving")
	o.receiving = append(o.receiving, r)
}
func (o *watchdogObserver) RetryRecovered(chat.Run) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, "recovered")
}

func TestWorkflowStreamStallRecoveryRetainsCompletedTools(t *testing.T) {
	for _, mode := range []string{"recover", "exhaust", "time_budget", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			requests := []string{}
			answer := `{"items":["verified"],"state":"ready"}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				requests = append(requests, string(body))
				n := len(requests)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				if n == 1 {
					responsesFixtureCall(w, "accepted-call")
					io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":3}}}\n\n")
					return
				}
				if mode == "recover" && n == 3 {
					responsesFixtureEvent(w, map[string]any{"type": "response.output_text.delta", "delta": answer})
					io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":13,\"output_tokens\":5}}}\n\n")
					return
				}
				responsesFixtureEvent(w, map[string]any{"type": "response.output_text.delta", "delta": answer})
				responsesFixtureCall(w, "discarded-call")
				w.(http.Flusher).Flush()
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
						io.WriteString(w, ": heartbeat\n\n")
						w.(http.Flusher).Flush()
					}
				}
			}))
			defer server.Close()
			loop, state, _ := newLoopFixture(t, nil)
			bindSubmissionRepair(loop, state)
			loop.models = protocolResolver{model: watchdogModel{responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), 120 * time.Millisecond}, protocol: modelcap.ProtocolOpenAIResponses}
			loop.retryDelay = func(int) time.Duration { return 0 }
			if mode == "time_budget" {
				loop.modelRecoveryBudget = 310 * time.Millisecond
			}
			o := &watchdogObserver{}
			loop.observer = o
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				time.AfterFunc(70*time.Millisecond, cancel)
			}
			got := loop.Run(ctx, "run")
			mu.Lock()
			defer mu.Unlock()
			for _, r := range requests[1:] {
				if r != requests[1] || !strings.Contains(r, "function_call_output") || strings.Contains(r, "discarded-call") {
					t.Fatal("failing stream changed replay or repeated tools")
				}
			}
			if len(state.calls) != 1 || state.run.ModelTurns != 2 {
				t.Fatal("completed tool rerun", state.run, len(state.calls))
			}
			if mode == "recover" {
				if got != OutcomeCompleted || len(requests) != 3 || state.messages[1].Parts[0].Text != answer || state.run.InputTokens != 24 || len(o.events) != 3 || strings.Join(o.events, ",") != "wait,receiving,recovered" {
					t.Fatal(got, len(requests), o.events, state.run)
				}
			} else {
				code := "MODEL_STREAM_STALLED"
				count := maxStreamRetries + 2
				if mode == "time_budget" {
					code = "MODEL_RECOVERY_TIMEOUT"
					count = 4
				}
				if mode == "cancel" {
					code = "RUN_CANCELLED"
					count = 2
				}
				if state.run.ErrorCode != code || len(requests) != count || state.messages[1].Parts[0].Text != "" || state.run.InputTokens != 11 {
					t.Fatal(got, len(requests), state.run)
				}
				for _, e := range o.events {
					if e == "recovered" {
						t.Fatal("opening headers falsely reported recovery")
					}
				}
				if mode == "exhaust" && len(o.retries) != 5 {
					t.Fatal("lost five retry count", o.retries)
				}
			}
		})
	}
}

func TestHealthyResponseIsNotAbortedByRecoveryTimeBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		for i := 0; i < 12; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			responsesFixtureEvent(w, map[string]any{"type": "response.output_text.delta", "delta": "x"})
			w.(http.Flusher).Flush()
		}
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	loop, state, _ := newLoopFixture(t, nil)
	loop.modelRecoveryBudget = 100 * time.Millisecond
	loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
	started := time.Now()
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || time.Since(started) < 200*time.Millisecond || state.messages[1].Parts[0].Text != strings.Repeat("x", 12) {
		t.Fatal(got, state.run)
	}
}

func TestRecoveryTimeBudgetBoundsRetryAfterAndPreservesCause(t *testing.T) {
	l := &Loop{observer: NopObserver{}, retryDelay: func(int) time.Duration { return 0 }, sleep: sleepContext}
	cause := &apperr.Error{Code: "MODEL_RATE_LIMITED", Retryable: true, RetryAfter: time.Hour, Details: "original diagnostic"}
	started := time.Now()
	err := l.boundedRetryWait(context.Background(), chat.Run{}, "request", 1, 5, cause, &recoveryAllowance{limit: 60 * time.Millisecond})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_RECOVERY_TIMEOUT" || !strings.Contains(appErr.Details, "original diagnostic") || time.Since(started) > time.Second {
		t.Fatal(err)
	}
}

func TestRecoveryTimeBudgetAlsoBoundsConnectionEstablishment(t *testing.T) {
	stop, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // allow net/http to monitor client disconnect
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-stop:
		}
	}))
	defer func() { close(stop); server.Close() }()
	loop, state, _ := newLoopFixture(t, nil)
	loop.modelRecoveryBudget = 80 * time.Millisecond
	loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
	started := time.Now()
	got := loop.Run(context.Background(), "run")
	if got != OutcomeFailed || state.run.ErrorCode != "MODEL_RECOVERY_TIMEOUT" || time.Since(started) > time.Second {
		t.Fatal(got, state.run)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("establishment timeout did not close the connection")
	}
}

func TestRecoveryBudgetDoesNotRegrantConnectionTimeToFirstIdleLease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(180 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				io.WriteString(w, ": heartbeat\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer server.Close()
	loop, state, _ := newLoopFixture(t, nil)
	loop.modelRecoveryBudget = 300 * time.Millisecond
	loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
	started := time.Now()
	got := loop.Run(context.Background(), "run")
	if elapsed := time.Since(started); got != OutcomeFailed || state.run.ErrorCode != "MODEL_RECOVERY_TIMEOUT" || elapsed > 410*time.Millisecond {
		t.Fatal(got, elapsed, state.run)
	}
}

type cancelledOpeningModel struct {
	model.ChatModel
	onInternalDeadline func()
}

func (m cancelledOpeningModel) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	<-ctx.Done()
	if m.onInternalDeadline != nil {
		m.onInternalDeadline()
	}
	return nil, ctx.Err()
}

func TestEstablishmentBudgetAndUserCancellationRemainDistinct(t *testing.T) {
	for _, mode := range []string{"internal_budget", "user_cancel", "simultaneous"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := cancelledOpeningModel{ChatModel: fake.New()}
			if mode == "user_cancel" {
				cancel()
			} else if mode == "simultaneous" {
				provider.onInternalDeadline = cancel
			}
			_, err := openWithinRecoveryAllowance(ctx, provider, model.ChatRequest{}, &recoveryAllowance{limit: 20 * time.Millisecond})
			if mode != "internal_budget" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("user cancellation changed identity", err)
				}
				return
			}
			var appErr *apperr.Error
			if !errors.As(err, &appErr) || appErr.Code != "MODEL_RECOVERY_TIMEOUT" || errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("internal deadline misreported as user cancellation", err)
			}
		})
	}
}

package agent

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestLongResponseReserveAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"responses", "chat", "anthropic"} {
		for _, mode := range []string{"recover", "stall", "drip_forever", "http_error", "no_headers", "cancel", "retry_after", "permanent"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var mu sync.Mutex
				var requests []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					requests = append(requests, string(body))
					n := len(requests)
					mu.Unlock()
					if n > 1 && mode == "http_error" {
						http.Error(w, "service unavailable", 503)
						return
					}
					if n > 1 && mode == "no_headers" {
						<-r.Context().Done()
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					write := func(data string) { fmt.Fprintf(w, "data: %s\n\n", data); w.(http.Flusher).Flush() }
					if protocol == "anthropic" {
						write(`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
						write(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
					}
					delta := func(value string) {
						switch protocol {
						case "responses":
							write(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, value))
						case "chat":
							write(fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, value))
						default:
							write(fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, value))
						}
					}
					if n == 1 {
						// Productive first attempt exceeds the failed-attempt budget,
						// then loses its terminal; JSON-shaped output is not accepted.
						for i := 0; i < 14; i++ {
							select {
							case <-r.Context().Done():
								return
							case <-time.After(20 * time.Millisecond):
								delta("x")
							}
						}
						if mode == "permanent" {
							if protocol == "chat" {
								write(`{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"invalid key"}}`)
							} else {
								write(`{"type":"error","error":{"type":"authentication_error","code":"invalid_api_key","message":"invalid key"}}`)
							}
						}
						return
					}
					if mode == "recover" {
						delta("recovered")
						switch protocol {
						case "responses":
							write(`{"type":"response.completed","response":{}}`)
						case "chat":
							write(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
							write(`[DONE]`)
						default:
							write(`{"type":"content_block_stop","index":0}`)
							write(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
							write(`{"type":"message_stop"}`)
						}
						return
					}
					delta("unaccepted")
					if mode == "cancel" {
						cancel()
					}
					ticker := time.NewTicker(20 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-r.Context().Done():
							return
						case <-ticker.C:
							if mode == "drip_forever" {
								delta("x")
							} else {
								io.WriteString(w, ": heartbeat\n\n")
								w.(http.Flusher).Flush()
							}
						}
					}
				}))
				defer server.Close()
				p := modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}
				var provider model.ChatModel
				api := modelcap.ProtocolOpenAIResponses
				switch protocol {
				case "responses":
					provider = responses.New(p, nil)
				case "chat":
					provider = openai.New(p, nil)
					api = modelcap.ProtocolOpenAIChat
				default:
					provider = anthropic.New(p, nil)
					api = modelcap.ProtocolAnthropic
				}
				loop, state, _ := newLoopFixture(t, nil)
				loop.models = protocolResolver{model: watchdogModel{provider, 100 * time.Millisecond}, protocol: api}
				loop.modelRecoveryBudget = 180 * time.Millisecond
				loop.retryDelay = func(int) time.Duration {
					if mode == "retry_after" {
						return time.Second
					}
					return 0
				}
				o := &watchdogObserver{}
				loop.observer = o
				started := time.Now()
				got := loop.Run(ctx, "run")
				if time.Since(started) > 2*time.Second {
					t.Fatal("reserve failed to bound generation")
				}
				mu.Lock()
				defer mu.Unlock()
				wantRequests := 2
				if mode == "retry_after" || mode == "permanent" {
					wantRequests = 1
				}
				if len(requests) != wantRequests || (len(requests) > 1 && requests[0] != requests[1]) {
					t.Fatal("request replay changed or extra retries", len(requests))
				}
				if mode == "permanent" {
					if got != OutcomeFailed || len(o.retries) != 0 || state.run.ErrorCode == "MODEL_RECOVERY_TIMEOUT" {
						t.Fatal("permanent error incorrectly retried", got, state.run, o.retries)
					}
					return
				}
				if len(o.retries) != 1 || o.retries[0].Reason != "bounded_recovery" || o.retries[0].MaxAttempts != 1 {
					t.Fatal(o.retries)
				}
				if mode == "recover" {
					if got != OutcomeCompleted || state.messages[1].Parts[0].Text != "recovered" {
						t.Fatal(got, state.run)
					}
					return
				}
				if state.messages[1].Parts[0].Text != "" {
					t.Fatal("partial output accepted")
				}
				if mode == "cancel" {
					if state.run.ErrorCode != "RUN_CANCELLED" {
						t.Fatal(state.run)
					}
					return
				}
				if got != OutcomeFailed || state.run.ErrorCode == "RUN_CANCELLED" {
					t.Fatal(got, state.run)
				}
				if !strings.Contains(state.run.ErrorMessage, fmt.Sprintf("实际重试 %d 次", wantRequests-1)) {
					t.Fatal(state.run.ErrorMessage)
				}
				if mode == "no_headers" || mode == "drip_forever" || mode == "retry_after" {
					if state.run.ErrorCode != "MODEL_RECOVERY_TIMEOUT" || state.requestUsage[1].StatusCode != 408 || state.requestUsage[1].ErrorCode != "MODEL_RECOVERY_TIMEOUT" {
						t.Fatal(state.run, state.requestUsage)
					}
				}
			})
		}
	}
}

func TestReserveEligibilityDoesNotRenewOnLaterFailures(t *testing.T) {
	start := time.Now().Add(-16 * time.Minute)
	end := time.Now()
	for _, mode := range []string{"first", "no_progress", "future_progress", "later_failure", "short", "already_reserved"} {
		t.Run(mode, func(t *testing.T) {
			a := recoveryAllowance{limit: 12 * time.Minute, spent: 16 * time.Minute, streamAttempts: 1}
			progress := end.Add(-5 * time.Minute)
			s := start
			switch mode {
			case "no_progress":
				progress = time.Time{}
			case "future_progress":
				progress = end.Add(time.Second)
			case "later_failure":
				a.streamAttempts = 2
			case "short":
				s = end.Add(-time.Minute)
			case "already_reserved":
				a.reserveDeadline = end.Add(time.Minute)
			}
			if got := a.reserveFirstLongResponse(s, end, progress); got != (mode == "first") {
				t.Fatal(got)
			}
			if mode == "first" && (a.spent != 16*time.Minute || a.reserveFirstLongResponse(s, end, progress)) {
				t.Fatal("cost erased or allowance renewed")
			}
		})
	}
}

func TestRecoveryTimeoutDiagnosticsPreserveIdentityAndActualAttempts(t *testing.T) {
	cause := &apperr.Error{Code: "MODEL_STREAM_STALLED", Details: "terminal_received=false", Retryable: true}
	a := &recoveryAllowance{limit: time.Minute, spent: 2 * time.Minute, requestAttempts: 1, lastError: cause}
	err := recoveryTimeExhausted(context.DeadlineExceeded, a)
	var e *apperr.Error
	if !errors.As(err, &e) || !strings.Contains(e.UserMessage, "实际重试 0 次") || !strings.Contains(e.Details, "terminal_received=false") {
		t.Fatal(err)
	}
	status, code, _ := requestFailureDetails(err)
	if status != 408 || code != "MODEL_RECOVERY_TIMEOUT" {
		t.Fatal(status, code)
	}
	// Waiting does not claim an HTTP request was actually reissued.
	l := &Loop{observer: NopObserver{}, retryDelay: func(int) time.Duration { return time.Hour }, sleep: sleepContext}
	a.reserveDeadline = time.Now().Add(10 * time.Millisecond)
	if err = l.boundedRetryWait(context.Background(), chat.Run{}, "stream", 1, 5, cause, a); err == nil || a.requestAttempts != 1 {
		t.Fatal(err, a.requestAttempts)
	}
	err = recoveryTimeExhausted(context.Canceled, a)
	if errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("internal reserve deadline became a manual cancel", err)
	}
}

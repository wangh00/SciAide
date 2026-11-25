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
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelutil"
)

func recoveryControlFields(t *testing.T, detail string) map[string]any {
	t.Helper()
	_, raw, ok := strings.Cut(detail, "\nProvider payload:\n")
	var fields map[string]any
	if !ok || json.Unmarshal([]byte(raw), &fields) != nil {
		t.Fatal("invalid recovery diagnostic envelope")
	}
	return fields
}

func TestRecoveryDiagnosticsTypedHostControlHasNoProviderPayload(t *testing.T) {
	for _, phase := range []string{"request", "stream"} {
		for _, tc := range []struct {
			name, code, reason string
			err                error
		}{
			{"cancel", "REQUEST_INTERRUPTED", "context_canceled", context.Canceled},
			{"wrapped_cancel", "REQUEST_INTERRUPTED", "context_canceled", fmt.Errorf("PRIVATE_SENTINEL: %w", context.Canceled)},
			{"deadline", "MODEL_TIMEOUT", "context_deadline_exceeded", context.DeadlineExceeded},
			{"wrapped_deadline", "MODEL_TIMEOUT", "context_deadline_exceeded", fmt.Errorf("PRIVATE_SENTINEL: %w", context.DeadlineExceeded)},
		} {
			t.Run(phase+"/"+tc.name, func(t *testing.T) {
				loop, state, _ := newLoopFixture(t, nil)
				runs := bindParts(loop, state)
				started := time.Now()
				loop.recordRecoveryFailure(state.run, phase, 3, started, started.Add(12*time.Millisecond), streamAttempt{}, tc.err)
				if len(runs.diagnostics) != 1 {
					t.Fatal("host control audit missing")
				}
				audit := runs.diagnostics[0]
				fields := recoveryControlFields(t, audit.Details)
				if audit.Code != tc.code || fields["code"] != tc.code || fields["source"] != "host_control" || fields["reason"] != tc.reason || audit.Phase != phase || audit.Attempt != 3 || audit.ElapsedMillis != 12 {
					t.Fatal("local control identity or accounting changed", audit.Code, fields)
				}
				for _, key := range []string{"details", "network", "httpStatus"} {
					if _, ok := fields[key]; ok {
						t.Fatal("local control invented provider or HTTP evidence", key)
					}
				}
				for _, text := range []string{"jsonError", "payloadSha256", "invalid_syntax", "PRIVATE_SENTINEL", "original error"} {
					if strings.Contains(audit.Details, text) {
						t.Fatal("local error text was treated as a provider body", text)
					}
				}
			})
		}
	}
}

func TestRecoveryDiagnosticsPrimaryTypedFailureWinsOverControlCause(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		err        error
		status     int
	}{
		{"http", "MODEL_UNAVAILABLE", modelutil.ClassifyStatus(503, []byte(`{"error":{"message":"context canceled PRIVATE_SENTINEL"}}`)), 503},
		{"joined_http_cancel", "MODEL_RATE_LIMITED", errors.Join(context.Canceled, modelutil.ClassifyStatus(429, []byte(`{"error":{"code":"context_canceled"}}`))), 429},
		{"typed_cancel_cause", "MODEL_STREAM_INVALID", &apperr.Error{Code: "MODEL_STREAM_INVALID", UserMessage: "Malformed provider event", Details: modelutil.StreamEventErrorDetails("Responses stream event", []byte("context canceled")), Cause: context.Canceled}, 0},
		{"typed_deadline_cause", "MODEL_TIMEOUT", &apperr.Error{Code: "MODEL_TIMEOUT", UserMessage: "Provider timeout", HTTPStatus: 408, Details: modelutil.ProviderErrorDetails("HTTP response", 408, []byte(`{"error":{"code":"timeout"}}`)), Cause: context.DeadlineExceeded}, 408},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop, state, _ := newLoopFixture(t, nil)
			runs := bindParts(loop, state)
			before := apperr.Public(tc.err)
			loop.recordRecoveryFailure(state.run, "request", 1, time.Now(), time.Now(), streamAttempt{}, tc.err)
			audit := runs.diagnostics[0]
			fields := recoveryControlFields(t, audit.Details)
			if audit.Code != tc.code || fields["source"] == "host_control" || fields["details"] == nil || strings.Contains(audit.Details, "PRIVATE_SENTINEL") {
				t.Fatal("primary failure was replaced by a cancellation or leaked", audit.Code, fields)
			}
			got, present := fields["httpStatus"]
			if tc.status == 0 && present || tc.status != 0 && got != float64(tc.status) {
				t.Fatal("HTTP status lost or invented", fields)
			}
			if apperr.Public(tc.err) != before {
				t.Fatal("diagnostic classification mutated the actual failure")
			}
		})
	}
}

func TestRecoveryDiagnosticsMissingPayloadDoesNotParseErrorText(t *testing.T) {
	for _, err := range []error{
		errors.New("context canceled"),
		errors.New("PRIVATE_SENTINEL"),
		&apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "No response payload", Cause: context.Canceled},
	} {
		loop, state, _ := newLoopFixture(t, nil)
		runs := bindParts(loop, state)
		loop.recordRecoveryFailure(state.run, "request", 1, time.Now(), time.Now(), streamAttempt{}, err)
		fields := recoveryControlFields(t, runs.diagnostics[0].Details)
		if fields["source"] != "error_identity" || fields["details"] != nil || strings.Contains(runs.diagnostics[0].Details, "jsonError") || strings.Contains(runs.diagnostics[0].Details, "PRIVATE_SENTINEL") {
			t.Fatal("an absent diagnostic payload was invented from Error()", fields)
		}
	}
}

func TestRecoveryDiagnosticsActualMalformedSSEIsNotHostCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: context canceled\n\n")
	}))
	defer server.Close()
	client := responses.New(modelprofile.Profile{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, nil)
	stream, err := client.Stream(context.Background(), model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "Only synthetic malformed SSE"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	var classified *apperr.Error
	if !errors.As(err, &classified) || classified.Code != "MODEL_STREAM_INVALID" || classified.Retryable || errors.Is(err, context.Canceled) {
		t.Fatal("actual malformed SSE classification changed", err)
	}
	loop, state, _ := newLoopFixture(t, nil)
	runs := bindParts(loop, state)
	// The actual stream payload spelling a control error is not a typed control cause.
	loop.recordRecoveryFailure(state.run, "stream", 1, time.Now(), time.Now(), streamAttempt{}, err)
	audit := runs.diagnostics[0]
	fields := recoveryControlFields(t, audit.Details)
	if audit.Code != "MODEL_STREAM_INVALID" || fields["source"] == "host_control" || !strings.Contains(audit.Details, "45296d4da583982803043ed41eaa2401c6a87028ca5dcd9d36a9057159a46030") || !strings.Contains(audit.Details, "invalid_syntax") {
		t.Fatal("genuine protocol failure was erased", audit.Code, fields)
	}
}

package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/modelutil"
)

func TestRecoveryDiagnosticsKeepOnlyTrustedHTTPStatus(t *testing.T) {
	for _, status := range []int{503, 429, 0} {
		loop, state, _ := newLoopFixture(t, nil)
		runs := bindParts(loop, state)
		body := []byte(`{"error":{"code":"upstream_error","type":"upstream_error","api_key":"PRIVATE_SENTINEL"}}`)
		err := modelutil.ClassifyStreamError("upstream_error", "stream_read_error", "PRIVATE_SENTINEL", "Source: HTTP response\nHTTP status: 503\nProvider payload:\n"+string(body))
		if status != 0 {
			err = modelutil.ClassifyStatus(status, body)
		}
		loop.recordRecoveryFailure(state.run, "stream", 1, time.Now(), time.Now(), streamAttempt{}, err)
		if len(runs.diagnostics) != 1 {
			t.Fatal("failure audit missing")
		}
		detail := runs.diagnostics[0].Details
		var payload map[string]any
		_, raw, _ := strings.Cut(detail, "\nProvider payload:\n")
		if json.Unmarshal([]byte(raw), &payload) != nil || strings.Contains(detail, "PRIVATE_SENTINEL") {
			t.Fatal("unsafe audit", detail)
		}
		got, present := payload["httpStatus"]
		if status == 0 && present || status != 0 && got != float64(status) {
			t.Fatal("trusted HTTP status lost or fabricated from source label", status, detail)
		}
	}
}

func TestRecoveryDiagnosticsPreserveActualNetworkMilestones(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(120 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := modelutil.NewStreamingHTTPClient(40 * time.Millisecond)
	defer client.CloseIdleConnections()
	_, err := client.Post(server.URL+"/PRIVATE_SENTINEL", "application/json", strings.NewReader(`{"input":"PRIVATE_SENTINEL"}`))
	if err == nil {
		t.Fatal("expected actual header timeout")
	}
	err = modelutil.ClassifyNetwork(err)
	loop, state, _ := newLoopFixture(t, nil)
	runs := bindParts(loop, state)
	// Exhaustion wrappers must preserve the original private transport cause.
	loop.recordRecoveryFailure(state.run, "request", 6, time.Now(), time.Now(), streamAttempt{}, retryExhaustedError(err, "request", 5))
	if len(runs.diagnostics) != 1 {
		t.Fatal("network audit missing")
	}
	detail := runs.diagnostics[0].Details
	var payload struct {
		Network    modelutil.HTTPRequestTrace `json:"network"`
		HTTPStatus *int                       `json:"httpStatus"`
	}
	_, raw, _ := strings.Cut(detail, "\nProvider payload:\n")
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.Network.NetworkPhase != "request_flush_or_response_headers" || payload.Network.RequestWriteCallbackMillis == nil || payload.HTTPStatus != nil || strings.Contains(detail, "PRIVATE_SENTINEL") {
		t.Fatal("network milestone dropped, unobserved status invented, or request leaked", detail)
	}
	if strings.Contains(detail, "jsonError") || !strings.Contains(detail, `"source": "host_network"`) {
		t.Fatal("local transport explanation pretended to be a malformed provider body", detail)
	}
	// Upstream text cannot manufacture transport evidence.
	forged := modelutil.ClassifyStatus(503, []byte(`{"network":{"networkPhase":"dns"}}`))
	if _, ok := modelutil.HTTPFailureTrace(forged); ok {
		t.Fatal("provider supplied a trusted phase")
	}
}

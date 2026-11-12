package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

func TestDebugErrorObserverOptInRedactionAndBounds(t *testing.T) {
	if debugErrorObserver(nil, nil, nil) != nil {
		t.Fatal("capture enabled by default")
	}
	var captured DebugProviderError
	observe := debugErrorObserver(func(v DebugProviderError) { captured = v }, []byte("KEY_SENTINEL"), map[string]string{"X-Tenant": "tenant /+ SECRET", "Authorization": "Bearer OTHER_SENTINEL"})
	observe("error", "KEY_SENTINEL", "KEY_SENTINEL tenant%20%2f+%20SECRET OTHER_SENTINEL "+strings.Repeat("中", 3000))
	encoded, _ := json.Marshal(captured)
	for _, secret := range []string{"KEY_SENTINEL", "OTHER_SENTINEL", "tenant%20%2F+%20SECRET"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("configured value leaked")
		}
	}
	if len([]rune(captured.Message)) > 2060 || !strings.HasSuffix(captured.Message, "[TRUNCATED]") {
		t.Fatal("unbounded error capture")
	}
}

func TestDebugStreamErrorObservationDoesNotChangePublicError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"type":"error","code":"invalid_tool_history","message":"bad item; KEY_SENTINEL"}`+"\n\n")
	}))
	defer server.Close()
	var captured []DebugProviderError
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 10}, []byte("KEY_SENTINEL"))
	client.SetDebugErrorObserver(func(e DebugProviderError) { captured = append(captured, e) })
	stream, err := client.Stream(context.Background(), model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Recv()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "MODEL_REQUEST_REJECTED" || appErr.Retryable {
		t.Fatal("capture changed error policy", err)
	}
	if strings.Contains(appErr.Details, "KEY_SENTINEL") || strings.Contains(appErr.Details, "bad item") {
		t.Fatal("capture escaped into public diagnostics")
	}
	if len(captured) != 1 || captured[0].Code != "invalid_tool_history" || captured[0].Message != "bad item; [REDACTED]" {
		t.Fatal("missing/redaction-invalid observation")
	}
}

func TestDebugHTTPErrorObservationIsExplicitAndRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":"invalid_tool_history","message":"bad item KEY_SENTINEL tenant%2fA%2bB"}}`)
	}))
	defer server.Close()
	for _, enabled := range []bool{false, true} {
		client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 10, CustomHeaders: map[string]string{"X-Tenant": "tenant/A+B"}}, []byte("KEY_SENTINEL"))
		var captured []DebugProviderError
		if enabled {
			client.SetDebugErrorObserver(func(e DebugProviderError) { captured = append(captured, e) })
		}
		_, err := client.Stream(context.Background(), model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "fixture"}}})
		if err == nil {
			t.Fatal("capture accepted an HTTP rejection")
		}
		if !enabled && len(captured) != 0 {
			t.Fatal("capture enabled by default")
		}
		if enabled && (len(captured) != 1 || captured[0].Kind != "http_error" || captured[0].Code != "400" || captured[0].Message != "bad item [REDACTED] [REDACTED]") {
			t.Fatal("missing/redaction-invalid HTTP observation", captured)
		}
	}
}

func TestDebugRedactionPreservesAllUnmatchedWireBytes(t *testing.T) {
	for _, secret := range [][]byte{nil, []byte("not-present"), []byte("tenant/A+B")} {
		redact := debugTextRedactor(secret, nil)
		text := "ordinary https://x/%2f/%aB/100% xx" + string([]byte{0xff})
		if clean := redact(text, 0); clean != text {
			t.Fatal("unmatched diagnostic bytes changed")
		}
	}
	redact := debugTextRedactor([]byte("tenant/A+B"), nil)
	text := "https://x/%2f tenant%2fA%2bB between%ab tenant%2FA%2BB after%fF"
	want := "https://x/%2f [REDACTED] between%ab [REDACTED] after%fF"
	if clean := redact(text, 0); clean != want {
		t.Fatal("mixed-case secret matching changed unrelated bytes", clean)
	}
}

func TestDebugRedactionMatchesLiteralKeyAfterUnrelatedPercent(t *testing.T) {
	redact := debugTextRedactor([]byte("abcdef123"), map[string]string{"X-Test": "abc/A+B"})
	text := "%abcdef123 %abc%2fA%2bB %2f"
	if clean := redact(text, 0); clean != "%[REDACTED] %[REDACTED] %2f" {
		t.Fatal("literal or encoded key was changed by surrounding percent context", clean)
	}
}

func TestDebugRedactionSinglePassHasLinearOutputBound(t *testing.T) {
	redact := debugTextRedactor([]byte("E"), map[string]string{"X-E": "E", "X-D": "D"})
	text := strings.Repeat("ED", 1<<18)
	clean := redact(text, 0)
	if len(clean) != len(text)*len("[REDACTED]") || strings.Count(clean, "[REDACTED]") != len(text) {
		t.Fatal("replacement markers were processed again or output is unbounded", len(clean))
	}
}

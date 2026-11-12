package responses

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelutil"
)

func TestCoalescedTerminalFaultIsFailureOnlyAndKeepsClassificationPriority(t *testing.T) {
	prefix := `{"sequence_number":133,"type":"response.reasoning_summary_text.delta","delta":"PRIVATE_PREFIX"}`
	for _, tc := range []struct {
		code, want string
		retry      bool
	}{
		{"upstream_stream_read_error", "MODEL_UNAVAILABLE", true},
		{"upstream_http2_stream_error", "MODEL_UNAVAILABLE", true},
		{"stream_read_error", "MODEL_UNAVAILABLE", true},
		{"usage_limit_reached", "MODEL_REQUEST_REJECTED", false},
		{"invalid_api_key", "MODEL_REQUEST_REJECTED", false},
		{"invalid_request_error", "MODEL_REQUEST_REJECTED", false},
		{"unknown_error", "MODEL_REQUEST_REJECTED", false},
		{"rate_limit_error", "MODEL_REQUEST_REJECTED", false},
		{"server_error", "MODEL_REQUEST_REJECTED", false},
		{"not_rate_limit_error", "MODEL_REQUEST_REJECTED", false},
		{"not_server_error", "MODEL_REQUEST_REJECTED", false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			terminal := fmt.Sprintf(`{"type":"error","sequence_number":0,"code":%q,"message":"PRIVATE_REASON","param":null}`, tc.code)
			data := prefix + "\n" + terminal
			wire := "event: response.reasoning_summary_text.delta\ndata: " + prefix + "\nevent: error\ndata: " + terminal + "\n\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, wire)
			}))
			defer server.Close()
			client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
			s, err := client.Stream(context.Background(), model.ChatRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			event, err := s.Recv()
			var appErr *apperr.Error
			if event.Type != "" || !errors.As(err, &appErr) || appErr.Code != tc.want || appErr.Retryable != tc.retry {
				t.Fatal("terminal fault was accepted or misclassified", event, err)
			}
			if strings.Contains(appErr.Details, "PRIVATE_") || !strings.Contains(appErr.Details, fmt.Sprintf("%x", sha256.Sum256([]byte(data)))) || !strings.Contains(appErr.Details, "missing_separator_before_terminal_error") {
				t.Fatal("original envelope identity or private-data boundary lost", appErr.Details)
			}
			if event, err := s.Recv(); err != io.EOF || event.Type != "" {
				t.Fatal("stream resumed after terminal fault", event, err)
			}
		})
	}
}

func TestCoalescedTerminalFaultRejectsAmbiguousOrBrokenPayloads(t *testing.T) {
	goodPrefix := `{"type":"response.reasoning_summary_text.delta","delta":"prefix"}`
	goodError := `{"type":"error","code":"upstream_stream_read_error","message":"interrupted"}`
	for _, data := range []string{
		goodPrefix + goodError,
		goodPrefix + " " + goodError,
		goodPrefix + "\n" + goodPrefix,
		goodPrefix + "\n" + goodError + "\n" + goodError,
		goodPrefix + "\n" + strings.TrimSuffix(goodError, "}"),
		`{"type":"response.completed","delta":"prefix"}` + "\n" + goodError,
		`{"type":"error","code":"invalid_api_key","delta":"prefix"}` + "\n" + goodError,
		`{"type":"unknown","delta":"prefix"}` + "\n" + goodError,
		`{"type":"response.reasoning_summary_text.delta","delta":{}}` + "\n" + goodError,
		`{"type":"response.reasoning_summary_text.delta","delta":"prefix","error":{"code":"invalid_api_key"}}` + "\n" + goodError,
		goodPrefix + "\n" + `{"type":"error","code":"invalid_api_key","code":"upstream_stream_read_error"}`,
		goodPrefix + "\n" + `{"type":"error","code":"upstream_stream_read_error","error":{"code":"invalid_api_key"}}`,
		goodPrefix + "\n" + `{"type":"error","code":"upstream_stream_read_error","message":"interrupted","param":{"code":"invalid_api_key"}}`,
		goodPrefix + "\n" + `{"type":"error","code":"upstream_stream_read_error","message":"interrupted","param":[]}`,
		goodPrefix + "\n" + `{"type":"error","code":"upstream_stream_read_error","message":"interrupted","param":0}`,
		goodPrefix + "\n" + `{"type":"error","code":"upstream_stream_read_error","message":"interrupted","sequence_number":{}}`,
		goodPrefix + "\n" + `{"type":"error","code":"upstream_stream_read_error","message":null}`,
		goodPrefix + "\n" + `{"type":"error","code":"upstream_stream_read_error"}`,
		goodPrefix + "\n" + `{"type":"error","code":"upstream_stream_read_error","message":""}`,
		goodPrefix + "\n" + strings.Replace(goodError, "interrupted", string([]byte{0xff}), 1),
		strings.Replace(goodPrefix, "prefix", string([]byte{0xff}), 1) + "\n" + goodError,
	} {
		if _, _, ok := decodedFaultForTest(data, true, "error"); ok {
			t.Fatal("ambiguous/broken envelope treated as a transport failure")
		}
	}
	for _, key := range []string{"sequence_number", "content_index", "summary_index", "output_index"} {
		for _, value := range []string{"{}", "[]", "null", "-1", "1.5", `"1"`, "1e0", "true", "9223372036854775808"} {
			bad := strings.TrimSuffix(goodPrefix, "}") + fmt.Sprintf(",%q:%s}", key, value)
			if _, _, ok := decodedFaultForTest(bad+"\n"+goodError, true, "error"); ok {
				t.Fatal("invalid prefix index accepted", key, value)
			}
		}
	}
	for _, channel := range []string{"", "message", "response.output_text.delta", "Error", " error"} {
		if _, _, ok := decodedFaultForTest(goodPrefix+"\n"+goodError, true, channel); ok {
			t.Fatal("non-error SSE channel accepted", channel)
		}
	}
	if _, _, ok := decodedFaultForTest(goodPrefix+"\n"+goodError, false, "error"); ok {
		t.Fatal("EOF envelope broadened")
	}
	valid := `{"type":"response.output_text.delta","delta":"{\"type\":\"error\",\"code\":\"upstream_stream_read_error\"}"}`
	if _, _, ok := decodedFaultForTest(valid, true, "error"); ok {
		t.Fatal("model text was interpreted as a terminal error")
	}
}

func TestCoalescedFaultRequiresExplicitErrorChannel(t *testing.T) {
	prefix := `{"type":"response.output_text.delta","delta":"prefix"}`
	terminal := `{"type":"error","code":"upstream_stream_read_error","message":"interrupted"}`
	for _, tc := range []struct{ name, wire string }{
		{"no-channel", "data: " + prefix + "\ndata: " + terminal + "\n\n"},
		{"non-error-channel", "event: response.output_text.delta\ndata: " + prefix + "\ndata: " + terminal + "\n\n"},
		{"error-channel-reset", "event: error\n\ndata: " + prefix + "\ndata: " + terminal + "\n\n"},
		{"error-header-before-both-fields", "event: error\ndata: " + prefix + "\ndata: " + terminal + "\n\n"},
		{"channel-body-mismatch", "event: response.reasoning_text.delta\ndata: " + prefix + "\nevent: error\ndata: " + terminal + "\n\n"},
		{"late-error-header-without-data", "event: response.output_text.delta\ndata: " + prefix + "\ndata: " + terminal + "\nevent: error\n\n"},
		{"intervening-header-cannot-relabel-prefix", "event: wrong\ndata: " + prefix + "\nevent: response.output_text.delta\nevent: error\ndata: " + terminal + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.wire)
			}))
			defer server.Close()
			client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
			s, err := client.Stream(context.Background(), model.ChatRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			event, err := s.Recv()
			var appErr *apperr.Error
			if event.Type != "" || !errors.As(err, &appErr) || appErr.Code != "MODEL_STREAM_INVALID" || appErr.Retryable {
				t.Fatal("ambiguous channel newly retried or accepted", err)
			}
		})
	}
}

func decodedFaultForTest(data string, complete bool, eventType string) (eventPayload, []byte, bool) {
	prefix, suffix, split := strings.Cut(data, "\n")
	var first eventPayload
	_ = json.Unmarshal([]byte(prefix), &first)
	wire := "event: " + first.Type + "\ndata: " + prefix
	if split {
		wire += "\nevent: " + eventType + "\ndata: " + strings.ReplaceAll(suffix, "\n", "\ndata: ")
	}
	if complete {
		wire += "\n\n"
	}
	d := modelutil.NewSSEDecoder(strings.NewReader(wire), 1<<20)
	if !d.Scan() {
		return eventPayload{}, nil, false
	}
	prefix, priorType, terminal, ok := d.CoalescedErrorFields()
	if !ok {
		return eventPayload{}, nil, false
	}
	return coalescedTerminalFault(prefix, priorType, terminal)
}

func TestCoalescedNativeItemsCannotEmitTextToolsOrPrivateProviderState(t *testing.T) {
	terminal := `{"type":"error","code":"upstream_stream_read_error","message":"interrupted","param":null}`
	for _, tc := range []struct{ name, leading, prefix string }{
		{"reasoning", "", `{"sequence_number":100,"type":"response.output_item.done","item":{"id":"r1","type":"reasoning","status":"completed","summary":[],"encrypted_content":"PRIVATE_ENCRYPTED_STATE"}}`},
		{"message", "", `{"type":"response.output_item.done","item":{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"PRIVATE_TEXT"}]}}`},
		{"added-tool", "", `{"type":"response.output_item.added","item":{"id":"f1","type":"function_call","call_id":"c1","name":"fixture","arguments":""}}`},
		{"completed-tool-with-accumulator", "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"f1\",\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"fixture\",\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"f1\",\"delta\":\"{\\\"private\\\":true}\"}\n\n", `{"type":"response.output_item.done","item":{"id":"f1","type":"function_call","call_id":"c1","name":"fixture","arguments":"{\"private\":true}"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var first eventPayload
			if err := json.Unmarshal([]byte(tc.prefix), &first); err != nil {
				t.Fatal(err)
			}
			wire := tc.leading + "event: " + first.Type + "\ndata: " + tc.prefix + "\nevent: error\ndata: " + terminal + "\n\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, wire)
			}))
			defer server.Close()
			client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
			s, err := client.Stream(context.Background(), model.ChatRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			event, err := s.Recv()
			var appErr *apperr.Error
			wantCode, wantRetry := "MODEL_STREAM_INVALID", false
			if tc.name == "reasoning" {
				wantCode, wantRetry = "MODEL_UNAVAILABLE", true
			}
			if event.Type != "" || !errors.As(err, &appErr) || appErr.Code != wantCode || appErr.Retryable != wantRetry || strings.Contains(appErr.Details, "PRIVATE_") {
				t.Fatal("failed native item was accepted or wrongly classified", event.Type, err)
			}
			if event, err = s.Recv(); event.Type != "" || err == nil || (wantRetry && err != io.EOF) {
				t.Fatal("completed content escaped terminal error")
			}
		})
	}
	for _, prefix := range []string{
		`{"type":"response.output_item.done","item":null}`,
		`{"type":"response.output_item.done","item":{"type":"unknown"}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","type":"function_call"}}`,
		`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"refusal","refusal":"declined"}]}}`,
		`{"type":"response.output_item.done","item":{"type":"message","content":{}}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning"},"error":{"code":"invalid_api_key"}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning"},"sequence_number":{}}`,
		`{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"refusal","type":"output_text","text":"x"}]}}`,
		`{"type":"response.output_item.done","item":{"type":"message","Type":"reasoning","id":"r1","summary":[],"content":[{"type":"refusal","refusal":"x"}]}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"r1","summary":[{"type":"refusal","type":"summary_text","text":"x"}]}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"r1","summary":[{"Type":"summary_text","type":"summary_text","text":"x"}]}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"r1","summary":[],"error":{"code":"invalid_api_key"}}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"r1","summary":null}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","summary":[]}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"r1"}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"r1","summary":[],"status":"incomplete"}}`,
	} {
		if _, _, ok := decodedFaultForTest(prefix+"\n"+terminal, true, "error"); ok {
			t.Fatal("invalid, duplicate or refusal prefix broadened recovery")
		}
	}
}

func TestCoalescedTransportDoesNotOverridePreviouslyObservedRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.refusal.delta\",\"delta\":\"declined\"}\n\nevent: response.reasoning_text.delta\ndata: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"x\"}\nevent: error\ndata: {\"type\":\"error\",\"code\":\"upstream_stream_read_error\",\"message\":\"x\"}\n\n")
	}))
	defer server.Close()
	c := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	s, err := c.Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Recv(); err != nil {
		t.Fatal(err)
	}
	_, err = s.Recv()
	if err == nil || modelutil.IsRetryable(err) {
		t.Fatal("refusal became retryable", err)
	}
}

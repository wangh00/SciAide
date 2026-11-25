package modelutil

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type snapshotRoundTripper struct {
	call   func(*http.Request) (*http.Response, error)
	closed int
}

func (s *snapshotRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return s.call(r) }
func (s *snapshotRoundTripper) CloseIdleConnections()                             { s.closed++ }

type snapshotUnreadBody struct{}

func (snapshotUnreadBody) Read([]byte) (int, error) { panic("observer read response body") }
func (snapshotUnreadBody) Close() error             { return nil }

func snapshotRequest(t *testing.T, body, run, kind string, turn int) *http.Request {
	t.Helper()
	ctx := model.WithRequestTraceContext(context.Background(), model.RequestTraceContext{RunID: run, TurnIndex: turn, Kind: kind, WindowTokens: 24000, EffectiveTokens: 22800, AutoCompactTokens: 21600, WindowSource: "manual"})
	r, err := http.NewRequestWithContext(ctx, "POST", "https://PRIVATE_URL.invalid/PRIVATE_PATH", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer PRIVATE_KEY")
	r.Header.Set("X-Private", "PRIVATE_HEADER")
	return r
}

func TestRequestSnapshotsDefaultOffAndAdmissionBothOrders(t *testing.T) {
	var calls, events int
	base := &snapshotRoundTripper{call: func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: snapshotUnreadBody{}}, nil
	}}
	client := &http.Client{Transport: base}
	if got := WithRequestSnapshots(client, modelcap.ProtocolOpenAIResponses, nil); got != client {
		t.Fatal("nil observer changed client")
	}
	if off, err := NewRequestSnapshotObserver(nil); err != nil || off != nil {
		t.Fatal("nil callback enabled capture")
	}
	for _, order := range []string{"observer_first", "gate_first"} {
		o, err := NewRequestSnapshotObserver(func(DebugRequestSnapshot) { events++ })
		if err != nil {
			t.Fatal(err)
		}
		denial := errors.New("PRIVATE_GATE_DENIAL")
		gate := func(context.Context) error { return denial }
		var c *http.Client
		if order == "observer_first" {
			c = WithRequestGate(WithRequestSnapshots(client, modelcap.ProtocolOpenAIResponses, o), gate)
		} else {
			c = WithRequestSnapshots(WithRequestGate(client, gate), modelcap.ProtocolOpenAIResponses, o)
		}
		_, err = c.Transport.RoundTrip(snapshotRequest(t, `{"input":[]}`, "run", "conversation", 1))
		if !errors.Is(err, denial) || calls != 0 || events != 0 {
			t.Fatal("denied request was dispatched or observed", order, calls, events, err)
		}
		c.CloseIdleConnections()
	}
	if base.closed != 2 {
		t.Fatal("idle cleanup not forwarded")
	}
}

func TestRequestSnapshotsFinalWireInventoryDiffAndPrivacy(t *testing.T) {
	for _, protocol := range []modelcap.APIProtocol{modelcap.ProtocolOpenAIChat, modelcap.ProtocolOpenAIResponses, modelcap.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var records []DebugRequestSnapshot
			o, err := NewRequestSnapshotObserver(func(v DebugRequestSnapshot) { records = append(records, v) })
			if err != nil {
				t.Fatal(err)
			}
			var sent []string
			response := &http.Response{StatusCode: 503, Body: snapshotUnreadBody{}}
			base := &snapshotRoundTripper{call: func(r *http.Request) (*http.Response, error) {
				b, _ := io.ReadAll(r.Body)
				sent = append(sent, string(b))
				if r.Header.Get("Authorization") != "Bearer PRIVATE_KEY" || !strings.Contains(r.URL.String(), "PRIVATE_URL") {
					t.Fatal("transport request changed")
				}
				return response, nil
			}}
			c := WithRequestSnapshots(&http.Client{Transport: base}, protocol, o)
			key := "messages"
			if protocol == modelcap.ProtocolOpenAIResponses {
				key = "input"
			}
			input := `{"role":"user","content":"PRIVATE_TEXT 中文 🧪 \"quoted\""}`
			tools := `[{"type":"function","function":{"name":"PRIVATE_TOOL","parameters":{"type":"object","properties":{"PRIVATE_FIELD":{"type":"string"}}}}}]`
			body := `{"` + key + `":[` + input + `],"tools":` + tools + `,"stream":true,"tool_choice":"auto","reasoning_effort":"high","max_tokens":1234,"PRIVATE_SETTING":"PRIVATE_VALUE","instructions":"PRIVATE_INSTRUCTIONS"}`
			run := "12345678-1234-4234-8234-123456789abc"
			send := func(b string, turn int) {
				r := snapshotRequest(t, b, run, "conversation", turn)
				got, e := c.Transport.RoundTrip(r)
				if e != nil || got != response {
					t.Fatal("HTTP outcome replaced", e)
				}
			}
			send(body, 1)
			second := strings.Replace(body, input+`]`, input+`,{"type":"reasoning","encrypted_content":"PRIVATE_CIPHERTEXT","role":"assistant"}]`, 1)
			send(second, 2)
			send(second, 2)
			send(strings.Replace(second, `"high"`, `"low"`, 1), 3)
			if len(records) != 4 || len(sent) != 4 || sent[0] != body || sent[1] != second || records[0].BodyBytes != len(body) || records[0].DeclaredBodyBytes != int64(len(body)) {
				t.Fatal("wire bytes/count changed")
			}
			a, b, retry, settings := records[0], records[1], records[2], records[3]
			if a.Inspection != "complete" || a.Stage != "admitted_transport_return" || a.Outcome != "response_headers" || a.HTTPStatus != 503 || a.RunID != run || a.TurnIndex != 1 || a.InputCount != 1 || a.ToolCount != 1 || a.Tools[0].SchemaBytes == 0 || a.ConfiguredBudget == nil || a.ConfiguredBudget.EffectiveTokens != 22800 || a.ConfiguredBudget.WindowSource != "manual" || a.Delta != nil {
				t.Fatal("wrong final inventory", a)
			}
			if b.Delta == nil || b.Delta.PreviousSequence != 1 || b.Delta.CommonInputPrefix != 1 || b.Delta.InputCountDelta != 1 || b.Delta.CommonToolPrefix != 1 || b.Delta.SettingsChanged || b.Delta.SameWire || !b.Delta.CompleteInventory {
				t.Fatal("input growth comparison wrong", b.Delta)
			}
			if retry.Delta == nil || !retry.Delta.SameWire || retry.Delta.BodyByteDelta != 0 || retry.Delta.InputCountDelta != 0 {
				t.Fatal("identical retry was not visible")
			}
			if settings.Delta == nil || !settings.Delta.SettingsChanged || settings.Reasoning != "low" || settings.Delta.CommonInputPrefix != 2 {
				t.Fatal("wire setting negotiation not visible")
			}
			encoded, _ := json.Marshal(records)
			for _, private := range []string{"PRIVATE_", hex.EncodeToString(o.key[:]), body, second} {
				if strings.Contains(string(encoded), private) {
					t.Fatal("snapshot leaked source or key")
				}
			}
			if a.WireFingerprint == hex.EncodeToString(o.key[:]) {
				t.Fatal("key exposed as fingerprint")
			}
			other, _ := NewRequestSnapshotObserver(func(DebugRequestSnapshot) {})
			if other.fingerprint([]byte("wire:"+body)) == a.WireFingerprint || other.session == o.session {
				t.Fatal("fingerprints reusable across capture sessions")
			}
		})
	}
}

func TestRequestSnapshotsBoundsFailuresAndUnknownLabels(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		mutate           func(*http.Request)
	}{
		{"invalid", `{"input":`, "invalid_or_ambiguous_json", nil},
		{"duplicate_root", `{"input":[],"input":[]}`, "invalid_or_ambiguous_json", nil},
		{"trailing_json", `{"input":[]} {}`, "invalid_or_ambiguous_json", nil},
		{"missing_copy", `{"input":[]}`, "body_unavailable", func(r *http.Request) { r.GetBody = nil }},
		{"copy_failure", `{"input":[]}`, "body_unavailable", func(r *http.Request) {
			r.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("PRIVATE_READ_ERROR") }
		}},
		{"too_large", strings.Repeat("x", maxSnapshotBody+1), "body_limit", func(r *http.Request) {
			r.GetBody = func() (io.ReadCloser, error) { t.Fatal("oversize body copy opened"); return nil, nil }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var record DebugRequestSnapshot
			o, _ := NewRequestSnapshotObserver(func(v DebugRequestSnapshot) { record = v })
			original := errors.New("PRIVATE_TRANSPORT_ERROR")
			base := &snapshotRoundTripper{call: func(r *http.Request) (*http.Response, error) {
				_, _ = io.Copy(io.Discard, r.Body)
				return nil, original
			}}
			r := snapshotRequest(t, tc.body, "PRIVATE_RUN", "PRIVATE_KIND", 1)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			_, err := WithRequestSnapshots(&http.Client{Transport: base}, modelcap.ProtocolOpenAIResponses, o).Transport.RoundTrip(r)
			encoded, _ := json.Marshal(record)
			if err != original || record.Inspection != tc.want || record.Outcome != "transport_failure" || record.Delta != nil || strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatal("observation failure affected HTTP or leaked", record.Inspection, record.Outcome, err)
			}
		})
	}
	var records []DebugRequestSnapshot
	o, _ := NewRequestSnapshotObserver(func(v DebugRequestSnapshot) { records = append(records, v) })
	base := &snapshotRoundTripper{call: func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		return nil, context.Canceled
	}}
	c := WithRequestSnapshots(&http.Client{Transport: base}, modelcap.ProtocolOpenAIResponses, o)
	input := `{"role":"PRIVATE_ROLE","type":"PRIVATE_TYPE","content":"PRIVATE_TEXT"}`
	body := `{"input":[` + strings.TrimSuffix(strings.Repeat(input+",", maxSnapshotItems+3), ",") + `],"tools":[],"stream":"PRIVATE_STREAM","tool_choice":"PRIVATE_CHOICE","reasoning":{"effort":"PRIVATE_EFFORT"}}`
	for i := 1; i <= 2; i++ {
		_, _ = c.Transport.RoundTrip(snapshotRequest(t, body, "PRIVATE_RUN", "conversation", i))
	}
	last := records[1]
	if last.InputCount != maxSnapshotItems+3 || len(last.Inputs) != maxSnapshotItems || last.OmittedInputs != 3 || last.Outcome != "context_canceled" || last.Stream != nil || last.ToolChoice != "other" || last.Reasoning != "other" || last.Delta == nil || last.Delta.CompleteInventory {
		t.Fatal("bounded inventory or incomplete delta was mislabeled")
	}
	encoded, _ := json.Marshal(records)
	if strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("untrusted labels escaped")
	}
}

func TestRequestSnapshotsScopesOrderAndSinkCannotMutateBaseline(t *testing.T) {
	var records []DebugRequestSnapshot
	o, _ := NewRequestSnapshotObserver(func(v DebugRequestSnapshot) {
		records = append(records, v)
		if len(v.Inputs) > 0 {
			v.Inputs[0].Fingerprint = "MODIFIED_BY_SINK"
		}
	})
	base := &snapshotRoundTripper{call: func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		return &http.Response{StatusCode: 200, Body: snapshotUnreadBody{}}, nil
	}}
	c := WithRequestSnapshots(&http.Client{Transport: base}, modelcap.ProtocolOpenAIResponses, o)
	send := func(run, kind string, turn int) {
		_, _ = c.Transport.RoundTrip(snapshotRequest(t, `{"input":[{"role":"user","content":"x"}]}`, run, kind, turn))
	}
	send("a", "conversation", 2)
	send("b", "conversation", 2)
	send("a", "compaction", 2)
	send("a", "conversation", 1)
	send("a", "conversation", 3)
	if records[1].Delta != nil || records[2].Delta != nil || records[3].Comparison != "out_of_order" || records[4].Delta == nil || records[4].Delta.PreviousSequence != 1 || records[4].Delta.CommonInputPrefix != 1 {
		t.Fatal("baseline mixed runs/kinds, rolled back or was mutated")
	}
	for i := 0; i < maxSnapshotScopes+2; i++ {
		send(strings.Repeat("a", i+1), "conversation", 1)
	}
	if len(o.previous) > maxSnapshotScopes {
		t.Fatal("comparison cache is unbounded")
	}
	send("a", "conversation", 4)
	if records[len(records)-1].Delta != nil {
		t.Fatal("evicted baseline was invented")
	}
	panicObserver, _ := NewRequestSnapshotObserver(func(DebugRequestSnapshot) { panic("sink failed") })
	if _, err := WithRequestSnapshots(&http.Client{Transport: base}, modelcap.ProtocolOpenAIResponses, panicObserver).Transport.RoundTrip(snapshotRequest(t, `{}`, "a", "conversation", 1)); err != nil {
		t.Fatal("sink failure replaced HTTP result")
	}
}

func TestRequestSnapshotsActualHeaderTimeoutIsNotCallerDeadline(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport_header_timeout", true: "caller_deadline"}[callerDeadline], func(t *testing.T) {
			release := make(chan struct{})
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				entered <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			var record DebugRequestSnapshot
			o, _ := NewRequestSnapshotObserver(func(v DebugRequestSnapshot) { record = v })
			headerTimeout := 100 * time.Millisecond
			ctx := context.Background()
			cancel := func() {}
			want := "transport_timeout"
			if callerDeadline {
				headerTimeout = 5 * time.Second
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				want = "context_deadline"
			}
			defer cancel()
			client := WithRequestSnapshots(NewStreamingHTTPClient(headerTimeout), modelcap.ProtocolOpenAIResponses, o)
			defer client.CloseIdleConnections()
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(`{"input":[]}`))
			response, err := client.Do(req)
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil || record.Outcome != want || record.Inspection != "complete" || record.HTTPStatus != 0 {
				t.Fatal("transport timeout confused with caller deadline", record.Outcome, err)
			}
			select {
			case <-entered:
			default:
				t.Fatal("request did not reach controlled header wait")
			}
		})
	}
}

func TestRequestSnapshotsCustomCancelCauseRequiresMatchingTransportError(t *testing.T) {
	cause := errors.New("PRIVATE_HOST_CAUSE")
	unrelated := errors.New("PRIVATE_TRANSPORT_ERROR")
	for _, matching := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching_host_cause", false: "unrelated_fault"}[matching], func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(cause)
			returned, want := unrelated, "transport_failure"
			if matching {
				returned, want = cause, "context_canceled"
			}
			var record DebugRequestSnapshot
			o, _ := NewRequestSnapshotObserver(func(v DebugRequestSnapshot) { record = v })
			base := &snapshotRoundTripper{call: func(*http.Request) (*http.Response, error) { return nil, returned }}
			r := snapshotRequest(t, `{"input":[]}`, "synthetic", "conversation", 1).WithContext(ctx)
			_, err := WithRequestSnapshots(&http.Client{Transport: base}, modelcap.ProtocolOpenAIResponses, o).Transport.RoundTrip(r)
			encoded, _ := json.Marshal(record)
			if err != returned || record.Outcome != want || record.Inspection != "complete" || strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatal("host cause confused with unrelated transport failure", record.Outcome)
			}
		})
	}
}

func TestRequestSnapshotsConcurrentScopesAndUnsupportedLabels(t *testing.T) {
	var records []DebugRequestSnapshot
	o, _ := NewRequestSnapshotObserver(func(v DebugRequestSnapshot) { records = append(records, v) })
	base := &snapshotRoundTripper{call: func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		return &http.Response{StatusCode: 200, Body: snapshotUnreadBody{}}, nil
	}}
	c := WithRequestSnapshots(&http.Client{Transport: base}, modelcap.ProtocolOpenAIResponses, o)
	var wg sync.WaitGroup
	for i := 1; i <= 16; i++ {
		r := snapshotRequest(t, `{"input":[]}`, strings.Repeat("a", i), "conversation", 1)
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Transport.RoundTrip(r) }()
	}
	wg.Wait()
	if len(records) != 16 {
		t.Fatal("concurrent snapshots lost")
	}
	for i, v := range records {
		if v.Sequence != uint64(i+1) || v.Delta != nil {
			t.Fatal("concurrent unrelated scopes were merged")
		}
	}
	unknown := WithRequestSnapshots(&http.Client{Transport: base}, modelcap.APIProtocol("PRIVATE_PROTOCOL"), o)
	_, _ = unknown.Transport.RoundTrip(snapshotRequest(t, `{"input":[]}`, "run", "conversation", 1))
	v := records[len(records)-1]
	encoded, _ := json.Marshal(v)
	if v.Protocol != "unknown" || v.Inspection != "unsupported_protocol" || strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("unknown labels leaked or pretended to be supported")
	}
}

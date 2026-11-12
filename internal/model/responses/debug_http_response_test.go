package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type debugResponseTransportFixture struct {
	fn     func(*http.Request) (*http.Response, error)
	closed int
}

func (f *debugResponseTransportFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	return f.fn(r)
}
func (f *debugResponseTransportFixture) CloseIdleConnections() { f.closed++ }

type debugResponseBodyFixture struct {
	io.Reader
	reads  int
	closed bool
}

func (b *debugResponseBodyFixture) Read(p []byte) (int, error) { b.reads++; return b.Reader.Read(p) }
func (b *debugResponseBodyFixture) Close() error               { b.closed = true; return nil }

func TestDebugHTTPResponseDefaultOffAndTransportTransparency(t *testing.T) {
	c := New(modelprofile.Profile{}, nil)
	before := c.http
	c.SetDebugHTTPResponseObserver(nil)
	if c.http != before {
		t.Fatal("nil observer changed the default transport")
	}
	body := &debugResponseBodyFixture{Reader: strings.NewReader("untouched body")}
	response := &http.Response{StatusCode: 429, ProtoMajor: 1, ProtoMinor: 1, ContentLength: 14, Header: http.Header{"Retry-After": {"120"}}, Body: body}
	req, _ := http.NewRequest(http.MethodPost, "https://PRIVATE_REQUEST_URL", strings.NewReader("PRIVATE_REQUEST_BODY"))
	base := &debugResponseTransportFixture{fn: func(actual *http.Request) (*http.Response, error) {
		if actual != req {
			t.Fatal("request was replaced")
		}
		return response, nil
	}}
	c.http = &http.Client{Transport: base, Timeout: 17 * time.Second}
	calls := 0
	c.SetDebugHTTPResponseObserver(func(v DebugHTTPResponse) {
		calls++
		if body.reads != 0 || body.closed || v.HTTPStatus != 429 || v.Headers["Retry-After"] != "120" {
			t.Fatal("observation read body or lost headers")
		}
		v.Headers["Retry-After"] = "1"
	})
	actual, err := c.http.Transport.RoundTrip(req)
	if err != nil || actual != response || response.Header.Get("Retry-After") != "120" || calls != 1 || c.http.Timeout != 17*time.Second {
		t.Fatal("transport semantics changed")
	}
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	c.http.CloseIdleConnections()
	if string(data) != "untouched body" || !body.closed || base.closed != 1 {
		t.Fatal("body or idle-connection ownership changed")
	}
	sentinel := errors.New("original transport failure")
	base.fn = func(*http.Request) (*http.Response, error) { return nil, sentinel }
	actual, err = c.http.Transport.RoundTrip(req)
	if actual != nil || err != sentinel || calls != 1 {
		t.Fatal("transport failure was replaced or invented a response")
	}
}

func TestDebugHTTPResponseHeaderRedactionAllowlistAndBounds(t *testing.T) {
	c := New(modelprofile.Profile{CustomHeaders: map[string]string{"X-Tenant": "tenant/A+B", "Authorization": "Bearer OTHER_SENTINEL"}}, []byte("KEY_SENTINEL"))
	h := make(http.Header)
	h.Set("X-Request-Id", "KEY_SENTINEL tenant%2fA%2bB OTHER_SENTINEL")
	h.Set("Retry-After", "3600")
	h.Set("Server", "gateway https://user:pass@private.test/path?token=opaque")
	h.Set("Via", "Bearer UNKNOWN_CREDENTIAL")
	h.Set("X-Correlation-Id", "token=UNKNOWN_TOKEN")
	h.Set("Cf-Ray", strings.Repeat("中", 300))
	h.Set("Openai-Request-Id", strings.Repeat("b", 1024))
	h.Set("Traceparent", strings.Repeat("x", 1020)+"KEY_SENTINEL")
	for _, name := range []string{"Set-Cookie", "Authorization", "Proxy-Authenticate", "Location", "X-Other-Private"} {
		h.Set(name, "DO_NOT_CAPTURE")
	}
	var event DebugHTTPResponse
	c.http = &http.Client{Transport: &debugResponseTransportFixture{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 502, Header: h, Body: http.NoBody}, nil
	}}}
	c.SetDebugHTTPResponseObserver(func(v DebugHTTPResponse) { event = v })
	req, _ := http.NewRequest(http.MethodGet, "https://PRIVATE_REQUEST_URL", nil)
	_, _ = c.http.Transport.RoundTrip(req)
	encoded, _ := json.Marshal(event)
	for _, denied := range []string{"KEY_SENTINEL", "OTHER_SENTINEL", "tenant%2fA%2bB", "UNKNOWN_CREDENTIAL", "UNKNOWN_TOKEN", "DO_NOT_CAPTURE", "PRIVATE_REQUEST_URL", "private.test", "user:pass"} {
		if strings.Contains(string(encoded), denied) {
			t.Fatalf("private value leaked: %s", denied)
		}
	}
	if event.Headers["Retry-After"] != "3600" || event.Headers["X-Request-Id"] != "[REDACTED] [REDACTED] [REDACTED]" {
		t.Fatal("redaction or uncapped Retry-After evidence changed", event.Headers)
	}
	if len(event.OmittedHeaders) != 1 || event.OmittedHeaders[0] != "Traceparent" || event.Headers["Traceparent"] != "" {
		t.Fatal("oversized header was prefix-truncated before redaction")
	}
	if !strings.HasSuffix(event.Headers["Openai-Request-Id"], "[TRUNCATED]") {
		t.Fatal("exact 1024-byte header was incorrectly omitted")
	}
	if !strings.HasSuffix(event.Headers["Cf-Ray"], "[TRUNCATED]") || len([]rune(event.Headers["Cf-Ray"])) > 267 || len(encoded) > 8192 {
		t.Fatal("unbounded header evidence")
	}
}

func TestDebugHTTPResponsePreservesNativeRequestsStatusAndRetryAfter(t *testing.T) {
	for _, status := range []int{502, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(data))
				w.Header().Set("X-Request-Id", "PRIVATE_RESPONSE_ID")
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"fixture rejection"}}`)
			}))
			defer server.Close()
			for _, enabled := range []bool{false, true} {
				c := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
				var admissions, observations int
				if enabled {
					c.SetDebugHTTPResponseObserver(func(e DebugHTTPResponse) {
						observations++
						if e.Headers["Retry-After"] != "3" {
							t.Error("missing Retry-After")
						}
						e.Headers["Retry-After"] = "1"
					})
				}
				c.SetRequestGate(func(context.Context) error { admissions++; return nil })
				_, err := c.Stream(context.Background(), model.ChatRequest{RequestedReasoningLevel: modelcap.ReasoningHigh, ResolvedReasoningLevel: modelcap.ReasoningHigh, Messages: []model.Message{{Role: model.RoleUser, Content: "Reply only with: CHAT_OK"}}})
				var public *apperr.Error
				if !errors.As(err, &public) || public.HTTPStatus != status || public.RetryAfter != 3*time.Second || admissions != 1 {
					t.Fatal("status, retry delay or admission count changed", err)
				}
				if (enabled && observations != 1) || (!enabled && observations != 0) || strings.Contains(public.Details, "PRIVATE_RESPONSE_ID") {
					t.Fatal("private capture escaped or changed default")
				}
			}
			if len(bodies) != 2 || bodies[0] != bodies[1] {
				t.Fatal("enabling observation changed the native HTTP payload or request count")
			}
		})
	}
}

func TestDebugHTTPResponseRedirectGateAndSuccessfulStream(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.Header().Set("X-Request-Id", "req-fixture")
		if r.URL.Path != "/final" {
			w.Header().Set("Location", "/final")
			w.WriteHeader(307)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"CHAT_OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	for _, cap := range []int{1, 2} {
		received.Store(0)
		c := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
		var statuses []int
		c.SetDebugHTTPResponseObserver(func(e DebugHTTPResponse) { statuses = append(statuses, e.HTTPStatus) })
		admissions := 0
		denial := errors.New("fixture admission denied")
		c.SetRequestGate(func(context.Context) error {
			admissions++
			if admissions > cap {
				return denial
			}
			return nil
		})
		stream, err := c.Stream(context.Background(), model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "fixture"}}})
		if cap == 1 {
			if !errors.Is(err, denial) || received.Load() != 1 || len(statuses) != 1 || statuses[0] != 307 {
				t.Fatal("redirect admission or response observations changed", err, statuses)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		done := false
		for {
			e, eErr := stream.Recv()
			if eErr == io.EOF {
				break
			}
			if eErr != nil {
				t.Fatal(eErr)
			}
			if e.Type == model.EventTextDelta {
				text.WriteString(e.Text)
			}
			if e.Type == model.EventDone {
				done = true
			}
		}
		_ = stream.Close()
		if received.Load() != 2 || admissions != 2 || len(statuses) != 2 || statuses[0] != 307 || statuses[1] != 200 || text.String() != "CHAT_OK" || !done {
			t.Fatal("stream was consumed, duplicated or not gated", statuses, text.String(), done)
		}
	}
}

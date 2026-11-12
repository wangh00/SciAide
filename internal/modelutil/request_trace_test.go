package modelutil

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/browserhttp"
)

func TestNetworkTimeoutDiagnosisPreservesHeaderWait(t *testing.T) {
	const body = `{"input":"PRIVATE_SENTINEL"}`
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		received <- string(data)
		time.Sleep(120 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewStreamingHTTPClient(40 * time.Millisecond)
	defer client.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/PRIVATE_SENTINEL", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer PRIVATE_SENTINEL")
	_, err := client.Do(req)
	var timeout interface{ Timeout() bool }
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatal("fixture did not reach response-start timeout", err)
	}
	if got := <-received; got != body {
		t.Fatal("request body changed")
	}
	var classified *apperr.Error
	if !errors.As(ClassifyNetwork(err), &classified) || classified.Code != "MODEL_TIMEOUT" || !classified.Retryable || classified.HTTPStatus != 0 {
		t.Fatal("network classification changed")
	}
	if !strings.Contains(classified.Details, `"networkPhase": "request_flush_or_response_headers"`) {
		t.Fatal("response-header wait is erased from the diagnostic", classified.Details)
	}
	if strings.Contains(classified.Details, "PRIVATE_SENTINEL") {
		t.Fatal("network trace exposed request data")
	}
}

type traceRoundTripper func(*http.Request) (*http.Response, error)

func (f traceRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNetworkTraceMilestonesAndCallerHooks(t *testing.T) {
	for _, phase := range []string{"request_start", "connection_acquire", "dns", "connect", "tls_handshake", "request_write", "request_flush_or_response_headers", "response_started"} {
		t.Run(phase, func(t *testing.T) {
			oldHookCalls := 0
			prior := &httptrace.ClientTrace{GetConn: func(string) { oldHookCalls++ }}
			ctx := httptrace.WithClientTrace(context.Background(), prior)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://PRIVATE_SENTINEL/path?key=PRIVATE_SENTINEL", strings.NewReader("PRIVATE_SENTINEL"))
			req.Header.Set("Authorization", "Bearer PRIVATE_SENTINEL")
			base := traceRoundTripper(func(r *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(r.Context())
				if phase != "request_start" {
					trace.GetConn("PRIVATE_SENTINEL")
				}
				switch phase {
				case "dns":
					trace.DNSStart(httptrace.DNSStartInfo{Host: "PRIVATE_SENTINEL"})
					trace.DNSDone(httptrace.DNSDoneInfo{Err: context.DeadlineExceeded})
				case "connect":
					trace.ConnectStart("tcp", "PRIVATE_SENTINEL")
					trace.ConnectDone("tcp", "PRIVATE_SENTINEL", context.DeadlineExceeded)
				case "tls_handshake":
					trace.TLSHandshakeStart()
					trace.TLSHandshakeDone(tls.ConnectionState{}, context.DeadlineExceeded)
				case "request_write", "request_flush_or_response_headers", "response_started":
					trace.GotConn(httptrace.GotConnInfo{Reused: true})
					writeErr := error(context.DeadlineExceeded)
					if phase != "request_write" {
						writeErr = nil
					}
					trace.WroteRequest(httptrace.WroteRequestInfo{Err: writeErr})
					if phase == "response_started" {
						trace.GotFirstResponseByte()
					}
				}
				return nil, context.DeadlineExceeded
			})
			_, err := (tracedModelTransport{base: base, timeout: time.Minute}).RoundTrip(req)
			trace, ok := HTTPFailureTrace(err)
			if !ok || trace.NetworkPhase != phase || trace.ResponseStartTimeoutMillis != 60000 || trace.RequestBytes != 16 || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("milestone or error identity lost", trace, err)
			}
			wantHooks := 1
			if phase == "request_start" {
				wantHooks = 0
			}
			if oldHookCalls != wantHooks || httptrace.ContextClientTrace(req.Context()) != prior {
				t.Fatal("caller hooks overwritten or original request mutated")
			}
			data, _ := json.Marshal(trace)
			if strings.Contains(string(data), "PRIVATE_SENTINEL") {
				t.Fatal("trace exposed request data")
			}
		})
	}
}

func TestNetworkTraceHTTP2HeaderWaitAndConnectionReuse(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wait" {
			time.Sleep(400 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	base := server.Client().Transport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = 150 * time.Millisecond
	client := &http.Client{Transport: tracedModelTransport{base: browserhttp.NewFixed(base), timeout: 150 * time.Millisecond}}
	defer client.CloseIdleConnections()
	first, err := client.Get(server.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, first.Body)
	first.Body.Close()
	if first.ProtoMajor != 2 {
		t.Fatal("not actual HTTP/2", first.Proto)
	}
	_, err = client.Get(server.URL + "/wait")
	trace, ok := HTTPFailureTrace(err)
	if !ok || trace.NetworkPhase != "request_flush_or_response_headers" || !trace.ConnectionReused || trace.ConnectionAcquiredMillis == nil || trace.RequestWriteCallbackMillis == nil || trace.FirstResponseByteMillis != nil {
		t.Fatal("HTTP2/reused-connection milestones missing", trace, err)
	}
}

func TestNetworkTraceKeepsCancellationAndAdmissionSemantics(t *testing.T) {
	client := NewStreamingHTTPClient(time.Second)
	defer client.CloseIdleConnections()
	denial := &apperr.Error{Code: "DEBUG_MODEL_BUDGET_EXHAUSTED", UserMessage: "fixture gate"}
	client = WithRequestGate(client, func(context.Context) error { return denial })
	req, _ := http.NewRequest(http.MethodPost, "https://PRIVATE_SENTINEL", nil)
	_, err := client.Do(req)
	if ClassifyNetwork(err) != denial {
		t.Fatal("admission error reclassified")
	}
	if _, ok := HTTPFailureTrace(err); ok {
		t.Fatal("admission denial pretended to reach transport")
	}
	_, err = (tracedModelTransport{base: traceRoundTripper(func(*http.Request) (*http.Response, error) { return nil, context.Canceled })}).RoundTrip(req)
	if !errors.Is(ClassifyNetwork(err), context.Canceled) || IsRetryable(ClassifyNetwork(err)) {
		t.Fatal("cancellation changed")
	}
}

type traceTemporaryError struct{}

func (traceTemporaryError) Error() string   { return "fixture temporary timeout" }
func (traceTemporaryError) Timeout() bool   { return true }
func (traceTemporaryError) Temporary() bool { return true }

func TestNetworkTracePreservesURLTimeoutInterfaces(t *testing.T) {
	for _, cause := range []error{traceTemporaryError{}, errors.Join(traceTemporaryError{}), context.Canceled} {
		req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
		_, err := (tracedModelTransport{base: traceRoundTripper(func(*http.Request) (*http.Response, error) { return nil, cause })}).RoundTrip(req)
		before, after := &url.Error{Err: cause}, &url.Error{Err: err}
		if before.Timeout() != after.Timeout() || before.Temporary() != after.Temporary() || !errors.Is(err, cause) {
			t.Fatal("url.Error timeout/temporary/unwrap semantics changed")
		}
	}
}

func TestNetworkTraceWriteHookIsNotProofOfFinalFlush(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
	var callback *httptrace.ClientTrace
	_, err := (tracedModelTransport{base: traceRoundTripper(func(r *http.Request) (*http.Response, error) {
		callback = httptrace.ContextClientTrace(r.Context())
		callback.GetConn("PRIVATE_SENTINEL")
		callback.ConnectStart("tcp", "PRIVATE_SENTINEL")
		callback.ConnectStart("tcp", "PRIVATE_SENTINEL") // one acquisition, two possible dials
		callback.GotConn(httptrace.GotConnInfo{})
		callback.WroteRequest(httptrace.WroteRequestInfo{})
		return nil, errors.New("PRIVATE_SENTINEL final flush failed")
	})}).RoundTrip(req)
	trace, _ := HTTPFailureTrace(err)
	if trace.NetworkPhase != "request_flush_or_response_headers" || trace.ConnectionAcquisitions != 1 {
		t.Fatal("invented response wait or TCP attempt count", trace)
	}
	// Late hooks must not change the already-frozen error snapshot.
	callback.GotFirstResponseByte()
	after, _ := HTTPFailureTrace(err)
	if after.FirstResponseByteMillis != nil || after.NetworkPhase != trace.NetworkPhase {
		t.Fatal("late callback changed frozen error")
	}
	if strings.Contains(apperr.Public(ClassifyNetwork(err)).Details, "PRIVATE_SENTINEL") {
		t.Fatal("flush failure exposed private transport text")
	}
}

func TestNetworkTLSFailureOmitsURLFromPublicDetails(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	client := NewStreamingHTTPClient(time.Second)
	defer client.CloseIdleConnections()
	_, err := client.Get(server.URL + "/PRIVATE_SENTINEL?api_key=PRIVATE_SENTINEL")
	var classified *apperr.Error
	if !errors.As(ClassifyNetwork(err), &classified) || classified.Code != "MODEL_TLS_INVALID" || classified.Retryable || strings.Contains(classified.Details, "PRIVATE_SENTINEL") {
		t.Fatal("TLS failure was retried or public details exposed URL")
	}
	trace, ok := HTTPFailureTrace(classified)
	if !ok || trace.NetworkPhase != "tls_handshake" || trace.RequestWriteCallbackMillis != nil {
		t.Fatal("failed TLS incorrectly claimed a sent request", trace)
	}
}

func TestNetworkTraceDoesNotBlameLateParallelDialLoser(t *testing.T) {
	for _, laterTLSFailure := range []bool{false, true} {
		req, _ := http.NewRequest(http.MethodGet, "https://fixture.invalid", nil)
		_, err := (tracedModelTransport{base: traceRoundTripper(func(r *http.Request) (*http.Response, error) {
			trace := httptrace.ContextClientTrace(r.Context())
			trace.GetConn("fixture.invalid")
			trace.ConnectStart("tcp", "IPv6")
			trace.ConnectStart("tcp", "IPv4")
			trace.ConnectDone("tcp", "IPv4", nil)
			trace.TLSHandshakeStart()
			trace.TLSHandshakeDone(tls.ConnectionState{}, nil)
			trace.ConnectDone("tcp", "IPv6", context.Canceled)
			if laterTLSFailure {
				trace.TLSHandshakeStart()
				trace.TLSHandshakeDone(tls.ConnectionState{}, context.DeadlineExceeded)
			}
			return nil, context.DeadlineExceeded // later proxy CONNECT or nested TLS failure
		})}).RoundTrip(req)
		trace, _ := HTTPFailureTrace(err)
		if trace.NetworkPhase != "connection_acquire" || trace.ConnectionAcquiredMillis != nil {
			t.Fatal("late failed dial undid successful connection or guessed nested failure", trace)
		}
	}
}

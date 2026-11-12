package modelutil

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/apperr"
)

// HTTPRequestTrace contains only host-observed milestones, never addresses,
// headers, body data, TLS state or provider text. Times are relative to the
// transport attempt, not claims about time spent generating model output.
type HTTPRequestTrace struct {
	NetworkPhase               string `json:"networkPhase"`
	ResponseStartTimeoutMillis int64  `json:"responseStartTimeoutMillis"`
	ElapsedMillis              int64  `json:"elapsedMillis"`
	RequestBytes               int64  `json:"requestBytes"`
	ConnectionAcquisitions     int    `json:"connectionAcquisitions"`
	ConnectionReused           bool   `json:"connectionReused"`
	ConnectionAcquiredMillis   *int64 `json:"connectionAcquiredMillis,omitempty"`
	RequestWriteCallbackMillis *int64 `json:"requestWriteCallbackMillis,omitempty"`
	FirstResponseByteMillis    *int64 `json:"firstResponseByteMillis,omitempty"`
}

type httpRequestFailure struct {
	cause error
	trace HTTPRequestTrace
}

func (e *httpRequestFailure) Error() string {
	return "model HTTP request failed during " + e.trace.NetworkPhase
}
func (e *httpRequestFailure) Unwrap() error { return e.cause }
func (e *httpRequestFailure) Timeout() bool {
	timeout, ok := e.cause.(interface{ Timeout() bool })
	return ok && timeout.Timeout()
}

func (e *httpRequestFailure) Temporary() bool {
	temporary, ok := e.cause.(interface{ Temporary() bool })
	return ok && temporary.Temporary()
}

// HTTPFailureTrace does not parse diagnostics or accept provider-claimed
// phases. The private wrapper is created only by our actual HTTP transport.
func HTTPFailureTrace(err error) (HTTPRequestTrace, bool) {
	var failure *httpRequestFailure
	if !errors.As(err, &failure) {
		return HTTPRequestTrace{}, false
	}
	return failure.trace, true
}

type tracedModelTransport struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (t tracedModelTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t tracedModelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	var mu sync.Mutex
	state := HTTPRequestTrace{NetworkPhase: "request_start", ResponseStartTimeoutMillis: t.timeout.Milliseconds(), RequestBytes: req.ContentLength}
	var dnsStarted, connectStarted, tlsStarted bool
	var dnsSucceeded, connectSucceeded, tlsSucceeded bool
	update := func(f func()) { mu.Lock(); defer mu.Unlock(); f() }
	elapsed := func() *int64 { value := time.Since(started).Milliseconds(); return &value }
	trace := &httptrace.ClientTrace{
		GetConn: func(string) {
			update(func() {
				state.ConnectionAcquisitions++
				state.ConnectionAcquiredMillis, state.RequestWriteCallbackMillis, state.FirstResponseByteMillis = nil, nil, nil
				state.ConnectionReused = false
				dnsStarted, connectStarted, tlsStarted = false, false, false
				dnsSucceeded, connectSucceeded, tlsSucceeded = false, false, false
			})
		},
		DNSStart:          func(httptrace.DNSStartInfo) { update(func() { dnsStarted = true }) },
		DNSDone:           func(info httptrace.DNSDoneInfo) { update(func() { dnsSucceeded = dnsSucceeded || info.Err == nil }) },
		ConnectStart:      func(string, string) { update(func() { connectStarted = true }) },
		ConnectDone:       func(_, _ string, err error) { update(func() { connectSucceeded = connectSucceeded || err == nil }) },
		TLSHandshakeStart: func() { update(func() { tlsStarted = true }) },
		TLSHandshakeDone:  func(_ tls.ConnectionState, err error) { update(func() { tlsSucceeded = tlsSucceeded || err == nil }) },
		GotConn: func(info httptrace.GotConnInfo) {
			update(func() { state.ConnectionAcquiredMillis, state.ConnectionReused = elapsed(), info.Reused })
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				update(func() { state.RequestWriteCallbackMillis = elapsed() })
			}
		},
		GotFirstResponseByte: func() { update(func() { state.FirstResponseByteMillis = elapsed() }) },
	}
	// WithClientTrace composes with a caller's existing hooks. Body/URL/headers
	// and timeout, proxy, TLS, admission and retry policy are not changed.
	response, err := t.base.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err == nil {
		return response, nil
	}
	mu.Lock()
	state.ElapsedMillis = time.Since(started).Milliseconds()
	switch {
	case state.FirstResponseByteMillis != nil:
		state.NetworkPhase = "response_started"
	case state.RequestWriteCallbackMillis != nil:
		// Some transports call WroteRequest BEFORE their final buffered Flush.
		// A successful hook is not proof of network delivery or header wait.
		state.NetworkPhase = "request_flush_or_response_headers"
	case state.ConnectionAcquiredMillis != nil:
		state.NetworkPhase = "request_write"
	case tlsStarted && !tlsSucceeded:
		state.NetworkPhase = "tls_handshake"
	case dnsStarted && !dnsSucceeded && !connectSucceeded && !tlsStarted:
		state.NetworkPhase = "dns"
	case connectStarted && !connectSucceeded && !tlsStarted:
		state.NetworkPhase = "connect"
	case state.ConnectionAcquisitions > 0:
		// Includes pool/HTTP2/proxy acquisition; do not invent a more specific
		// phase when the transport did not expose a corresponding milestone.
		// Parallel dial losers may complete AFTER a winner. A late failure
		// cannot undo an observed success or diagnose a later proxy failure.
		// Multiple TLS layers after a success are likewise conservative here.
		state.NetworkPhase = "connection_acquire"
	}
	frozen := state // callbacks may finish after RoundTrip returns
	mu.Unlock()
	return response, &httpRequestFailure{cause: err, trace: frozen}
}

func withHTTPFailureTrace(classified, original error) error {
	trace, ok := HTTPFailureTrace(original)
	var public *apperr.Error
	if !ok || !errors.As(classified, &public) {
		return classified
	}
	copy := *public
	data, _ := json.Marshal(trace)
	copy.Details = boundProviderDetails(copy.Details + "\n" + ProviderErrorDetails("host network request", 0, data))
	return &copy
}

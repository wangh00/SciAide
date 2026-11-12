package modelutil

import (
	"context"
	"net/http"
)

type modelRequestGateError struct{ cause error }

func (e *modelRequestGateError) Error() string { return "local model request admission denied" }
func (e *modelRequestGateError) Unwrap() error { return e.cause }

type gatedTransport struct {
	base http.RoundTripper
	gate func(context.Context) error
}

func (t gatedTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t gatedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.gate(r.Context()); err != nil {
		return nil, &modelRequestGateError{cause: err}
	}
	return t.base.RoundTrip(r)
}

// Counts each transport attempt, including in-Stream negotiation/redirects.
// Nil preserves the existing client and transport exactly.
func WithRequestGate(client *http.Client, gate func(context.Context) error) *http.Client {
	if gate == nil {
		return client
	}
	copy := *client
	base := copy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copy.Transport = gatedTransport{base: base, gate: gate}
	return &copy
}

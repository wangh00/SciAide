package mcp

import (
	"context"
	"io"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The SDK detaches its connection context and may send a cancellation notice
// while initialization is failing. Cancel those HTTP requests too, rather than
// waiting for its separate notification timeout during application shutdown.
// Once discovery succeeds, the initialization deadline MUST NOT kill a session.
func bindHTTPStartup(ctx context.Context, transport mcpsdk.Transport) func(bool) {
	t, ok := transport.(*mcpsdk.StreamableClientTransport)
	if !ok || t.HTTPClient == nil {
		return func(bool) {}
	}
	lifetime, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(ctx, cancel)
	client := *t.HTTPClient
	client.Transport = startupHTTPTransport{base: client.Transport, abort: lifetime}
	t.HTTPClient = &client
	return func(success bool) {
		stop()
		if !success {
			cancel()
		}
	}
}

type startupHTTPTransport struct {
	base  http.RoundTripper
	abort context.Context
}

func (t startupHTTPTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(t.abort, cancel)
	cleanup := func() { stop(); cancel() }
	if t.abort.Err() != nil {
		cleanup()
		return nil, t.abort.Err()
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	res, err := base.RoundTrip(r.Clone(ctx))
	if err != nil {
		cleanup()
		return nil, err
	}
	res.Body = &startupHTTPBody{ReadCloser: res.Body, cleanup: cleanup}
	return res, nil
}

type startupHTTPBody struct {
	io.ReadCloser
	cleanup func()
}

func (b *startupHTTPBody) Close() error { defer b.cleanup(); return b.ReadCloser.Close() }

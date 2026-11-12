package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSuccessfulHTTPStartupOutlivesInitializeContext(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("alive")) }))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	transport := &mcpsdk.StreamableClientTransport{HTTPClient: s.Client()}
	finish := bindHTTPStartup(ctx, transport)
	finish(true)
	cancel()
	r, err := transport.HTTPClient.Get(s.URL)
	if err != nil {
		t.Fatal("successful session killed by init cancellation", err)
	}
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil || string(b) != "alive" {
		t.Fatal(string(b), err)
	}
}

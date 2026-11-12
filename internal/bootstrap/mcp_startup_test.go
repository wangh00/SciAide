package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wangh00/SciAide/internal/app/mcpserver"
)

func TestDesktopMCPStartupConnectsOnceAndPreservesManualDisconnect(t *testing.T) {
	var calls atomic.Int32
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fixture", Version: "1"}, nil)
	h := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return s }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); h.ServeHTTP(w, r) }))
	defer httpServer.Close()
	root := t.TempDir()
	a, err := New(Options{RootDir: root, ConnectMCPOnStartup: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// Replace endpoint in isolated fixture only; the production preset is untouched.
	v, err := a.mcpService.Get(context.Background(), mcpserver.DefaultExaID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.mcpService.Save(context.Background(), mcpserver.SaveCommand{ID: v.ID, Name: v.Name, Namespace: v.Namespace, Transport: v.Transport, URL: httpServer.URL, Enabled: true, AutoStart: true, Trust: mcpserver.TrustUser})
	if err != nil {
		t.Fatal(err)
	}
	a.Startup(context.Background())
	select {
	case <-a.startupDone:
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not complete")
	}
	if !a.mcp.Connected(v.ID) || calls.Load() == 0 {
		t.Fatal("not connected")
	}
	if _, err = a.mcpService.Disconnect(context.Background(), v.ID); err != nil {
		t.Fatal(err)
	}
	n := calls.Load()
	a.Startup(context.Background())
	if calls.Load() != n || a.mcp.Connected(v.ID) {
		t.Fatal("manual disconnect undone")
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.Startup(context.Background())
	if b.mcp.Connected(v.ID) || calls.Load() != n {
		t.Fatal("fixture construction unexpectedly networked")
	}
}

func TestDesktopCloseCancelsBlockedMCPStartup(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer s.Close()
	defer close(release)
	a, err := New(Options{RootDir: t.TempDir(), ConnectMCPOnStartup: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	v, err := a.mcpService.Get(context.Background(), mcpserver.DefaultExaID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.mcpService.Save(context.Background(), mcpserver.SaveCommand{ID: v.ID, Name: v.Name, Namespace: v.Namespace, Transport: v.Transport, URL: s.URL, Enabled: true, AutoStart: true, Trust: mcpserver.TrustUser})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	a.Startup(context.Background())
	if time.Since(start) > time.Second {
		t.Fatal("startup blocked the UI")
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP did not initialize")
	}
	done := make(chan error, 1)
	go func() { done <- a.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited for remote timeout")
	}
}

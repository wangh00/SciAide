package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wangh00/SciAide/internal/app/mcpserver"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestCloneSnapshotPreservesEmptyCollections(t *testing.T) {
	snapshot := cloneSnapshot(mcpserver.CapabilitySnapshot{
		Tools:     []mcpserver.ToolInfo{},
		Resources: []string{},
		Prompts:   []string{},
	})
	if snapshot.Tools == nil || snapshot.Resources == nil || snapshot.Prompts == nil {
		t.Fatalf("cloneSnapshot() returned nil collections: %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if bytes.Contains(encoded, []byte("null")) {
		t.Fatalf("cloneSnapshot() encoded null collections: %s", encoded)
	}
}

func TestManagerDiscoversAndInvokesThroughRegistry(t *testing.T) {
	ctx := context.Background()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fixture", Version: "1.2.3"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "paper/search", Description: "search papers", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "found"}}}, nil
	})
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	registry := tool.NewRegistry()
	manager := NewManager(registry, nil)
	manager.transportFactory = func(mcpserver.Server, map[string]string) (mcpsdk.Transport, error) { return clientTransport, nil }
	configured := mcpserver.Server{ID: "fixture", Namespace: "papers", Transport: mcpserver.TransportStdio, TimeoutSeconds: 10}
	snapshot, err := manager.Connect(ctx, configured)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if snapshot.ProtocolVersion == "" || snapshot.ServerVersion != "1.2.3" || len(snapshot.Tools) != 1 || snapshot.Tools[0].QualifiedName != "mcp.papers.paper_search" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	implementation, err := registry.Resolve(ctx, "mcp.papers.paper_search")
	if err != nil {
		t.Fatal(err)
	}
	result, err := implementation.Invoke(ctx, tool.Invocation{Arguments: json.RawMessage(`{}`)})
	if err != nil || result.Text != "found" || result.Status != tool.ResultSuccess {
		t.Fatalf("Invoke() = %#v, %v", result, err)
	}
	if err := manager.Disconnect(ctx, configured.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(ctx, "mcp.papers.paper_search"); err == nil {
		t.Fatal("MCP tool remained registered after disconnect")
	}
}

func TestManagerSerializesCallsWithinOneServerSession(t *testing.T) {
	ctx := context.Background()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "serial", Version: "1"}, nil)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var active, maximum atomic.Int32
	server.AddTool(&mcpsdk.Tool{Name: "browser", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		current := active.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		defer active.Add(-1)
		entered <- struct{}{}
		<-release
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
	})
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	registry := tool.NewRegistry()
	manager := NewManager(registry, nil)
	manager.transportFactory = func(mcpserver.Server, map[string]string) (mcpsdk.Transport, error) { return clientTransport, nil }
	if _, err := manager.Connect(ctx, mcpserver.Server{ID: "serial", Namespace: "serial", Transport: mcpserver.TransportStdio, TimeoutSeconds: 10}); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	implementation, err := registry.Resolve(ctx, "mcp.serial.browser")
	if err != nil {
		t.Fatal(err)
	}
	var calls sync.WaitGroup
	errs := make(chan error, 2)
	for index := range 2 {
		calls.Add(1)
		go func(index int) {
			defer calls.Done()
			_, invokeErr := implementation.Invoke(ctx, tool.Invocation{CallID: string(rune('a' + index)), Arguments: json.RawMessage(`{}`)})
			errs <- invokeErr
		}(index)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first MCP call did not enter the server")
	}
	select {
	case <-entered:
		t.Fatal("second MCP call entered the same server concurrently")
	case <-time.After(80 * time.Millisecond):
	}
	close(release)
	calls.Wait()
	close(errs)
	for invokeErr := range errs {
		if invokeErr != nil {
			t.Fatalf("Invoke() error = %v", invokeErr)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent server calls = %d", maximum.Load())
	}
}

func TestCancelledStdioCallRetiresItsSession(t *testing.T) {
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "cancel", Version: "1"}, nil)
	started := make(chan struct{})
	server.AddTool(&mcpsdk.Tool{Name: "blocked", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	registry := tool.NewRegistry()
	manager := NewManager(registry, nil)
	var connects atomic.Int32
	manager.transportFactory = func(mcpserver.Server, map[string]string) (mcpsdk.Transport, error) {
		if connects.Add(1) == 1 {
			return clientTransport, nil
		}
		return nil, errors.New("fixture refuses replacement connection")
	}
	configured := mcpserver.Server{ID: "cancel", Namespace: "cancel", Transport: mcpserver.TransportStdio, TimeoutSeconds: 10}
	if _, err := manager.Connect(context.Background(), configured); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	implementation, err := registry.Resolve(context.Background(), "mcp.cancel.blocked")
	if err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithCancel(context.Background())
	invoked := make(chan error, 1)
	go func() {
		_, invokeErr := implementation.Invoke(callCtx, tool.Invocation{CallID: "cancel-call", Arguments: json.RawMessage(`{}`)})
		invoked <- invokeErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("MCP call did not start")
	}
	cancel()
	select {
	case invokeErr := <-invoked:
		if invokeErr == nil || !errors.Is(invokeErr, context.Canceled) {
			t.Fatalf("cancelled Invoke() error = %v", invokeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled MCP call did not return")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, resolveErr := registry.Resolve(context.Background(), "mcp.cancel.blocked")
		if !manager.Connected(configured.ID) && resolveErr != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cancelled MCP session remained reusable")
}

func TestStdioCallUsesConfiguredServerTimeout(t *testing.T) {
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "timeout", Version: "1"}, nil)
	started := make(chan struct{})
	server.AddTool(&mcpsdk.Tool{Name: "blocked", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	registry := tool.NewRegistry()
	manager := NewManager(registry, nil)
	manager.transportFactory = func(mcpserver.Server, map[string]string) (mcpsdk.Transport, error) {
		return clientTransport, nil
	}
	configured := mcpserver.Server{ID: "timeout", Namespace: "timeout", Transport: mcpserver.TransportStdio, TimeoutSeconds: 1}
	if _, err := manager.Connect(context.Background(), configured); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	implementation, err := registry.Resolve(context.Background(), "mcp.timeout.blocked")
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	_, invokeErr := implementation.Invoke(context.Background(), tool.Invocation{CallID: "timeout-call", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(invokeErr, context.DeadlineExceeded) {
		t.Fatalf("timed out Invoke() error = %v", invokeErr)
	}
	if elapsed := time.Since(startedAt); elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("configured timeout elapsed = %s", elapsed)
	}
	select {
	case <-started:
	default:
		t.Fatal("timed call never reached MCP server")
	}
}

func TestMinimalEnvironmentDoesNotInheritUnrelatedValues(t *testing.T) {
	t.Setenv("SCIAIDE_UNRELATED_SECRET", "must-not-leak")
	values := minimalEnvironment(map[string]string{"LANG": "C"}, map[string]string{"TOKEN": "secret"})
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	if seen["SCIAIDE_UNRELATED_SECRET=must-not-leak"] || !seen["LANG=C"] || !seen["TOKEN=secret"] {
		t.Fatalf("environment = %v", values)
	}
}

func TestMCPStderrWriterRedactsResolvedSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	writer := newMCPStderrWriter(logger, "server-1", map[string]string{"TOKEN": "private-token"})
	if _, err := writer.Write([]byte("authorization=private-token\nsecond line\rwith break\n")); err != nil {
		t.Fatal(err)
	}
	logged := output.String()
	if strings.Contains(logged, "private-token") || !strings.Contains(logged, "[REDACTED]") || !strings.Contains(logged, "server-1") || strings.Contains(logged, `\r`) {
		t.Fatalf("stderr log was not safely redacted: %s", logged)
	}
}

func TestSanitizeNameCollisionFailsClosed(t *testing.T) {
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fixture", Version: "1"}, nil)
	for _, name := range []string{"a/b", "a_b"} {
		server.AddTool(&mcpsdk.Tool{Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{}, nil
		})
	}
	session, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	registry := tool.NewRegistry()
	manager := NewManager(registry, nil)
	manager.transportFactory = func(mcpserver.Server, map[string]string) (mcpsdk.Transport, error) { return clientTransport, nil }
	_, err = manager.Connect(context.Background(), mcpserver.Server{ID: "collision", Namespace: "collision", TimeoutSeconds: 10})
	if err == nil {
		t.Fatal("sanitized name collision was accepted")
	}
	definitions, listErr := registry.Definitions(context.Background())
	if listErr != nil || len(definitions) != 0 {
		t.Fatalf("definitions = %#v, %v", definitions, listErr)
	}
}

func TestHTTPRedirectCannotChangeAuthorityOrScheme(t *testing.T) {
	for _, target := range []string{"https://evil.example/mcp", "http://mcp.example/mcp"} {
		parsed, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRedirect("https://mcp.example/mcp", parsed); err == nil {
			t.Fatalf("redirect to %q was accepted", target)
		}
	}
	parsed, _ := url.Parse("https://mcp.example/other")
	if err := validateRedirect("https://mcp.example/mcp", parsed); err != nil {
		t.Fatalf("same-authority redirect rejected: %v", err)
	}
}

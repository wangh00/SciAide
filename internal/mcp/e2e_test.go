package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wangh00/SciAide/internal/app/mcpserver"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func fixtureMCPServer(name string, crash bool) *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "e2e-1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "echo", Description: "fixture echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: name + "-ok"}}}, nil
	})
	if crash {
		server.AddTool(&mcpsdk.Tool{Name: "crash", Description: "terminate fixture", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			os.Exit(23)
			return nil, nil
		})
	}
	server.AddResource(&mcpsdk.Resource{URI: "fixture://" + name + "/paper", Name: "paper"}, func(context.Context, *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{URI: "fixture://" + name + "/paper", MIMEType: "text/plain", Text: "not injected"}}}, nil
	})
	server.AddPrompt(&mcpsdk.Prompt{Name: "review", Description: "fixture prompt"}, func(context.Context, *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
		return &mcpsdk.GetPromptResult{Messages: []*mcpsdk.PromptMessage{}}, nil
	})
	return server
}

// TestMCPStdioHelperProcess runs in a child copy of the Go test binary. The
// parent reaches it only through the production CommandTransport path, so this
// is a real stdio/process-lifecycle test without requiring an external runtime.
func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("SCIAIDE_MCP_HELPER") != "1" {
		return
	}
	server := fixtureMCPServer("stdio", os.Getenv("SCIAIDE_MCP_HELPER_MODE") == "crash")
	_ = server.Run(context.Background(), &mcpsdk.StdioTransport{})
	os.Exit(0)
}

func stdioFixtureConfig(id, namespace, mode string) mcpserver.Server {
	return mcpserver.Server{
		ID: id, Name: id, Namespace: namespace, Transport: mcpserver.TransportStdio,
		Command: os.Args[0], Args: []string{"-test.run=^TestMCPStdioHelperProcess$"},
		Env:            map[string]string{"SCIAIDE_MCP_HELPER": "1", "SCIAIDE_MCP_HELPER_MODE": mode},
		TimeoutSeconds: 10,
	}
}

func TestManagerStdioTransportE2E(t *testing.T) {
	ctx := context.Background()
	registry := tool.NewRegistry()
	manager := NewManager(registry, nil)
	configured := stdioFixtureConfig("stdio-e2e", "stdioe2e", "normal")
	snapshot, err := manager.Connect(ctx, configured)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ServerVersion != "e2e-1" || len(snapshot.Tools) != 1 || len(snapshot.Resources) != 1 || len(snapshot.Prompts) != 1 {
		t.Fatalf("stdio snapshot = %#v", snapshot)
	}
	qualified := "mcp.stdioe2e.echo"
	implementation, err := registry.Resolve(ctx, qualified)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := implementation.Definition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertMCPPermissionBoundary(t, definition, qualified)
	result, err := implementation.Invoke(ctx, tool.Invocation{Arguments: json.RawMessage(`{}`)})
	if err != nil || result.Status != tool.ResultSuccess || result.Text != "stdio-ok" {
		t.Fatalf("stdio invocation = %#v, %v", result, err)
	}
	definitions, err := registry.Definitions(ctx)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("resources/prompts were registered as tools: %#v, %v", definitions, err)
	}
	if err := manager.Disconnect(ctx, configured.ID); err != nil {
		t.Fatal(err)
	}
	if manager.Connected(configured.ID) {
		t.Fatal("stdio session remained connected")
	}
	if _, err := registry.Resolve(ctx, qualified); err == nil {
		t.Fatal("stdio tool remained registered after disconnect")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerStreamableHTTPAndNamespacesE2E(t *testing.T) {
	ctx := context.Background()
	registry := tool.NewRegistry()
	manager := NewManager(registry, nil)

	type fixture struct {
		id, namespace string
		server        *httptest.Server
		headerSeen    atomic.Bool
	}
	fixtures := []*fixture{{id: "http-alpha", namespace: "alpha"}, {id: "http-beta", namespace: "beta"}}
	t.Cleanup(func() {
		_ = manager.Close()
		for _, value := range fixtures {
			if value.server != nil {
				value.server.CloseClientConnections()
				value.server.Close()
			}
		}
	})
	for _, value := range fixtures {
		native := fixtureMCPServer(value.namespace, false)
		handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return native }, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
		value.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.Header.Get("X-SciAide-E2E") == value.id {
				value.headerSeen.Store(true)
			}
			handler.ServeHTTP(w, request)
		}))
		configured := mcpserver.Server{
			ID: value.id, Name: value.id, Namespace: value.namespace, Transport: mcpserver.TransportStreamableHTTP,
			URL: value.server.URL, Headers: map[string]string{"X-SciAide-E2E": value.id}, TimeoutSeconds: 10,
		}
		snapshot, err := manager.Connect(ctx, configured)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Tools) != 1 || len(snapshot.Resources) != 1 || len(snapshot.Prompts) != 1 || !value.headerSeen.Load() {
			t.Fatalf("HTTP snapshot/header for %s = %#v / %v", value.id, snapshot, value.headerSeen.Load())
		}
	}

	for _, value := range fixtures {
		qualified := "mcp." + value.namespace + ".echo"
		implementation, err := registry.Resolve(ctx, qualified)
		if err != nil {
			t.Fatal(err)
		}
		definition, err := implementation.Definition(ctx)
		if err != nil {
			t.Fatal(err)
		}
		assertMCPPermissionBoundary(t, definition, qualified)
		result, err := implementation.Invoke(ctx, tool.Invocation{Arguments: json.RawMessage(`{}`)})
		if err != nil || result.Text != value.namespace+"-ok" {
			t.Fatalf("HTTP invocation %s = %#v, %v", value.id, result, err)
		}
	}
	definitions, err := registry.Definitions(ctx)
	if err != nil || len(definitions) != 2 {
		t.Fatalf("HTTP definitions = %#v, %v", definitions, err)
	}
}

type runtimeObserver struct{ changed chan error }

func (runtimeObserver) Starting(string) {}
func (o runtimeObserver) RuntimeChanged(_ string, _ mcpserver.CapabilitySnapshot, err error) {
	select {
	case o.changed <- err:
	default:
	}
}

func TestManagerRemovesToolsAfterUnexpectedStdioExit(t *testing.T) {
	ctx := context.Background()
	registry := tool.NewRegistry()
	manager := NewManager(registry, nil)
	defer manager.Close()
	observer := runtimeObserver{changed: make(chan error, 1)}
	manager.SetRuntimeObserver(observer)
	configured := stdioFixtureConfig("stdio-crash", "crashing", "crash")
	if _, err := manager.Connect(ctx, configured); err != nil {
		t.Fatal(err)
	}
	implementation, err := registry.Resolve(ctx, "mcp.crashing.crash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.Invoke(ctx, tool.Invocation{Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("crashing MCP tool unexpectedly succeeded")
	}
	select {
	case observed := <-observer.changed:
		if observed == nil {
			t.Fatal("unexpected exit was reported as healthy")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unexpected MCP exit was not observed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for manager.Connected(configured.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.Connected(configured.ID) {
		t.Fatal("crashed MCP session remained online")
	}
	if _, err := registry.Resolve(ctx, "mcp.crashing.crash"); err == nil {
		t.Fatal("crashed MCP tool remained registered")
	}
}

func assertMCPPermissionBoundary(t *testing.T, definition tool.Definition, qualified string) {
	t.Helper()
	if definition.Risk != tool.RiskModerate || definition.Idempotent || len(definition.Permissions) != 1 || definition.Permissions[0].Kind != tool.PermissionToolInvoke || definition.Permissions[0].Resource != qualified {
		t.Fatalf("MCP permission boundary = %#v", definition)
	}
}

func TestNamespacePrefixKeepsSameNativeToolDistinct(t *testing.T) {
	if left, right := namespacePrefix("Alpha")+sanitizeName("paper/search"), namespacePrefix("Beta")+sanitizeName("paper/search"); left == right || left != "mcp.alpha.paper_search" || right != "mcp.beta.paper_search" {
		t.Fatalf("qualified names = %q / %q", left, right)
	}
}

func TestStdioFixtureCommandIsExecutable(t *testing.T) {
	configured := stdioFixtureConfig("fixture", "fixture", "normal")
	if configured.Command == "" {
		t.Fatal("missing helper command")
	}
	if _, err := os.Stat(configured.Command); err != nil {
		t.Fatalf("helper command %q: %v", configured.Command, err)
	}
	if got := fmt.Sprint(configured.Args); got != "[-test.run=^TestMCPStdioHelperProcess$]" {
		t.Fatalf("helper args = %s", got)
	}
}

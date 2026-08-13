package mcp

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wangh00/SciAide/internal/app/mcpserver"
	"github.com/wangh00/SciAide/internal/app/tool"
)

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

package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wangh00/SciAide/internal/app/mcpserver"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func defaultExaConfig() mcpserver.Server {
	return mcpserver.Server{ID: mcpserver.DefaultExaID, Namespace: "exa", Transport: mcpserver.TransportStreamableHTTP, URL: mcpserver.DefaultExaURL, Enabled: true, AutoStart: true, Trust: mcpserver.TrustUser, TimeoutSeconds: 30}
}

func TestOnlyExactBundledExaSearchHasReadOnlyPolicy(t *testing.T) {
	for _, mode := range []string{"preset", "other_id", "other_url", "other_tool"} {
		s := defaultExaConfig()
		name := "web_search_exa"
		switch mode {
		case "other_id":
			s.ID = "user"
		case "other_url":
			s.URL = "https://other.test/mcp"
		case "other_tool":
			name = "agent_run"
		}
		a, _, err := newToolAdapter(s, &mcpsdk.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		d, _ := a.Definition(context.Background())
		if mode == "preset" {
			if !d.Idempotent || d.Permissions[0].Kind != tool.PermissionNetworkDomain {
				t.Fatal(d)
			}
		} else {
			assertMCPPermissionBoundary(t, d, d.QualifiedName)
		}
	}
}

// Opt-in public, keyless smoke test. No model, user DB, secret or local file
// contents are sent. Normal go test never contacts Exa.
func TestDefaultExaLiveSearch(t *testing.T) {
	if os.Getenv("SCIAIDE_EXA_LIVE") != "1" {
		t.Skip("explicit public service smoke test only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r := tool.NewRegistry()
	m := NewManager(r, nil)
	defer m.Close()
	snapshot, err := m.Connect(ctx, defaultExaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].QualifiedName != mcpserver.DefaultExaTool {
		t.Fatal(snapshot)
	}
	a, err := r.Resolve(ctx, mcpserver.DefaultExaTool)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Invoke(ctx, tool.Invocation{Arguments: json.RawMessage(`{"query":"Exa MCP official documentation anonymous rate limits","objective":"Find official Exa documentation about anonymous MCP access and rate limits.","numResults":2}`)})
	if err != nil || result.Status != tool.ResultSuccess || len(result.Text) == 0 {
		t.Fatal(result, err)
	}
	t.Logf("SDK initialize/discover/search passed: tools=%d, result bytes=%d", len(snapshot.Tools), len(result.Text))
}

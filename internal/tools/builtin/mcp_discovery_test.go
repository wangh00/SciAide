package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/mcpserver"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type discoveryServers []mcpserver.Server

func (s discoveryServers) List(context.Context) ([]mcpserver.Server, error) { return s, nil }

type discoveryTool struct{ d tool.Definition }

func (t discoveryTool) Definition(context.Context) (tool.Definition, error) { return t.d, nil }
func (t discoveryTool) Invoke(context.Context, tool.Invocation) (tool.Result, error) {
	panic("discovery must not invoke")
}
func TestMCPDiscoveryRedactionScopeAndPaging(t *testing.T) {
	ctx := context.Background()
	r := tool.NewRegistry()
	for _, n := range []string{"mcp.browser.tabs", "mcp.browser.navigate", "mcp.other.navigate", "builtin.fixture"} {
		if err := r.Register(ctx, discoveryTool{tool.Definition{QualifiedName: n, Description: "browser navigation", Version: "1", Risk: tool.RiskLow, InputSchema: json.RawMessage(`{"type":"object"}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	servers := discoveryServers{{Name: "Browser", Namespace: "browser", Enabled: true, Status: mcpserver.StatusReady, Command: "TOPSECRET", Env: map[string]string{"token": "TOPSECRET"}}, {Name: "Offline", Namespace: "offline", Status: mcpserver.StatusFailed, LastError: "TOPSECRET"}}
	var scope map[string]bool
	readScope := func(context.Context, string) (map[string]bool, error) { return scope, nil }
	list := NewMCPList(r, servers, readScope)
	search := NewToolsSearch(r, servers, readScope)
	invoke := func(v *MCPDiscovery, args string) tool.Result {
		result, err := v.Invoke(ctx, tool.Invocation{RunID: "run", Arguments: json.RawMessage(args)})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := invoke(list, `{}`)
	if strings.Contains(result.Text, "TOPSECRET") || !strings.Contains(result.Text, "Offline") || !strings.Contains(result.Text, `"availableToolCount":2`) {
		t.Fatal(result.Text)
	}
	result = invoke(search, `{"server":"browser","limit":1}`)
	if !strings.Contains(result.Text, `"hasMore":true`) || !strings.Contains(result.Text, "fingerprint") || strings.Contains(result.Text, "mcp.other") {
		t.Fatal(result.Text)
	}
	result = invoke(search, `{"query":"navigate","offset":999999}`)
	if !strings.Contains(result.Text, `"tools":[]`) {
		t.Fatal(result.Text)
	}
	scope = map[string]bool{"mcp.other.navigate": true}
	result = invoke(search, `{"query":"navigate"}`)
	if strings.Contains(result.Text, "mcp.browser") || !strings.Contains(result.Text, "mcp.other.navigate") {
		t.Fatal(result.Text)
	}
	scope = map[string]bool{}
	if result = invoke(search, `{"server":"browser"}`); !strings.Contains(result.Text, `"tools":[]`) || !strings.Contains(result.Text, `"scopeLimited":true`) || !strings.Contains(result.Text, "当前科研执行范围") {
		t.Fatal(result.Text)
	}
	if result = invoke(list, `{}`); !strings.Contains(result.Text, "Browser") || !strings.Contains(result.Text, `"availableToolCount":0`) || !strings.Contains(result.Text, `"scopeLimited":true`) {
		t.Fatal("scope must not hide server status: " + result.Text)
	}
	scope = nil
	if err := r.ReplaceNamespace(ctx, "mcp.browser.", nil); err != nil {
		t.Fatal(err)
	}
	if result = invoke(search, `{"server":" browser "}`); !strings.Contains(result.Text, `"tools":[]`) || !strings.Contains(result.Text, `"scopeLimited":false`) {
		t.Fatal("disconnected tools remain searchable: " + result.Text)
	}
	if _, err := search.Invoke(ctx, tool.Invocation{RunID: "run", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("empty search accepted")
	}
	if _, err := list.Invoke(ctx, tool.Invocation{Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("missing scope accepted")
	}
}

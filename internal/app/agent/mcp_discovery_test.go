package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/tools/builtin"
)

func TestMCPDiscoveryLoadsNextTurnAndBlocksSameBatchGuess(t *testing.T) {
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "search", Name: mcpSearchTool, Arguments: json.RawMessage(`{"server":"browser"}`)}}}, {Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "guess", Name: "mcp.browser.tabs", Arguments: json.RawMessage(`{}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	second := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "actual", Name: "mcp.browser.tabs", Arguments: json.RawMessage(`{}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	third := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, first, second, third)
	registry := loop.registry.(tool.MutableRegistry)
	executed := 0
	def := tool.Definition{QualifiedName: "mcp.browser.tabs", Description: "tabs", Version: "1", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true}
	if err := registry.Register(context.Background(), fixtureTool{definition: def, invoke: func(tool.Invocation) tool.Result {
		executed++
		return tool.Result{Status: tool.ResultSuccess, Text: "tabs"}
	}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(context.Background(), builtin.NewToolsSearch(loop.registry, nil, func(context.Context, string) (map[string]bool, error) { return nil, nil })); err != nil {
		t.Fatal(err)
	}
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
		t.Fatalf("%s %+v", got, state.run)
	}
	if executed != 1 {
		t.Fatalf("executed %d times", executed)
	}
	requests := provider.Requests()
	if len(requests) != 3 {
		t.Fatal(len(requests))
	}
	for i, req := range requests {
		found := false
		for _, d := range req.Tools {
			if d.Name == def.QualifiedName {
				found = true
			}
		}
		if found != (i > 0) {
			t.Fatalf("turn %d MCP visible=%v", i, found)
		}
	}
	calls, _ := loop.tools.ListByRun(context.Background(), "run")
	for _, c := range calls {
		if c.ProviderCallID == "guess" && c.Status != tool.CallFailed {
			t.Fatalf("guess not rejected: %+v", c)
		}
		if c.ProviderCallID == "guess" && c.ToolVersion != "unavailable" {
			t.Fatalf("hidden tool metadata leaked into rejection: %+v", c)
		}
	}
}
func TestMCPDiscoveryRestoresOnlyMatchingHostSearchResults(t *testing.T) {
	def := tool.Definition{QualifiedName: "mcp.browser.tabs", Version: "1", InputSchema: json.RawMessage(`{"type":"object"}`)}
	search := tool.Definition{QualifiedName: mcpSearchTool}
	data, _ := json.Marshal(map[string]any{"tools": []map[string]string{{"name": def.QualifiedName, "fingerprint": tool.DefinitionFingerprint(def)}}})
	c := tool.Call{ToolName: mcpSearchTool, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Structured: data}}
	defs, _ := deferredMCPDefinitions([]tool.Definition{search, def}, []tool.Call{c})
	if !definitionVisible(defs, def.QualifiedName) {
		t.Fatal("resume lost selection")
	}
	c.ToolName = "mcp.malicious.search"
	defs, _ = deferredMCPDefinitions([]tool.Definition{search, def}, []tool.Call{c})
	if definitionVisible(defs, def.QualifiedName) {
		t.Fatal("untrusted tool unlocked definitions")
	}
	c.ToolName = mcpSearchTool
	def.Version = "2"
	defs, _ = deferredMCPDefinitions([]tool.Definition{search, def}, []tool.Call{c})
	if definitionVisible(defs, def.QualifiedName) {
		t.Fatal("changed contract unlocked")
	}
	defs, _ = deferredMCPDefinitions([]tool.Definition{search}, []tool.Call{c})
	if len(defs) != 1 {
		t.Fatal("scope widened")
	}
	c.Status = tool.CallFailed
	defs, _ = deferredMCPDefinitions([]tool.Definition{search, def}, []tool.Call{c})
	if len(defs) != 1 {
		t.Fatal("failed discovery unlocked")
	}
}

type mcpAskCoordinator struct {
	service *tool.Service
	state   *loopState
}

func (c mcpAskCoordinator) EvaluateCall(ctx context.Context, p, id string) (permission.Coordination, error) {
	calls, _ := c.service.ListByRun(ctx, "run")
	for _, call := range calls {
		if call.ID == id && call.ToolName == mcpSearchTool {
			return (allowCoordinator{c.service}).EvaluateCall(ctx, p, id)
		}
	}
	return (statefulAskCoordinator{c.service, c.state}).EvaluateCall(ctx, p, id)
}
func TestMCPDiscoveryApprovalResume(t *testing.T) {
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "search", Name: mcpSearchTool, Arguments: json.RawMessage(`{"server":"browser"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	second := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "actual", Name: "mcp.browser.tabs", Arguments: json.RawMessage(`{}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	third := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, first, second, third)
	service := loop.tools.(*tool.Service)
	loop.approvals = mcpAskCoordinator{service, state}
	r := loop.registry.(tool.MutableRegistry)
	def := tool.Definition{QualifiedName: "mcp.browser.tabs", Description: "tabs", Version: "1", Risk: tool.RiskModerate, Idempotent: false, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionToolInvoke, Resource: "mcp.browser.tabs"}}, InputSchema: json.RawMessage(`{"type":"object"}`)}
	r.Register(context.Background(), fixtureTool{definition: def, invoke: func(tool.Invocation) tool.Result { return tool.Result{Status: tool.ResultSuccess, Text: "tabs"} }})
	r.Register(context.Background(), builtin.NewToolsSearch(loop.registry, nil, func(context.Context, string) (map[string]bool, error) { return nil, nil }))
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval {
		t.Fatalf("%s %+v", outcome, state.run)
	}
	calls, _ := service.ListByRun(context.Background(), "run")
	for _, c := range calls {
		if c.ToolName == def.QualifiedName {
			if _, err := service.Start(context.Background(), c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	state.transitionRun(chat.RunWaitingApproval, chat.RunRunning)
	if outcome := loop.Resume(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("resume %s %+v", outcome, state.run)
	}
	requests := provider.Requests()
	found := false
	for _, d := range requests[len(requests)-1].Tools {
		found = found || d.Name == def.QualifiedName
	}
	if !found {
		t.Fatal("resumed model lost selected tool")
	}
}

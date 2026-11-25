package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/tools/builtin"
)

func TestBuiltinColdDiscoveryLoadsNextTurnNotSameBatch(t *testing.T) {
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "search", Name: mcpSearchTool, Arguments: json.RawMessage(`{"kind":"builtin","query":"builtin.knowledge.search","limit":1}`)}}}, {Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "guess", Name: "builtin.knowledge.search", Arguments: json.RawMessage(`{}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	second := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "actual", Name: "builtin.knowledge.search", Arguments: json.RawMessage(`{}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	third := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, first, second, third)
	loop.models = budgetResolver{model: provider, budget: modelcap.ResolveContextBudget(6000, 5400, modelcap.ContextWindowSourceManual)}
	registry := loop.registry.(tool.MutableRegistry)
	executed := 0
	for _, name := range []string{"builtin.knowledge.search", "builtin.python.execute", "builtin.shell.execute", "builtin.workspace.read_text"} {
		d := tool.Definition{QualifiedName: name, Description: strings.Repeat("definition ", 140), Version: "1", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true}
		if err := registry.Register(context.Background(), fixtureTool{definition: d, invoke: func(inv tool.Invocation) tool.Result {
			executed++
			return tool.Result{Status: tool.ResultSuccess, Text: "known result"}
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Register(context.Background(), builtin.NewToolsSearch(loop.registry, nil, func(context.Context, string) (map[string]bool, error) { return nil, nil })); err != nil {
		t.Fatal(err)
	}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatal("cold optional definition flow failed", outcome, state.run.ErrorCode, state.run.ErrorDetails)
	}
	requests := provider.Requests()
	if len(requests) != 3 || executed != 1 {
		t.Fatal("unexpected retry or side effect", len(requests), executed)
	}
	for i, r := range requests {
		found := false
		for _, d := range r.Tools {
			found = found || d.Name == "builtin.knowledge.search"
		}
		if found != (i > 0) {
			t.Fatal("definition activation did not wait for successful next turn", i, found)
		}
	}
	calls, _ := loop.tools.ListByRun(context.Background(), "run")
	for _, c := range calls {
		if c.ProviderCallID == "guess" && (c.Status != tool.CallFailed || c.ToolVersion != "unavailable") {
			t.Fatal("same-batch guess executed or revealed hidden metadata", c)
		}
	}
}

func TestBuiltinDiscoveryRestoresOnlyAuthorizedMatchingHostReceipt(t *testing.T) {
	d := tool.Definition{QualifiedName: "builtin.knowledge.search", Description: strings.Repeat("search ", 200), Version: "1", InputSchema: json.RawMessage(`{"type":"object"}`)}
	search := tool.Definition{QualifiedName: mcpSearchTool}
	raw, _ := json.Marshal(map[string]any{"kind": "builtin", "tools": []map[string]string{{"name": d.QualifiedName, "fingerprint": tool.DefinitionFingerprint(d)}}})
	call := tool.Call{ToolName: mcpSearchTool, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Structured: raw}}
	out, _, _ := deferredBuiltinDefinitions([]tool.Definition{search, d}, []tool.Call{call})
	if !definitionVisible(out, d.QualifiedName) {
		t.Fatal("matching original host receipt lost")
	}
	for _, mutate := range []func(*tool.Call, *tool.Definition){func(c *tool.Call, d *tool.Definition) { c.Status = tool.CallFailed }, func(c *tool.Call, d *tool.Definition) { c.ToolName = "mcp.fake.search" }, func(c *tool.Call, d *tool.Definition) { d.Version = "2" }} {
		c, v := call, d
		mutate(&c, &v)
		out, _, _ := deferredBuiltinDefinitions([]tool.Definition{search, v}, []tool.Call{c})
		if definitionVisible(out, v.QualifiedName) {
			t.Fatal("failed/forged/stale discovery activated definition")
		}
	}
	out, _, _ = deferredBuiltinDefinitions([]tool.Definition{search}, []tool.Call{call})
	if len(out) != 1 {
		t.Fatal("receipt widened current scope")
	}
}

func TestBuiltinColdDiscoveryDoesNotGrantApprovalAndResumesMatchingContract(t *testing.T) {
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "search", Name: mcpSearchTool, Arguments: json.RawMessage(`{"kind":"builtin","query":"builtin.knowledge.search","limit":1}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	second := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "actual", Name: "builtin.knowledge.search", Arguments: json.RawMessage(`{}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	third := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, first, second, third)
	loop.models = budgetResolver{model: provider, budget: modelcap.ResolveContextBudget(6000, 5400, modelcap.ContextWindowSourceManual)}
	service := loop.tools.(*tool.Service)
	loop.approvals = mcpAskCoordinator{service, state}
	registry := loop.registry.(tool.MutableRegistry)
	executed := 0
	for _, name := range []string{"builtin.knowledge.search", "builtin.python.execute", "builtin.shell.execute", "builtin.workspace.read_text"} {
		d := tool.Definition{QualifiedName: name, Description: strings.Repeat("definition ", 140), Version: "1", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskModerate, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionToolInvoke, Resource: name}}}
		if err := registry.Register(context.Background(), fixtureTool{definition: d, invoke: func(tool.Invocation) tool.Result {
			executed++
			return tool.Result{Status: tool.ResultSuccess, Text: "approved result"}
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Register(context.Background(), builtin.NewToolsSearch(loop.registry, nil, func(context.Context, string) (map[string]bool, error) { return nil, nil })); err != nil {
		t.Fatal(err)
	}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval || executed != 0 {
		t.Fatal("discovery bypassed original approval", outcome, executed, state.run)
	}
	calls, _ := service.ListByRun(context.Background(), "run")
	for _, c := range calls {
		if c.ToolName == "builtin.knowledge.search" {
			if _, err := service.Start(context.Background(), c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	state.transitionRun(chat.RunWaitingApproval, chat.RunRunning)
	if outcome := loop.Resume(context.Background(), "run"); outcome != OutcomeCompleted || executed != 1 {
		t.Fatal("approved resume lost contract or repeated execution", outcome, executed, state.run)
	}
}

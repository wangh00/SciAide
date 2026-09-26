package agent

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"testing"
)

func TestWebOptInFiltersModelTools(t *testing.T) {
	for _, disabled := range []bool{true, false} {
		loop, state, provider := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "answer"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
		state.run.WebSearchDisabled = disabled
		for _, name := range []string{"builtin.web.search", "builtin.web.open", "builtin.browser.open"} {
			if err := loop.registry.(tool.MutableRegistry).Register(context.Background(), fixtureTool{definition: tool.Definition{QualifiedName: name, Version: "1", Description: "web", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true}, invoke: func(tool.Invocation) tool.Result { t.Fatal("unexpected execution"); return tool.Result{} }}); err != nil {
				t.Fatal(err)
			}
		}
		if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
			t.Fatalf("outcome=%v run=%+v", got, state.run)
		}
		for _, req := range provider.Requests() {
			found := 0
			for _, d := range req.Tools {
				if isWebBrowsingTool(d.Name) {
					found++
				}
			}
			if disabled && found != 0 || !disabled && found != 3 {
				t.Fatalf("disabled=%v web tools=%d", disabled, found)
			}
		}
	}
}

func TestDisabledWebCannotExecuteOnApprovalResume(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	state.run.WebSearchDisabled = true
	_, err := loop.processCalls(context.Background(), &state.run, "project", []tool.Call{{ID: "web", ToolName: "builtin.web.open", Status: tool.CallRunning}})
	if err == nil {
		t.Fatal("disabled pending web call executed")
	}
	if len(provider.Requests()) != 0 {
		t.Fatal("unexpected model request")
	}
}

func TestDisabledWebHallucinationIsRejectedThenAnswerContinues(t *testing.T) {
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "web", Name: "builtin.web.search", Arguments: json.RawMessage(`{}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "answer without browsing"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, _ := newLoopFixture(t, nil, first, second)
	state.run.WebSearchDisabled = true
	if err := loop.registry.(tool.MutableRegistry).Register(context.Background(), fixtureTool{definition: tool.Definition{QualifiedName: "builtin.web.search", Version: "1", Description: "web", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true}, invoke: func(tool.Invocation) tool.Result { t.Fatal("disabled web executed"); return tool.Result{} }}); err != nil {
		t.Fatal(err)
	}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatalf("outcome=%v run=%+v", got, state.run)
	}
	calls, err := loop.tools.ListByRun(context.Background(), state.run.ID)
	if err != nil || len(calls) != 1 || !calls[0].Status.Terminal() {
		t.Fatalf("rejection not persisted: %+v %v", calls, err)
	}
}

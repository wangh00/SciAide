package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type resourceViewFixture struct {
	view     resource.View
	err      error
	requests []resource.Request
}

func (f *resourceViewFixture) Prepare(_ context.Context, r resource.Request) (resource.View, error) {
	f.requests = append(f.requests, r)
	return f.view, f.err
}
func TestResourceModeAdvertisesOnlyIssuedActionsAndBlocksRawAdapters(t *testing.T) {
	ref := "res_" + strings.Repeat("a", 32)
	for _, name := range []string{resource.OpenTool, "builtin.workspace.list", "invalid-reference"} {
		t.Run(name, func(t *testing.T) {
			callName := name
			args := `{"actionId":"` + ref + `"}`
			if name == "builtin.workspace.list" {
				args = `{"path":"/workspace"}`
			}
			if name == "invalid-reference" {
				callName = resource.OpenTool
				args = `{"actionId":"res_` + strings.Repeat("b", 32) + `"}`
			}
			steps := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "action-call", Name: callName, Arguments: json.RawMessage(args)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
			loop, state, provider := newLoopFixture(t, nil, steps, submissionScript(`{"items":["no input"],"state":"blocked"}`))
			state.workflowAI = true
			def := tool.Definition{QualifiedName: resource.OpenTool, Description: "resource action", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["actionId"],"properties":{"actionId":{"type":"string"}}}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
			var executions int
			reg := loop.registry.(*tool.MemoryRegistry)
			for _, d := range []tool.Definition{def, {QualifiedName: "builtin.workspace.list", Description: "raw locator adapter", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}} {
				if err := reg.Register(context.Background(), fixtureTool{definition: d, invoke: func(tool.Invocation) tool.Result {
					executions++
					return tool.Result{Status: tool.ResultSuccess, Text: "resource opened"}
				}}); err != nil {
					t.Fatal(err)
				}
			}
			loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", AllowedToolNames: []string{resource.OpenTool, "builtin.workspace.list"}, StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
			f := &resourceViewFixture{view: resource.View{Definitions: []tool.Definition{def}, Context: "host resource menu", OpenIDs: map[string]bool{ref: true}}}
			loop.resources = f
			if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
				t.Fatalf("outcome=%s err=%s", got, state.run.ErrorMessage)
			}
			want := 0
			if name == resource.OpenTool {
				want = 1
			}
			if executions != want {
				t.Fatalf("adapter execution=%d want=%d", executions, want)
			}
			if len(provider.Requests()) != 2 || len(f.requests) != 2 || f.requests[0].WorkflowStepID != "step" {
				t.Fatal("resource view not bound/refreshed")
			}
			for _, request := range provider.Requests() {
				for _, d := range request.Tools {
					if d.Name == "builtin.workspace.list" {
						t.Fatal("raw adapter advertised")
					}
				}
			}
		})
	}
}
func TestResourceModeDoesNotFallbackIfCatalogCannotBePrepared(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionScript(`{"items":[],"state":"blocked"}`))
	state.workflowAI = true
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", AllowedToolNames: []string{resource.OpenTool}, StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
	loop.resources = &resourceViewFixture{err: errors.New("scope missing")}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "RESOURCE_INTERFACE_FAILED" || len(provider.Requests()) != 0 {
		t.Fatalf("catalog failure fell back: %s %+v", got, state.run)
	}
}

func TestOrdinaryChatDoesNotAdvertiseUnboundResourceInterface(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionScript("ordinary answer"))
	reg := loop.registry.(*tool.MemoryRegistry)
	d := tool.Definition{QualifiedName: resource.OpenTool, Description: "resource action", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
	if err := reg.Register(context.Background(), fixtureTool{definition: d, invoke: func(tool.Invocation) tool.Result { t.Fatal("unbound resource executed"); return tool.Result{} }}); err != nil {
		t.Fatal(err)
	}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatal(got)
	}
	for _, req := range provider.Requests() {
		for _, tool := range req.Tools {
			if resourceInterfaceTool(tool.Name) {
				t.Fatal("ordinary chat was offered a tool with no resource session")
			}
		}
	}
}

func TestResourceProgressAndSubmissionPhase(t *testing.T) {
	calls := []tool.Call{}
	for i := 0; i < 2; i++ {
		calls = append(calls, tool.Call{ToolName: resource.OpenTool, Result: &tool.Result{Status: tool.ResultSuccess, Structured: json.RawMessage(`{"actionId":"res_read","label":"统计方法"}`)}})
	}
	text, final := resourceStageProgress(11, true, calls)
	if final || !strings.Contains(text, `"count":2`) || !strings.Contains(text, "11/12") {
		t.Fatal(text)
	}
	if _, final = resourceStageProgress(12, true, calls); !final {
		t.Fatal("planning did not enter submission")
	}
	if _, final = resourceStageProgress(12, false, calls); final {
		t.Fatal("execution used planning budget")
	}
	if _, final = resourceStageProgress(40, false, calls); !final {
		t.Fatal("no room reserved for submission")
	}
}

func TestResourceSubmissionDisablesToolsAndEnforcesProviderCompliance(t *testing.T) {
	for _, violates := range []bool{false, true} {
		script := submissionToolScript(`{"items":["provisional plan"],"state":"blocked"}`)
		if violates {
			script = []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "unexpected", Name: resource.OpenTool, Arguments: json.RawMessage(`{"actionId":"res_read"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
		}
		loop, state, provider := newLoopFixture(t, nil, script)
		state.workflowAI = true
		state.run.ModelTurns = 12
		loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", SkillDiscovery: true, AllowedToolNames: []string{resource.OpenTool}, StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
		loop.resources = &resourceViewFixture{view: resource.View{Context: "issued catalog"}}
		outcome := loop.Run(context.Background(), state.run.ID)
		if violates {
			if outcome != OutcomeFailed || state.run.ErrorCode != "MODEL_SUBMISSION_TOOL_VIOLATION" {
				t.Fatalf("%s %+v", outcome, state.run)
			}
		} else if outcome != OutcomeCompleted {
			t.Fatalf("%s %+v", outcome, state.run)
		}
		reqs := provider.Requests()
		if len(reqs) != 1 || reqs[0].ForcedTool != stageSubmissionTool {
			t.Fatal("submission not constrained")
		}
	}
}

func TestResourceExplorationReachesSubmissionFromCleanRun(t *testing.T) {
	scripts := [][]fake.Step{}
	ref := "res_" + strings.Repeat("a", 32)
	for i := 0; i < 12; i++ {
		scripts = append(scripts, []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: fmt.Sprintf("read-%d", i), Name: resource.OpenTool, Arguments: json.RawMessage(`{"actionId":"` + ref + `"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}})
	}
	scripts = append(scripts, submissionToolScript(`{"items":["provisional design; experiment not yet performed"],"state":"blocked"}`))
	loop, state, provider := newLoopFixture(t, nil, scripts...)
	state.workflowAI = true
	def := tool.Definition{QualifiedName: resource.OpenTool, Description: "resource", InputSchema: json.RawMessage(`{"type":"object","required":["actionId"],"properties":{"actionId":{"type":"string"}}}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
	if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: def, invoke: func(tool.Invocation) tool.Result {
		return tool.Result{Status: tool.ResultSuccess, Text: "read", Structured: json.RawMessage(`{"actionId":"` + ref + `","label":"统计参考"}`)}
	}}); err != nil {
		t.Fatal(err)
	}
	loop.resources = &resourceViewFixture{view: resource.View{Definitions: []tool.Definition{def}, OpenIDs: map[string]bool{ref: true}}}
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", SkillDiscovery: true, AllowedToolNames: []string{resource.OpenTool}, StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted {
		t.Fatalf("%s %+v", out, state.run)
	}
	reqs := provider.Requests()
	if len(reqs) != 13 || state.run.ModelTurns != 13 || len(state.calls) != 12 {
		t.Fatalf("requests %d turns %d calls %d", len(reqs), state.run.ModelTurns, len(state.calls))
	}
	for i, req := range reqs {
		if (req.ForcedTool == stageSubmissionTool) != (i == 12) {
			t.Fatalf("incorrect phase at %d", i)
		}
	}
}

func submissionToolScript(value string) []fake.Step {
	return []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "submit-result", Name: stageSubmissionTool, Arguments: json.RawMessage(value)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
}

func TestSubmissionRequestIsolatedFromExplorationProtocol(t *testing.T) {
	old := model.ChatRequest{Tools: []model.ToolDefinition{{Name: resource.OpenTool}}, ProviderTurns: []model.ProviderTurn{{TurnIndex: 1}}, Messages: []model.Message{{Role: model.RoleSystem, Content: "frozen stage"}, {Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "old-call", Name: resource.OpenTool, Arguments: json.RawMessage(`{}`)}}}, {Role: model.RoleTool, ToolCallID: "old-call", Content: "retained-evidence"}}}
	req := stageSubmissionRequest(old, submissionRepairSchema)
	if len(req.Tools) != 1 || req.Tools[0].Name != stageSubmissionTool || req.ForcedTool != stageSubmissionTool || len(req.ProviderTurns) != 0 || req.DisableTools {
		t.Fatalf("invalid submission request: %+v", req)
	}
	if string(req.Tools[0].InputSchema) != string(submissionRepairSchema) {
		t.Fatal("schema changed")
	}
	found := false
	for _, m := range req.Messages {
		if m.Role == model.RoleTool || len(m.ToolCalls) > 0 || m.ToolCallID != "" {
			t.Fatal("live exploration history replayed")
		}
		found = found || strings.Contains(m.Content, "retained-evidence")
	}
	if !found || len(old.ProviderTurns) != 1 || len(old.Messages[1].ToolCalls) != 1 {
		t.Fatal("lost evidence or mutated original audit")
	}
}

func TestSubmissionInterfaceRetainsValidationAndExactCandidateAudit(t *testing.T) {
	bad := ` {"items":{},"state":"ready"} `
	good := ` {"items":["evidence"],"state":"ready"} `
	loop, state, provider := newLoopFixture(t, nil, submissionToolScript(bad), submissionToolScript(good))
	state.workflowAI = true
	state.run.ModelTurns = 12
	loop.resources = &resourceViewFixture{}
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", SkillDiscovery: true, AllowedToolNames: []string{resource.OpenTool}, StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted {
		t.Fatalf("%s %+v", out, state.run)
	}
	if len(provider.Requests()) != 2 || state.journalDrafts[13] != bad || state.journalDrafts[14] != good || len(state.calls) != 0 {
		t.Fatalf("candidate not audited/validated: %+v", state)
	}
	for _, req := range provider.Requests() {
		if req.ForcedTool != stageSubmissionTool {
			t.Fatal("repair fell back to exploration")
		}
	}
}

func TestSubmissionPseudoToolAndMissingResultAreDistinct(t *testing.T) {
	for _, item := range []struct{ text, code string }{{"<tool_call>builtin_resource_open<arg_key>actionId</arg_key></tool_call>", "MODEL_SUBMISSION_PSEUDO_TOOL"}, {"Now I will submit the complete route JSON.", "MODEL_SUBMISSION_MISSING"}} {
		loop, state, _ := newLoopFixture(t, nil, submissionScript(item.text))
		state.workflowAI = true
		state.run.ModelTurns = 12
		loop.resources = &resourceViewFixture{}
		loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", SkillDiscovery: true, AllowedToolNames: []string{resource.OpenTool}, StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
		if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != item.code || len(state.calls) != 0 {
			t.Fatalf("%s %+v", out, state.run)
		}
	}
}

func TestSubmissionRepairDoesNotReplayOrphanNativeToolCalls(t *testing.T) {
	for _, protocol := range []modelcap.APIProtocol{modelcap.ProtocolAnthropic, modelcap.ProtocolOpenAIResponses} {
		scripts := [][]fake.Step{}
		for i, raw := range []string{`{"items":{},"state":"ready"}`, `{"items":["verified"],"state":"ready"}`} {
			itemType := "tool_use"
			if protocol == modelcap.ProtocolOpenAIResponses {
				itemType = "function_call"
			}
			id := fmt.Sprintf("submit-%d", i)
			item := model.ProviderItem{Ordinal: 0, Type: itemType, CallID: id, Payload: json.RawMessage(fmt.Sprintf(`{"type":%q,"id":%q,"name":"submit_stage_result","arguments":%q}`, itemType, id, raw))}
			scripts = append(scripts, []fake.Step{{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &item}}, {Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: id, Name: stageSubmissionTool, Arguments: json.RawMessage(raw)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}})
		}
		loop, state, provider := newLoopFixture(t, nil, scripts...)
		state.workflowAI = true
		state.run.ModelTurns = 12
		loop.models = protocolResolver{model: provider, protocol: protocol}
		loop.resources = &resourceViewFixture{}
		loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", SkillDiscovery: true, AllowedToolNames: []string{resource.OpenTool}, StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
		if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted {
			t.Fatalf("%s %s %+v", protocol, out, state.run)
		}
		if len(state.providerTurns) != 2 {
			t.Fatal("native audit lost")
		}
		for _, r := range provider.Requests() {
			if len(r.ProviderTurns) != 0 || r.ForcedTool != stageSubmissionTool {
				t.Fatal("native control item replayed without result")
			}
		}
	}
}

func TestEarlyProgressResponseSwitchesToSubmissionInterface(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionScript("Now I will submit the result."), submissionToolScript(`{"items":["provisional"],"state":"blocked"}`))
	state.workflowAI = true
	loop.resources = &resourceViewFixture{}
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", SkillDiscovery: true, AllowedToolNames: []string{resource.OpenTool}, StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted {
		t.Fatalf("%s %+v", out, state.run)
	}
	reqs := provider.Requests()
	if len(reqs) != 2 || reqs[1].ForcedTool != stageSubmissionTool {
		t.Fatal("progress-only loop was not replaced with explicit submission")
	}
}

func TestSubmissionAcceptsJSONBodyWithoutExecutingEmbeddedToolText(t *testing.T) {
	raw := ` {"items":["Literal <tool_call> is data"],"state":"blocked"} `
	loop, state, _ := newLoopFixture(t, nil, submissionScript(raw))
	state.workflowAI = true
	state.run.ModelTurns = 12
	loop.resources = &resourceViewFixture{}
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", SkillDiscovery: true, AllowedToolNames: []string{resource.OpenTool}, StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || state.journalDrafts[13] != raw || len(state.calls) != 0 {
		t.Fatalf("%s %+v", out, state.run)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/multimodal"
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

// Reproduce the actual failure shape: a large immutable stage prompt is
// accepted on the first request; changing the host-reference flag only after
// budgeting makes that SAME prompt look new on the second request.
func TestWorkflowContextUsageKeepsFrozenPromptFingerprintAcrossTools(t *testing.T) {
	ref := "res_" + strings.Repeat("a", 32)
	args := json.RawMessage(`{"actionId":"` + ref + `"}`)
	usage := model.Usage{InputTokens: 61420}
	first := []fake.Step{
		{Event: model.Event{Type: model.EventUsage, Usage: &usage}},
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &model.ProviderItem{Ordinal: 0, Type: "function_call", CallID: "read", Payload: json.RawMessage(`{"type":"function_call","call_id":"read","name":"resource_open","arguments":"{\"actionId\":\"` + ref + `\"}"}`)}}},
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "read", Name: resource.OpenTool, Arguments: args}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	loop, state, provider := newLoopFixture(t, nil, first, submissionScript(`{"items":["verified"],"state":"ready"}`))
	input := strings.Repeat("已冻结研究要求，不能改写或遗漏。", 9500)
	state.messages[0].Parts[0].Text = input
	state.workflowAI = true
	definition := tool.Definition{QualifiedName: resource.OpenTool, Description: "Read issued resource", InputSchema: json.RawMessage(`{"type":"object","required":["actionId"],"properties":{"actionId":{"type":"string"}}}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
	executions := 0
	if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: definition, invoke: func(tool.Invocation) tool.Result {
		executions++
		return tool.Result{Status: tool.ResultSuccess, Text: "verified field types"}
	}}); err != nil {
		t.Fatal(err)
	}
	loop.resources = &resourceViewFixture{view: resource.View{Definitions: []tool.Definition{definition}, Context: "issued menu", OpenIDs: map[string]bool{ref: true}}}
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", AllowedToolNames: []string{resource.OpenTool}, ExecutionSystemContext: "execution policy", ExecutionDynamicState: "stage active", StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
	loop.models = incrementalCheckpointResolver{model: provider}
	if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeCompleted {
		t.Fatalf("outcome=%s details=%s requests=%d", outcome, state.run.ErrorDetails, len(provider.Requests()))
	}
	if len(provider.Requests()) != 2 || executions != 1 || state.run.ContextCompacted {
		t.Fatal("replayed tools or added unnecessary compaction", len(provider.Requests()), executions)
	}
	for _, req := range provider.Requests() {
		found := false
		for _, msg := range req.Messages {
			if msg.Content == input {
				found = true
				if !msg.HostToolReferences {
					t.Fatal("frozen prompt did not retain its host-reference projection")
				}
			}
		}
		if !found {
			t.Fatal("lost exact frozen prompt")
		}
	}
}

func TestContextHostReferencesFollowMessageIdentityNotMatchingText(t *testing.T) {
	text := "same text mentioning builtin.resource.open"
	messages := []conversation.Message{
		checkpointText("old", "old-run", text),
		checkpointText("current", "run", text),
		{ID: "assistant", RunID: "run", Role: conversation.RoleAssistant, Parts: []conversation.MessagePart{{Type: "text", Text: text}}},
	}
	request, _, err := NewContextBuilder(0).BuildWithRuntimeContext(context.Background(), messages, "", "current", nil, nil, skill.RunContext{}, ContextLimits{EffectiveTokens: 200000, AutoCompactTokens: 180000, CurrentUserHostToolReferences: true}, contextmemory.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	for i, message := range request.Messages[1:] {
		if message.HostToolReferences != (i == 1) {
			t.Fatal("historical or assistant message received current stage authority", i)
		}
	}
}

func TestSubmissionWithoutUsageDoesNotSendOverBudgetSchema(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionScript(`{"items":[],"state":"ready"}`))
	state.workflowAI = true
	// Starting at the persisted exploration limit enters the original SUBMIT
	// phase immediately, without reexecuting any completed tool.
	state.run.ModelTurns = 40
	state.run.ContextBudgetTokens, state.run.AutoCompactTokenLimit = 6000, 5400
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["items","state"],"properties":{"items":{"type":"array","items":{"type":"string"}},"state":{"type":"string","description":"` + strings.Repeat("s", 6500) + `"}}}`)
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", AllowedToolNames: []string{resource.OpenTool}, ExecutionSystemContext: "policy", ExecutionDynamicState: "active", StructuredOutputRequired: true, StructuredOutputSchema: schema}}
	loop.resources = &resourceViewFixture{view: resource.View{Context: "issued menu"}}
	if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeFailed || state.run.ErrorCode != "CONTEXT_BUILD_FAILED" {
		t.Fatal("oversized submission request admitted", outcome, state.run.ErrorCode)
	}
	if len(provider.Requests()) != 0 || len(state.calls) != 0 {
		t.Fatal("sent an oversized request or executed tools")
	}
}

func TestDerivedSubmissionFitsOnlyPreviewsAndPreservesProtectedInput(t *testing.T) {
	current := model.Message{Role: model.RoleUser, Content: "原目标全部保留，按已确认方法执行。", HostToolReferences: true}
	human := model.Message{Role: model.RoleTool, ToolCallID: "human", Content: strings.Repeat("我的研究决定不得删减", 200) + "最后的人类限制"}
	preview := model.Message{Role: model.RoleTool, ToolCallID: "read", Content: "EXACT-HEAD\n" + strings.Repeat("ordinary preview", 1600) + "\nEXACT-TAIL"}
	calls := []tool.Call{{ProviderCallID: "human", ToolName: userinput.ToolName, Result: &tool.Result{Status: tool.ResultSuccess}}, {ProviderCallID: "read", ToolName: "builtin.workspace.read_text", Result: &tool.Result{Status: tool.ResultSuccess}}}
	source := model.ChatRequest{Messages: []model.Message{current, human, preview}, Tools: []model.ToolDefinition{{Name: "read", InputSchema: json.RawMessage(`{}`)}}}
	before, _ := json.Marshal(source)
	derive := func(candidate model.ChatRequest) model.ChatRequest {
		return stageSubmissionRequest(candidate, submissionRepairSchema)
	}
	request, used, fits := fitDerivedRequest(source, calls, nil, 7000, derive)
	if !fits || used > 7000 || request.ForcedTool != stageSubmissionTool || request.Tools[0].Name != stageSubmissionTool {
		t.Fatal("submission did not fit within original window", used, fits)
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) || !reflect.DeepEqual(request.Messages[0], current) || !strings.Contains(request.Messages[1].Content, human.Content) || !strings.Contains(request.Messages[2].Content, "EXACT-HEAD") || !strings.Contains(request.Messages[2].Content, "EXACT-TAIL") || !strings.Contains(request.Messages[2].Content, "tool result truncated") {
		t.Fatal("source/input/human/schema mutated or preview lost explicit bounds")
	}
	if _, _, fits := fitDerivedRequest(source, calls, nil, 1000, derive); fits {
		t.Fatal("oversized human decision was silently truncated")
	}
}

func TestDerivedBudgetPreservesNativeItemsAndFitsTheirResultOnly(t *testing.T) {
	native := model.ProviderItem{Ordinal: 0, Type: "function_call", CallID: "read", Payload: json.RawMessage(`{"type":"function_call","call_id":"read","arguments":"{}"}`)}
	source := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "original research request"}}, ProviderTurns: []model.ProviderTurn{{TurnIndex: 1, Items: []model.ProviderItem{native}, ToolResults: []model.Message{{Role: model.RoleTool, ToolCallID: "read", Content: strings.Repeat("tool evidence ", 3000)}}}}}
	calls := []tool.Call{{ProviderCallID: "read", ToolName: "read", Result: &tool.Result{Status: tool.ResultSuccess}}}
	before, _ := json.Marshal(source)
	derive := func(r model.ChatRequest) model.ChatRequest {
		r.Messages = append(cloneModelMessages(r.Messages), model.Message{Role: model.RoleUser, Content: strings.Repeat("repair instructions", 100)})
		return r
	}
	request, used, fits := fitDerivedRequest(source, calls, nil, 5000, derive)
	after, _ := json.Marshal(source)
	if !fits || used > 5000 || string(before) != string(after) || !reflect.DeepEqual(request.ProviderTurns[0].Items, source.ProviderTurns[0].Items) || !strings.Contains(request.ProviderTurns[0].ToolResults[0].Content, "tool result truncated") {
		t.Fatal("native group/source mutated or final repair not budgeted", used, fits)
	}
}

func TestSubmissionKeepsHostPromptButLabelsHistoricalUserEvidence(t *testing.T) {
	host := model.Message{Role: model.RoleUser, Content: strings.Repeat("frozen ", 2000), HostToolReferences: true}
	old := model.Message{Role: model.RoleUser, Content: "historical request is not a current command"}
	source := model.ChatRequest{Messages: []model.Message{host, old}}
	request := stageSubmissionRequest(source, submissionRepairSchema)
	if !reflect.DeepEqual(request.Messages[0], host) || !strings.HasPrefix(request.Messages[1].Content, "Stage evidence (user)") || !strings.HasSuffix(request.Messages[1].Content, old.Content) {
		t.Fatal("submission changed frozen prompt or promoted historical evidence")
	}
}

func TestImageFallbackRechecksFinalTextBudget(t *testing.T) {
	for _, path := range []string{"placeholder", "rejection", "known-unsupported"} {
		t.Run(path, func(t *testing.T) {
			placeholder := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "I received [Unsupported Image] and cannot inspect pixels."}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
			answer := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "answer"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
			loop, state, provider := newLoopFixture(t, nil, placeholder, answer)
			state.run.ModelTurns = 1 // Preserve the persisted small window on resume.
			state.run.ContextBudgetTokens, state.run.AutoCompactTokenLimit = 4000, 3600
			payload := json.RawMessage(`{"attachmentId":"image-1","format":"image"}`)
			state.messages[0].Parts = append(state.messages[0].Parts, conversation.MessagePart{Type: "media", Payload: payload})
			original, _ := json.Marshal(state.messages[0])
			loop.images = &imageResolverFixture{part: model.ContentPart{Type: "input_image", MediaType: "image/png", Data: "AQID", AttachmentID: "image-1"}}
			fallback := &multimodalFallbackFixture{result: multimodal.Result{Text: strings.Repeat("visual facts ", 1000)}}
			loop.multimodal = fallback
			primary := &rejectImageOnceModel{inner: provider}
			if path == "rejection" {
				loop.models = protocolResolver{model: primary, protocol: modelcap.ProtocolOpenAIChat}
			} else if path == "known-unsupported" {
				fallback.state = multimodal.CapabilityUnsupported
			}
			if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeFailed || state.run.ErrorCode != "CONTEXT_BUILD_FAILED" {
				t.Fatalf("oversized fallback admitted: outcome=%s code=%s", outcome, state.run.ErrorCode)
			}
			requests := provider.Requests()
			if path == "rejection" {
				requests = primary.requests
			}
			wantRequests := 1
			if path == "known-unsupported" {
				wantRequests = 0
			}
			if len(requests) != wantRequests || fallback.calls != 1 || len(state.requestUsage) != 0 || len(state.calls) != 0 {
				t.Fatalf("invalid accounting or duplicate operations: requests=%d fallback=%d conversationUsage=%d tools=%d", len(requests), fallback.calls, len(state.requestUsage), len(state.calls))
			}
			after, _ := json.Marshal(state.messages[0])
			if string(original) != string(after) {
				t.Fatal("fallback budget changed original user/image input")
			}
		})
	}
}

func TestOptionalQuestionContinuationUsesResolvedLegacyBudget(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(questionExplanation+"\n\n"+capturedQuestionIntro), actualQuestionTurn())
	registerQuestionFixture(t, loop, state)
	// Old/resumed records can lack a persisted window; the builder supplies its
	// established default. A local zero must not disable an otherwise valid card.
	state.run.ModelTurns = 1
	state.run.ContextBudgetTokens, state.run.AutoCompactTokenLimit = 0, 0
	if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeWaitingApproval || state.run.Status != chat.RunWaitingUserInput || len(provider.Requests()) != 2 {
		t.Fatal("valid question handoff stopped because legacy run budget was zero", outcome, state.run.Status, len(provider.Requests()))
	}
}

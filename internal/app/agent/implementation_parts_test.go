package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

var partsFixtureSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["code","methodSummary","dependencies","analysisInput","researchChanges"],"properties":{"code":{"type":"string","minLength":1,"maxLength":120000},"methodSummary":{"type":"string","minLength":1},"dependencies":{"type":"array","items":{"type":"string"}},"analysisInput":{"type":"object"},"researchChanges":{"type":"array"}}}`)
var partsMetadata = json.RawMessage(`{"methodSummary":"keep the full agreed analysis","dependencies":[],"analysisInput":{},"researchChanges":[]}`)

type partsFixtureRuns struct {
	workflowContractRuns
	drafts      []chat.ImplementationDraft
	diagnostics []chat.ModelRecoveryFailure
	saveErr     bool
}

func (s *partsFixtureRuns) ImplementationDraft(context.Context, string) (chat.ImplementationDraft, bool, error) {
	if len(s.drafts) == 0 {
		return chat.ImplementationDraft{}, false, nil
	}
	return s.drafts[len(s.drafts)-1], true, nil
}
func (s *partsFixtureRuns) SaveImplementationDraft(_ context.Context, v chat.ImplementationDraft) error {
	if s.saveErr {
		return fmt.Errorf("injected checkpoint storage failure")
	}
	s.drafts = append(s.drafts, v)
	return nil
}
func (s *partsFixtureRuns) RecordModelRecoveryFailure(_ context.Context, v chat.ModelRecoveryFailure) error {
	s.diagnostics = append(s.diagnostics, v)
	return nil
}

func partScript(index int, code string, complete bool) []fake.Step {
	object := map[string]any{"index": index, "code": code, "complete": complete}
	if index == 1 {
		object["metadata"] = partsMetadata
	}
	arguments, _ := json.Marshal(object)
	return []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: fmt.Sprintf("part-%d", index), Name: implementationPartTool, Arguments: arguments}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
}

func bindParts(loop *Loop, state *loopState) *partsFixtureRuns {
	state.workflowAI = true
	contract := chat.WorkflowAIExecution{ID: "execution", NodeID: "method_implementation", WorkflowRunID: "workflow", WorkflowStepID: "step", Attempt: 1,
		PromptVersion: "dynamic-implementation-v6-impl-parts-v1", PromptSHA256: strings.Repeat("a", 64), InputSHA256: strings.Repeat("b", 64), OutputSchemaSHA256: strings.Repeat("c", 64), OutputSchema: partsFixtureSchema, AllowedTools: json.RawMessage(`[]`)}
	runs := &partsFixtureRuns{workflowContractRuns: workflowContractRuns{loopState: state, contract: contract, found: true}}
	loop.runs = runs
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowRunID: "workflow", WorkflowStepID: "step", StructuredOutputRequired: true, StructuredOutputSchema: partsFixtureSchema}}
	return runs
}

func TestImplementationPartsRetryOnlyUnfinishedUnit(t *testing.T) {
	broken := partScript(2, "SHOULD_NOT_BE_SAVED", true)
	broken[len(broken)-1] = fake.Step{Err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "fixture disconnected", Details: `{"code":"upstream_http2_stream_error","api_key":"never expose"}`, Retryable: true}}
	loop, state, provider := newLoopFixture(t, nil, partScript(1, "import json\n", false), broken, partScript(2, "result = {'verified': True}\nresult", true))
	loop.retryDelay = func(int) time.Duration { return 0 }
	runs := bindParts(loop, state)
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
		t.Fatal(got, state.run)
	}
	if len(runs.drafts) != 2 || !runs.drafts[1].Complete || len(state.calls) != 0 || len(provider.Requests()) != 3 {
		t.Fatal("wrong checkpoint/tool/request lifecycle", runs.drafts, state.calls, provider.Requests())
	}
	if strings.Contains(runs.drafts[1].Code, "SHOULD_NOT") || runs.drafts[1].Code != "import json\nresult = {'verified': True}\nresult" {
		t.Fatal("stitched failed source", runs.drafts)
	}
	requests := provider.Requests()
	if !strings.Contains(requests[1].Messages[len(requests[1].Messages)-1].Content, "import json") || !strings.Contains(string(implementationPartSchema(requests[1])), `"enum":[2]`) || string(implementationPartSchema(requests[1])) != string(implementationPartSchema(requests[2])) {
		t.Fatal("retry regenerated prefix")
	}
	if len(runs.diagnostics) != 1 || strings.Contains(runs.diagnostics[0].Details, "never expose") || !strings.Contains(runs.diagnostics[0].Details, "upstream_http2_stream_error") {
		t.Fatal("lost/unsafe per-attempt error", runs.diagnostics)
	}
	if !strings.Contains(state.messages[1].Parts[0].Text, "import json") || len(state.requestUsage) != 2 {
		t.Fatal("final assembly or accounting lost", state.messages, state.requestUsage)
	}
}

func TestImplementationCompleteCheckpointRevalidatesWithoutModelRequest(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	runs := bindParts(loop, state)
	runs.drafts = []chat.ImplementationDraft{{RunID: "run", ExecutionID: "execution", WorkflowRunID: "workflow", WorkflowStepID: "step", Attempt: 1, InputSHA256: runs.contract.InputSHA256, PromptSHA256: runs.contract.PromptSHA256, SchemaSHA256: runs.contract.OutputSchemaSHA256, GenerationID: "saved", PartCount: 2, Metadata: partsMetadata, Code: "result = {'verified': True}\nresult", Complete: true, SourceTurnIndex: 1}}
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 0 || len(state.requestUsage) != 0 || !strings.Contains(state.messages[1].Parts[0].Text, "verified") {
		t.Fatal(got, state.run)
	}
}

func TestImplementationCompleteCheckpointDoesNotNeedModelContext(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	runs := bindParts(loop, state)
	state.run.ModelTurns = 1
	state.run.ContextWindowTokens, state.run.ContextBudgetTokens, state.run.AutoCompactTokenLimit = 500, 450, 440
	state.messages[0].Parts[0].Text = strings.Repeat("frozen research evidence must remain\n", 500)
	runs.drafts = []chat.ImplementationDraft{{RunID: "run", ExecutionID: "execution", WorkflowRunID: "workflow", WorkflowStepID: "step", Attempt: 1, InputSHA256: runs.contract.InputSHA256, PromptSHA256: runs.contract.PromptSHA256, SchemaSHA256: runs.contract.OutputSchemaSHA256, GenerationID: "saved", PartCount: 2, Metadata: partsMetadata, Code: "result = {'verified': True}\nresult", Complete: true, SourceTurnIndex: 1}}
	host := &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), accepted: json.RawMessage(`{"methodSummary":"keep the full agreed analysis","dependencies":[],"analysisInput":{},"researchChanges":[],"code":"result = {'verified': True}\nresult"}`)}
	loop.research = host
	before := state.messages[0].Parts[0].Text
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 0 || len(state.requestUsage) != 0 || len(host.validated) != 1 || state.messages[0].Parts[0].Text != before {
		t.Fatal("local complete validation used model context or bypassed validation", got, state.run, len(host.validated))
	}
}

func TestImplementationCorrectionDiagnosticSurvivesPartialGenerationRestart(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	runs := bindParts(loop, state)
	runs.drafts = []chat.ImplementationDraft{{RunID: "run", ExecutionID: "execution", WorkflowRunID: "workflow", WorkflowStepID: "step", Attempt: 1, InputSHA256: runs.contract.InputSHA256, PromptSHA256: runs.contract.PromptSHA256, SchemaSHA256: runs.contract.OutputSchemaSHA256, GenerationID: "rejected", PartCount: 1, Metadata: partsMetadata, Code: "bad\n", Complete: true, Rejected: true, CorrectionCount: 1, ValidationError: "preserve paired IDs and correct the frozen test", SourceTurnIndex: 1}}
	p, err := loop.loadImplementationParts(context.Background(), state.run, runs.contract, true)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := p.consume(modelTurn{toolCalls: []model.ToolCall{{ID: "corrected-first", Name: implementationPartTool, Arguments: partScript(1, "import json\n", false)[0].Event.ToolCall.Arguments}}}, implementationPartSchema(p.request(model.ChatRequest{}, false)))
	if err != nil {
		t.Fatal(err)
	}
	runs.drafts = append(runs.drafts, next)
	restored, err := loop.loadImplementationParts(context.Background(), state.run, runs.contract, true)
	if err != nil || restored == nil || restored.draft.CorrectionCount != 1 || restored.validationError != "preserve paired IDs and correct the frozen test" {
		t.Fatal("correction lost after cancellation/restart", restored, err)
	}
	request := restored.request(model.ChatRequest{}, false)
	if !strings.Contains(request.Messages[len(request.Messages)-2].Content, "preserve paired IDs") {
		t.Fatal("continuation no longer knows why the full implementation failed", request)
	}
}

func TestImplementationSourceExchangePreservesNativeExplorationEvidence(t *testing.T) {
	for _, protocol := range []modelcap.APIProtocol{modelcap.ProtocolOpenAIResponses, modelcap.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			script := partScript(2, "result = {'ok': True}\nresult", true)
			item := model.ProviderItem{Ordinal: 0, Type: "function_call", CallID: "part-2", Payload: json.RawMessage(`{"type":"function_call","call_id":"part-2","name":"submit_implementation_part","arguments":"{}"}`)}
			if protocol == modelcap.ProtocolAnthropic {
				item.Type = "tool_use"
				item.Payload = json.RawMessage(`{"type":"tool_use","id":"part-2","name":"submit_implementation_part","input":{}}`)
			}
			script = append(script[:1], append([]fake.Step{{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &item}}}, script[1:]...)...)
			loop, state, provider := newLoopFixture(t, nil, script)
			runs := bindParts(loop, state)
			state.run.ModelTurns = 2
			state.run.APIProtocol = protocol
			loop.models = protocolResolver{model: provider, protocol: protocol}
			state.calls["completed-read"] = tool.Call{ID: "completed-read", RunID: "run", ProviderCallID: "read", ToolName: "builtin.fixture", Arguments: json.RawMessage(`{"query":"agreed method"}`), Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Text: "exact-paired-evidence-sentinel", Structured: json.RawMessage(`{}`)}}
			state.callOrder = append(state.callOrder, "completed-read")
			state.providerTurns = []model.ProviderTurn{{TurnIndex: 1, Protocol: protocol, Items: []model.ProviderItem{{Ordinal: 0, Type: "function_call", CallID: "read", Payload: json.RawMessage(`{"type":"function_call","call_id":"read","name":"builtin.fixture","arguments":"{\"query\":\"agreed method\"}"}`)}}}}
			runs.drafts = []chat.ImplementationDraft{{RunID: "run", ExecutionID: "execution", WorkflowRunID: "workflow", WorkflowStepID: "step", Attempt: 1, InputSHA256: runs.contract.InputSHA256, PromptSHA256: runs.contract.PromptSHA256, SchemaSHA256: runs.contract.OutputSchemaSHA256, GenerationID: "saved", PartCount: 1, Metadata: partsMetadata, Code: "import json\n", SourceTurnIndex: 2}}
			if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 1 {
				t.Fatal(got, state.run)
			}
			raw, _ := json.Marshal(provider.Requests()[0])
			if !strings.Contains(string(raw), "exact-paired-evidence-sentinel") || !strings.Contains(string(raw), "agreed method") || len(provider.Requests()[0].ProviderTurns) != 0 {
				t.Fatal("source-only exchange discarded completed exploration", string(raw))
			}
			if state.messages[0].Role != conversation.RoleUser || len(state.calls) != 1 {
				t.Fatal("evidence replay changed history or repeated the read")
			}
		})
	}
}

func TestImplementationControlHistoryIsNotReplayedAfterRestart(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, partScript(2, "result = {'ok':True}\nresult", true))
	runs := bindParts(loop, state)
	runs.drafts = []chat.ImplementationDraft{{RunID: "run", ExecutionID: "execution", WorkflowRunID: "workflow", WorkflowStepID: "step", Attempt: 1, InputSHA256: runs.contract.InputSHA256, PromptSHA256: runs.contract.PromptSHA256, SchemaSHA256: runs.contract.OutputSchemaSHA256, GenerationID: "saved", PartCount: 1, Metadata: partsMetadata, Code: "import json\n", SourceTurnIndex: 1}}
	state.providerTurns = []model.ProviderTurn{{TurnIndex: 1, Items: []model.ProviderItem{{Type: "function_call", Payload: json.RawMessage(`{"name":"submit_implementation_part","arguments":"{}"}`)}}}}
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 1 || len(provider.Requests()[0].ProviderTurns) != 0 {
		t.Fatal(got, state.run, provider.Requests())
	}
}

func TestImplementationSavedPrefixCannotReopenIOOrChangeMetadata(t *testing.T) {
	for _, script := range [][]fake.Step{
		{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "other", Name: "builtin.workspace.write", Arguments: json.RawMessage(`{"path":"a","content":"b"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}},
	} {
		loop, state, provider := newLoopFixture(t, nil, partScript(1, "import json\n", false), script)
		runs := bindParts(loop, state)
		if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || len(state.calls) != 0 || len(runs.drafts) != 1 || len(provider.Requests()) != 2 {
			t.Fatal(got, state.run)
		}
	}
}

func TestImplementationRejectedCheckpointKeepsCorrectionCountAfterRestart(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, partScript(1, "bad fourth\n", true))
	runs := bindParts(loop, state)
	runs.drafts = []chat.ImplementationDraft{{RunID: "run", ExecutionID: "execution", WorkflowRunID: "workflow", WorkflowStepID: "step", Attempt: 1, InputSHA256: runs.contract.InputSHA256, PromptSHA256: runs.contract.PromptSHA256, SchemaSHA256: runs.contract.OutputSchemaSHA256, GenerationID: "rejected", PartCount: 2, Metadata: partsMetadata, Code: "bad\n", Complete: true, Rejected: true, CorrectionCount: 2, ValidationError: "scientific contract still invalid", SourceTurnIndex: 1}}
	loop.research = &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "invalid full method"}}}
	if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != 1 {
		t.Fatal(got, state.run)
	}
	if !strings.Contains(provider.Requests()[0].Messages[len(provider.Requests()[0].Messages)-1].Content, "scientific contract still invalid") || len(provider.Requests()[0].Tools) != 1 {
		t.Fatal("lost correction diagnostic or reopened IO", provider.Requests())
	}
}

func TestImplementationPartContextOverflowDoesNotDropSavedSource(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, partScript(1, "a = 1\n", false), partScript(2, "result = {}\nresult", true))
	runs := bindParts(loop, state)
	loop.builder = NewContextBuilder(450)
	state.run.ModelTurns = 1
	state.run.ContextWindowTokens, state.run.ContextBudgetTokens, state.run.AutoCompactTokenLimit = 500, 450, 440
	runs.drafts = []chat.ImplementationDraft{{RunID: "run", ExecutionID: "execution", WorkflowRunID: "workflow", WorkflowStepID: "step", Attempt: 1, InputSHA256: runs.contract.InputSHA256, PromptSHA256: runs.contract.PromptSHA256, SchemaSHA256: runs.contract.OutputSchemaSHA256, GenerationID: "large", PartCount: 1, Metadata: partsMetadata, Code: strings.Repeat("# saved source must remain\n", 250), SourceTurnIndex: 1}}
	if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || len(provider.Requests()) != 0 || len(runs.drafts) != 1 {
		t.Fatal("overflow sent truncated source", got, state.run)
	}
}

func TestImplementationCheckpointFailureDoesNotAcceptOrExecute(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, partScript(1, "result = {'ok':True}\nresult", true))
	runs := bindParts(loop, state)
	runs.saveErr = true
	if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || state.run.ErrorCode != "IMPLEMENTATION_CHECKPOINT_SAVE_FAILED" || len(provider.Requests()) != 1 || len(state.calls) != 0 || len(runs.drafts) != 0 {
		t.Fatal(got, state.run)
	}
}

func TestImplementationRejectsBadOrderingMetadataAndMixedTools(t *testing.T) {
	for _, mode := range []string{"wrong_index", "no_newline", "extra_metadata", "mixed_tools", "empty", "too_large"} {
		t.Run(mode, func(t *testing.T) {
			loop, state, _ := newLoopFixture(t, nil)
			runs := bindParts(loop, state)
			p, err := loop.loadImplementationParts(context.Background(), state.run, runs.contract, true)
			if err != nil {
				t.Fatal(err)
			}
			r := p.request(model.ChatRequest{}, false)
			args := map[string]any{"index": 1, "metadata": partsMetadata, "code": "a = 1\n", "complete": false}
			switch mode {
			case "wrong_index":
				args["index"] = 2
			case "no_newline":
				args["code"] = "a = 1"
			case "extra_metadata":
				args["metadata"] = json.RawMessage(`{"code":"not allowed"}`)
			case "empty":
				args["code"] = "\n"
			case "too_large":
				args["code"] = strings.Repeat("a", 120001) + "\n"
			}
			raw, _ := json.Marshal(args)
			turn := modelTurn{toolCalls: []model.ToolCall{{ID: "one", Name: implementationPartTool, Arguments: raw}}}
			if mode == "mixed_tools" {
				turn.toolCalls = append(turn.toolCalls, model.ToolCall{ID: "two", Name: "builtin.workspace.write"})
			}
			if _, _, err = p.consume(turn, implementationPartSchema(r)); err == nil || p.draft.PartCount != 0 {
				t.Fatal("accepted invalid part", mode)
			}
		})
	}
}

func TestImplementationFullValidatorAndCorrectionBudgetRemain(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, partScript(1, "bad\n", true), partScript(1, "still bad\n", true), partScript(1, "bad third\n", true))
	runs := bindParts(loop, state)
	host := &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), errors: []error{
		&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "full frozen method check failed"},
		&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "full frozen method check failed"},
		&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "full frozen method check failed"},
	}}
	loop.research = host
	if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != 3 || len(host.validated) != 3 {
		t.Fatal(got, state.run, len(host.validated))
	}
	if len(runs.drafts) != 5 || !runs.drafts[1].Rejected || !runs.drafts[3].Rejected || runs.drafts[4].CorrectionCount != 2 {
		t.Fatal("correction checkpoint budget lost", runs.drafts)
	}
}

func TestImplementationPlainCandidateCorrectionBudgetPersists(t *testing.T) {
	rejected := "```json\n" + string(partsMetadata) + "\n```\n```python\nbad\n```"
	loop, state, provider := newLoopFixture(t, nil, submissionScript(rejected), partScript(1, "import json\n", false), []fake.Step{{Err: context.Canceled}})
	runs := bindParts(loop, state)
	host := &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "invalid scientific computation"}}}
	loop.research = host
	if got := loop.Run(context.Background(), "run"); got != OutcomeCancelled || len(runs.drafts) != 2 || len(host.validated) != 1 || len(provider.Requests()) != 3 {
		t.Fatal(got, state.run, len(runs.drafts), len(host.validated))
	}
	if !runs.drafts[0].Rejected || runs.drafts[0].PartCount != 0 || runs.drafts[1].CorrectionCount != 1 || !strings.Contains(runs.drafts[1].ValidationError, "invalid scientific computation") {
		t.Fatal("legacy response reset the durable correction budget", runs.drafts)
	}
}

func TestImplementationProtocolViolationStillAccountsCompletedRequest(t *testing.T) {
	violating := []fake.Step{
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "invented", Name: "builtin.workspace.write", Arguments: json.RawMessage(`{"path":"a","content":"b"}`)}}},
		{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 1111, OutputTokens: 222}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	loop, state, provider := newLoopFixture(t, nil, partScript(1, "import json\n", false), violating)
	bindParts(loop, state)
	if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || state.run.ErrorCode != "IMPLEMENTATION_PART_MISSING" || len(state.calls) != 0 || len(state.requestUsage) != len(provider.Requests()) || state.run.InputTokens != 1111 || state.run.OutputTokens != 222 {
		t.Fatal("completed response lost usage or executed invalid IO", got, state.run, state.requestUsage)
	}
	if !strings.Contains(state.journalDrafts[2], "invented") {
		t.Fatal("invalid completed response lost immutable audit", state.journalDrafts)
	}
}

func TestImplementationMixedAndDuplicateCallsKeepAudit(t *testing.T) {
	for _, mode := range []string{"mixed", "duplicate_id"} {
		t.Run(mode, func(t *testing.T) {
			script := partScript(1, "# ORIGINAL-SOURCE-SENTINEL\nresult = {'ok': True}\nresult", true)
			other := model.ToolCall{ID: "unexpected", Name: "builtin.workspace.write", Arguments: json.RawMessage(`{"path":"a","content":"b"}`)}
			if mode == "duplicate_id" {
				other.ID = script[0].Event.ToolCall.ID
			}
			script = append(script[:1], append([]fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &other}}, {Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 1000, OutputTokens: 200}}}}, script[1:]...)...)
			loop, state, provider := newLoopFixture(t, nil, script)
			runs := bindParts(loop, state)
			if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || len(runs.drafts) != 0 || len(state.calls) != 0 || len(provider.Requests()) != 1 {
				t.Fatal("invalid controls became a checkpoint or IO", got, state.run)
			}
			if !strings.Contains(state.journalDrafts[1], "ORIGINAL-SOURCE-SENTINEL") || !strings.Contains(state.journalDrafts[1], "builtin.workspace.write") || len(state.requestUsage) != 1 || state.run.InputTokens != 1000 {
				t.Fatal("completed rejected response lost source, calls or usage", state.journalDrafts, state.requestUsage)
			}
		})
	}
}

func TestImplementationIncompleteNativeResponseCannotCheckpoint(t *testing.T) {
	args := string(partScript(1, "result = {'ok': True}\nresult", true)[0].Event.ToolCall.Arguments)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []any{
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "reasoning", "id": "unfinished-reasoning"}},
			map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"type": "function_call", "id": "item-part", "call_id": "part-call", "name": implementationPartTool, "arguments": args}},
			map[string]any{"type": "response.incomplete", "response": map[string]any{"status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"}, "usage": map[string]any{"input_tokens": 1000, "output_tokens": 20}}},
		} {
			raw, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
	}))
	defer server.Close()
	loop, state, _ := newLoopFixture(t, nil)
	runs := bindParts(loop, state)
	loop.models = protocolResolver{model: responses.New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil), protocol: modelcap.ProtocolOpenAIResponses}
	if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || len(runs.drafts) != 0 || len(state.calls) != 0 {
		t.Fatal("incomplete native response became recoverable or executable source", got, len(runs.drafts), state.run)
	}
}

func TestImplementationControlRequiresNativeProtocolState(t *testing.T) {
	for _, protocol := range []modelcap.APIProtocol{modelcap.ProtocolOpenAIResponses, modelcap.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, partScript(1, "result = {'ok': True}\nresult", true))
			runs := bindParts(loop, state)
			loop.models = protocolResolver{model: provider, protocol: protocol}
			if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || state.run.ErrorCode != "MODEL_PROTOCOL_STATE_MISSING" || len(runs.drafts) != 0 {
				t.Fatal("source branch bypassed native integrity guard", got, state.run)
			}
		})
	}
}

func TestImplementationLegacyRejectionKeepsEvidenceBeforeRepairPart(t *testing.T) {
	rejected := "```json\n" + string(partsMetadata) + "\n```\n```python\nbad\n```"
	loop, state, _ := newLoopFixture(t, nil, submissionScript(rejected), []fake.Step{{Err: context.Canceled}})
	runs := bindParts(loop, state)
	state.calls["completed-read"] = tool.Call{ID: "completed-read", RunID: "run", ProviderCallID: "read", ToolName: "builtin.fixture", Arguments: json.RawMessage(`{"query":"frozen method"}`), Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Text: "READ-ONCE-EVIDENCE-SENTINEL", Structured: json.RawMessage(`{}`)}}
	state.callOrder = append(state.callOrder, "completed-read")
	loop.research = &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "invalid scientific computation"}}}
	if got := loop.Run(context.Background(), "run"); got != OutcomeCancelled || len(runs.drafts) != 1 || !runs.drafts[0].Rejected || runs.drafts[0].PartCount != 0 {
		t.Fatal("legacy rejection fixture failed", got, state.run, len(runs.drafts))
	}
	marker := runs.drafts[0]
	if len(marker.SourceEvidence) != 1 || !strings.Contains(marker.SourceEvidence[0].Result, "READ-ONCE-EVIDENCE-SENTINEL") {
		t.Fatal("cancel before first repair part lost completed evidence", marker.SourceEvidence)
	}
	p, err := loop.loadImplementationParts(context.Background(), state.run, runs.contract, true)
	if err != nil {
		t.Fatal(err)
	}
	source, calls := p.withSavedEvidence(model.ChatRequest{}, nil)
	request, err := fitFinalRequest(source, calls, nil, 100_000, func(r model.ChatRequest) model.ChatRequest { return p.request(r, false) })
	raw, _ := json.Marshal(request)
	if err != nil || !strings.Contains(string(raw), "READ-ONCE-EVIDENCE-SENTINEL") || p.draft.CorrectionCount != 1 || len(request.Tools) != 1 || len(request.ProviderTurns) != 0 {
		t.Fatal("repair marker did not resume a source-only evidence exchange", err, string(raw))
	}
}

func TestImplementationSavedEvidencePreservesRunIdentityAndFitsPreview(t *testing.T) {
	old := model.CompletedToolEvidence{SourceRunID: "old-run", CallID: "read", ToolName: "builtin.workspace.read_text", Arguments: json.RawMessage("{\n  \"path\": \"old.txt\"\n}"), Result: strings.Repeat("earlier-result ", 5000)}
	p := &implementationParts{draft: chat.ImplementationDraft{RunID: "new-run", SourceEvidence: []model.CompletedToolEvidence{old}}}
	source := model.ChatRequest{Messages: []model.Message{
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "read", Name: "builtin.workspace.read_text", Arguments: json.RawMessage(`{"path":"new.txt"}`)}}},
		{Role: model.RoleTool, ToolCallID: "read", Content: "new-run-result"},
	}}
	current := []tool.Call{{RunID: "new-run", ProviderCallID: "read", ToolName: "builtin.workspace.read_text", Result: &tool.Result{Status: tool.ResultSuccess}}}
	before, _ := json.Marshal(p.draft)
	source, calls := p.withSavedEvidence(source, current)
	request, err := fitFinalRequest(source, calls, nil, 5000, func(r model.ChatRequest) model.ChatRequest {
		r.CompletedToolEvidence = completedToolEvidence(r, p.draft.RunID)
		return stageSubmissionEvidence(r)
	})
	if err != nil || len(request.CompletedToolEvidence) != 2 || len(request.ProviderTurns) != 0 {
		t.Fatal("old/current equal call ID collided or preview could not fit", err, len(request.CompletedToolEvidence))
	}
	if request.CompletedToolEvidence[0].SourceRunID != "old-run" || string(request.CompletedToolEvidence[0].Arguments) != `{"path":"old.txt"}` || !strings.Contains(request.CompletedToolEvidence[0].Result, "truncated") || request.CompletedToolEvidence[1].SourceRunID != "new-run" || request.CompletedToolEvidence[1].Result != "new-run-result" {
		t.Fatal("saved evidence identity or admitted preview changed", request.CompletedToolEvidence)
	}
	after, _ := json.Marshal(p.draft)
	if string(before) != string(after) {
		t.Fatal("fitting previews rewrote the immutable evidence")
	}
	for _, message := range request.Messages {
		if message.Role == model.RoleTool || len(message.ToolCalls) > 0 {
			t.Fatal("saved evidence became an executable/native call")
		}
	}
}

func TestImplementationTransportAllowanceDoesNotExtendExploration(t *testing.T) {
	scripts := make([][]fake.Step, 0, 50)
	for i := 0; i < 49; i++ {
		scripts = append(scripts, []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: fmt.Sprintf("read-%d", i), Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"next missing fact"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}})
	}
	scripts = append(scripts, partScript(1, "result = {'ok': True}\nresult", true))
	loop, state, provider := newLoopFixture(t, nil, scripts...)
	runs := bindParts(loop, state)
	runs.contract.AllowedTools = json.RawMessage(`["builtin.fixture"]`)
	loop.research.(*staticResearchGuidance).value.AllowedToolNames = []string{"builtin.fixture"}
	if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_TURN_LIMIT" || len(state.calls) != maxWorkflowAIModelTurns || len(provider.Requests()) != maxWorkflowAIModelTurns {
		t.Fatal("source part allowance extended exploratory turns", got, state.run, len(state.calls), len(provider.Requests()))
	}
}

func TestImplementationFailedPartialArgumentDiagnosticUsesRealAdapters(t *testing.T) {
	for _, protocol := range []modelcap.APIProtocol{modelcap.ProtocolOpenAIChat, modelcap.ProtocolOpenAIResponses, modelcap.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			delta := `{"index":1,"code":"` + strings.Repeat("# 已接收但未完成的源码", 100)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				var events []any
				switch protocol {
				case modelcap.ProtocolOpenAIChat:
					events = []any{map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "part", "type": "function", "function": map[string]any{"name": implementationPartTool, "arguments": delta}}}}}}}, map[string]any{"error": map[string]any{"code": "upstream_http2_stream_error", "message": "Upstream HTTP/2 stream failed"}}}
				case modelcap.ProtocolOpenAIResponses:
					events = []any{map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "item-part", "call_id": "part", "name": implementationPartTool, "arguments": ""}}, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "item-part", "output_index": 0, "delta": delta}, map[string]any{"type": "error", "code": "upstream_http2_stream_error", "message": "Upstream HTTP/2 stream failed"}}
				case modelcap.ProtocolAnthropic:
					events = []any{map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "part", "name": implementationPartTool, "input": map[string]any{}}}, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": delta}}, map[string]any{"type": "error", "error": map[string]any{"type": "overloaded_error", "message": "Upstream HTTP/2 stream failed"}}}
				}
				for _, event := range events {
					raw, _ := json.Marshal(event)
					fmt.Fprintf(w, "data: %s\n\n", raw)
				}
			}))
			defer server.Close()
			loop, state, _ := newLoopFixture(t, nil)
			runs := bindParts(loop, state)
			profile := modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}
			var client model.ChatModel
			switch protocol {
			case modelcap.ProtocolOpenAIChat:
				client = openai.New(profile, nil)
			case modelcap.ProtocolOpenAIResponses:
				client = responses.New(profile, nil)
			default:
				client = anthropic.New(profile, nil)
			}
			loop.models = protocolResolver{model: client, protocol: protocol}
			loop.retryDelay = func(int) time.Duration { return 0 }
			if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || len(runs.diagnostics) != 6 || len(runs.drafts) != 0 || len(state.calls) != 0 {
				t.Fatal(got, state.run, len(runs.diagnostics), len(runs.drafts))
			}
			if runs.diagnostics[0].PartialChars != utf8.RuneCountInString(delta) || strings.Contains(runs.diagnostics[0].Details, "已接收但未完成的源码") {
				t.Fatal("wrong partial count or leaked argument source", runs.diagnostics[0])
			}
		})
	}
}

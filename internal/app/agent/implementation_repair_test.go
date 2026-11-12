package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func invalidPartScript(index int, code string, missingDependency bool) []fake.Step {
	script := partScript(index, code, false)
	if missingDependency {
		var args map[string]any
		_ = json.Unmarshal(script[0].Event.ToolCall.Arguments, &args)
		delete(args["metadata"].(map[string]any), "dependencies")
		script[0].Event.ToolCall.Arguments, _ = json.Marshal(args)
	}
	return script
}

func TestImplementationMissingDependencyCorrectsSamePartWithoutExecutingOrResetting(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, invalidPartScript(1, "# REJECTED-SOURCE\n", true), partScript(1, "import json\n", false), invalidPartScript(3, "# WRONG-INDEX\n", false), partScript(2, "result = {'verified': True}\nresult", true))
	runs := bindParts(loop, state)
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
		t.Fatal(got, state.run)
	}
	if len(provider.Requests()) != 4 || len(state.calls) != 0 || len(runs.drafts) != 4 {
		t.Fatal("correction restarted tools/stage", len(provider.Requests()), state.calls, len(runs.drafts))
	}
	if runs.drafts[0].PartCount != 0 || runs.drafts[0].Code != "" || runs.drafts[0].PartCorrectionCount != 1 || runs.drafts[1].PartCount != 1 || runs.drafts[2].PartCount != 1 || runs.drafts[2].Code != "import json\n" || runs.drafts[3].Code != "import json\nresult = {'verified': True}\nresult" || runs.drafts[3].PartCorrectionCount != 0 || runs.drafts[3].CorrectionCount != 0 {
		t.Fatal(runs.drafts)
	}
	for i, want := range map[int]string{1: "$.metadata.dependencies", 3: "index"} {
		r := provider.Requests()[i]
		found := false
		for _, message := range r.Messages {
			found = found || strings.Contains(message.Content, "Diagnostic: IMPLEMENTATION_PART_INVALID: $."+strings.TrimPrefix(want, "$."))
		}
		if len(r.Tools) != 1 || !found {
			t.Fatal("no focused correction diagnostic", i, want)
		}
	}
	if !strings.Contains(state.journalDrafts[1], "REJECTED-SOURCE") || strings.Contains(state.messages[1].Parts[0].Text, "REJECTED-SOURCE") {
		t.Fatal("rejected payload became source or audit disappeared")
	}
}

func TestImplementationPartFormatBudgetStopsAndNeverAcceptsRejectedCode(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, invalidPartScript(1, "# rejected\n", true), invalidPartScript(1, "# rejected again\n", true), invalidPartScript(1, "# rejected third\n", true))
	runs := bindParts(loop, state)
	if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || state.run.ErrorCode != "IMPLEMENTATION_PART_REPAIR_EXHAUSTED" || len(provider.Requests()) != 3 || len(runs.drafts) != 2 || runs.drafts[1].PartCorrectionCount != 2 || len(state.calls) != 0 {
		t.Fatal(got, state.run, runs.drafts)
	}
}

func TestImplementationPlainMissingControlCanBeCorrectedButCannotReopenIO(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, partScript(1, "import json\n", false), submissionScript("I will submit later"), partScript(2, "result = {'ok': True}\nresult", true))
	runs := bindParts(loop, state)
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 3 || len(state.calls) != 0 || len(runs.drafts) != 3 || runs.drafts[1].Code != "import json\n" {
		t.Fatal(got, state.run, runs.drafts)
	}
}

func bindTechnicalRepair(t *testing.T, loop *Loop, state *loopState, base string) *partsFixtureRuns {
	t.Helper()
	runs := bindParts(loop, state)
	var prior map[string]any
	_ = json.Unmarshal(partsMetadata, &prior)
	prior["code"] = base
	input, _ := json.Marshal(map[string]any{"_pythonRepair": map[string]any{"priorImplementation": prior, "failureSummary": "KeyError: label_counts"}})
	runs.contract.Input = input
	runs.contract.InputSHA256 = chat.ImplementationCodeSHA256(string(input))
	runs.contract.PromptVersion += "-" + chat.ImplementationPatchVersion
	return runs
}

func patchScript(base string, old string, new string, declare bool) []fake.Step {
	args := map[string]any{"baseCodeSha256": chat.ImplementationCodeSHA256(base), "replacements": []any{map[string]any{"old": old, "new": new}}}
	if declare {
		args["researchChanges"] = []any{}
	}
	raw, _ := json.Marshal(args)
	return []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "patch", Name: implementationPatchTool, Arguments: raw}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
}

func TestImplementationTechnicalPatchMissingFieldCorrectsWithoutRewritingMetadata(t *testing.T) {
	base := "audit = {'split_label_counts': {'train': 10}}\nresult = audit['label_counts']\nresult"
	loop, state, provider := newLoopFixture(t, nil, patchScript(base, "audit['label_counts']", "audit['split_label_counts']", false), patchScript(base, "audit['label_counts']", "audit['split_label_counts']", true))
	runs := bindTechnicalRepair(t, loop, state, base)
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 2 || len(state.calls) != 0 || len(runs.drafts) != 2 {
		t.Fatal(got, state.run, runs.drafts)
	}
	last := runs.drafts[1]
	var before, after any
	_ = json.Unmarshal(partsMetadata, &before)
	_ = json.Unmarshal(last.Metadata, &after)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if !last.Complete || last.SourcePatch == nil || string(a) != string(b) || !strings.Contains(last.Code, "result = audit['split_label_counts']") || runs.drafts[0].Code != "" || len(provider.Requests()[1].Tools) != 2 {
		t.Fatal("patch dropped metadata or used rejected edits", string(last.Metadata), last.Code)
	}
	if !strings.Contains(provider.Requests()[1].Messages[len(provider.Requests()[1].Messages)-1].Content, "researchChanges") {
		t.Fatal("missing precise patch correction")
	}
}

func TestImplementationPatchDisconnectRetriesOnlySmallPayloadAndPreservesBase(t *testing.T) {
	base := "result = missing\nresult"
	broken := patchScript(base, "missing", "1", true)
	broken[len(broken)-1] = fake.Step{Err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "disconnect", Retryable: true}}
	loop, state, provider := newLoopFixture(t, nil, broken, patchScript(base, "missing", "1", true))
	loop.retryDelay = func(int) time.Duration { return 0 }
	runs := bindTechnicalRepair(t, loop, state, base)
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 2 || len(runs.drafts) != 1 || runs.drafts[0].SourcePatch == nil || len(runs.diagnostics) != 1 {
		t.Fatal(got, state.run, runs.drafts)
	}
}

func TestImplementationFullValidationStillRejectsTechnicalPatch(t *testing.T) {
	base := "result = missing\nresult"
	loop, state, provider := newLoopFixture(t, nil, patchScript(base, "missing", "1", true), partScript(1, "result = {'ok': True}\nresult", true))
	runs := bindTechnicalRepair(t, loop, state, base)
	host := &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), accepted: json.RawMessage(`{"code":"corrected","methodSummary":"unchanged","dependencies":[],"analysisInput":{},"researchChanges":[]}`), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "scientific contract must remain intact"}}}
	loop.research = host
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(host.validated) != 2 || len(provider.Requests()) != 2 || len(runs.drafts) != 3 || !runs.drafts[1].Rejected {
		t.Fatal(got, state.run, runs.drafts)
	}
	if len(provider.Requests()[1].Tools) != 1 || provider.Requests()[1].Tools[0].Name != implementationPartTool {
		t.Fatal("rejected source allowed patch resurrection", provider.Requests()[1])
	}
}

func TestImplementationCurrentPartCorrectionSurvivesResume(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	runs := bindParts(loop, state)
	p, err := loop.loadImplementationParts(context.Background(), state.run, runs.contract, true)
	if err != nil {
		t.Fatal(err)
	}
	v := p.formatCorrection("rejected payload", "$.metadata.dependencies: required property is missing", implementationPartTool)
	v.SourceTurnIndex, v.UpdatedAt = 1, time.Now()
	if err := runs.SaveImplementationDraft(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	restored, err := loop.loadImplementationParts(context.Background(), state.run, runs.contract, true)
	if err != nil || restored.draft.PartCorrectionCount != 1 || restored.draft.GenerationID != p.draft.GenerationID {
		t.Fatal(restored, err)
	}
	r := restored.request(model.ChatRequest{}, false)
	if r.ForcedTool != implementationPartTool || !strings.Contains(r.Messages[len(r.Messages)-1].Content, "$.metadata.dependencies") {
		t.Fatal(r)
	}
}

func TestImplementationTechnicalRepairCanFallBackToFullSourceWithChangedRequirements(t *testing.T) {
	base := "result = missing\nresult"
	loop, state, provider := newLoopFixture(t, nil, patchScript(base, "missing", "1", false), partScript(1, "result = {'ok': True}\nresult", true))
	runs := bindTechnicalRepair(t, loop, state, base)
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 2 || runs.drafts[1].SourcePatch != nil || runs.drafts[1].PartCorrectionCount != 0 {
		t.Fatal(got, state.run, runs.drafts)
	}
}

func TestImplementationOptionalPatchDoesNotBlockCustomMetadataContract(t *testing.T) {
	base := "result = missing\nresult"
	script := partScript(1, "result = {'ok': True}\nresult", true)
	var payload map[string]any
	_ = json.Unmarshal(script[0].Event.ToolCall.Arguments, &payload)
	var metadata map[string]any
	_ = json.Unmarshal(partsMetadata, &metadata)
	metadata["assumptions"] = []string{"supplied for this contract"}
	payload["metadata"] = metadata
	script[0].Event.ToolCall.Arguments, _ = json.Marshal(payload)
	loop, state, provider := newLoopFixture(t, nil, script)
	runs := bindTechnicalRepair(t, loop, state, base)
	var schema map[string]any
	_ = json.Unmarshal(partsFixtureSchema, &schema)
	schema["required"] = append(schema["required"].([]any), "assumptions")
	schema["properties"].(map[string]any)["assumptions"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	runs.contract.OutputSchema, _ = json.Marshal(schema)
	loop.research.(*staticResearchGuidance).value.StructuredOutputSchema = runs.contract.OutputSchema
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 1 || len(runs.drafts) != 1 || runs.drafts[0].SourcePatch != nil {
		t.Fatal("optional patch became a stage gate", got, state.run, runs.drafts)
	}
	for _, def := range provider.Requests()[0].Tools {
		if def.Name == implementationPatchTool {
			t.Fatal("offered a patch that would silently bypass new required metadata")
		}
	}
	if !strings.Contains(string(runs.drafts[0].Metadata), "supplied for this contract") || len(state.calls) != 0 {
		t.Fatal("custom metadata lost or extra IO executed", runs.drafts[0])
	}
}

func TestImplementationFullRejectionThenFormatCorrectionDoesNotReprojectFrozenEvidence(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil, invalidPartScript(1, "# rejected source\n", true), partScript(1, "result = {'ok': True}\nresult", true))
	runs := bindParts(loop, state)
	evidence := []model.CompletedToolEvidence{{SourceRunID: "old-run", CallID: "read", ToolName: "builtin.workspace.read_text", Arguments: json.RawMessage(`{ "path": "materials.txt" }`), Result: "READ-ONCE-FROZEN"}}
	runs.drafts = []chat.ImplementationDraft{{RunID: "run", ExecutionID: "execution", WorkflowRunID: "workflow", WorkflowStepID: "step", Attempt: 1, InputSHA256: runs.contract.InputSHA256, PromptSHA256: runs.contract.PromptSHA256, SchemaSHA256: runs.contract.OutputSchemaSHA256, GenerationID: "rejected", PartCount: 1, Metadata: partsMetadata, Code: "bad\n", Complete: true, Rejected: true, CorrectionCount: 1, ValidationError: "correct the scientific implementation", SourceTurnIndex: 1, SourceEvidence: evidence}}
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(runs.drafts) != 3 || runs.drafts[1].CorrectionCount != 1 || runs.drafts[1].PartCorrectionCount != 1 {
		t.Fatal(got, state.run, runs.drafts)
	}
	for _, draft := range runs.drafts[1:] {
		if len(draft.SourceEvidence) != 1 || string(draft.SourceEvidence[0].Arguments) != string(evidence[0].Arguments) || draft.SourceEvidence[0].SourceRunID != "old-run" {
			t.Fatal("frozen evidence was recomputed after complete-candidate rejection", draft.SourceEvidence)
		}
	}
}

func TestImplementationTechnicalPatchFormatCorrectionWithRealProtocolAdapters(t *testing.T) {
	for _, protocol := range []modelcap.APIProtocol{modelcap.ProtocolOpenAIChat, modelcap.ProtocolOpenAIResponses, modelcap.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			base := "result = missing\nresult"
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests++
				if !strings.Contains(string(body), implementationPatchTool) {
					t.Error("patch tool not offered")
				}
				if requests == 2 && !strings.Contains(string(body), "$.researchChanges") {
					t.Error("patch format correction not sent")
				}
				script := patchScript(base, "missing", "1", requests != 1)
				args := string(script[0].Event.ToolCall.Arguments)
				w.Header().Set("Content-Type", "text/event-stream")
				switch protocol {
				case modelcap.ProtocolOpenAIChat:
					event, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("patch-%d", requests), "type": "function", "function": map[string]any{"name": implementationPatchTool, "arguments": args}}}}, "finish_reason": "tool_calls"}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", event)
				case modelcap.ProtocolOpenAIResponses:
					item := map[string]any{"type": "function_call", "id": fmt.Sprintf("item-%d", requests), "call_id": fmt.Sprintf("patch-%d", requests), "name": implementationPatchTool, "arguments": args}
					for _, event := range []any{map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}, map[string]any{"type": "response.completed", "response": map[string]any{"id": "response", "status": "completed", "output": []any{item}}}} {
						raw, _ := json.Marshal(event)
						fmt.Fprintf(w, "data: %s\n\n", raw)
					}
				default:
					var input any
					_ = json.Unmarshal([]byte(args), &input)
					for _, event := range []any{map[string]any{"type": "message_start", "message": map[string]any{"id": "message", "type": "message", "role": "assistant", "content": []any{}}}, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("patch-%d", requests), "name": implementationPatchTool, "input": input}}, map[string]any{"type": "content_block_stop", "index": 0}, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}}, map[string]any{"type": "message_stop"}} {
						raw, _ := json.Marshal(event)
						fmt.Fprintf(w, "data: %s\n\n", raw)
					}
				}
			}))
			defer server.Close()
			loop, state, _ := newLoopFixture(t, nil)
			runs := bindTechnicalRepair(t, loop, state, base)
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
			if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || requests != 2 || len(runs.drafts) != 2 || runs.drafts[1].SourcePatch == nil || len(state.calls) != 0 {
				t.Fatal(got, state.run, requests, runs.drafts)
			}
			if requests != 2 || len(state.providerTurns) > 2 {
				t.Fatal("control replay corrupted protocol", requests)
			}
		})
	}
}

// Mirrors the observed failure shapes without embedding private research code.
func TestImplementationRepairRequiresCompleteNestedPayload(t *testing.T) {
	misplaced := invalidPartScript(1, "# keep this unsaved code\n", true)
	var args map[string]any
	_ = json.Unmarshal(misplaced[0].Event.ToolCall.Arguments, &args)
	delete(args, "code")
	args["dependencies"] = []string{}
	misplaced[0].Event.ToolCall.Arguments, _ = json.Marshal(args)
	loop, state, provider := newLoopFixture(t, nil, invalidPartScript(1, "result = 1\n", true), misplaced, partScript(1, "result = 1\nresult", true))
	runs := bindParts(loop, state)
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
		t.Fatal(got, state.run)
	}
	if len(provider.Requests()) != 3 || runs.drafts[0].Code != "" || runs.drafts[1].Code != "" || runs.drafts[2].Code != "result = 1\nresult" {
		t.Fatal("rejected code adopted or restart", runs.drafts)
	}
	for i := 1; i < 3; i++ {
		encoded, _ := json.Marshal(provider.Requests()[i])
		for _, want := range []string{"ENTIRE current unsaved", "include code again", "nested inside metadata"} {
			if !strings.Contains(string(encoded), want) {
				t.Fatal("missing repair guidance", want)
			}
		}
	}
	diagnostic := runs.drafts[1].PartValidationError
	for _, want := range []string{"$.code", "$.metadata.dependencies", "NOT at the argument root"} {
		if !strings.Contains(diagnostic, want) {
			t.Fatal("incomplete diagnostics", diagnostic)
		}
	}
}

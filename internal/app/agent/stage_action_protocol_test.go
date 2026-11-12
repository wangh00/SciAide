package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestStageActionOffersRealSubmissionBeforeAnyExploration(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionToolScript(`{"items":["direct result"],"state":"ready"}`))
	bindReadConvergenceFixture(t, loop, state)
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
		t.Fatal("explicit first-turn submission was treated as IO", got, state.run.ErrorCode)
	}
	requests := provider.Requests()
	if len(requests) != 1 || requests[0].ForcedTool != "" || requests[0].RequireTool || requests[0].DisableTools {
		t.Fatal("exploration completion was forced or unavailable")
	}
	found := false
	for _, d := range requests[0].Tools {
		found = found || d.Name == stageSubmissionTool
	}
	if !found || len(state.calls) != 0 {
		t.Fatal("explicit submit was not advertised or was executed as research IO")
	}
}

func TestPlannerActionIsTypedWithoutForcingSubmissionOrChangingFrozenSchema(t *testing.T) {
	source := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "design only; no external lookup"}}, Tools: []model.ToolDefinition{{Name: "builtin.skill.load", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	got := plannerExplorationRequest(source, earlyPlannerSubmissionSchema)
	if !got.RequireTool || got.ForcedTool != "" || got.DisableTools || len(got.Tools) != 2 || got.Tools[0].Name != "builtin.skill.load" || got.Tools[1].Name != stageSubmissionTool || string(got.Tools[1].InputSchema) != string(earlyPlannerSubmissionSchema) {
		t.Fatal("typed planning contract locked Skill IO or changed the output schema", got)
	}
	if len(source.Messages) != 1 || len(source.Tools) != 1 || !strings.Contains(submissionRequestText(got), "does not waive actual Skill loading") {
		t.Fatal("planning protocol mutated its source or waived a real prerequisite")
	}
	limit := currentRequestBudgetTokens(source, nil)
	_, err := fitFinalRequest(source, nil, nil, limit, func(r model.ChatRequest) model.ChatRequest {
		return plannerExplorationRequest(r, earlyPlannerSubmissionSchema)
	})
	if err == nil {
		t.Fatal("typed planner schema was added after the final context-budget check")
	}
}

func TestStageActionOptionalControlNeverDisplacesAdmittedEvidence(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint("native=", native), func(t *testing.T) {
			m := model.Message{Role: model.RoleTool, ToolCallID: "source", Content: strings.Repeat("source evidence. ", 1250) + "UNIQUE_SCIENTIFIC_FACT" + strings.Repeat("source evidence. ", 1250)}
			source := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "return the result"}, m}}
			if native {
				source.Messages = source.Messages[:1]
				source.ProviderTurns = []model.ProviderTurn{{ToolResults: []model.Message{m}}}
			}
			calls := []tool.Call{{ProviderCallID: "source", ToolName: "builtin.fixture", Result: &tool.Result{Status: tool.ResultSuccess}}}
			limit := currentRequestBudgetTokens(source, nil) + 10
			// Reproduce the dangerous alternative: adding the optional interface
			// first fits ONLY by shortening the previously admissible evidence.
			trimmed, err := fitFinalRequest(source, calls, nil, limit, func(r model.ChatRequest) model.ChatRequest { return stageExplorationRequest(r, submissionRepairSchema) })
			views := convergenceToolViews(trimmed)
			if err != nil || len(views) != 1 || views[0].Content == m.Content || strings.Contains(views[0].Content, "UNIQUE_SCIENTIFIC_FACT") {
				t.Fatal("fixture did not reproduce optional-schema displacement", err)
			}
			got, enabled := withOptionalStageAction(source, submissionRepairSchema, nil, limit)
			if enabled || !reflect.DeepEqual(got, source) {
				t.Fatal("optional control shortened admitted evidence")
			}
			if _, err := withPlannerStageAction(source, submissionRepairSchema, nil, limit); err == nil || convergenceToolViews(source)[0].Content != m.Content {
				t.Fatal("mandatory planner control displaced previously admitted evidence instead of stopping", err)
			}
			got, enabled = withOptionalStageAction(source, submissionRepairSchema, nil, limit+8000)
			if !enabled || got.ForcedTool != "" || got.RequireTool {
				t.Fatal("roomy optional completion was unavailable or forced")
			}
		})
	}
}

func TestStageActionSmallWindowFallsBackWithoutExtraModelCall(t *testing.T) {
	loop, state, p := newLoopFixture(t, nil, submissionScript(`{"items":["legacy accepted"],"state":"ready"}`))
	bindReadConvergenceFixture(t, loop, state)
	var schema map[string]any
	_ = json.Unmarshal(submissionRepairSchema, &schema)
	schema["description"] = strings.Repeat("fixed structured contract. ", 10000)
	large, _ := json.Marshal(schema)
	loop.research.(*staticResearchGuidance).value.StructuredOutputSchema = large
	runs := loop.runs.(workflowContractRuns)
	runs.contract.OutputSchema = large
	loop.runs = runs
	loop.models = budgetResolver{model: p, budget: modelcap.ResolveContextBudget(5000, 0, modelcap.ContextWindowSourceManual)}
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(p.Requests()) != 1 || len(state.calls) != 0 {
		t.Fatal("optional schema broke a legacy request", got, state.run.ErrorCode, len(p.Requests()))
	}
	for _, d := range p.Requests()[0].Tools {
		if d.Name == stageSubmissionTool {
			t.Fatal("unadmitted optional schema still advertised")
		}
	}
}

func TestStageMaterialAddressingInstructionFollowsFrozenReaderNotSubmitMode(t *testing.T) {
	current := tool.Definition{QualifiedName: workflow.ResearchTaskReadTool, InputSchema: materialReaderSchema}
	legacy := json.RawMessage(`{"type":"object","properties":{"section":{"type":"string","enum":["stage_input"]},"offset":{"type":"integer"}}}`)
	for _, frozen := range []json.RawMessage{materialReaderSchema, legacy} {
		d := executionTaskReadDefinitions([]tool.Definition{current}, true, frozen)[0]
		wantPointer := strings.Contains(string(frozen), `"pointer"`)
		if strings.Contains(d.Description, stageMaterialInstruction) != wantPointer || strings.Contains(string(d.InputSchema), `"pointer"`) != wantPointer {
			t.Fatal("addressing instruction requires optional submit or expands frozen reader", d)
		}
	}
}

func TestStageActionRejectsMixedAndDuplicateControlsWithoutAnyIO(t *testing.T) {
	for _, mode := range []string{"submit_then_read", "read_then_submit", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			submit := model.ToolCall{ID: "submit", Name: stageSubmissionTool, Arguments: json.RawMessage(`{"items":["done"],"state":"ready"}`)}
			read := model.ToolCall{ID: "read", Name: "builtin.research.task.read", Arguments: json.RawMessage(`{"section":"stage_input","offset":0}`)}
			calls := []model.ToolCall{submit, read}
			if mode == "read_then_submit" {
				calls = []model.ToolCall{read, submit}
			}
			if mode == "duplicate" {
				second := submit
				second.ID = "submit2"
				calls = []model.ToolCall{submit, second}
			}
			script := []fake.Step{}
			for i := range calls {
				script = append(script, fake.Step{Event: model.Event{Type: model.EventToolCall, ToolCall: &calls[i]}})
			}
			script = append(script, fake.Step{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}})
			loop, state, p := newLoopFixture(t, nil, script)
			bindReadConvergenceFixture(t, loop, state)
			if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || state.run.ErrorCode != "MODEL_SUBMISSION_TOOL_VIOLATION" || len(state.calls) != 0 || len(p.Requests()) != 1 {
				t.Fatal("mixed completion escaped into IO", got, state.run.ErrorCode, len(state.calls))
			}
		})
	}
}

func TestStageActionKeepsLegacyOutputAndOriginalCorrectionBudget(t *testing.T) {
	for _, candidate := range []string{`{"items":{},"state":"ready"}`, `"promise to finish"`, `null`, `{}`} {
		t.Run(fmt.Sprint(candidate), func(t *testing.T) {
			loop, state, p := newLoopFixture(t, nil, submissionToolScript(candidate), submissionToolScript(candidate), submissionToolScript(candidate))
			bindReadConvergenceFixture(t, loop, state)
			if got := loop.Run(context.Background(), "run"); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(p.Requests()) != 3 || len(state.calls) != 0 {
				t.Fatal("direct control renewed continuation allowance", got, state.run.ErrorCode, len(p.Requests()))
			}
			for _, req := range p.Requests()[1:] {
				if req.ForcedTool != stageSubmissionTool || len(req.Tools) != 1 {
					t.Fatal("correction reopened exploration")
				}
			}
		})
	}
	loop, state, p := newLoopFixture(t, nil, submissionScript(`{"items":["legacy accepted"],"state":"ready"}`))
	bindReadConvergenceFixture(t, loop, state)
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(p.Requests()) != 1 {
		t.Fatal("valid legacy terminal output rejected", got, state.run.ErrorCode)
	}
}

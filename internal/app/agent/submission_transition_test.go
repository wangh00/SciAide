package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/skillrun"
)

func submissionRequestText(request model.ChatRequest) string {
	parts := make([]string, 0, len(request.Messages))
	for _, message := range request.Messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

func bindEarlySubmissionFixture(loop *Loop, state *loopState, schema json.RawMessage, resourceMode, discovery bool) {
	state.workflowAI = true
	guidance := workflow.ResearchGuidance{WorkflowRunID: "workflow", WorkflowStepID: "step", StructuredOutputRequired: true, StructuredOutputSchema: schema, SkillDiscovery: discovery}
	if resourceMode {
		guidance.AllowedToolNames = []string{resource.OpenTool}
		loop.resources = &resourceViewFixture{}
	}
	loop.research = &staticResearchGuidance{bound: true, value: guidance}
}

func TestEarlyRejectedStageUniformlyEntersSubmissionInterface(t *testing.T) {
	good := `{"items":["retained evidence"],"state":"ready"}`
	for _, mode := range []struct {
		name      string
		resources bool
		discovery bool
	}{
		{"legacy_no_resources", false, false},
		{"before_twelve_turn_budget", true, true},
		{"before_forty_turn_budget", true, false},
	} {
		for _, candidate := range []struct{ name, text string }{
			{"trailing_quote", "```json\n" + good + "\"\n```"},
			{"schema_error", `{"items":{},"state":"ready"}`},
			{"progress_only", "I will submit the complete result next."},
		} {
			t.Run(mode.name+"/"+candidate.name, func(t *testing.T) {
				loop, state, provider := newLoopFixture(t, nil, submissionScript(candidate.text), submissionToolScript(good))
				bindEarlySubmissionFixture(loop, state, submissionRepairSchema, mode.resources, mode.discovery)
				if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
					t.Fatal(outcome, state.run)
				}
				requests := provider.Requests()
				if len(requests) != 2 || requests[0].ForcedTool != "" || requests[1].ForcedTool != stageSubmissionTool || len(requests[1].Tools) != 1 {
					t.Fatal("early rejected result remained in exploration instead of controlled submission", len(requests), requests[len(requests)-1].ForcedTool)
				}
				if candidate.name == "progress_only" && strings.Contains(submissionRequestText(requests[1]), "If tools are still required, call them first; otherwise end with the complete fenced JSON object") {
					t.Fatal("forced submission still received conflicting exploration/fenced-output guidance")
				}
				if state.journalDrafts[1] != candidate.text || state.journalDrafts[2] != good || len(state.calls) != 0 {
					t.Fatal("original candidate lost or control executed as research IO", state.journalDrafts, state.calls)
				}
			})
		}
	}
}

func TestEarlyFormatFailuresKeepTwoCorrectionLimitAndSpecificDiagnosis(t *testing.T) {
	bad := "```json\n" + `{"items":["正文"],"state":"ready"}` + "\"\n```"
	loop, state, provider := newLoopFixture(t, nil, submissionScript(bad), submissionScript(bad), submissionScript(bad))
	bindEarlySubmissionFixture(loop, state, submissionRepairSchema, false, false)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || len(provider.Requests()) != 3 || len(state.calls) != 0 {
		t.Fatal("format failure became success, repeated IO, or renewed its bounded budget", outcome, state.run, len(provider.Requests()))
	}
	if !strings.Contains(state.run.ErrorMessage, "提交格式") || !strings.Contains(state.run.ErrorMessage, "不代表") || !strings.Contains(state.run.ErrorDetails, "尾部") || !strings.Contains(state.run.ErrorDetails, "UTF-8") {
		t.Fatal("format failure was reported as an opaque scientific failure", state.run.ErrorMessage, state.run.ErrorDetails)
	}
	for _, request := range provider.Requests()[1:] {
		if request.ForcedTool != stageSubmissionTool {
			t.Fatal("format correction did not lock submission")
		}
	}
}

func TestEarlyBusinessRejectionStillUsesFullHostValidation(t *testing.T) {
	good := `{"items":["retained evidence"],"state":"ready"}`
	loop, state, provider := newLoopFixture(t, nil, submissionScript(good), submissionToolScript(good), submissionToolScript(good))
	bindEarlySubmissionFixture(loop, state, submissionRepairSchema, false, false)
	host := &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), errors: []error{}}
	for range 3 {
		host.errors = append(host.errors, &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", UserMessage: "科研阶段提交未通过业务检查", Cause: errors.New("scientific result remains unsupported by the frozen evidence")})
	}
	loop.research = host
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || len(provider.Requests()) != 3 || len(host.validated) != 3 || !strings.Contains(state.run.ErrorDetails, "unsupported") {
		t.Fatal("structured submission bypassed scientific validation", outcome, state.run, len(host.validated))
	}
	for _, request := range provider.Requests()[1:] {
		if request.ForcedTool != stageSubmissionTool {
			t.Fatal("generic scientific correction reopened exploration")
		}
	}
}

func TestEarlySubmissionCorrectionCannotExecuteUnrelatedTool(t *testing.T) {
	bad := "```json\n" + `{"items":["evidence"],"state":"ready"}` + "\"\n```"
	unrelated := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "unexpected", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"repeat work"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	loop, state, _ := newLoopFixture(t, nil, submissionScript(bad), unrelated)
	bindEarlySubmissionFixture(loop, state, submissionRepairSchema, false, false)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || state.run.ErrorCode != "MODEL_SUBMISSION_TOOL_VIOLATION" || len(state.calls) != 0 {
		t.Fatal("correction executed unrelated research IO", outcome, state.run, state.calls)
	}
}

func TestCapturedMethodSubmissionUniformTransition(t *testing.T) {
	dir := os.Getenv("SCIAIDE_METHOD_SUBMISSION_REPLAY_DIR")
	if dir == "" {
		t.Skip("no task-scoped method submission capture")
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	schema := json.RawMessage(read("frozen-output.schema.json"))
	var cases []struct {
		Turn              int    `json:"turn"`
		DeletedByteOffset int    `json:"deletedByteOffset"`
		OriginalSHA256    string `json:"originalSha256"`
		DerivedSHA256     string `json:"derivedSha256"`
	}
	if err := json.Unmarshal(read("replay-cases.json"), &cases); err != nil || len(cases) != 3 {
		t.Fatal("missing exact-byte replay manifest", err)
	}
	for _, replay := range cases {
		turn := replay.Turn
		t.Run(fmt.Sprintf("original_turn_%d", turn), func(t *testing.T) {
			original := string(read(fmt.Sprintf("turn-%d.provider-final.original.txt", turn)))
			derived := string(read(fmt.Sprintf("turn-%d.one-quote-removed.derived.txt", turn)))
			position := replay.DeletedByteOffset
			if fmt.Sprintf("%x", sha256.Sum256([]byte(original))) != replay.OriginalSHA256 || fmt.Sprintf("%x", sha256.Sum256([]byte(derived))) != replay.DerivedSHA256 || position < 0 || position >= len(original) || original[position] != '"' || original[:position]+original[position+1:] != derived {
				t.Fatal("replay export changed original provider bytes or more than the one diagnostic quote")
			}
			corrected, _, err := workflow.NormalizeAIStageSubmission(derived, schema, "method_selection")
			if err != nil {
				t.Fatal(err)
			}
			loop, state, provider := newLoopFixture(t, nil, submissionScript(original), submissionToolScript(string(corrected)))
			bindEarlySubmissionFixture(loop, state, schema, true, false)
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 2 || provider.Requests()[1].ForcedTool != stageSubmissionTool || state.journalDrafts[1] != original {
				t.Fatal("captured early method-stage format failure did not enter controlled submission", outcome, state.run)
			}
		})
	}
}

type plannerSubmissionPrerequisiteFixture struct {
	staticDynamicSkillRouter
	loaded    bool
	checks    int
	snapshots []skillrun.Snapshot
	readErr   error
}

func (f *plannerSubmissionPrerequisiteFixture) ListRunSkillSnapshots(context.Context, string) ([]skillrun.Snapshot, error) {
	f.checks++
	if !f.loaded {
		return nil, nil
	}
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.snapshots != nil {
		return f.snapshots, nil
	}
	return []skillrun.Snapshot{{Name: "scientific-writing", ContentHash: "content", PackageHash: "package"}}, nil
}

var earlyPlannerSubmissionSchema = json.RawMessage(`{"type":"object","required":["normalizedQuestion","selectedSkills","routes"],"properties":{"normalizedQuestion":{"type":"string"},"selectedSkills":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}}}},"routes":{"type":"array","items":{"type":"object"}}}}`)

func TestEarlyPlannerCorrectionsKeepActualSkillLoadingAvailable(t *testing.T) {
	good := `{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"}],"routes":[]}`
	for _, resources := range []bool{false, true} {
		for _, progressOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("resources_%t/progress_%t", resources, progressOnly), func(t *testing.T) {
				first := "```json\n" + good + "\"\n```"
				if progressOnly {
					first = "I will select and load the necessary Skills next."
				}
				name, args := "builtin.skill.load", `{"name":"scientific-writing"}`
				ref := "res_" + strings.Repeat("a", 32)
				if resources {
					name, args = resource.OpenTool, `{"actionId":"`+ref+`"}`
				}
				load := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "load-selected-skill", Name: name, Arguments: json.RawMessage(args)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
				loop, state, provider := newLoopFixture(t, nil, submissionScript(first), submissionScript(good), load, submissionScript(good))
				bindEarlySubmissionFixture(loop, state, earlyPlannerSubmissionSchema, resources, true)
				guidance := loop.research.(*staticResearchGuidance)
				guidance.value.AllowedToolNames = []string{name}
				router := &plannerSubmissionPrerequisiteFixture{}
				loop.skillRouter = router
				definition := tool.Definition{QualifiedName: name, Description: "load the selected Skill", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
				if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: definition, invoke: func(tool.Invocation) tool.Result {
					router.loaded = true
					return tool.Result{Status: tool.ResultSuccess, Text: "Skill instructions", Structured: json.RawMessage(`{"sourceTool":"builtin.skill.load","sourceArguments":{"name":"scientific-writing"}}`)}
				}}); err != nil {
					t.Fatal(err)
				}
				if resources {
					loop.resources = &resourceViewFixture{view: resource.View{Definitions: []tool.Definition{definition}, OpenIDs: map[string]bool{ref: true}, SkillActions: map[string]string{"scientific-writing": ref}}}
				}
				if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || !router.loaded || router.checks != 3 || len(state.calls) != 1 || len(provider.Requests()) != 4 {
					t.Fatal("early planner correction locked out or bypassed actual Skill loading", outcome, state.run, router, len(state.calls))
				}
				for _, request := range provider.Requests() {
					if request.ForcedTool != "" || len(request.Tools) != 1 || request.Tools[0].Name != name {
						t.Fatal("planner prerequisite lost its original frozen interface", request.Tools, request.ForcedTool)
					}
				}
				feedback := provider.Requests()[2].Messages
				last := feedback[len(feedback)-1].Content
				if resources && (strings.Contains(last, "builtin.skill.load") || !strings.Contains(last, resource.OpenTool) || !strings.Contains(last, ref)) {
					t.Fatal("Skill correction instructed a hidden tool instead of the offered bound action", last)
				}
				if !resources && !strings.Contains(last, "builtin.skill.load") {
					t.Fatal("native Skill-loading guidance lost its offered interface", last)
				}
				for _, message := range provider.Requests()[3].Messages {
					if message.Role == model.RoleUser && message.HostToolReferences && strings.Contains(message.Content, "WORKFLOW_PLANNER_SKILLS_MISSING") {
						t.Fatal("successful actual Skill loading left a stale missing-Skill request", message.Content)
					}
				}
			})
		}
	}
}

func TestPlannerAlreadyInSubmissionDoesNotReopenExploration(t *testing.T) {
	good := `{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"}],"routes":[]}`
	load := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "forbidden-late-load", Name: resource.OpenTool, Arguments: json.RawMessage(`{"actionId":"unissued"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	loop, state, provider := newLoopFixture(t, nil, submissionToolScript(good), load)
	bindEarlySubmissionFixture(loop, state, earlyPlannerSubmissionSchema, true, true)
	state.run.ModelTurns = 12
	loop.skillRouter = &plannerSubmissionPrerequisiteFixture{}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || state.run.ErrorCode != "MODEL_SUBMISSION_TOOL_VIOLATION" || len(state.calls) != 0 || len(provider.Requests()) != 2 {
		t.Fatal("planner reopened exploration after a submission control", outcome, state.run)
	}
	for _, request := range provider.Requests() {
		if request.ForcedTool != stageSubmissionTool || len(request.Tools) != 1 {
			t.Fatal("planner lost submission lock")
		}
	}
}

func TestPlannerSkillFeedbackFollowsNextTurnSubmissionBoundary(t *testing.T) {
	good := `{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"}],"routes":[]}`
	loop, state, provider := newLoopFixture(t, nil, submissionScript(good), submissionToolScript(good), submissionToolScript(good))
	bindEarlySubmissionFixture(loop, state, earlyPlannerSubmissionSchema, true, true)
	state.run.ModelTurns = 11
	loop.skillRouter = &plannerSubmissionPrerequisiteFixture{}
	ref := "res_" + strings.Repeat("b", 32)
	loop.resources = &resourceViewFixture{view: resource.View{
		Definitions: []tool.Definition{{QualifiedName: resource.OpenTool, InputSchema: json.RawMessage(`{"type":"object"}`)}},
		OpenIDs:     map[string]bool{ref: true}, SkillActions: map[string]string{"scientific-writing": ref},
	}}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_PLANNER_SKILLS_INVALID" || len(state.calls) != 0 || len(provider.Requests()) != 3 {
		t.Fatal("submission boundary bypassed Skill validation or renewed its correction budget", outcome, state.run)
	}
	requests := provider.Requests()
	if requests[0].ForcedTool != "" || len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != resource.OpenTool {
		t.Fatal("twelfth turn did not retain the exploration interface")
	}
	for _, request := range requests[1:] {
		if request.ForcedTool != stageSubmissionTool || len(request.Tools) != 1 || request.Tools[0].Name != stageSubmissionTool {
			t.Fatal("later Skill correction reopened exploration")
		}
		found := false
		for _, message := range request.Messages {
			if message.Role == model.RoleUser && message.HostToolReferences && strings.Contains(message.Content, "WORKFLOW_PLANNER_SKILLS_MISSING") {
				found = true
				if !strings.Contains(message.Content, "没有提供 Skill 加载入口") || strings.Contains(message.Content, resource.OpenTool) || strings.Contains(message.Content, ref) || strings.Contains(message.Content, "builtin.skill.load") {
					t.Fatal("next-turn feedback still promises the previous turn's loading interface", message.Content)
				}
			}
		}
		if !found {
			t.Fatal("submission request lost the rejected candidate's Skill diagnostic")
		}
	}
}

func TestPlannerSkillFeedbackRefreshesOnlyFromActualSnapshots(t *testing.T) {
	const first = "scientific-writing"
	const second = "scientific-brainstorming"
	good := `{"normalizedQuestion":"question","selectedSkills":[{"name":"` + first + `"},{"name":"` + second + `"}],"routes":[]}`
	for _, kind := range []string{"partial", "read_failed", "tool_text_only"} {
		t.Run(kind, func(t *testing.T) {
			ref := "res_" + strings.Repeat("c", 32)
			load := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "load-one", Name: resource.OpenTool, Arguments: json.RawMessage(`{"actionId":"` + ref + `"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
			loop, state, provider := newLoopFixture(t, nil, submissionScript(good), load, submissionScript(good), submissionScript(good))
			bindEarlySubmissionFixture(loop, state, earlyPlannerSubmissionSchema, true, true)
			router := &plannerSubmissionPrerequisiteFixture{snapshots: []skillrun.Snapshot{{Name: first, ContentHash: "content", PackageHash: "package"}}}
			loop.skillRouter = router
			definition := tool.Definition{QualifiedName: resource.OpenTool, Description: "load Skill", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
			if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: definition, invoke: func(tool.Invocation) tool.Result {
				router.loaded = kind != "tool_text_only"
				if kind == "read_failed" {
					router.readErr = errors.New("snapshot read failed")
				}
				return tool.Result{Status: tool.ResultSuccess, Text: "Both Skills loaded successfully"}
			}}); err != nil {
				t.Fatal(err)
			}
			loop.resources = &resourceViewFixture{view: resource.View{Definitions: []tool.Definition{definition}, OpenIDs: map[string]bool{ref: true}, SkillActions: map[string]string{first: ref}}}
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_PLANNER_SKILLS_INVALID" || len(provider.Requests()) != 4 || len(state.calls) != 1 {
				t.Fatal("unverified tool text or partial snapshots bypassed the original Skill check/budget", outcome, state.run)
			}
			if strings.Contains(state.run.ErrorMessage, "actionId") || strings.Contains(state.run.ErrorMessage, resource.OpenTool) || strings.Contains(state.run.ErrorMessage, ref) {
				t.Fatal("terminal error offered a now-inactive Run-bound loading action", state.run.ErrorMessage)
			}
			feedback := provider.Requests()[2].Messages
			last := feedback[len(feedback)-1].Content
			if strings.Contains(last, "已满足") {
				t.Fatal("failed or partial evidence was declared sufficient", last)
			}
			switch kind {
			case "partial":
				if strings.Contains(last, first) || !strings.Contains(last, second) || strings.Contains(last, ref) {
					t.Fatal("partially completed loads were repeated or remaining missing names were lost", last)
				}
			case "read_failed":
				if !strings.Contains(last, "snapshot read failed") || strings.Contains(last, "当前加载入口") {
					t.Fatal("snapshot failure was converted into a fake success or model loading command", last)
				}
			case "tool_text_only":
				if !strings.Contains(last, first) || !strings.Contains(last, second) || !strings.Contains(last, ref) {
					t.Fatal("untrusted tool text replaced actual snapshot evidence", last)
				}
			}
		})
	}
}

func TestPlannerSkillFeedbackDoesNotOverrideNewFormatCorrection(t *testing.T) {
	good := `{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"}],"routes":[]}`
	for _, next := range []string{"I am preparing the result.", good + `"`} {
		loop, state, provider := newLoopFixture(t, nil, submissionScript(good), submissionScript(next), submissionScript(good), submissionScript(good))
		bindEarlySubmissionFixture(loop, state, earlyPlannerSubmissionSchema, true, true)
		loop.skillRouter = &plannerSubmissionPrerequisiteFixture{}
		loop.Run(context.Background(), "run")
		if len(provider.Requests()) != 4 {
			t.Fatal("new format correction changed the bounded Skill correction sequence", state.run)
		}
		for _, message := range provider.Requests()[2].Messages {
			if message.Role == model.RoleUser && message.HostToolReferences && strings.Contains(message.Content, "WORKFLOW_PLANNER_SKILLS_MISSING") {
				t.Fatal("stale Skill diagnostic overrode the subsequent format/progress correction", message.Content)
			}
		}
	}
}

func bindPlannerActionFixture(loop *Loop, state *loopState) {
	bindEarlySubmissionFixture(loop, state, earlyPlannerSubmissionSchema, true, true)
	contract := stageBudgetContract(json.RawMessage(`{}`), "Submit the complete research plan after its actual Skill loads.")
	contract.NodeID, contract.WorkflowStepID = "explore", "step"
	contract.ID, contract.Attempt = "execution", 1
	contract.OutputSchema = earlyPlannerSubmissionSchema
	contract.OutputSchemaSHA256 = fmt.Sprintf("%x", sha256.Sum256(contract.OutputSchema))
	contract.AllowedTools = json.RawMessage(`["builtin.resource.open"]`)
	loop.runs = workflowContractRuns{loopState: state, contract: contract, found: true}
}

func bindNativePlannerActionFixture(loop *Loop, state *loopState) {
	bindPlannerActionFixture(loop, state)
	runs := loop.runs.(workflowContractRuns)
	runs.contract.NodeKind = string(workflow.NodeAgentStage)
	runs.contract.PromptVersion = "research-starter-semantic-v6|" + workflow.NativePlannerProtocolVersion
	loop.runs = runs
}

func TestLegacyPlannerProgressContinuationKeepsFrozenTransport(t *testing.T) {
	good := `{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"}],"routes":[]}`
	ref := "res_" + strings.Repeat("b", 32)
	load := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "load-after-progress", Name: resource.OpenTool, Arguments: json.RawMessage(`{"actionId":"` + ref + `"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	loop, state, provider := newLoopFixture(t, nil, submissionScript("I have prepared a plan and will submit it next."), load, submissionToolScript(good))
	bindPlannerActionFixture(loop, state)
	runs := loop.runs.(workflowContractRuns)
	runs.contract.PromptVersion = "research-starter-semantic-v6"
	loop.runs = runs
	router := &plannerSubmissionPrerequisiteFixture{}
	loop.skillRouter = router
	definition := tool.Definition{QualifiedName: resource.OpenTool, Description: "Load actual Skill", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
	if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: definition, invoke: func(tool.Invocation) tool.Result {
		router.loaded = true
		return tool.Result{Status: tool.ResultSuccess, Text: "actual retained Skill instructions"}
	}}); err != nil {
		t.Fatal(err)
	}
	loop.resources = &resourceViewFixture{view: resource.View{Definitions: []tool.Definition{definition}, OpenIDs: map[string]bool{ref: true}, SkillActions: map[string]string{"scientific-writing": ref}}}
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || !router.loaded || len(state.calls) != 1 || len(provider.Requests()) != 3 {
		t.Fatal("native continuation lost actual Skill prerequisites", got, state.run, len(state.calls))
	}
	request := provider.Requests()[1]
	if request.RequireTool || request.ForcedTool != "" || len(request.Tools) != 2 {
		t.Fatal("legacy progress continuation changed frozen transport")
	}
	for _, m := range request.Messages {
		if m.Role == model.RoleUser && m.HostToolReferences && strings.Contains(m.Content, "previous response") {
			if m.Content != structuredOutputContinuation() {
				t.Fatal("legacy planning continuation was silently upgraded", m.Content)
			}
			return
		}
	}
	t.Fatal("native planning continuation was not projected into the actual request")
}

func TestPlannerNativeSubmissionKeepsSkillPrerequisitesAndAudit(t *testing.T) {
	good := `{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"}],"routes":[]}`
	for _, nativeType := range []string{"", "function_call", "tool_use"} {
		for _, schemaError := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/schema_error_%t", nativeType, schemaError), func(t *testing.T) {
				first := good
				if schemaError {
					first = `{"selectedSkills":[],"routes":[]}`
				}
				control := submissionToolScript(first)
				control[0].Event.ToolCall.ID = "plan-first"
				control = append([]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "Original control commentary."}}}, control...)
				var original model.ProviderItem
				if nativeType != "" {
					payload, _ := json.Marshal(map[string]any{"type": nativeType, "name": stageSubmissionTool, "arguments": first})
					original = model.ProviderItem{Ordinal: 1, Type: nativeType, CallID: "plan-first", Payload: payload}
					thinking := model.ProviderItem{Ordinal: 0, Type: "thinking", Payload: json.RawMessage(`{"type":"thinking","thinking":"PRIVATE_THINKING_SENTINEL","signature":"signed"}`)}
					control = append(control[:len(control)-1], fake.Step{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &thinking}}, fake.Step{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &original}}, control[len(control)-1])
				}
				ref := "res_" + strings.Repeat("d", 32)
				load := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "actual-plan-load", Name: resource.OpenTool, Arguments: json.RawMessage(`{"actionId":"` + ref + `"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
				scripts := [][]fake.Step{control}
				if schemaError {
					scripts = append(scripts, submissionToolScript(good))
				}
				scripts = append(scripts, load, submissionToolScript(good))
				if nativeType != "" {
					for i := 1; i < len(scripts); i++ {
						call := scripts[i][0].Event.ToolCall
						call.ID = fmt.Sprintf("plan-call-%d", i)
						payload, _ := json.Marshal(map[string]any{"type": nativeType, "name": call.Name, "arguments": string(call.Arguments)})
						item := model.ProviderItem{Ordinal: 0, Type: nativeType, CallID: call.ID, Payload: payload}
						steps := scripts[i]
						scripts[i] = append(steps[:len(steps)-1], fake.Step{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &item}}, steps[len(steps)-1])
					}
				}
				loop, state, provider := newLoopFixture(t, nil, scripts...)
				bindNativePlannerActionFixture(loop, state)
				if nativeType == "function_call" {
					state.run.APIProtocol = "openai_responses"
				} else if nativeType == "tool_use" {
					state.run.APIProtocol = "anthropic_messages"
				}
				router := &plannerSubmissionPrerequisiteFixture{}
				loop.skillRouter = router
				definition := tool.Definition{QualifiedName: resource.OpenTool, Description: "Load issued Skill", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
				if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: definition, invoke: func(tool.Invocation) tool.Result {
					router.loaded = true
					return tool.Result{Status: tool.ResultSuccess, Text: "actual Skill body"}
				}}); err != nil {
					t.Fatal(err)
				}
				loop.resources = &resourceViewFixture{view: resource.View{Definitions: []tool.Definition{definition}, OpenIDs: map[string]bool{ref: true}, SkillActions: map[string]string{"scientific-writing": ref}}}
				if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(state.calls) != 1 || !router.loaded || len(provider.Requests()) != len(scripts) {
					t.Fatal("native planning control bypassed or locked its prerequisites", got, state.run, len(state.calls))
				}
				for _, request := range provider.Requests() {
					if request.ForcedTool != "" || !request.RequireTool || len(request.Tools) != 2 || strings.Contains(submissionRequestText(request), "PRIVATE_THINKING_SENTINEL") {
						t.Fatal("planning action locked IO, lost its control or exposed private items")
					}
				}
				if state.journalCommentaries[1] != "Original control commentary." || state.journalDrafts[1] != first {
					t.Fatal("native control commentary or exact arguments were overwritten", state.journalDrafts[1])
				}
				if nativeType != "" && (len(state.providerTurns) != len(scripts) || string(state.providerTurns[0].Items[1].Payload) != string(original.Payload)) {
					t.Fatal("request-only projection modified persisted native audit")
				}
			})
		}
	}
}

type failingPlannerCommentaryRuns struct{ workflowContractRuns }

func (failingPlannerCommentaryRuns) SaveModelTurnSubmissionCommentary(context.Context, string, int, string, time.Time) error {
	return errors.New("injected commentary audit write failure")
}

func TestPlannerNativeSubmissionPreservesOriginalBoundaries(t *testing.T) {
	good := `{"normalizedQuestion":"I cannot comply with this request.","selectedSkills":[],"routes":[]}`
	for _, kind := range []string{"refusal", "quoted_in_json", "mixed", "schema_budget", "phase_boundary", "reloaded_control", "audit_write_failed"} {
		t.Run(kind, func(t *testing.T) {
			script := submissionToolScript(good)
			if kind == "refusal" {
				script = append([]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "I cannot comply with this request."}}}, script...)
			}
			if kind == "audit_write_failed" {
				script = append([]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "Original visible commentary."}}}, script...)
			}
			if kind == "mixed" {
				script = append(script[:1], fake.Step{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "must-not-execute", Name: resource.OpenTool, Arguments: json.RawMessage(`{"actionId":"unissued"}`)}}}, script[1])
			}
			if kind == "schema_budget" {
				script = submissionToolScript(`{"selectedSkills":[],"routes":[]}`)
			}
			if kind == "phase_boundary" {
				script = submissionToolScript(`{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"}],"routes":[]}`)
			}
			if kind == "reloaded_control" {
				payload, _ := json.Marshal(map[string]string{"type": "function_call", "name": stageSubmissionTool, "arguments": good})
				item := model.ProviderItem{Ordinal: 0, Type: "function_call", CallID: "submit-result", Payload: payload}
				script = append(script[:1], fake.Step{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &item}}, script[1])
			}
			loop, state, provider := newLoopFixture(t, nil, script, script, script)
			bindNativePlannerActionFixture(loop, state)
			loop.skillRouter = &plannerSubmissionPrerequisiteFixture{}
			if kind == "audit_write_failed" {
				loop.runs = failingPlannerCommentaryRuns{workflowContractRuns: loop.runs.(workflowContractRuns)}
			}
			if kind == "phase_boundary" {
				state.run.ModelTurns = 11
			}
			if kind == "reloaded_control" {
				state.run.ModelTurns = 1
				state.run.APIProtocol = "openai_responses"
				state.providerTurns = []model.ProviderTurn{{TurnIndex: 1, Protocol: state.run.APIProtocol, Items: []model.ProviderItem{{Ordinal: 0, Type: "function_call", CallID: "old-control", Payload: json.RawMessage(`{"type":"function_call","name":"submit_stage_result","arguments":"{}"}`)}}}}
			}
			if state.run.ModelTurns > 0 {
				// These fixtures start after earlier tool turns; explicitly seed the
				// ready host state instead of pretending missing legacy state is zero.
				seedPlannerCheckpoint(t, loop, state)
			}
			outcome := loop.Run(context.Background(), "run")
			want, requests := "", 1
			switch kind {
			case "refusal":
				want = "WORKFLOW_AI_MODEL_REFUSED"
			case "mixed":
				want = "MODEL_SUBMISSION_TOOL_VIOLATION"
			case "audit_write_failed":
				want = "WORKFLOW_AI_SUBMISSION_AUDIT_FAILED"
			case "schema_budget":
				want, requests = "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED", 3
			case "phase_boundary":
				want, requests = "WORKFLOW_PLANNER_SKILLS_INVALID", 3
			}
			if len(provider.Requests()) != requests || len(state.calls) != 0 || want != state.run.ErrorCode || (want == "" && outcome != OutcomeCompleted) || (want != "" && outcome != OutcomeFailed) {
				t.Fatal("native planning changed refusal, IO, validation or correction boundaries", outcome, state.run, len(provider.Requests()))
			}
			if kind == "phase_boundary" && (provider.Requests()[1].ForcedTool != stageSubmissionTool || len(provider.Requests()[1].Tools) != 1) {
				t.Fatal("optional native planner control renewed the original exploration budget")
			}
			if kind == "reloaded_control" && (len(state.providerTurns) != 2 || state.providerTurns[0].Items[0].CallID != "old-control") {
				t.Fatal("restart projection modified native history")
			}
		})
	}
}

func TestProgressContinuationPreservesImplementationPartPayload(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionScript("Preparing the implementation."), partScript(1, "source = SCIAIDE_INPUTS[0]\noutputs = SCIAIDE_OUTPUTS\nresult = {'ok': True}", true))
	bindParts(loop, state)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 2 || len(state.calls) != 0 {
		t.Fatal("progress continuation replaced the implementation protocol", outcome, state.run)
	}
	request := provider.Requests()[1]
	text := submissionRequestText(request)
	if request.ForcedTool != implementationPartTool || len(request.Tools) != 1 || strings.Contains(text, "complete frozen result object") || strings.Contains(text, "If tools are still required, call them first") || !strings.Contains(text, "Host next implementation part index: 1") {
		t.Fatal("implementation-part control received contradictory full-result guidance", request.ForcedTool)
	}
}

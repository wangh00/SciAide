package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/skillrun"
)

var submissionRepairSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["items","state"],"properties":{"items":{"type":"array","items":{"type":"string"}},"state":{"type":"string","enum":["ready","blocked"]}}}`)

func submissionScript(text string) []fake.Step {
	return []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: text}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
}

func bindSubmissionRepair(loop *Loop, state *loopState) {
	state.workflowAI = true
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
}

func TestWorkflowInvalidSchemaNeverCallsModel(t *testing.T) {
	for _, schema := range []string{`{"type":"object","required":["x","x"]}`, `{"type":"array"}`, `{"type":"string"}`, `{"type":"object","additionalProperties":false,"required":["x"],"properties":{}}`} {
		loop, state, provider := newLoopFixture(t, nil, submissionScript(`{}`))
		bindSubmissionRepair(loop, state)
		loop.research.(*staticResearchGuidance).value.StructuredOutputSchema = json.RawMessage(schema)
		if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_SCHEMA_INVALID" {
			t.Fatalf("outcome=%s error=%s", got, state.run.ErrorCode)
		}
		if len(provider.Requests()) != 0 {
			t.Fatal("invalid host schema consumed a model request")
		}
	}
}

func TestWorkflowHostSchemaErrorDoesNotAskModelToCorrect(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionScript(`{"items":["value"],"state":"ready"}`))
	bindSubmissionRepair(loop, state)
	host := &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), errors: []error{workflow.ValidateAIStageSchema(json.RawMessage(`{"required":["x","x"]}`))}}
	loop.research = host
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_SCHEMA_INVALID" {
		t.Fatalf("host schema error retried as model error: %s %s", got, state.run.ErrorCode)
	}
	if len(provider.Requests()) != 1 || len(host.validated) != 1 {
		t.Fatal("host schema error triggered a model correction request")
	}
}

func TestWorkflowSubmissionNormalizesWithoutAnotherModelCallAndPreservesRaw(t *testing.T) {
	raw := "```json\n{\"items\":\"one, two; three\",\"state\":\" READY \"}\n```"
	loop, state, provider := newLoopFixture(t, nil, submissionScript(raw))
	bindSubmissionRepair(loop, state)
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatalf("outcome=%s error=%s", got, state.run.ErrorMessage)
	}
	if len(provider.Requests()) != 1 {
		t.Fatalf("deterministic conversion requested model again: %d", len(provider.Requests()))
	}
	if got := state.messages[1].Parts[0].Text; got != raw {
		t.Fatalf("normalized projection replaced original answer: %q", got)
	}
	if len(state.runSteps) != 1 || state.runSteps[0].Commentary != raw {
		t.Fatalf("raw submission not preserved: %#v", state.runSteps)
	}
}

func TestWorkflowSubmissionLongRawUsesJournalAndBoundedPreview(t *testing.T) {
	raw := " \n" + `{"items":["` + strings.Repeat("x", 150000) + `幼"],"state":"ready"}` + "\n \t"
	loop, state, _ := newLoopFixture(t, nil, submissionScript(raw))
	bindSubmissionRepair(loop, state)
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatalf("long submission=%s run=%#v", got, state.run)
	}
	if state.journalDrafts[1] != raw {
		t.Fatal("journal trimmed or truncated original submission")
	}
	if len(state.runSteps) != 1 || len([]rune(state.runSteps[0].Commentary)) != maxRunStepCommentaryRunes {
		t.Fatal("RunStep preview exceeded storage boundary")
	}
}

func TestWorkflowSubmissionAuditFailureStopsBeforeAcceptanceOrRepair(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		for _, journalFails := range []bool{false, true} {
			raw := `{"items":["value"],"state":"ready"}`
			if invalid {
				raw = `{"items":{},"state":"ready"}`
			}
			loop, state, provider := newLoopFixture(t, nil, submissionScript(raw))
			bindSubmissionRepair(loop, state)
			loop.runs = &faultInjectingRuns{loopState: state, failJournal: journalFails, failRunStep: !journalFails}
			if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || len(provider.Requests()) != 1 || state.messages[1].Parts[0].Text != "" {
				t.Fatalf("audit failure accepted/repaired: %s %#v", got, state.run)
			}
		}
	}
}

func TestWorkflowSubmissionOverJournalLimitFailsClosed(t *testing.T) {
	raw := `{"items":["` + strings.Repeat("x", 200001) + `"],"state":"ready"}`
	loop, state, provider := newLoopFixture(t, nil, submissionScript(raw))
	bindSubmissionRepair(loop, state)
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_SUBMISSION_AUDIT_FAILED" || len(provider.Requests()) != 1 {
		t.Fatalf("oversize submission not stopped: %s %#v", got, state.run)
	}
}

func TestWorkflowSubmissionSchemaErrorRepairsInSameRun(t *testing.T) {
	raw := `{"items":{"unexpected":"shape"},"state":"ready"}`
	valid := `{"items":["verified"],"state":"ready"}`
	loop, state, provider := newLoopFixture(t, nil, submissionScript(raw), submissionScript(valid))
	bindSubmissionRepair(loop, state)
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatalf("outcome=%s error=%s", got, state.run.ErrorMessage)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	last := requests[1].Messages
	if last[len(last)-2].Role != model.RoleAssistant || last[len(last)-2].Content != raw || !strings.Contains(last[len(last)-1].Content, "items") || !strings.Contains(last[len(last)-1].Content, "Do not repeat completed tools") {
		t.Fatalf("missing exact rejected candidate/repair feedback: %#v", last)
	}
	if state.run.ID != "run" || state.run.ModelTurns != 2 || len(state.runSteps) != 2 || state.runSteps[0].Commentary != raw || state.runSteps[1].Commentary != valid {
		t.Fatalf("same-run raw audit failed: run=%#v steps=%#v", state.run, state.runSteps)
	}
}

func TestWorkflowSubmissionCorrectionBudgetExhausted(t *testing.T) {
	bad := `{"items":{},"state":"ready"}`
	scripts := make([][]fake.Step, maxWorkflowContentCorrections+1)
	for i := range scripts {
		scripts[i] = submissionScript(bad)
	}
	loop, state, provider := newLoopFixture(t, nil, scripts...)
	bindSubmissionRepair(loop, state)
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed {
		t.Fatalf("outcome=%s", got)
	}
	if state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != maxWorkflowContentCorrections+1 || len(state.runSteps) != maxWorkflowContentCorrections+1 {
		t.Fatalf("unbounded or unaudited content repair: %#v, calls=%d steps=%d", state.run, len(provider.Requests()), len(state.runSteps))
	}
	if state.messages[1].Parts[0].Text != "" {
		t.Fatal("invalid submission committed as final answer")
	}
}

func TestWorkflowSubmissionRepairRetainsCompletedToolResults(t *testing.T) {
	first := []fake.Step{
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "read-once", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	loop, state, provider := newLoopFixture(t, nil, first, submissionScript(`{"items":{},"state":"ready"}`), submissionScript(`{"items":["verified"],"state":"ready"}`))
	bindSubmissionRepair(loop, state)
	loop.research.(*staticResearchGuidance).value.AllowedToolNames = []string{"builtin.fixture"}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatalf("outcome=%s error=%s", got, state.run.ErrorMessage)
	}
	requests := provider.Requests()
	if len(requests) != 3 || len(state.calls) != 1 {
		t.Fatalf("completed tool repeated or stage restarted: requests=%d calls=%d", len(requests), len(state.calls))
	}
	found := false
	for _, message := range requests[2].Messages {
		if message.Role == model.RoleTool && strings.Contains(message.Content, "可信执行") {
			found = true
		}
	}
	if !found {
		t.Fatalf("repair context lost completed tool results: %#v", requests[2].Messages)
	}
}

func TestWorkflowSubmissionNetworkRetriesDoNotConsumeContentBudget(t *testing.T) {
	bad := `{"items":{},"state":"ready"}`
	valid := `{"items":["verified"],"state":"ready"}`
	loop, state, provider := newLoopFixture(t, nil, submissionScript(bad), submissionScript(bad), submissionScript(valid))
	bindSubmissionRepair(loop, state)
	network := &openingFailureModel{inner: provider, remaining: 2, err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "temporary", Retryable: true}}
	loop.models = fakeResolver{model: network}
	loop.retryDelay = func(int) time.Duration { return 0 }
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatalf("network retry consumed content budget: %s, %#v", got, state.run)
	}
	if len(provider.Requests()) != 3 || state.run.ModelTurns != 3 {
		t.Fatalf("logical submission turns=%d requests=%d", state.run.ModelTurns, len(provider.Requests()))
	}
}

func TestOrdinaryChatDoesNotValidateResearchSubmissions(t *testing.T) {
	raw := `{"items":{},"state":"unexpected"}`
	loop, state, provider := newLoopFixture(t, nil, submissionScript(raw))
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: submissionRepairSchema}}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted || len(provider.Requests()) != 1 || state.messages[1].Parts[0].Text != raw {
		t.Fatalf("ordinary chat changed: %s %#v", got, state.run)
	}
}

type submissionHostFixture struct {
	*staticResearchGuidance
	errors    []error
	validated [][4]string
	accepted  json.RawMessage
}

func (f *submissionHostFixture) ValidateResearchSubmission(_ context.Context, conversationID, runID, stepID, text string) (json.RawMessage, error) {
	f.validated = append(f.validated, [4]string{conversationID, runID, stepID, text})
	if index := len(f.validated) - 1; index < len(f.errors) && f.errors[index] != nil {
		return nil, f.errors[index]
	}
	if f.accepted != nil {
		return f.accepted, nil
	}
	value, _, err := workflow.NormalizeAIStageSubmission(text, submissionRepairSchema)
	return value, err
}

func TestWorkflowSubmissionLiveHostOwnsCustomNodeProtocol(t *testing.T) {
	raw := `{"methodSummary":"custom","analysisInput":{},"code":"ordinary JSON field"}`
	loop, state, provider := newLoopFixture(t, nil, submissionScript(raw))
	bindSubmissionRepair(loop, state)
	// Deliberately stale projected schema would reject this. The live host's
	// actual custom node contract, not schema-name inference, is authoritative.
	host := &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), accepted: json.RawMessage(raw)}
	loop.research = host
	if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeCompleted || len(provider.Requests()) != 1 || len(host.validated) != 1 {
		t.Fatalf("schema fallback overruled custom host: %s %#v", outcome, state.run)
	}
}

type submissionAuditFault struct {
	*loopState
	failDraft bool
}

func (f *submissionAuditFault) UpdateModelTurnDraft(ctx context.Context, id string, turn int, draft string, count int, at time.Time) error {
	if f.failDraft {
		return &apperr.Error{Code: "DRAFT_WRITE_FAILED", UserMessage: "injected"}
	}
	return f.loopState.UpdateModelTurnDraft(ctx, id, turn, draft, count, at)
}
func (f *submissionAuditFault) FinishModelTurn(context.Context, string, int, chat.ModelTurnStatus, string, int, time.Time) error {
	return &apperr.Error{Code: "JOURNAL_FINISH_FAILED", UserMessage: "injected"}
}

func TestWorkflowSubmissionDraftAndFinishFailuresAreFatal(t *testing.T) {
	for _, draftFails := range []bool{false, true} {
		loop, state, provider := newLoopFixture(t, nil, submissionScript(`{"items":["value"],"state":"ready"}`))
		bindSubmissionRepair(loop, state)
		loop.runs = &submissionAuditFault{loopState: state, failDraft: draftFails}
		if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_SUBMISSION_AUDIT_FAILED" || len(provider.Requests()) != 1 {
			t.Fatalf("failed raw audit accepted: %s %#v", outcome, state.run)
		}
	}
}

func TestWorkflowSubmissionHostBusinessValidationUsesSameChatAndDurableBinding(t *testing.T) {
	first := `{"items":["unverified"],"state":"ready"}`
	second := `{"items":["verified"],"state":"ready"}`
	loop, state, provider := newLoopFixture(t, nil, submissionScript(first), submissionScript(second))
	bindSubmissionRepair(loop, state)
	guidance := loop.research.(*staticResearchGuidance)
	guidance.value.WorkflowRunID, guidance.value.WorkflowStepID = "stale-run", "stale-step"
	host := &submissionHostFixture{staticResearchGuidance: guidance, errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", UserMessage: "computedFacts requires existing evidence"}}}
	loop.research = host
	loop.runs = workflowContractRuns{loopState: state, found: true, contract: chat.WorkflowAIExecution{ID: "execution", WorkflowRunID: "frozen-run", WorkflowStepID: "frozen-step", AllowedTools: json.RawMessage(`[]`), OutputSchema: submissionRepairSchema}}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatalf("outcome=%s error=%s", got, state.run.ErrorMessage)
	}
	if len(provider.Requests()) != 2 || len(host.validated) != 2 || host.validated[0] != [4]string{"conversation", "frozen-run", "frozen-step", first} {
		t.Fatalf("wrong live host validation binding: %#v", host.validated)
	}
	request := provider.Requests()[1]
	if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "computedFacts requires existing evidence") {
		t.Fatal("input-aware rejection omitted from same-chat feedback")
	}
}

func TestWorkflowSubmissionHostInfrastructureErrorsAreFatal(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionScript(`{"items":["value"],"state":"ready"}`))
	bindSubmissionRepair(loop, state)
	host := &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance), errors: []error{&apperr.Error{Code: "WORKFLOW_BINDING_INVALID", UserMessage: "binding mismatch"}}}
	loop.research = host
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_BINDING_INVALID" {
		t.Fatalf("host failure treated as candidate correction: outcome=%s run=%#v", got, state.run)
	}
	if len(provider.Requests()) != 1 || len(host.validated) != 1 || len(state.runSteps) != 1 {
		t.Fatalf("fatal host error repeated model work: calls=%d", len(provider.Requests()))
	}
}

type submissionSkillRouter struct {
	staticDynamicSkillRouter
	checks         int
	availableAfter int
}

func (s *submissionSkillRouter) ListRunSkillSnapshots(context.Context, string) ([]skillrun.Snapshot, error) {
	s.checks++
	if s.checks <= s.availableAfter {
		return nil, nil
	}
	return []skillrun.Snapshot{{Name: "scientific-writing", ContentHash: "content", PackageHash: "package"}}, nil
}

func TestWorkflowSubmissionNormalizesBeforeSkillValidationAndPreservesSkillBudget(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["normalizedQuestion","selectedSkills","routes"],"properties":{"normalizedQuestion":{"type":"string"},"selectedSkills":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}}}},"routes":{"type":"array","items":{"type":"object","properties":{"stagePlans":{"type":"array","items":{"type":"object","properties":{"skillNames":{"type":"array","items":{"type":"string"}}}}}}}}}}`)
	raw := `{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"}],"routes":[{"stagePlans":[{"skillNames":"scientific-writing"}]}]}`
	for _, unavailable := range []int{0, 2, 3} {
		t.Run(string(rune('0'+unavailable)), func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, submissionScript(raw), submissionScript(raw), submissionScript(raw))
			bindSubmissionRepair(loop, state)
			loop.research.(*staticResearchGuidance).value.StructuredOutputSchema = schema
			router := &submissionSkillRouter{availableAfter: unavailable}
			loop.skillRouter = router
			outcome := loop.Run(context.Background(), state.run.ID)
			if unavailable < 3 {
				if outcome != OutcomeCompleted || len(provider.Requests()) != unavailable+1 || router.checks != unavailable+1 {
					t.Fatalf("normalization bypassed Skill validation/budget: outcome=%s requests=%d checks=%d run=%#v", outcome, len(provider.Requests()), router.checks, state.run)
				}
			} else if outcome != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_PLANNER_SKILLS_INVALID" || len(provider.Requests()) != 3 {
				t.Fatalf("Skill correction did not stop after two: %s %#v", outcome, state.run)
			}
			if unavailable > 0 {
				messages := provider.Requests()[1].Messages
				if messages[len(messages)-2].Content != raw || !strings.Contains(messages[len(messages)-1].Content, "scientific-writing") {
					t.Fatal("Skill correction lost original candidate or missing Skill diagnostic")
				}
			}
		})
	}
}

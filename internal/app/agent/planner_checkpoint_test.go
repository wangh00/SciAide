package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/skillrun"
)

func (s *loopState) PlannerCheckpoint(context.Context, string) (chat.PlannerCheckpoint, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plannerCheckpoint == nil {
		return chat.PlannerCheckpoint{}, false, nil
	}
	return *s.plannerCheckpoint, true, nil
}

func (s *loopState) CheckPlannerCheckpointActive(_ context.Context, runID string, _ chat.WorkflowAIExecution) error {
	if s.run.ID != runID || s.run.Status != chat.RunRunning {
		return fmt.Errorf("fixture planner is not active")
	}
	return nil
}

func (s *loopState) SavePlannerCheckpoint(_ context.Context, value chat.PlannerCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := chat.ValidatePlannerCheckpoint(value); err != nil {
		return err
	}
	want := chat.ModelTurnCompleted
	if value.RequestInFlight {
		want = chat.ModelTurnStreaming
	}
	if s.journalStatuses[value.TurnIndex] != want || s.run.ModelTurns != value.TurnIndex {
		return fmt.Errorf("checkpoint response mismatch")
	}
	if value.Candidate != "" && (s.journalStatuses[value.CandidateTurnIndex] != chat.ModelTurnCompleted || s.journalDrafts[value.CandidateTurnIndex] != value.Candidate) {
		return fmt.Errorf("checkpoint baseline mismatch")
	}
	if s.plannerCheckpoint != nil {
		if chat.SamePlannerCheckpoint(*s.plannerCheckpoint, value) {
			return nil
		}
		if !chat.ValidPlannerCheckpointTransition(*s.plannerCheckpoint, value) {
			return fmt.Errorf("checkpoint transition mismatch")
		}
	} else if !value.RequestInFlight || value.TurnIndex != 1 {
		return fmt.Errorf("checkpoint cannot renew a legacy budget")
	}
	s.plannerCheckpoint = &value
	return nil
}

func seedPlannerCheckpoint(t *testing.T, loop *Loop, state *loopState) {
	t.Helper()
	c, found, err := loop.runs.(interface {
		WorkflowAIContract(context.Context, string) (chat.WorkflowAIExecution, bool, error)
	}).WorkflowAIContract(context.Background(), "run")
	if err != nil || !found {
		t.Fatal("missing fixture contract", err)
	}
	state.plannerCheckpoint = &chat.PlannerCheckpoint{Version: 1, RunID: "run", ExecutionID: c.ID, WorkflowRunID: c.WorkflowRunID, WorkflowStepID: c.WorkflowStepID, Attempt: c.Attempt,
		InputSHA256: c.InputSHA256, PromptSHA256: c.PromptSHA256, SchemaSHA256: c.OutputSchemaSHA256, TurnIndex: state.run.ModelTurns, UpdatedAt: time.Now()}
}

func newPlannerApprovalLoop(l *Loop) *Loop {
	return NewLoop(l.runs, l.conversations, l.tools, l.registry, l.approvals, l.executor, l.models, l.observer, Options{ContextBuilder: l.builder, Checkpoints: l.checkpoints, SkillContexts: l.skillContexts, SkillRouter: l.skillRouter, Research: l.research, Resources: l.resources})
}

func TestPlannerApprovalResumeDoesNotRenewCorrectionBudget(t *testing.T) {
	good := `{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"}],"routes":[]}`
	for _, kind := range []string{"format", "skills", "missing"} {
		t.Run(kind, func(t *testing.T) {
			candidate, corrections, code := good+`}`, 2, "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED"
			if kind == "skills" {
				candidate, code = good, "WORKFLOW_PLANNER_SKILLS_INVALID"
			}
			if kind == "missing" {
				candidate, corrections, code = "I will submit the research plan next.", maxStructuredContinuationAttempts, "WORKFLOW_AI_OUTPUT_MISSING"
			}
			// Planner Web/Browser calls are moderate-risk and can really request
			// approval in Plan mode; the fixture itself performs no network I/O.
			load := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "search-before-approval", Name: "builtin.web.search", Arguments: json.RawMessage(`{"query":"synthetic research design"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
			scripts := make([][]fake.Step, 0, 8)
			for range corrections {
				scripts = append(scripts, submissionScript(candidate))
			}
			scripts = append(scripts, load)
			for range 4 {
				scripts = append(scripts, submissionScript(candidate))
			}
			loop, state, provider := newLoopFixture(t, nil, scripts...)
			bindPlannerActionFixture(loop, state)
			contractRuns := loop.runs.(workflowContractRuns)
			contractRuns.contract.AllowedTools = json.RawMessage(`["builtin.web.search"]`)
			loop.runs = contractRuns
			loop.research.(*staticResearchGuidance).value.AllowedToolNames = []string{"builtin.web.search"}
			loop.resources = nil
			loop.skillRouter = &plannerSubmissionPrerequisiteFixture{}
			definition := tool.Definition{QualifiedName: "builtin.web.search", Description: "controlled no-I/O research search", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskModerate, Idempotent: true, Version: "1"}
			if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: definition, invoke: func(tool.Invocation) tool.Result {
				// Successful tool text is deliberately not a persistent Skill snapshot.
				return tool.Result{Status: tool.ResultSuccess, Text: "loaded", Structured: json.RawMessage(`{}`)}
			}}); err != nil {
				t.Fatal(err)
			}
			service := loop.tools.(*tool.Service)
			loop.approvals = statefulAskCoordinator{service, state}
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval || len(provider.Requests()) != corrections+1 {
				t.Fatal("did not reach the real approval boundary", outcome, state.run, len(provider.Requests()))
			}
			calls, err := service.ListByRun(context.Background(), "run")
			if err != nil || len(calls) != 1 {
				t.Fatal("missing pending approval", err, len(calls))
			}
			if _, err := service.Start(context.Background(), calls[0].ID); err != nil {
				t.Fatal(err)
			}
			state.transitionRun(chat.RunWaitingApproval, chat.RunRunning)
			// All execution locals are reconstructed, as after an application restart.
			resumed := newPlannerApprovalLoop(loop)
			outcome := resumed.Run(context.Background(), "run")
			if outcome != OutcomeFailed || state.run.ErrorCode != code || len(provider.Requests()) != corrections+2 {
				t.Fatalf("approval renewed %s correction budget: outcome=%s code=%s requests=%d, want %s and %d", kind, outcome, state.run.ErrorCode, len(provider.Requests()), code, corrections+2)
			}
			if len(state.calls) != 1 || state.journalDrafts[1] != candidate {
				t.Fatal("approval recovery lost original audit or repeated tools")
			}
			if kind != "missing" && !strings.Contains(submissionRequestText(provider.Requests()[corrections+1]), candidate) {
				t.Fatal(fmt.Sprintf("rejected %s candidate disappeared on approval resume", kind))
			}
		})
	}
}

func TestPlannerCheckpointUncertainRecoveryStopsBeforeModelAndToolIO(t *testing.T) {
	for _, kind := range []string{"legacy_missing", "in_flight", "wrong_binding", "turn_gap", "load_failed", "inactive"} {
		t.Run(kind, func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, submissionScript("must not be requested"))
			bindPlannerActionFixture(loop, state)
			state.run.ModelTurns = 1
			if kind != "legacy_missing" {
				seedPlannerCheckpoint(t, loop, state)
				if kind == "in_flight" {
					state.plannerCheckpoint.RequestInFlight = true
				}
				if kind == "wrong_binding" {
					state.plannerCheckpoint.ExecutionID = "different"
				}
				if kind == "turn_gap" {
					state.run.ModelTurns = 2
				}
			}
			if kind == "load_failed" {
				loop.runs = failingPlannerCheckpointRuns{workflowContractRuns: loop.runs.(workflowContractRuns), loadFailure: true}
			}
			if kind == "inactive" {
				loop.runs = inactivePlannerCheckpointRuns{workflowContractRuns: loop.runs.(workflowContractRuns)}
			}
			call, err := loop.tools.(*tool.Service).ProposeRegistered(context.Background(), loop.registry, "builtin.fixture", tool.CreateCommand{RunID: "run", ProviderCallID: "already-approved", Arguments: json.RawMessage(`{"query":"must not execute"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loop.tools.(*tool.Service).Start(context.Background(), call.ID); err != nil {
				t.Fatal(err)
			}
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || !strings.HasPrefix(state.run.ErrorCode, "WORKFLOW_PLANNER_CHECKPOINT_") || len(provider.Requests()) != 0 {
				t.Fatal("uncertain recovery made an unmetered request", outcome, state.run, len(provider.Requests()))
			}
			if state.calls[call.ID].Result != nil {
				t.Fatal("pending tool executed before verifying host checkpoint")
			}
		})
	}
}

type inactivePlannerCheckpointRuns struct{ workflowContractRuns }

func (inactivePlannerCheckpointRuns) CheckPlannerCheckpointActive(context.Context, string, chat.WorkflowAIExecution) error {
	return fmt.Errorf("controlled interrupted owning Workflow")
}

type failingPlannerCheckpointRuns struct {
	workflowContractRuns
	loadFailure bool
	failReady   bool
}

func (r failingPlannerCheckpointRuns) PlannerCheckpoint(ctx context.Context, id string) (chat.PlannerCheckpoint, bool, error) {
	if r.loadFailure {
		return chat.PlannerCheckpoint{}, false, fmt.Errorf("controlled checkpoint read failure")
	}
	return r.loopState.PlannerCheckpoint(ctx, id)
}

func (r failingPlannerCheckpointRuns) SavePlannerCheckpoint(ctx context.Context, v chat.PlannerCheckpoint) error {
	if !r.failReady || !v.RequestInFlight {
		return fmt.Errorf("controlled checkpoint write failure")
	}
	return r.loopState.SavePlannerCheckpoint(ctx, v)
}

func TestPlannerCheckpointWriteFailuresNeverStartAnUntrackedNextRequest(t *testing.T) {
	for _, decisionFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("decision_%t", decisionFailure), func(t *testing.T) {
			bad := `{"normalizedQuestion":"q","selectedSkills":[],"routes":[]} trailing`
			loop, state, provider := newLoopFixture(t, nil, submissionScript(bad), submissionScript(bad))
			bindPlannerActionFixture(loop, state)
			loop.runs = failingPlannerCheckpointRuns{workflowContractRuns: loop.runs.(workflowContractRuns), failReady: decisionFailure}
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_PLANNER_CHECKPOINT_SAVE_FAILED" {
				t.Fatal(outcome, state.run)
			}
			want := 0
			if decisionFailure {
				want = 1
			}
			if len(provider.Requests()) != want {
				t.Fatal("checkpoint failure allowed an untracked or repeated request", len(provider.Requests()), want)
			}
		})
	}
}

func TestPlannerCheckpointLargeCandidatesAndDiagnosticsKeepOriginalValidation(t *testing.T) {
	good := `{"normalizedQuestion":"q","selectedSkills":[],"routes":[]}`
	for _, kind := range []string{"large_escaped_candidate", "long_unknown_property"} {
		t.Run(kind, func(t *testing.T) {
			bad := `{"normalizedQuestion":"` + strings.Repeat("<", 180000) + `","selectedSkills":[],"routes":{}}`
			if kind == "long_unknown_property" {
				bad = `{"normalizedQuestion":"q","selectedSkills":[],"routes":[],"` + strings.Repeat("unexpected_", 4000) + `":true}`
			}
			loop, state, provider := newLoopFixture(t, nil, submissionScript(bad), submissionToolScript(good))
			bindPlannerActionFixture(loop, state)
			if kind == "long_unknown_property" {
				contractRuns := loop.runs.(workflowContractRuns)
				var schema map[string]any
				_ = json.Unmarshal(contractRuns.contract.OutputSchema, &schema)
				schema["additionalProperties"] = false
				contractRuns.contract.OutputSchema, _ = json.Marshal(schema)
				contractRuns.contract.OutputSchemaSHA256 = fmt.Sprintf("%x", sha256.Sum256(contractRuns.contract.OutputSchema))
				loop.runs = contractRuns
			}
			// Match a configured window which actually admits this candidate;
			// this test concerns checkpoint capacity, not undersized contexts.
			loop.models = budgetResolver{model: provider, budget: modelcap.ResolveContextBudget(272000, 244800, modelcap.ContextWindowSourceManual)}
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 2 || state.journalDrafts[1] != bad {
				t.Fatal("checkpoint changed original formatting repair or audit", outcome, state.run, len(provider.Requests()))
			}
			if kind == "long_unknown_property" && (!strings.Contains(submissionRequestText(provider.Requests()[1]), "path truncated; SHA256=") || !strings.Contains(submissionRequestText(provider.Requests()[1]), "Schema diagnostic limit reached")) {
				t.Fatal("long diagnostic projection lost its explicit truncation marker")
			}
			if kind == "long_unknown_property" {
				for _, required := range []string{"unknown property", "Correct only the result", "Do not repeat completed tools", "submit the complete corrected result"} {
					if !strings.Contains(submissionRequestText(provider.Requests()[1]), required) {
						t.Fatal("bounded diagnostic lost error category or fixed correction requirements", required)
					}
				}
			}
		})
	}
}

func TestPlannerBoundedFeedbackIsIdempotentAndPreservesBothEnds(t *testing.T) {
	value := "fixed-start\n" + strings.Repeat("路径", 40000) + "\nunknown property; fixed-end"
	for _, limit := range []int{6000, 7000, 14000} {
		got := boundedPlannerFeedback(value, limit)
		if boundedPlannerFeedback(got, limit) != got || !strings.HasPrefix(got, "fixed-start") || !strings.HasSuffix(got, "fixed-end") || !strings.Contains(got, "unknown property") {
			t.Fatal("bounded diagnostic lost semantics or changed on resume", limit)
		}
	}
}

type plannerModerateApprovalFixture struct {
	service *tool.Service
	state   *loopState
}

func (c plannerModerateApprovalFixture) EvaluateCall(ctx context.Context, projectID, callID string) (permission.Coordination, error) {
	call, err := c.service.Get(ctx, callID)
	if err != nil {
		return permission.Coordination{}, err
	}
	if call.Risk == tool.RiskLow {
		return (allowCoordinator{c.service}).EvaluateCall(ctx, projectID, callID)
	}
	return (statefulAskCoordinator{c.service, c.state}).EvaluateCall(ctx, projectID, callID)
}

func TestPlannerApprovalResumeRechecksActualSkillSnapshotsWithoutAcceptingBaseline(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete_%t", complete), func(t *testing.T) {
			candidate := `{"normalizedQuestion":"question","selectedSkills":[{"name":"scientific-writing"},{"name":"statistical-analysis"}],"routes":[]}`
			ref := "res_" + strings.Repeat("c", 32)
			load := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "load-snapshot", Name: resource.OpenTool, Arguments: json.RawMessage(`{"actionId":"` + ref + `"}`)}}}, {Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "ask-before-resubmit", Name: "builtin.web.search", Arguments: json.RawMessage(`{"query":"controlled-no-io"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
			loop, state, provider := newLoopFixture(t, nil, submissionScript(candidate), submissionScript(candidate), load, submissionToolScript(candidate))
			bindPlannerActionFixture(loop, state)
			contractRuns := loop.runs.(workflowContractRuns)
			contractRuns.contract.AllowedTools = json.RawMessage(`["builtin.resource.open","builtin.web.search"]`)
			loop.runs = contractRuns
			loop.research.(*staticResearchGuidance).value.AllowedToolNames = []string{resource.OpenTool, "builtin.web.search"}
			router := &plannerSubmissionPrerequisiteFixture{snapshots: []skillrun.Snapshot{{Name: "scientific-writing", ContentHash: "content", PackageHash: "package"}}}
			if complete {
				router.snapshots = append(router.snapshots, skillrun.Snapshot{Name: "statistical-analysis", ContentHash: "content2", PackageHash: "package2"})
			}
			loop.skillRouter = router
			definitions := []tool.Definition{}
			for _, name := range []string{resource.OpenTool, "builtin.web.search"} {
				risk := tool.RiskLow
				if name == "builtin.web.search" {
					risk = tool.RiskModerate
				}
				definition := tool.Definition{QualifiedName: name, Description: "controlled snapshot/approval fixture", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: risk, Idempotent: true, Version: "1"}
				if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: definition, invoke: func(tool.Invocation) tool.Result {
					router.loaded = true
					return tool.Result{Status: tool.ResultSuccess, Text: "not proof; the separate snapshot repository is authoritative", Structured: json.RawMessage(`{}`)}
				}}); err != nil {
					t.Fatal(err)
				}
				definitions = append(definitions, definition)
			}
			loop.resources = &resourceViewFixture{view: resource.View{Definitions: definitions, OpenIDs: map[string]bool{ref: true}, SkillActions: map[string]string{"scientific-writing": ref}}}
			service := loop.tools.(*tool.Service)
			loop.approvals = plannerModerateApprovalFixture{service, state}
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval || len(provider.Requests()) != 3 {
				t.Fatal("missing approval boundary", outcome, state.run)
			}
			calls, _ := service.ListByRun(context.Background(), "run")
			for _, call := range calls {
				if call.ToolName == "builtin.web.search" {
					if _, err := service.Finish(context.Background(), call.ID, tool.Result{Status: tool.ResultDenied, Text: "controlled denial"}, "TOOL_PERMISSION_DENIED", "controlled denial"); err != nil {
						t.Fatal(err)
					}
				}
			}
			state.transitionRun(chat.RunWaitingApproval, chat.RunRunning)
			resumed := newPlannerApprovalLoop(loop)
			outcome := resumed.Run(context.Background(), "run")
			if len(provider.Requests()) != 4 || !router.loaded {
				t.Fatal("did not recheck actual snapshots", outcome, state.run, len(provider.Requests()))
			}
			if complete && outcome != OutcomeCompleted || !complete && (outcome != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_PLANNER_SKILLS_INVALID") {
				t.Fatal("Skill results bypassed validation or renewed budget", complete, outcome, state.run)
			}
			feedback := ""
			for _, message := range provider.Requests()[3].Messages {
				if message.Role == model.RoleUser && message.HostToolReferences {
					feedback += "\n" + message.Content
				}
			}
			if complete {
				if !strings.Contains(feedback, "上次候选仍未被接受") || strings.Contains(feedback, "WORKFLOW_PLANNER_SKILLS_MISSING") {
					t.Fatal("fully loaded snapshots silently accepted baseline or kept stale error", feedback)
				}
			} else if !strings.Contains(feedback, "statistical-analysis") || strings.Contains(feedback, ref) {
				t.Fatal("partial snapshots repeated an already loaded action", feedback)
			}
			if state.plannerCheckpoint.SkillCorrections != 2 || state.journalDrafts[1] != candidate {
				t.Fatal("resume lost original baseline or spent count")
			}
		})
	}
}

package workflow

import (
	"context"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestRecoveredFailedToolCallPreservesExplicitPrerequisiteStop(t *testing.T) {
	d := RunDetail{Run: Run{ID: "restart-run", Compilation: Compilation{Nodes: []CompiledNode{{OutputSchema: researchAcceptanceReviewSchema()}}}}}
	step := Step{ID: "calculation", NodeID: "python_analysis", NodeKind: NodePython, Status: StepRunning, Attempt: 1}
	call := tool.Call{ID: "persisted-call", Status: tool.CallFailed, Result: &tool.Result{Status: tool.ResultError,
		Structured: raw(`{"status":"error","exception":{"type":"SciAideResearchBlocked","message":"resolve the measurement choice"}}`)}}
	repository := &repairRecoveryRepository{}
	if err := repairRecoveryService(repository).commitToolResult(context.Background(), d, step, call); err != nil {
		t.Fatal(err)
	}
	if repository.failure != researchPrerequisiteBlockedCode || len(repository.output) != 0 {
		t.Fatal("recovered error was published as successful output", repository.failure, string(repository.output))
	}
}

func TestPrerequisiteSignalIsExplicitAndDoesNotGuessScientificResults(t *testing.T) {
	d := RunDetail{Run: Run{Compilation: Compilation{Nodes: []CompiledNode{{OutputSchema: researchAcceptanceReviewSchema()}}}}}
	step := Step{NodeID: "python_analysis", NodeKind: NodePython}
	call := tool.Call{Result: &tool.Result{Status: tool.ResultError, Structured: raw(`{"status":"error","exception":{"type":"SciAideResearchBlocked","message":"measurement rule unavailable"}}`)}}
	if reason, ok := declaredResearchBlocker(d, step, call); !ok || reason != "measurement rule unavailable" {
		t.Fatal(reason, ok)
	}
	if _, ok := pythonExecutionRepairReason(call); ok {
		t.Fatal("scientific blocker triggered auto repair")
	}
	for _, value := range []string{`{"status":"success","value":{"status":"blocked","calculation_started":false,"partial":true}}`, `{"status":"success","value":{"nullFinding":true}}`, `{"status":"error","exception":{"type":"ValueError","message":"blocked"}}`} {
		call.Result.Structured = raw(value)
		if _, ok := declaredResearchBlocker(d, step, call); ok {
			t.Fatal("keyword/value scan", value)
		}
	}
}

func TestPrerequisiteDiscussionOffersResponsibleStagesNotDownstreamReport(t *testing.T) {
	d := boundDataFixture()
	d.Run.Status = RunFailed
	d.Run.ErrorCode = researchPrerequisiteBlockedCode
	d.Run.CurrentStep = 3
	d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, CompiledNode{ID: "review", OutputSchema: researchAcceptanceReviewSchema()})
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "data_preflight", ToNode: "method_implementation"}, Edge{FromNode: "method_implementation", ToNode: "python_analysis"})
	for i := range d.Steps {
		d.Steps[i].Status = StepCompleted
	}
	d.Steps[2].NodeKind = NodePython
	d.Steps[2].Status = StepFailed
	d.Steps[2].ErrorCode = researchPrerequisiteBlockedCode
	d.Steps[3].Status = StepQueued
	targets := discussionRevisionTargets(d)
	if len(targets) != 2 {
		t.Fatal(targets)
	}
	if _, err := newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: "method_implementation", Summary: "resolve unavailable measurement rule", Changes: []string{"apply the explicitly agreed sensitivity analysis"}, Reason: "the implementation declares the blocker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: "report_drafting", Summary: "skip calculation", Changes: []string{"only rewrite the report"}, Reason: "avoid input"}); err == nil {
		t.Fatal("downstream shortcut")
	}
	d.Run.ErrorCode = "TRANSPORT_ERROR"
	if len(discussionRevisionTargets(d)) != 0 || validateDiscussionDelivery(d) == nil {
		t.Fatal("generic error entered scientific repair")
	}
}

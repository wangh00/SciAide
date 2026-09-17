package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestPythonRepairBudgetPersistsAndResetsOnlyAtBoundary(t *testing.T) {
	event := RuntimeEvent{Type: "workflow.upstream_revision_queued", Payload: raw(`{"failedStepId":"python","automatic":true}`)}
	detail := RunDetail{Events: []RuntimeEvent{event, event, event}}
	if consecutivePythonRepairs(detail, "python") != 3 {
		t.Fatal("lost persisted attempts")
	}
	detail.Events = append(detail.Events, RuntimeEvent{Type: "workflow.human_decided", Payload: raw(`{"stepId":"producer"}`)})
	if consecutivePythonRepairs(detail, "python") != 3 {
		t.Fatal("approval reset budget")
	}
	for _, boundary := range []RuntimeEvent{{Type: "workflow.step_completed", Payload: raw(`{"stepId":"python"}`)}, {Type: "workflow.step_retry_queued", Payload: raw(`{"stepId":"python"}`)}, {Type: "workflow.upstream_revision_queued", Payload: raw(`{"failedStepId":"python","manual":true}`)}, {Type: "workflow.user_revision_queued", Payload: raw(`{}`)}} {
		copy := detail
		copy.Events = append(append([]RuntimeEvent{}, detail.Events...), boundary, event)
		if consecutivePythonRepairs(copy, "python") != 1 {
			t.Fatal("boundary failed", boundary.Type)
		}
	}
}

type pythonRepairRepo struct {
	repairRecoveryRepository
	queued []RuntimeEvent
	waited bool
}

func (r *pythonRepairRepo) QueueAutomaticPythonRepair(_ context.Context, _, _, _ string, _ time.Time, event RuntimeEvent) error {
	r.queued = append(r.queued, event)
	return nil
}
func (r *pythonRepairRepo) WaitAgentStage(_ context.Context, _, _ string, output json.RawMessage, _ time.Time, _ RuntimeEvent) error {
	r.waited = true
	r.output = output
	return nil
}

func TestPythonRepairStopsBeforeFourthAutomaticRevision(t *testing.T) {
	r := &pythonRepairRepo{}
	s := &RuntimeService{repository: r, now: time.Now, newID: func() (string, error) { return "event", nil }}
	producer := Step{ID: "producer", NodeID: "method_implementation", Status: StepCompleted, Ordinal: 0, Attempt: 1, Output: raw(`{"analysis":{"code":"old"}}`)}
	failed := Step{ID: "python", NodeID: "python_analysis", Ordinal: 2, Status: StepRunning}
	detail := RunDetail{Run: Run{ID: "run"}, Steps: []Step{producer, failed}}
	for i := 0; i < 4; i++ {
		ok, err := s.queueAutomaticPythonRepair(context.Background(), detail, failed, tool.Call{ID: "call"}, "TypeError")
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if i < 3 {
			if len(r.queued) != i+1 {
				t.Fatal("missing repair")
			}
			detail.Events = append(detail.Events, r.queued[i])
		}
	}
	if len(r.queued) != 3 || r.failure != "WORKFLOW_PYTHON_REPAIR_EXHAUSTED" {
		t.Fatal(len(r.queued), r.failure)
	}
}

func TestImplementationAutomaticallyContinuesUnlessScientificMetadataChanges(t *testing.T) {
	node := CompiledNode{ID: "method_implementation", Kind: NodeAgentStage, ReviewPolicy: AIReviewAuto, PromptVersion: dynamicImplementationPromptVersion, OutputSchema: dynamicImplementationSchema()}
	base := raw(`{"methodSummary":"Welch t","dependencies":[],"analysisInput":{"alpha":0.05},"assumptions":[],"limitations":[],"researchChanges":[],"code":"SCIAIDE_INPUTS; SCIAIDE_OUTPUTS; result = {}"}`)
	for _, name := range []string{"first", "code-only", "parameter", "method", "declared-change", "missing-declaration"} {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			_ = json.Unmarshal(base, &value)
			input := raw(`{}`)
			if name != "first" {
				input = rawObject(map[string]any{"_pythonRepair": automaticPythonRepair{PriorImplementation: base}})
			}
			switch name {
			case "code-only":
				value["code"] = "SCIAIDE_INPUTS; SCIAIDE_OUTPUTS; result = {'fixed': True}"
			case "parameter":
				value["analysisInput"] = map[string]any{"alpha": 0.1}
			case "method":
				value["methodSummary"] = "Mann Whitney"
			case "declared-change":
				value["researchChanges"] = []any{map[string]any{"before": "Welch", "after": "MW", "reason": "distribution", "impact": "different estimand"}}
			case "missing-declaration":
				delete(value, "researchChanges")
			}
			output := rawObject(value)
			if name == "missing-declaration" {
				if _, err := implementationConfirmation(node, input, output); err == nil {
					t.Fatal("missing declaration accepted")
				}
				return
			}
			step := Step{ID: "producer", NodeID: node.ID, Status: StepRunning, Attempt: 1, Input: input, InputSHA256: hashJSON(input)}
			detail := RunDetail{Run: Run{ID: "run", Compilation: Compilation{Nodes: []CompiledNode{node}}}, Steps: []Step{step}}
			e := AIExecution{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, Output: output, OutputSHA256: hashJSON(output), Status: "completed"}
			r := &pythonRepairRepo{}
			s := &RuntimeService{repository: r, now: time.Now, newID: func() (string, error) { return "event", nil }}
			if err := s.projectFinishedAIExecution(context.Background(), detail, step, node, e); err != nil {
				t.Fatal(err)
			}
			want := name == "parameter" || name == "method" || name == "declared-change"
			if r.failure != "" || r.waited != want {
				t.Fatal("incorrect continuation", r.failure, r.waited, want)
			}
		})
	}
}

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Embed the interface so unexpected persistence calls fail the test rather
// than involving a real database or model executor.
type repairRecoveryRepository struct {
	RuntimeRepository
	output    json.RawMessage
	execution AIExecution
	failure   string
}

func (r *repairRecoveryRepository) CompleteStep(_ context.Context, _, _ string, output json.RawMessage, _ int, _ bool, _ json.RawMessage, _ time.Time, _ RuntimeEvent) error {
	r.output = cloneRaw(output)
	return nil
}

func (r *repairRecoveryRepository) WaitAgentStage(_ context.Context, _, _ string, output json.RawMessage, _ time.Time, _ RuntimeEvent) error {
	r.output = cloneRaw(output)
	return nil
}

func (r *repairRecoveryRepository) FailStep(_ context.Context, _, _ string, _ StepStatus, _ RunStatus, code, _ string, _ time.Time, _ RuntimeEvent) error {
	r.failure = code
	return nil
}

func (r *repairRecoveryRepository) FinishAIExecution(_ context.Context, execution AIExecution) error {
	r.execution = execution
	return nil
}

func (r *repairRecoveryRepository) RecordEvent(context.Context, RuntimeEvent) error { return nil }

func repairRecoveryFixture() (RunDetail, Step, CompiledNode, AIExecution) {
	node := CompiledNode{ID: "result", Kind: NodeAIAnalysis, OutputSchema: raw(`{"type":"object","required":["summary","queries"],"properties":{"summary":{"type":"string"},"queries":{"type":"array","items":{"type":"string","maxLength":3}}}}`)}
	step := Step{ID: "result-step", NodeID: node.ID, Attempt: 3, Input: raw(`{"data":"new"}`), Status: StepRunning}
	step.InputSHA256 = hashJSON(step.Input)
	detail := RunDetail{Run: Run{ID: "run", Compilation: Compilation{Nodes: []CompiledNode{node}}}, Steps: []Step{step}}
	execution := AIExecution{WorkflowStepID: step.ID, Attempt: step.Attempt, Status: "completed", InputSHA256: step.InputSHA256, Output: raw(`{"summary":"frozen repaired result","queries":["ok"]}`), OutputText: "original invalid model response"}
	execution.OutputSHA256 = hashJSON(execution.Output)
	return detail, step, node, execution
}

func repairRecoveryService(repository *repairRecoveryRepository) *RuntimeService {
	return &RuntimeService{repository: repository, now: time.Now, newID: func() (string, error) { return "event", nil }}
}

func TestTargetedAIRepairRequiresSameInputAndInvalidCurrentOutput(t *testing.T) {
	detail, step, node, current := repairRecoveryFixture()
	current.OutputText = `{"summary":"new scientific result","queries":["ok"]}`
	baseline := AIExecution{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, OutputText: `{"summary":"old scientific result","queries":["toolong"]}`, ErrorCode: "WORKFLOW_AI_OUTPUT_INVALID", ErrorMessage: "$.queries[0]: violates maxLength"}
	for _, tt := range []struct {
		name       string
		inputHash  string
		currentErr error
		want       bool
	}{
		{"same input repair", step.InputSHA256, errors.New("invalid current output"), true},
		{"different input", hashJSON([]byte(`{"data":"old"}`)), errors.New("invalid current output"), false},
		{"missing input", "", errors.New("invalid current output"), false},
		{"already valid", step.InputSHA256, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			baseline.InputSHA256 = tt.inputHash
			detail.AIExecutions = []AIExecution{baseline}
			_, _, ok := mergeTargetedAIRepair(detail, step, node, current, tt.currentErr)
			if ok != tt.want {
				t.Fatalf("merged = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestCompleteAIExecutionPreservesValidCurrentOutput(t *testing.T) {
	detail, step, node, execution := repairRecoveryFixture()
	execution.OutputText = `{"summary":"new scientific result","queries":["ok"]}`
	detail.AIExecutions = []AIExecution{{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, OutputText: `{"summary":"old scientific result","queries":["toolong"]}`, ErrorCode: "WORKFLOW_AI_OUTPUT_INVALID", ErrorMessage: "$.queries[0]: violates maxLength"}}
	repository := &repairRecoveryRepository{}
	if err := repairRecoveryService(repository).completeAIExecution(context.Background(), detail, step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if repository.failure != "" || !strings.Contains(string(repository.execution.Output), "new scientific result") {
		t.Fatalf("result=%s failure=%s", repository.execution.Output, repository.failure)
	}
}

func TestCompletedAIExecutionRecoversPreviouslyFrozenDerivedResult(t *testing.T) {
	detail, step, node, execution := repairRecoveryFixture()
	// Historical hosts may have frozen a derived result instead of their raw
	// model response. New submissions no longer merge responses, but recovery
	// must continue to validate and honor already frozen historical results.
	execution.OutputText = `{"summary":42,"queries":["ok"]}`
	recovered := &repairRecoveryRepository{}
	if err := repairRecoveryService(recovered).projectFinishedAIExecution(context.Background(), detail, step, node, execution); err != nil {
		t.Fatal(err)
	}
	analysis, err := outputPort(recovered.output, "analysis")
	if recovered.failure != "" || err != nil || !rawJSONEqual(analysis, execution.Output) {
		t.Fatalf("recovery failure=%s output=%s, want %s", recovered.failure, recovered.output, execution.Output)
	}
}

func TestCompletedAIExecutionRecoveryValidatesFrozenOutput(t *testing.T) {
	for _, tt := range []struct {
		name    string
		mutate  func(*RunDetail, *Step, *CompiledNode, *AIExecution)
		failure string
	}{
		{name: "raw response invalid"},
		{name: "raw response differs", mutate: func(_ *RunDetail, _ *Step, _ *CompiledNode, e *AIExecution) {
			e.OutputText = `{"summary":"unrepaired model result","queries":["ok"]}`
		}},
		{name: "human review", mutate: func(_ *RunDetail, _ *Step, n *CompiledNode, _ *AIExecution) {
			n.Kind, n.ReviewPolicy = NodeAgentStage, AIReviewHuman
		}},
		{name: "tampered output", mutate: func(_ *RunDetail, _ *Step, _ *CompiledNode, e *AIExecution) { e.Output = raw(`{}`) }, failure: "WORKFLOW_AI_OUTPUT_SNAPSHOT_INVALID"},
		{name: "different input", mutate: func(_ *RunDetail, _ *Step, _ *CompiledNode, e *AIExecution) { e.InputSHA256 = "old" }, failure: "WORKFLOW_AI_OUTPUT_SNAPSHOT_INVALID"},
		{name: "tampered input", mutate: func(_ *RunDetail, s *Step, _ *CompiledNode, _ *AIExecution) { s.Input = raw(`{}`) }, failure: "WORKFLOW_AI_OUTPUT_SNAPSHOT_INVALID"},
		{name: "wrong attempt", mutate: func(_ *RunDetail, _ *Step, _ *CompiledNode, e *AIExecution) { e.Attempt-- }, failure: "WORKFLOW_AI_OUTPUT_SNAPSHOT_INVALID"},
		{name: "schema invalid", mutate: func(_ *RunDetail, _ *Step, _ *CompiledNode, e *AIExecution) {
			e.Output = raw(`{}`)
			e.OutputSHA256 = hashJSON(e.Output)
		}, failure: "WORKFLOW_AI_OUTPUT_INVALID"},
		{name: "missing required skill", mutate: func(_ *RunDetail, s *Step, n *CompiledNode, e *AIExecution) {
			n.SkillRouting = true
			s.Input = raw(`{"routeContext":{"selectedSkills":[{"name":"statistics-method","role":"method","stageIds":["result"],"contentHash":"content-v1","packageHash":"package-v1"}]}}`)
			s.InputSHA256 = hashJSON(s.Input)
			e.InputSHA256 = s.InputSHA256
		}, failure: "WORKFLOW_SKILL_SNAPSHOT_INVALID"},
		{name: "invalid citation snapshot", mutate: func(d *RunDetail, s *Step, n *CompiledNode, _ *AIExecution) {
			s.Ordinal = 1
			d.Steps = append(d.Steps, Step{NodeID: "evidence", Ordinal: 0, Status: StepCompleted, Output: raw(`invalid`)})
			d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, CompiledNode{ID: "evidence", Kind: NodeCitationSelection})
			d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: "evidence", ToNode: n.ID})
		}, failure: "WORKFLOW_AI_CITATIONS_INVALID"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail, step, node, execution := repairRecoveryFixture()
			if tt.mutate != nil {
				tt.mutate(&detail, &step, &node, &execution)
			}
			repository := &repairRecoveryRepository{}
			if err := repairRecoveryService(repository).projectFinishedAIExecution(context.Background(), detail, step, node, execution); err != nil {
				t.Fatal(err)
			}
			if repository.failure != tt.failure {
				t.Fatalf("failure=%q, want %q", repository.failure, tt.failure)
			}
			if tt.failure == "" {
				analysis, err := outputPort(repository.output, "analysis")
				if err != nil || !rawJSONEqual(analysis, execution.Output) {
					t.Fatalf("recovered=%s, err=%v", repository.output, err)
				}
			} else if len(repository.output) != 0 {
				t.Fatal("invalid recovery was projected")
			}
		})
	}
}

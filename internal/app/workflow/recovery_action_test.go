package workflow

import (
	"context"
	"strings"
	"testing"
)

func TestRefusedModelKeepsExplicitSameStageRecovery(t *testing.T) {
	for _, code := range []string{"WORKFLOW_AI_MODEL_REFUSED", "MODEL_OUTPUT_REFUSED"} {
		n := CompiledNode{ID: "review", Kind: NodeAgentStage}
		d := RunDetail{Run: Run{Status: RunFailed, Compilation: Compilation{Nodes: []CompiledNode{n}}}, Steps: []Step{{ID: "current", NodeID: n.ID, NodeKind: n.Kind, Status: StepFailed, Attempt: 2, ErrorCode: code}}}
		a := recoveryAction(d)
		if a == nil || a.Label != "重试当前阶段" || !a.CallsModel || a.StepID != "current" || !strings.Contains(a.Summary, "模型明确拒绝") || !strings.Contains(a.Summary, "不会自动重发") {
			t.Fatal("refusal offered scientific revision or hid model rejection", a)
		}
	}
}

func TestProtocolRecoverySeparatesTechnicalFailureAndPreservesExplicitRetry(t *testing.T) {
	for _, kind := range []NodeKind{NodeAgentStage, NodeAIAnalysis} {
		for _, code := range []string{"MODEL_STREAM_INVALID", "MODEL_RESPONSE_INVALID", "WORKFLOW_AI_NATIVE_ACTION_MISSING"} {
			n := CompiledNode{ID: "report_drafting", Name: "报告撰写", Kind: kind}
			d := RunDetail{Run: Run{Status: RunFailed, Compilation: Compilation{Nodes: []CompiledNode{n}}}, Steps: []Step{{ID: "current", NodeID: n.ID, NodeKind: n.Kind, Status: StepFailed, Attempt: 2, ErrorCode: code}}}
			a := recoveryAction(d)
			if a == nil || a.FailureKind != "model_protocol" || a.Kind != "retry_stage" || !a.CallsModel || a.RequiresConfirmation || a.Attempt != 2 || a.StepID != "current" {
				t.Fatalf("protocol error altered retry capabilities: %+v", a)
			}
			for _, want := range []string{"不代表科研审查不通过", "报告撰写", "仍有效", "不会自动恢复", "不是从断开的网络流续传", "实现分件可复用", "后续阶段", "费用"} {
				if !strings.Contains(a.Summary, want) {
					t.Fatalf("recovery scope missing %q: %s", want, a.Summary)
				}
			}
			d.Run.Status = RunCompleted
			if recoveryAction(d) != nil {
				t.Fatal("completed stage became retryable")
			}
		}
	}
	// An error code cannot turn a local tool or a scientific rejection into AI recovery.
	n := CompiledNode{ID: "gate", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.review.gate"}}
	for _, code := range []string{"MODEL_STREAM_INVALID", "RESEARCH_REVIEW_NOT_APPROVED"} {
		d := RunDetail{Run: Run{Status: RunFailed, Compilation: Compilation{Nodes: []CompiledNode{n}}}, Steps: []Step{{ID: "gate-step", NodeID: n.ID, NodeKind: n.Kind, Status: StepFailed, ErrorCode: code}}}
		a := recoveryAction(d)
		if a == nil || a.FailureKind != "" || a.CallsModel {
			t.Fatalf("local/scientific recovery was relabeled: %+v", a)
		}
	}
}

func TestStalledReadRecoveryExplainsLimitWithoutScientificRevision(t *testing.T) {
	n := CompiledNode{ID: "draft", Name: "报告撰写", Kind: NodeAgentStage}
	d := RunDetail{Run: Run{Status: RunFailed, Compilation: Compilation{Nodes: []CompiledNode{n}}}, Steps: []Step{{ID: "current", NodeID: n.ID, NodeKind: n.Kind, Status: StepFailed, Attempt: 1, ErrorCode: "WORKFLOW_AI_READ_STALLED"}}}
	a := recoveryAction(d)
	if a == nil || a.Kind != "retry_stage" || !a.CallsModel || a.RequiresConfirmation || !strings.Contains(a.Summary, "直接重试可能再次遇到相同限制") || !strings.Contains(a.Summary, "不会自动恢复") {
		t.Fatal("read convergence failure offered science revision or hid cost/limit", a)
	}
}

func TestRecoveryUsesCapabilitiesNotDomainNames(t *testing.T) {
	for _, tc := range []struct {
		tool, code, kind string
		model            bool
	}{
		{"builtin.research.workflow.review.gate", "TOOL_INVOCATION_FAILED", "retry_gate", false},
		{"builtin.research.workflow.review.gate", "RESEARCH_REVIEW_NOT_APPROVED", "revise", false},
		{"builtin.research.workflow.report", "TOOL_INVOCATION_FAILED", "retry_publication", false},
		{"arbitrary.domain.execute", "WORKFLOW_RESEARCH_PREREQUISITE_BLOCKED", "needs_input", false},
		{"arbitrary.domain.execute", "TOOL_INVOCATION_FAILED", "retry_stage", false},
		{"", "WORKFLOW_AI_SUBMISSION_INVALID", "retry_stage", true},
	} {
		t.Run(tc.kind+tc.tool, func(t *testing.T) {
			n := CompiledNode{ID: "new-domain-stage", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: tc.tool}}
			if tc.model {
				n.Kind = NodeAgentStage
				n.Tool = nil
			}
			d := RunDetail{Run: Run{Status: RunFailed, Compilation: Compilation{Nodes: []CompiledNode{n}}}, Steps: []Step{{ID: "step", NodeID: n.ID, NodeKind: n.Kind, Status: StepFailed, Attempt: 2, ErrorCode: tc.code}}}
			a := recoveryAction(d)
			if a == nil || a.Kind != tc.kind || a.CallsModel != tc.model || a.StepID != "step" || a.Attempt != 2 {
				t.Fatalf("action=%+v", a)
			}
			d.Run.Status = RunCompleted
			if recoveryAction(d) != nil {
				t.Fatal("completed task offered replay")
			}
		})
	}
}

func TestRetryRejectsOtherFailedStepThanCurrentAction(t *testing.T) {
	d := recommendationFixture(t)
	d.Run.CurrentStep = 5
	d.Steps[5].ErrorCode = "TOOL_INVOCATION_FAILED"
	d.Steps[0].Status = StepFailed // inconsistent stale error must not be actionable
	s, repo := gateRetryService(t, d)
	if _, err := s.Retry(context.Background(), RetryCommand{RunID: d.Run.ID, StepID: d.Steps[0].ID}); err == nil || repo.gateResets != 0 || repo.resets != 0 {
		t.Fatal("replayed unrelated failed stage", err)
	}
}

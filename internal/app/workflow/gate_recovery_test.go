package workflow

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"reflect"
	"strings"
	"testing"
	"time"
)

type gateRetryRepository struct {
	recommendationRetryRepository
	gateResets int
	resetID    string
}

func (r *gateRetryRepository) ResetStepForRetry(_ context.Context, _, stepID string, _ *HumanDecision, _ time.Time, event RuntimeEvent, _ RetryGuard) error {
	r.gateResets++
	r.resetID = stepID
	r.event = event
	return nil
}
func gateRetryService(t *testing.T, d RunDetail) (*RuntimeService, *gateRetryRepository) {
	t.Helper()
	d.Run.ResearchTaskID = "task-test"
	d.Run.InputsSHA256 = hashJSON(d.Run.Inputs)
	d.Run.Compilation.CompilationSHA256 = ""
	b, e := canonicalJSON(d.Run.Compilation)
	if e != nil {
		t.Fatal(e)
	}
	d.Run.CompilationSHA256 = hashBytes(b)
	d.Run.Compilation.CompilationSHA256 = d.Run.CompilationSHA256
	repo := &gateRetryRepository{recommendationRetryRepository: recommendationRetryRepository{detail: d}}
	return &RuntimeService{repository: repo, projects: fixedProjectLoader{value: project.Project{WorkspacePath: t.TempDir()}}, permissions: permission.NewEngine(nil), tools: tool.NewService(nil, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "id", nil }, closed: true}, repo
}
func TestGateTechnicalFailureRetriesOnlyDeterministicStage(t *testing.T) {
	for _, code := range []string{"WORKFLOW_INPUT_BINDING_FAILED", "WORKFLOW_TOOL_PROPOSAL_FAILED", "TOOL_INVOCATION_FAILED"} {
		t.Run(code, func(t *testing.T) {
			d := recommendationFixture(t)
			g := &d.Steps[5]
			g.ErrorCode = code
			g.ErrorMessage = "snapshot transport error"
			g.Input = raw(`{}`)
			g.InputSHA256 = ""
			d.Run.Compilation.Outputs = []Output{{Name: "report_draft", FromNode: "report_drafting", FromPort: "analysis"}, {Name: "delivery_gate", FromNode: "delivery_gate", FromPort: "structured"}}
			if researchRevisionRecommendation(d) != nil || len(researchRevisionTargets(d)) != 0 {
				t.Fatal("technical failure offered scientific repair")
			}
			if a := assessResearchDelivery(d); a == nil || a.Status != "blocked" {
				t.Fatal("technical failure described as rejection", a)
			}
			s, r := gateRetryService(t, d)
			before := mustJSON(r.detail)
			for _, cmd := range []RetryCommand{{RunID: d.Run.ID, StepID: g.ID, UseRecommendation: true}, {RunID: d.Run.ID, StepID: g.ID, RevisionNodeID: "report_drafting"}} {
				if _, e := s.Retry(context.Background(), cmd); e == nil || r.gateResets != 0 || r.resets != 0 {
					t.Fatal("technical error reran AI stages", e)
				}
			}
			if _, e := s.Retry(context.Background(), RetryCommand{RunID: d.Run.ID, StepID: g.ID}); e != nil || r.gateResets != 1 || r.resetID != g.ID || r.resets != 0 {
				t.Fatal("local gate retry failed", e)
			}
			if string(before) != string(mustJSON(r.detail)) || r.event.Type != "workflow.step_retry_queued" {
				t.Fatal("source stages modified")
			}
			r.detail.Steps[3].Output = raw(`{"analysis":{"summary":"changed report"}}`)
			if _, e := s.Retry(context.Background(), RetryCommand{RunID: d.Run.ID, StepID: g.ID}); e == nil || r.gateResets != 1 {
				t.Fatal("stale review accepted", e)
			}
		})
	}
}
func TestLargeGateBindingPreservesFullReviewedSnapshot(t *testing.T) {
	d := recommendationFixture(t)
	in := decodeObject(d.Steps[4].Input)
	in["savedEvidence"] = strings.Repeat("科研证据😀", 75000)
	d.Steps[4].Input = mustJSON(in)
	d.Steps[4].InputSHA256 = hashJSON(d.Steps[4].Input)
	mutateRecommendationReview(t, &d, func(v map[string]any) { v["reviewedInputSha256"] = d.Steps[4].InputSHA256 })
	before := mustJSON(d)
	bound, e := bindNodeInput(d, d.Run.Compilation.Nodes[5])
	if e != nil {
		t.Fatal(e)
	}
	if len(bound) <= 256*1024 || tool.ValidateWorkflowArguments(bound) != nil {
		t.Fatal("large host envelope not exercised")
	}
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(bound, &envelope)
	if string(envelope["subject"]) != string(d.Steps[4].Input) || string(before) != string(mustJSON(d)) {
		t.Fatal("snapshot shortened/rewritten")
	}
	d.Steps[5].Input = bound
	d.Steps[5].InputSHA256 = hashJSON(bound)
	if rec := researchRevisionRecommendation(d); rec == nil || rec.Status != "recommended" || rec.NodeID != "method_selection" {
		t.Fatal("large rejection lost plan", rec)
	}
	for _, kind := range []NodeKind{NodeTool, NodePython} {
		node := CompiledNode{ID: "local", Kind: kind, Arguments: mustJSON(map[string]any{"source": strings.Repeat("x", tool.MaxWorkflowSnapshotRunes)})}
		if _, e := bindNodeInput(RunDetail{}, node); e == nil {
			t.Fatal("storage capacity exceeded")
		}
	}
}
func TestGateFailureClassificationRequiresBusinessOutcome(t *testing.T) {
	d := recommendationFixture(t)
	node := d.Run.Compilation.Nodes[5]
	for _, tc := range []struct {
		code, message string
		want          bool
	}{{"RESEARCH_REVIEW_NOT_APPROVED", "decision", true}, {"TOOL_INVOCATION_FAILED", "独立审查未通过：仍有缺项", true}, {"TOOL_INVOCATION_FAILED", "hash mismatch", false}, {"WORKFLOW_INPUT_BINDING_FAILED", "too large", false}, {"", "", false}} {
		g := d.Steps[5]
		g.ErrorCode, g.ErrorMessage = tc.code, tc.message
		if got := isRejectedReviewGate(node, g); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	// A real refusal must still require explicit repair scope; it cannot be
	// retried as a local check to falsely clear the decision.
	s, r := gateRetryService(t, d)
	if _, e := s.Retry(context.Background(), RetryCommand{RunID: d.Run.ID, StepID: d.Steps[5].ID}); e == nil || r.gateResets != 0 {
		t.Fatal("business rejection bypassed", e)
	}
	if !reflect.DeepEqual(researchRevisionTargets(d), researchRevisionTargets(r.detail)) {
		t.Fatal("retry changed scope")
	}
}

func TestLargeUnicodeAIInputAlreadyFitsWithoutFurtherPaging(t *testing.T) {
	input := mustJSON(map[string]any{"evidenceScreening": map[string]any{"savedSource": strings.Repeat("已保存来源原文", 20000)}})
	node := CompiledNode{ID: "review", Kind: NodeAgentStage, Arguments: input, AllowedTools: []ToolSnapshot{{QualifiedName: ResearchTaskReadTool}}}
	bound, err := bindNodeInput(RunDetail{}, node)
	if len(input) <= 256*1024 || len([]rune(string(input))) >= 180000 || err != nil || !rawJSONEqual(bound, input) {
		t.Fatal("bytes confused with model character footprint", len(input), err)
	}
	node.AllowedTools = nil
	if _, err := bindNodeInput(RunDetail{}, node); err == nil {
		t.Fatal("AI reader permission widened")
	}
}

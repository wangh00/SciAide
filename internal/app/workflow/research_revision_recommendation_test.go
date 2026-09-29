package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func recommendationFixture(t *testing.T) RunDetail {
	t.Helper()
	ids := []string{"method_selection", "method_implementation", "python_analysis", "report_drafting", "independent_review", "delivery_gate"}
	names := []string{"选择方法", "实现方法", "执行计算", "撰写报告", "独立审查", "交付检查"}
	detail := RunDetail{Run: Run{ID: "run", Status: RunFailed, Inputs: raw(`{}`), Compilation: Compilation{Order: ids}}}
	for ordinal, nodeID := range ids {
		node := CompiledNode{ID: nodeID, Name: names[ordinal], Kind: NodeAgentStage, OutputSchema: raw(`{"type":"object"}`)}
		step := Step{ID: nodeID + "-step", NodeID: nodeID, NodeKind: node.Kind, Ordinal: ordinal, Attempt: 2, Status: StepCompleted, Output: raw(`{"analysis":{"summary":"current result"}}`)}
		if nodeID == "python_analysis" {
			node.Kind, node.SideEffect = NodePython, true
		}
		if nodeID == "independent_review" {
			node.OutputSchema = withRevisionPlanSchema(independentReviewSchema())
		}
		if nodeID == "delivery_gate" {
			node.Kind, node.Tool = NodeTool, &ToolSnapshot{QualifiedName: "builtin.research.workflow.review.gate"}
			step.Status = StepFailed
		}
		step.NodeKind = node.Kind
		detail.Run.Compilation.Nodes = append(detail.Run.Compilation.Nodes, node)
		detail.Steps = append(detail.Steps, step)
		if ordinal > 0 {
			detail.Run.Compilation.Edges = append(detail.Run.Compilation.Edges, Edge{FromNode: ids[ordinal-1], FromPort: "analysis", ToNode: nodeID, ToPort: "context"})
		}
	}
	// The gate's subject edge identifies the actual immediately reviewed producer.
	detail.Run.Compilation.Edges = detail.Run.Compilation.Edges[:4]
	detail.Run.Compilation.Edges = append(detail.Run.Compilation.Edges, Edge{FromNode: "report_drafting", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"}, Edge{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"})
	targets := revisionPlanTargetsForReview(detail, "independent_review")
	input := rawObject(map[string]any{"context": raw(`{"summary":"current result"}`), "revisionTargets": targets})
	detail.Steps[4].Input, detail.Steps[4].InputSHA256 = input, hashJSON(input)
	review := map[string]any{
		"approved": false, "reviewedInputSha256": hashJSON(input), "verifiedClaims": []string{}, "unsupportedClaims": []string{}, "citationIssues": []string{}, "numericIssues": []string{}, "methodIssues": []string{"method does not match study"},
		"requiredCorrections": []string{"clarify the report", "correct the method"}, "confidence": "medium", "limitations": []string{},
		"revisionPlan": []researchRevisionPlanItem{{0, "report_drafting", "report wording is ambiguous", "this is the last producer", false, ""}, {1, "method_selection", "frozen method is unsuitable", "editing prose cannot change the method", false, ""}},
	}
	freezeRecommendationReview(t, &detail, review)
	return detail
}

func freezeRecommendationReview(t *testing.T, detail *RunDetail, value map[string]any) {
	t.Helper()
	full := rawObject(value)
	core, err := reviewCoreForGate(full)
	if err != nil {
		t.Fatal(err)
	}
	review := &detail.Steps[4]
	review.Output = rawObject(map[string]any{"analysis": full})
	detail.Steps[5].Input = rawObject(map[string]any{"subject": review.Input, "review": core})
	detail.Steps[5].InputSHA256 = hashJSON(detail.Steps[5].Input)
	detail.AIExecutions = []AIExecution{{WorkflowStepID: review.ID, Attempt: review.Attempt, Status: "completed", InputSHA256: review.InputSHA256, Output: full, OutputSHA256: hashJSON(full)}}
}

func mutateRecommendationReview(t *testing.T, detail *RunDetail, mutate func(map[string]any)) {
	t.Helper()
	full, _ := outputPort(detail.Steps[4].Output, "analysis")
	var value map[string]any
	if err := json.Unmarshal(full, &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	freezeRecommendationReview(t, detail, value)
}

func TestRevisionRecommendationUsesEarliestStageAndFullReplayScope(t *testing.T) {
	detail := recommendationFixture(t)
	result := researchRevisionRecommendation(detail)
	if result == nil || result.Status != "recommended" || result.NodeID != "method_selection" || !result.RepeatsSideEffects || result.ReviewOutputSHA256 == "" || len(result.Reasons) != 2 {
		t.Fatalf("recommendation=%+v", result)
	}
	if want := []string{"选择方法", "实现方法", "执行计算", "撰写报告", "独立审查", "交付检查"}; !reflect.DeepEqual(result.AffectedStages, want) {
		t.Fatalf("replay scope=%v, want %v", result.AffectedStages, want)
	}
	// Plan ordering and the first listed correction never override earliest ordinal.
	mutateRecommendationReview(t, &detail, func(v map[string]any) { p := v["revisionPlan"].([]any); p[0], p[1] = p[1], p[0] })
	if result = researchRevisionRecommendation(detail); result.NodeID != "method_selection" {
		t.Fatalf("reordered=%+v", result)
	}
}

func TestRevisionRecommendationRejectsInvalidOrUnlinkedPlans(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*testing.T, *RunDetail)
	}{
		{"unknown target", func(t *testing.T, d *RunDetail) {
			mutateRecommendationReview(t, d, func(v map[string]any) {
				v["revisionPlan"].([]any)[0].(map[string]any)["nodeId"] = "question_refinement"
			})
		}},
		{"missing correction", func(t *testing.T, d *RunDetail) {
			mutateRecommendationReview(t, d, func(v map[string]any) { v["revisionPlan"] = v["revisionPlan"].([]any)[:1] })
		}},
		{"duplicate correction", func(t *testing.T, d *RunDetail) {
			mutateRecommendationReview(t, d, func(v map[string]any) { v["revisionPlan"].([]any)[1].(map[string]any)["correctionIndex"] = 0 })
		}},
		{"missing why later insufficient", func(t *testing.T, d *RunDetail) {
			mutateRecommendationReview(t, d, func(v map[string]any) { v["revisionPlan"].([]any)[0].(map[string]any)["whyNotLater"] = " " })
		}},
		{"missing user input description", func(t *testing.T, d *RunDetail) {
			mutateRecommendationReview(t, d, func(v map[string]any) { v["revisionPlan"].([]any)[0].(map[string]any)["needsUserInput"] = true })
		}},
		{"stage no longer completed", func(_ *testing.T, d *RunDetail) { d.Steps[0].Status = StepQueued }},
		{"wrong review attempt", func(_ *testing.T, d *RunDetail) { d.AIExecutions[0].Attempt-- }},
		{"tampered AI output", func(_ *testing.T, d *RunDetail) { d.AIExecutions[0].Output = raw(`{}`) }},
		{"tampered review input", func(_ *testing.T, d *RunDetail) { d.Steps[4].Input = raw(`{}`) }},
		{"stale gate", func(_ *testing.T, d *RunDetail) {
			d.Steps[5].Input = raw(`{"subject":{},"review":{}}`)
			d.Steps[5].InputSHA256 = hashJSON(d.Steps[5].Input)
		}},
		{"no AI execution", func(_ *testing.T, d *RunDetail) { d.AIExecutions = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := recommendationFixture(t)
			tt.mutate(t, &detail)
			result := researchRevisionRecommendation(detail)
			if result == nil || result.Status != "unavailable" || result.NodeID != "" {
				t.Fatalf("invalid plan became executable: %+v", result)
			}
		})
	}
}

func TestRevisionRecommendationNeedsUserInputAndLegacyUnavailable(t *testing.T) {
	detail := recommendationFixture(t)
	mutateRecommendationReview(t, &detail, func(v map[string]any) {
		p := v["revisionPlan"].([]any)[1].(map[string]any)
		p["nodeId"] = ""
		p["needsUserInput"] = true
		p["requiredInput"] = "提供原始随访数据"
	})
	result := researchRevisionRecommendation(detail)
	if result.Status != "needs_input" || result.NodeID != "" || len(result.AffectedStages) != 0 || !strings.Contains(result.Summary, "提供原始随访数据") {
		t.Fatalf("needs input=%+v", result)
	}
	if len(result.RequiredInputs) != 1 || result.RequiredInputs[0] != "提供原始随访数据" {
		t.Fatalf("missing actionable requirements: %+v", result)
	}
	if _, err := resolveReviewRevisionRequest(detail, detail.Steps[5], RetryCommand{UseRecommendation: true, ExpectedReviewSHA256: result.ReviewOutputSHA256}); err == nil {
		t.Fatal("missing input authorized recommended retry")
	}
	if target, err := resolveReviewRevisionRequest(detail, detail.Steps[5], RetryCommand{RevisionNodeID: "report_drafting"}); err != nil || target != "report_drafting" {
		t.Fatalf("explicit manual selection=%s %v", target, err)
	}
	mutateRecommendationReview(t, &detail, func(v map[string]any) { delete(v, "revisionPlan") })
	detail.Run.Compilation.Nodes[4].OutputSchema = independentReviewSchema()
	result = researchRevisionRecommendation(detail)
	if result.Status != "unavailable" || result.NodeID != "" || !strings.Contains(result.Summary, "未提供修改位置") {
		t.Fatalf("legacy=%+v", result)
	}
	if _, _, _, err := reviewRevisionFromTarget(detail, detail.Steps[5], "report_drafting"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := reviewRevisionFromTarget(detail, detail.Steps[5], ""); err == nil {
		t.Fatal("blank target silently defaulted to report")
	}
}

func TestRevisionRecommendationRequestRejectsStaleReviewAndDifferentTarget(t *testing.T) {
	detail := recommendationFixture(t)
	recommendation := researchRevisionRecommendation(detail)
	command := RetryCommand{UseRecommendation: true, ExpectedReviewSHA256: recommendation.ReviewOutputSHA256}
	if target, err := resolveReviewRevisionRequest(detail, detail.Steps[5], command); err != nil || target != "method_selection" {
		t.Fatalf("current request=%s %v", target, err)
	}
	command.RevisionNodeID = "report_drafting"
	if _, err := resolveReviewRevisionRequest(detail, detail.Steps[5], command); err == nil {
		t.Fatal("later target accepted as recommendation")
	}
	command.RevisionNodeID = ""
	mutateRecommendationReview(t, &detail, func(v map[string]any) { v["revisionPlan"].([]any)[0].(map[string]any)["reason"] = "updated rationale" })
	if _, err := resolveReviewRevisionRequest(detail, detail.Steps[5], command); err == nil || !strings.Contains(err.Error(), "已变化") {
		t.Fatalf("stale review error=%v", err)
	}
	command.ExpectedReviewSHA256 = ""
	if _, err := resolveReviewRevisionRequest(detail, detail.Steps[5], command); err == nil {
		t.Fatal("missing token accepted")
	}
}

func TestRevisionPlanPromptAndGateRespectFrozenSchema(t *testing.T) {
	detail := recommendationFixture(t)
	node := detail.Run.Compilation.Nodes[4]
	bound, err := bindNodeInput(detail, node)
	if err != nil || !strings.Contains(string(bound), "revisionTargets") {
		t.Fatalf("bound=%s %v", bound, err)
	}
	if prompt := buildAIStagePrompt(detail, detail.Steps[4], node); !strings.Contains(prompt, "Return revisionPlan") {
		t.Fatal("new review lacks revision instruction")
	}
	node.OutputSchema = independentReviewSchema()
	if prompt := buildAIStagePrompt(detail, detail.Steps[4], node); strings.Contains(prompt, "Return revisionPlan") {
		t.Fatal("legacy schema was instructed to emit invalid new field")
	}
	bound, err = bindNodeInput(detail, detail.Run.Compilation.Nodes[5])
	if err != nil {
		t.Fatal(err)
	}
	var gate map[string]json.RawMessage
	_ = json.Unmarshal(bound, &gate)
	if strings.Contains(string(gate["review"]), "revisionPlan") || !rawJSONEqual(gate["subject"], detail.Steps[4].Input) {
		t.Fatal("gate did not preserve original closed tool contract and complete reviewed input")
	}
}

type recommendationRetryRepository struct {
	RuntimeRepository
	detail   RunDetail
	resets   int
	decision *HumanDecision
	event    RuntimeEvent
}

func (r *recommendationRetryRepository) GetRun(context.Context, string, string) (RunDetail, error) {
	return r.detail, nil
}
func (r *recommendationRetryRepository) ResetStepsForUpstreamRetry(_ context.Context, _, _, _ string, decision *HumanDecision, _ time.Time, event RuntimeEvent) error {
	r.resets++
	r.decision = decision
	r.event = event
	return nil
}

func TestRetryRecommendationRequiresExplicitConfirmationBeforeReset(t *testing.T) {
	detail := recommendationFixture(t)
	detail.Run.ResearchTaskID = "task-test"
	detail.Run.InputsSHA256 = hashJSON(detail.Run.Inputs)
	encoded, err := canonicalJSON(detail.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	detail.Run.CompilationSHA256 = hashBytes(encoded)
	detail.Run.Compilation.CompilationSHA256 = detail.Run.CompilationSHA256
	repository := &recommendationRetryRepository{detail: detail}
	// Missing optional subject stores yield empty activity projections; no DB.
	service := &RuntimeService{repository: repository, projects: fixedProjectLoader{value: project.Project{WorkspacePath: t.TempDir()}}, permissions: permission.NewEngine(nil), tools: tool.NewService(nil, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "id", nil }, closed: true}
	recommendation := researchRevisionRecommendation(detail)
	command := RetryCommand{RunID: detail.Run.ID, StepID: detail.Steps[5].ID, UseRecommendation: true, ExpectedReviewSHA256: recommendation.ReviewOutputSHA256}
	if _, err := service.Retry(context.Background(), command); err == nil || !strings.Contains(err.Error(), "副作用") || repository.resets != 0 {
		t.Fatalf("unconfirmed=%v resets=%d", err, repository.resets)
	}
	command.ConfirmSideEffect = true
	if _, err := service.Retry(context.Background(), command); err != nil || repository.resets != 1 || repository.decision == nil {
		t.Fatalf("confirmed=%v resets=%d", err, repository.resets)
	}
	var revision reviewRevision
	if json.Unmarshal(repository.event.Payload, &revision) != nil || !revision.UsedRecommendation || revision.ProducerNodeID != "method_selection" || revision.ReviewOutputSHA256 != recommendation.ReviewOutputSHA256 || !strings.Contains(string(revision.IndependentReview), "revisionPlan") {
		t.Fatalf("audit=%s", repository.event.Payload)
	}
	command.ExpectedReviewSHA256 = "stale"
	if _, err := service.Retry(context.Background(), command); err == nil || repository.resets != 1 {
		t.Fatalf("stale request=%v resets=%d", err, repository.resets)
	}
}

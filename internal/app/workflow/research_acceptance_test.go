package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func acceptanceFixture(t *testing.T) (json.RawMessage, map[string]any) {
	t.Helper()
	contract := raw(`{"researchQuestion":"睡眠日记研究设计","successCriteria":["明确研究对象和变量","列出数据采集与分析计划"]}`)
	criteria, err := researchAcceptanceCriteria(contract)
	if err != nil {
		t.Fatal(err)
	}
	input := rawObject(map[string]any{"context": map[string]any{"title": "设计"}, "researchContract": contract, "acceptanceCriteria": criteria})
	output := map[string]any{"approved": true, "reviewedInputSha256": hashJSON(input), "verifiedClaims": []string{"设计完整"}, "unsupportedClaims": []string{}, "citationIssues": []string{}, "numericIssues": []string{}, "methodIssues": []string{}, "requiredCorrections": []string{}, "confidence": "medium", "limitations": []string{"不是实证结果"}, "acceptanceChecks": []ResearchAcceptanceCheck{{"criterion-1", "met", "对象和变量章节"}, {"criterion-2", "met", "采集和分析章节"}}}
	return input, output
}

func TestResearchAcceptanceRequiresCompleteConsistentCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		valid  bool
	}{
		{"complete", func(map[string]any) {}, true},
		{"missing", func(v map[string]any) {
			v["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "对象"}}
		}, false},
		{"duplicate", func(v map[string]any) {
			v["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "对象"}, {"criterion-1", "met", "对象"}}
		}, false},
		{"invented", func(v map[string]any) {
			v["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "对象"}, {"criterion-3", "met", "无关条件"}}
		}, false},
		{"empty basis", func(v map[string]any) {
			v["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", " "}, {"criterion-2", "met", "方法"}}
		}, false},
		{"false approval", func(v map[string]any) {
			v["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "对象"}, {"criterion-2", "not_met", "未提供采集计划"}}
		}, false},
		{"honest rejection", func(v map[string]any) {
			v["approved"] = false
			v["requiredCorrections"] = []string{"补充采集计划"}
			v["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "对象"}, {"criterion-2", "not_met", "未提供采集计划"}}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, output := acceptanceFixture(t)
			tc.mutate(output)
			err := validateWorkflowAIStageOutput(CompiledNode{OutputSchema: researchAcceptanceReviewSchema()}, rawObject(output), input)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestResearchAcceptanceCannotReplaceOriginalCriteria(t *testing.T) {
	input, output := acceptanceFixture(t)
	var stage map[string]any
	_ = json.Unmarshal(input, &stage)
	stage["acceptanceCriteria"] = []ResearchAcceptanceCriterion{{"criterion-1", "只要能生成文本"}, {"criterion-2", "格式正确即可"}}
	if err := validateResearchAcceptance(rawObject(output), rawObject(stage)); err == nil {
		t.Fatal("weakened acceptance criteria accepted")
	}
}

func TestAcceptanceGateKeepsOriginalToolContractAndFullAudit(t *testing.T) {
	input, output := acceptanceFixture(t)
	full := rawObject(output)
	detail := RunDetail{Run: Run{Compilation: Compilation{Nodes: []CompiledNode{{ID: "review", Kind: NodeAIAnalysis, OutputSchema: researchAcceptanceReviewSchema()}, {ID: "gate", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.review.gate"}}}, Edges: []Edge{{FromNode: "review", FromPort: "analysis", ToNode: "gate", ToPort: "review"}}}}, Steps: []Step{{ID: "review-step", NodeID: "review", Status: StepCompleted, Input: input, Output: rawObject(map[string]any{"analysis": full})}}}
	bound, err := bindNodeInput(detail, detail.Run.Compilation.Nodes[1])
	if err != nil {
		t.Fatal(err)
	}
	var gate map[string]json.RawMessage
	_ = json.Unmarshal(bound, &gate)
	if strings.Contains(string(gate["review"]), "acceptanceChecks") {
		t.Fatal("extended review leaked into old closed tool schema")
	}
	if !rawJSONEqual(gate["subject"], input) {
		t.Fatal("gate did not cover the exact reviewed context")
	}
	if !strings.Contains(string(detail.Steps[0].Output), "acceptanceChecks") {
		t.Fatal("immutable acceptance audit was lost")
	}
}

func TestSemanticPlannerCanSelectSubstantiveStagesWithoutHostSpine(t *testing.T) {
	plan := plannerRegressionPayload(t, false)
	entry := plan["routes"].([]any)[0].(map[string]any)
	entry["stagePlans"] = []map[string]any{{"stageId": "research_design", "objective": "形成可执行采集方案"}}
	if _, err := extractWorkflowAIStageOutput(string(rawObject(plan)), CompiledNode{ID: "explore", OutputSchema: semanticResearchStarterSchema()}); err != nil {
		t.Fatalf("planner schema rejected a valid substantive selection: %v", err)
	}
	route := ResearchRoute{StagePlans: []ResearchStagePlan{{StageID: "research_design", Objective: "形成可执行采集方案"}}}
	compiled, err := materializeSemanticResearchRoute(route)
	if err != nil {
		t.Fatal(err)
	}
	available, _, err := validateDynamicResearchRoute(compiled, ResearchStarterContext{})
	if err != nil || !available {
		t.Fatalf("host failed to supply required stages: %v", err)
	}
	for _, id := range []string{"question_refinement", "method_selection", "research_design", "independent_review", "delivery_gate"} {
		if !stringSetOf(compiled.StageIDs)[id] {
			t.Fatalf("missing stage %s", id)
		}
	}
}

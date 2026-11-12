package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func deliveryDesignRoute() ResearchRoute {
	route := starterDesignRoute()
	route.Deliverables = []string{"方案", "问卷初稿", "分析计划"}
	route.StageIDs = append(route.StageIDs[:len(route.StageIDs)-2], "report_drafting", "independent_review", "delivery_gate")
	route.Layers[len(route.Layers)-1].Stages = append([]ResearchRouteStage{{StageID: "report_drafting", Objective: "交付完整正文", Outputs: []string{"报告"}}}, route.Layers[len(route.Layers)-1].Stages...)
	return route
}

func TestDeliveryRouteBindsOriginalRequestMaterialsAndReview(t *testing.T) {
	goal := "交付大学生睡眠方案、问卷初稿及分析计划，无数据，不作因果推断"
	template, inputs, _, err := dynamicResearchRouteTemplate(deliveryDesignRoute(), ResearchStarterContext{ResearchIdea: goal}, true)
	if err != nil {
		t.Fatal(err)
	}
	compilation, err := NewCompiler(referenceTemplateRegistry(t)).Compile(context.Background(), template.Definition)
	if err != nil {
		t.Fatal(err)
	}
	design := deliveryDesignFixture()
	material := map[string]any{"title": "自编题项初稿（未验证）", "markdown": "|变量|题干|选项|\n|清醒|过去一周上课困倦频率？|从不/偶尔/经常|\n编码 0/1/2，不作临床诊断。"}
	design["deliverables"] = []any{material}
	contract := map[string]any{"successCriteria": []string{"给出具体问卷题干、选项及分析计划"}}
	report := map[string]any{"markdown": material["markdown"], "claimSummary": []string{"未验证的设计草稿"}, "methodSummary": "横断面设计", "limitations": []string{"没有采集数据"}, "confidence": "medium"}
	detail := RunDetail{Run: Run{Inputs: inputs, Compilation: compilation}, Steps: []Step{}}
	outputs := map[string]any{"question_refinement": contract, "method_selection": map[string]any{"method": "横断面"}, "research_design": design, "report_drafting": report}
	for _, node := range compilation.Nodes {
		step := Step{ID: node.ID, NodeID: node.ID, Status: StepCompleted, Output: rawObject(map[string]any{"analysis": outputs[node.ID]})}
		detail.Steps = append(detail.Steps, step)
	}
	for _, node := range compilation.Nodes {
		if node.Kind != NodeAgentStage {
			continue
		}
		input, err := bindNodeInput(detail, node)
		if err != nil {
			t.Fatalf("%s: %v", node.ID, err)
		}
		var bound map[string]json.RawMessage
		_ = json.Unmarshal(input, &bound)
		if string(bound["originalRequest"]) != string(mustJSON(goal)) {
			t.Fatalf("%s lost raw goal", node.ID)
		}
		if node.ID == "report_drafting" && !rawJSONEqual(bound["context"], mustJSON(design)) {
			t.Fatal("report lost concrete materials")
		}
		if node.ID == "independent_review" {
			if !rawJSONEqual(bound["context"], mustJSON(report)) || !rawJSONEqual(bound["designContext"], mustJSON(design)) {
				t.Fatal("review lost current draft/design")
			}
			if !strings.Contains(string(bound["routeContext"]), "问卷初稿") {
				t.Fatal("review lost adopted deliverables")
			}
			if len(bound["acceptanceCriteria"]) == 0 || len(bound["revisionTargets"]) == 0 {
				t.Fatal("acceptance/revision contract lost")
			}
		}
		prompt := buildAIStagePrompt(detail, Step{NodeID: node.ID, Input: input}, node)
		for _, forbidden := range []string{"必须同时依据 evidenceScreening", "必须将 evidenceScreening", "回到 evidence_screening", "Frozen upstream evidenceScreening", "adoption through the existing evidence_extraction"} {
			if strings.Contains(prompt, forbidden) {
				t.Fatalf("%s retains irrelevant instruction %q", node.ID, forbidden)
			}
		}
	}
}

func deliveryDesignFixture() map[string]any {
	value := map[string]any{"title": "设计", "researchQuestion": "睡眠与学习状态的关联", "status": "research_design_not_empirical_result"}
	for _, key := range []string{"objectives", "hypotheses", "populationAndSampling", "variablesOrMaterials", "dataCollectionPlan", "analysisPlan", "qualityControls", "ethicsAndRisks", "milestones", "evidenceGaps", "limitations"} {
		value[key] = []string{"设计草稿，待验证"}
	}
	return value
}

func TestDynamicDesignOptionalMaterialsAreDomainIndependentAndLegacyCompatible(t *testing.T) {
	validator := tool.JSONSchemaValidator{}
	legacy := deliveryDesignFixture()
	if err := validator.Validate(dynamicResearchDesignSchema(), mustJSON(legacy)); err != nil {
		t.Fatal(err)
	}
	if schemaDeclaresProperty(researchDesignSchema(), "deliverables") {
		t.Fatal("changed legacy design schema")
	}
	for _, title := range []string{"问卷初稿", "访谈提纲", "实验操作协议", "分析规范"} {
		value := deliveryDesignFixture()
		value["deliverables"] = []any{map[string]any{"title": title, "markdown": "# " + title + "\n" + strings.Repeat("正文内容。", 600)}}
		if err := validator.Validate(dynamicResearchDesignSchema(), mustJSON(value)); err != nil {
			t.Fatal(err)
		}
		if err := validator.Validate(researchDesignSchema(), mustJSON(value)); err == nil {
			t.Fatal("legacy contract unexpectedly changed")
		}
	}
}

func TestDeliveryReviewOriginalAndRouteAreRequirementsNotEvidence(t *testing.T) {
	input := map[string]any{"originalRequest": "问卷初稿", "routeContext": map[string]any{"deliverables": []any{"问卷初稿"}}}
	for _, pointer := range []string{"/originalRequest", "/routeContext/deliverables/0"} {
		value, requirement, err := reviewBasisAt(input, pointer)
		if err != nil || !requirement || value != "问卷初稿" {
			t.Fatalf("requirement role lost: %v %v", requirement, err)
		}
	}
	if _, _, err := reviewBasisAt(input, "/routeContext/deliverables/1"); err == nil {
		t.Fatal("invented route requirement accepted")
	}
}

func TestDeliveryReviewLegacyAndEvidenceInstructionsRemain(t *testing.T) {
	legacy := CompiledNode{ID: "independent_review", PromptVersion: dynamicResearchReviewVersion, OutputSchema: trackedResearchReviewSchema()}
	prompt := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, legacy)
	if !strings.Contains(prompt, "Frozen upstream evidenceScreening") {
		t.Fatal("changed historical review guidance")
	}
	legacy.PromptVersion += "-" + routeDeliveryVersion
	prompt = buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{"evidenceScreening":{"evidenceGaps":[]}}`)}, legacy)
	if !strings.Contains(prompt, "Frozen upstream evidenceScreening") {
		t.Fatal("removed evidence-route checks")
	}
}

func TestDeliveryReviewRequirementCannotProveCompletion(t *testing.T) {
	input, output := trackingFixture()
	stage := decodeObject(input)
	stage["originalRequest"] = "交付具体问卷初稿"
	stage["routeContext"] = map[string]any{"deliverables": []any{"交付具体问卷初稿"}}
	for _, pointer := range []string{"/originalRequest", "/routeContext/deliverables/0"} {
		finding := output["reviewFindings"].([]trackedReviewFinding)[0]
		finding.Basis = []reviewBasis{{Pointer: pointer, Quote: "交付具体问卷初稿"}}
		output["reviewFindings"] = []trackedReviewFinding{finding}
		if validateTrackedReview(mustJSON(output), mustJSON(stage)) == nil {
			t.Fatal("requirement alone accepted as scientific basis")
		}
		finding.Basis = append(finding.Basis, reviewBasis{Pointer: "/context/markdown", Quote: "Methods omit baseline."})
		output["reviewFindings"] = []trackedReviewFinding{finding}
		if err := validateTrackedReview(mustJSON(output), mustJSON(stage)); err != nil {
			t.Fatal(err)
		}
	}
}

package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func plannerRegressionPayload(t *testing.T, legacy bool) map[string]any {
	t.Helper()
	route := starterDesignRoute()
	route.RequiredResources, route.Blockers = []string{}, []string{}
	for li := range route.Layers {
		for si := range route.Layers[li].Stages {
			stage := &route.Layers[li].Stages[si]
			stage.Methods, stage.SkillNames, stage.Inputs = []string{}, []string{}, []string{}
		}
	}
	if !legacy {
		route.StagePlans = legacyStagePlans(route)
		route.StageIDs, route.Layers, route.ReviewCheckpoints = nil, nil, nil
	}
	value, err := json.Marshal(ResearchStarterPlan{
		NormalizedQuestion: "夜间手机使用与大学生睡眠", ResearchType: "design",
		AvailableResources: []string{}, MissingInformation: []string{}, SelectedSkills: []ResearchSkillSelection{},
		Routes: []ResearchRoute{route}, RecommendedRouteID: route.RouteID,
		RecommendationReason: "先做设计，不声称获得实证结果", Confidence: "medium", Limitations: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(value, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPlannerClarificationNormalizationIsNarrow(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, tc := range []struct {
			name, clarification string
			valid               bool
		}{
			{"omitted", "", true},
			{"false without questions", `{"needsUserInput":false}`, true},
			{"false empty questions", `{"needsUserInput":false,"questions":[]}`, true},
			{"true without questions", `{"needsUserInput":true}`, false},
			{"true empty questions", `{"needsUserInput":true,"questions":[]}`, false},
			{"missing decision", `{"questions":[]}`, false},
			{"wrong type", `{"needsUserInput":"false"}`, false},
			{"null questions", `{"needsUserInput":false,"questions":null}`, false},
			{"unknown field", `{"needsUserInput":false,"surprise":true}`, false},
		} {
			t.Run(tc.name+map[bool]string{true: "/legacy", false: "/semantic"}[legacy], func(t *testing.T) {
				payload := plannerRegressionPayload(t, legacy)
				if tc.clarification != "" {
					payload["clarification"] = json.RawMessage(tc.clarification)
				}
				encoded, _ := json.Marshal(payload)
				schema := semanticResearchStarterSchema()
				if legacy {
					schema = legacyDynamicResearchStarterSchema()
				}
				node := CompiledNode{ID: "explore", OutputSchema: schema}
				output, err := extractWorkflowAIStageOutput("```json\n"+string(encoded)+"\n```", node)
				if err == nil {
					err = validateWorkflowAIStageOutput(node, output, raw(`{}`))
				}
				if (err == nil) != tc.valid {
					t.Fatalf("valid=%v: %v; output=%s", tc.valid, err, output)
				}
				if err == nil {
					if err := (tool.JSONSchemaValidator{}).Validate(schema, output); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestPlannerLegacyCompatibilityCannotBypassFrozenSchema(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		valid  bool
	}{
		{"valid legacy", func(p map[string]any) {}, true},
		{"missing question", func(p map[string]any) { delete(p, "normalizedQuestion") }, false},
		{"unknown root field", func(p map[string]any) { p["execute"] = "command" }, false},
		{"unknown route field", func(p map[string]any) { p["routes"].([]any)[0].(map[string]any)["execute"] = "command" }, false},
		{"missing availability", func(p map[string]any) { delete(p["routes"].([]any)[0].(map[string]any), "availableNow") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := plannerRegressionPayload(t, true)
			tc.mutate(payload)
			encoded, _ := json.Marshal(payload)
			schema := semanticResearchStarterSchema()
			output, err := extractWorkflowAIStageOutput(string(encoded), CompiledNode{ID: "explore", OutputSchema: schema})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v; output=%s", tc.valid, err, output)
			}
			if err == nil {
				if err := (tool.JSONSchemaValidator{}).Validate(schema, output); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPlannerAllowsDifferentDesignsWithSameWorkflowStages(t *testing.T) {
	first, second := starterDesignRoute(), starterDesignRoute()
	first.Title = "横断面问卷设计"
	second.RouteID, second.Title = "prospective_diary", "前瞻性睡眠日记设计"
	second.Layers[1].Stages[1].Objective = "连续记录暴露与结局，区分个体间与个体内差异"
	plan := ResearchStarterPlan{Routes: []ResearchRoute{first, second}, RecommendedRouteID: first.RouteID}
	if err := annotateStarterRoutesForSelection(&plan, ResearchStarterContext{}, nil); err != nil {
		t.Fatal(err)
	}
	for _, route := range plan.Routes {
		if route.Validation != "ready" {
			t.Fatalf("legitimate design rejected: %+v", route)
		}
	}
}

func TestPlannerKeepsAnalysisIntentWhenPreflightWasOmitted(t *testing.T) {
	route := starterDesignRoute()
	route.StagePlans = append(legacyStagePlans(route), ResearchStagePlan{StageID: "python_analysis", Objective: "分析真实的睡眠日记"})
	route.StageIDs, route.Layers = nil, nil
	materialized, err := materializeSemanticResearchRoute(route)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"data_preflight", "method_implementation", "dependency_preparation", "python_analysis", "result_interpretation"} {
		if !stringSetOf(materialized.StageIDs)[id] {
			t.Fatalf("analysis intent lost: %s", id)
		}
	}
	available, blockers, err := validateDynamicResearchRoute(materialized, ResearchStarterContext{})
	if err != nil || available || len(blockers) == 0 {
		t.Fatalf("missing input must block, not erase analysis: %v %v %v", available, blockers, err)
	}
}

func TestPlannerSharedAttachmentNameIsNotAnExecutableTaskPath(t *testing.T) {
	route := starterDataRoute()
	route.AvailableNow = true
	route.Blockers = []string{}
	ctx := ResearchStarterContext{ResearchIdea: "分析睡眠数据", PlannerVersion: dynamicResearchPlannerVersion, ResourceSnapshot: ResourceSnapshot{TabularFiles: []string{"sleep.csv"}}}
	plan := ResearchStarterPlan{Routes: []ResearchRoute{route}, RecommendedRouteID: route.RouteID}
	if err := annotateStarterRoutesForSelection(&plan, ctx, nil); err != nil {
		t.Fatal(err)
	}
	if plan.Routes[0].Validation != "blocked" {
		t.Fatal("an attachment display name cannot auto-bind a task file")
	}
	_, inputs, available, err := routeDefinition(plan.Routes[0], ctx)
	if err != nil || available || strings.Contains(string(inputs), "sleep.csv") {
		t.Fatalf("unsafe automatic input: %s %v %v", inputs, available, err)
	}
}

func TestPlannerDesignCaveatsSurviveProjectionAndAdoption(t *testing.T) {
	route := starterDesignRoute()
	route.AvailableNow = false
	route.Blockers = []string{"正式采集前须完成伦理审批", "量表授权尚待确认", "尚无原始数据"}
	plan := ResearchStarterPlan{Routes: []ResearchRoute{route}, RecommendedRouteID: route.RouteID}
	ctx := ResearchStarterContext{ResearchIdea: "先形成睡眠研究设计", PlannerVersion: dynamicResearchPlannerVersion}
	for attempt := 0; attempt < 2; attempt++ {
		if err := annotateStarterRoutesForSelection(&plan, ctx, nil); err != nil {
			t.Fatal(err)
		}
		projected := plan.Routes[0]
		if projected.Validation != "ready" || len(projected.Blockers) != 0 || len(projected.PlanningNotes) != 3 {
			t.Fatalf("design readiness erased or misclassified caveats: %+v", projected)
		}
	}
	_, inputs, available, err := routeDefinition(plan.Routes[0], ctx)
	if err != nil || !available {
		t.Fatalf("design should start: %v", err)
	}
	for _, note := range route.Blockers {
		if !strings.Contains(string(inputs), note) {
			t.Fatalf("adoption lost caveat: %s", note)
		}
	}
}

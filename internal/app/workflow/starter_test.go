package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/skillrun"
)

func starterTestInput(idea string) json.RawMessage {
	value, _ := json.Marshal(map[string]any{"starter_context": ResearchStarterContext{
		ResearchIdea: idea, ResourceSnapshot: ResourceSnapshot{},
		StageCatalog: dynamicResearchStageCatalog(), PlannerVersion: dynamicResearchPlannerVersion,
	}})
	return value
}

func starterTestPlan() json.RawMessage {
	value, _ := json.Marshal(ResearchStarterPlan{
		NormalizedQuestion: "夜间手机使用是否影响大学生睡眠？", ResearchType: "design",
		MissingInformation: []string{"尚无数据和文献"}, SelectedSkills: []ResearchSkillSelection{},
		Routes: []ResearchRoute{starterDesignRoute()}, RecommendedRouteID: "design_first",
		RecommendationReason: "当前无数据，先明确设计", Confidence: "medium",
		Limitations: []string{"尚未完成实证检验"},
	})
	return value
}

func starterDesignRoute() ResearchRoute {
	return ResearchRoute{
		RouteID: "design_first", Title: "研究设计", Reason: "先形成可执行方案", AvailableNow: true,
		Deliverables:      []string{"开题方案"},
		StageIDs:          []string{"question_refinement", "method_selection", "research_design", "independent_review", "delivery_gate"},
		ReviewCheckpoints: []string{"独立二次审查"},
		Layers: []ResearchRouteLayer{
			{LayerID: "question", Title: "问题", Objective: "明确边界", Stages: []ResearchRouteStage{{StageID: "question_refinement", Objective: "明确边界", Outputs: []string{"研究问题"}}}},
			{LayerID: "method", Title: "方法", Objective: "形成设计", Stages: []ResearchRouteStage{{StageID: "method_selection", Objective: "选择方法", Outputs: []string{"方法蓝图"}}, {StageID: "research_design", Objective: "形成设计", Outputs: []string{"研究设计"}}}},
			{LayerID: "delivery", Title: "交付", Objective: "核验交付", Stages: []ResearchRouteStage{{StageID: "independent_review", Objective: "独立审查", Outputs: []string{"审查结论"}}, {StageID: "delivery_gate", Objective: "核验交付", Outputs: []string{"交付许可"}}}},
		},
	}
}

func starterDataRoute() ResearchRoute {
	return ResearchRoute{
		RouteID: "data_when_ready", Title: "数据分析", Reason: "现有表格与课题无关", AvailableNow: false,
		Blockers: []string{"尚未提供与本课题相关的通勤传感器数据"}, Deliverables: []string{"可复现分析"},
		StageIDs:          []string{"question_refinement", "method_selection", "data_preflight", "method_implementation", "dependency_preparation", "python_analysis", "result_interpretation", "independent_review", "delivery_gate"},
		ReviewCheckpoints: []string{"方法确认", "独立二次审查"},
		Layers: []ResearchRouteLayer{
			{LayerID: "question", Title: "问题", Objective: "明确边界", Stages: []ResearchRouteStage{{StageID: "question_refinement", Objective: "明确边界", Outputs: []string{"研究问题"}}}},
			{LayerID: "method", Title: "方法", Objective: "准备分析", Stages: []ResearchRouteStage{{StageID: "method_selection", Objective: "选择方法", Outputs: []string{"方法蓝图"}}, {StageID: "data_preflight", Objective: "预检数据", Outputs: []string{"数据快照"}}, {StageID: "method_implementation", Objective: "实现方法", Outputs: []string{"分析代码"}}}},
			{LayerID: "analysis", Title: "分析", Objective: "执行并解释", Stages: []ResearchRouteStage{{StageID: "dependency_preparation", Objective: "准备依赖", Outputs: []string{"环境快照"}}, {StageID: "python_analysis", Objective: "执行分析", Outputs: []string{"计算结果"}}, {StageID: "result_interpretation", Objective: "解释结果", Outputs: []string{"结果解释"}}}},
			{LayerID: "delivery", Title: "交付", Objective: "核验交付", Stages: []ResearchRouteStage{{StageID: "independent_review", Objective: "独立审查", Outputs: []string{"审查结论"}}, {StageID: "delivery_gate", Objective: "核验交付", Outputs: []string{"交付许可"}}}},
		},
	}
}

func projectStarterResult(t *testing.T, detail RunDetail, loaded []skillrun.Snapshot) (ResearchStarterContext, ResearchStarterPlan) {
	t.Helper()
	inputs, plan, _, err := decodeStarterResult(detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := annotateStarterRoutesForSelection(&plan, inputs, loaded); err != nil {
		t.Fatal(err)
	}
	return inputs, plan
}

func TestStarterResultAcceptsSemanticallyEqualFormattedRunOutput(t *testing.T) {
	plan := starterTestPlan()
	var compactValue any
	if err := json.Unmarshal(plan, &compactValue); err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(map[string]any{"plan": compactValue})
	if err != nil {
		t.Fatal(err)
	}
	detail := RunDetail{
		Run:          Run{Inputs: starterTestInput("夜间手机使用与睡眠"), Outputs: compact},
		Steps:        []Step{{ID: "step", Status: StepCompleted, Attempt: 1}},
		AIExecutions: []AIExecution{{WorkflowStepID: "step", Attempt: 1, Status: "completed", Output: plan, OutputSHA256: hashJSON(plan)}},
	}
	_, parsed := projectStarterResult(t, detail, nil)
	if parsed.RecommendedRouteID != "design_first" || len(parsed.Routes) != 1 || len(parsed.Routes[0].Layers) != 3 {
		t.Fatalf("parsed route = %#v", parsed)
	}
}

func TestStarterResultRejectsMissingOrUnknownPlannerVersion(t *testing.T) {
	plan := starterTestPlan()
	var planValue any
	_ = json.Unmarshal(plan, &planValue)
	outputs, _ := json.Marshal(map[string]any{"plan": planValue})
	for _, version := range []string{"", "fixed-v1"} {
		context := ResearchStarterContext{ResearchIdea: "问题", StageCatalog: dynamicResearchStageCatalog(), PlannerVersion: version}
		inputs, _ := json.Marshal(map[string]any{"starter_context": context})
		detail := RunDetail{
			Run: Run{Inputs: inputs, Outputs: outputs}, Steps: []Step{{ID: "step", Status: StepCompleted, Attempt: 1}},
			AIExecutions: []AIExecution{{WorkflowStepID: "step", Attempt: 1, Status: "completed", Output: plan, OutputSHA256: hashJSON(plan)}},
		}
		if _, _, _, err := decodeStarterResult(detail); err == nil {
			t.Fatalf("planner version %q was accepted", version)
		}
	}
}

func TestStarterResultRejectsChangedRunOutput(t *testing.T) {
	plan := starterTestPlan()
	detail := RunDetail{
		Run:          Run{Inputs: starterTestInput("问题"), Outputs: json.RawMessage(`{"plan":{"normalizedQuestion":"已篡改"}}`)},
		Steps:        []Step{{ID: "step", Status: StepCompleted, Attempt: 1}},
		AIExecutions: []AIExecution{{WorkflowStepID: "step", Attempt: 1, Status: "completed", Output: plan, OutputSHA256: hashJSON(plan)}},
	}
	if _, _, _, err := decodeStarterResult(detail); err == nil {
		t.Fatal("changed Run output was accepted")
	}
}

func TestStarterResultMarksUntrustedStageCombinationUnavailable(t *testing.T) {
	plan := starterTestPlan()
	var value map[string]any
	_ = json.Unmarshal(plan, &value)
	routes := value["routes"].([]any)
	route := routes[0].(map[string]any)
	route["stageIds"] = []any{"question_refinement", "report_publication", "independent_review", "delivery_gate"}
	plan, _ = json.Marshal(value)
	outputs, _ := json.Marshal(map[string]any{"plan": value})
	detail := RunDetail{Run: Run{Inputs: starterTestInput("问题"), Outputs: outputs}, Steps: []Step{{ID: "step", Status: StepCompleted, Attempt: 1}}, AIExecutions: []AIExecution{{WorkflowStepID: "step", Attempt: 1, Status: "completed", Output: plan, OutputSHA256: hashJSON(plan)}}}
	inputs, parsed, _, err := decodeStarterResult(detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := annotateStarterRoutesForSelection(&parsed, inputs, nil); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Routes) != 1 || parsed.Routes[0].Validation != "invalid" || parsed.Routes[0].AvailableNow {
		t.Fatalf("untrusted stage combination was not isolated: %#v", parsed.Routes)
	}
}

func TestStarterDoesNotPromoteAIBlockedDataRouteBecauseUnrelatedTableExists(t *testing.T) {
	context := ResearchStarterContext{
		ResearchIdea: "研究手机传感器与通勤碳排放的关系",
		ResourceSnapshot: ResourceSnapshot{
			TabularFiles: []string{"unrelated-test-fixture.csv"},
		},
		StageCatalog: dynamicResearchStageCatalog(), PlannerVersion: dynamicResearchPlannerVersion,
	}
	inputs, _ := json.Marshal(map[string]any{"starter_context": context})
	plan := ResearchStarterPlan{
		NormalizedQuestion: "手机传感器能否估算通勤碳排放？",
		ResearchType:       "data",
		Routes:             []ResearchRoute{starterDataRoute()},
		RecommendedRouteID: "data_when_ready", Confidence: "medium",
	}
	planJSON, _ := json.Marshal(plan)
	outputs, _ := json.Marshal(map[string]any{"plan": plan})
	detail := RunDetail{
		Run:          Run{Inputs: inputs, Outputs: outputs},
		Steps:        []Step{{ID: "step", Status: StepCompleted, Attempt: 1}},
		AIExecutions: []AIExecution{{WorkflowStepID: "step", Attempt: 1, Status: "completed", Output: planJSON, OutputSHA256: hashJSON(planJSON)}},
	}
	starter, verified := projectStarterResult(t, detail, nil)
	if len(verified.Routes) != 1 || verified.Routes[0].AvailableNow {
		t.Fatalf("AI-blocked route was promoted: %#v", verified.Routes)
	}
	_, initialInputs, available, err := routeDefinition(verified.Routes[0], starter)
	if err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("routeDefinition promoted an AI-blocked route because an unrelated CSV exists")
	}
	var prepared struct {
		InputPaths []string `json:"input_paths"`
	}
	if json.Unmarshal(initialInputs, &prepared) != nil || len(prepared.InputPaths) != 0 {
		t.Fatalf("AI-blocked route bound an unrelated table: %s", initialInputs)
	}
}

func TestStarterRouteProjectionIsolatesInvalidRecommendedRoute(t *testing.T) {
	valid := starterDesignRoute()
	invalid := starterDesignRoute()
	invalid.RouteID = "invalid_recommended"
	invalid.StageIDs = []string{"question_refinement", "method_selection", "independent_review", "delivery_gate"}
	plan := ResearchStarterPlan{
		Routes:             []ResearchRoute{invalid, valid},
		RecommendedRouteID: invalid.RouteID,
	}
	context := ResearchStarterContext{
		PlannerVersion:   dynamicResearchPlannerVersion,
		StageCatalog:     dynamicResearchStageCatalog(),
		ResourceSnapshot: ResourceSnapshot{},
	}
	if err := annotateStarterRoutesForSelection(&plan, context, nil); err != nil {
		t.Fatalf("projection rejected a plan with one bad candidate: %v", err)
	}
	if got := plan.Routes[0].Validation; got != "invalid" {
		t.Fatalf("invalid recommended route validation = %q", got)
	}
	if plan.Routes[0].ValidationError == "" {
		t.Fatalf("invalid recommended route error is empty")
	}
	if got := plan.Routes[1].Validation; got != "ready" || !plan.Routes[1].AvailableNow {
		t.Fatalf("valid alternative route was not kept ready: validation=%q available=%v", got, plan.Routes[1].AvailableNow)
	}
}

func TestStarterRouteProjectionKeepsBlockedRouteAdoptable(t *testing.T) {
	route := starterDataRoute()
	plan := ResearchStarterPlan{Routes: []ResearchRoute{route}, RecommendedRouteID: route.RouteID}
	context := ResearchStarterContext{PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog()}
	if err := annotateStarterRoutesForSelection(&plan, context, nil); err != nil {
		t.Fatalf("blocked route projection failed: %v", err)
	}
	if got := plan.Routes[0].Validation; got != "blocked" {
		t.Fatalf("blocked route validation = %q", got)
	}
	if plan.Routes[0].AvailableNow {
		t.Fatal("blocked route was promoted to available")
	}
	if _, _, available, err := routeDefinition(plan.Routes[0], context); err != nil {
		t.Fatalf("blocked route could not be compiled for later use: %v", err)
	} else if available {
		t.Fatal("blocked route definition became immediately available")
	}
}

func TestStarterRouteProjectionDoesNotInventUnplannedDesignRoute(t *testing.T) {
	data := starterDataRoute()
	data.RouteID = "design_before_data" // A model is allowed to use this ID.
	plan := ResearchStarterPlan{Routes: []ResearchRoute{data}, RecommendedRouteID: data.RouteID}
	if err := annotateStarterRoutesForSelection(&plan, ResearchStarterContext{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) != 1 || plan.RecommendedRouteID != data.RouteID || plan.Routes[0].Validation != "blocked" {
		t.Fatalf("projection changed the frozen research intent: %+v", plan)
	}
	if err := annotateStarterRoutesForSelection(&plan, ResearchStarterContext{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) != 1 {
		t.Fatal("repeated projection invented a route")
	}
}

func TestStarterRouteProjectionPromotesNonDataRouteDespiteFalseAIAvailability(t *testing.T) {
	route := starterDesignRoute()
	route.AvailableNow = false
	route.Blockers = []string{"需要 CSV 数据才能继续"}
	plan := ResearchStarterPlan{Routes: []ResearchRoute{route}, RecommendedRouteID: route.RouteID}
	context := ResearchStarterContext{ResearchIdea: "没有数据的设计任务", StageCatalog: dynamicResearchStageCatalog(), PlannerVersion: dynamicResearchPlannerVersion}
	if err := annotateStarterRoutesForSelection(&plan, context, nil); err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	if plan.Routes[0].Validation != "ready" || !plan.Routes[0].AvailableNow || len(plan.Routes[0].Blockers) != 0 {
		t.Fatalf("non-data route remained blocked: %#v", plan.Routes[0])
	}
}

func TestStarterRouteProjectionUsesLoadedSkillWhenPlannerSummaryOmitsIt(t *testing.T) {
	route := starterDesignRoute()
	for li := range route.Layers {
		for si := range route.Layers[li].Stages {
			if route.Layers[li].Stages[si].StageID == "method_selection" {
				route.Layers[li].Stages[si].SkillNames = []string{"peer-review"}
			}
		}
	}
	plan := ResearchStarterPlan{Routes: []ResearchRoute{route}, RecommendedRouteID: route.RouteID}
	context := ResearchStarterContext{ResearchIdea: "文献设计", StageCatalog: dynamicResearchStageCatalog(), PlannerVersion: dynamicResearchPlannerVersion}
	loaded := []skillrun.Snapshot{{Name: "peer-review", ContentHash: strings.Repeat("a", 64), PackageHash: strings.Repeat("b", 64)}}
	if err := annotateStarterRoutesForSelection(&plan, context, loaded); err != nil {
		t.Fatalf("omitted top-level Skill should be recoverable: %v", err)
	}
	if plan.Routes[0].Validation != "ready" || !plan.Routes[0].AvailableNow {
		t.Fatalf("route remained unavailable: %#v", plan.Routes[0])
	}
}

func TestRouteNeedsTabularRecognizesAnalysisWithoutExplicitPreflight(t *testing.T) {
	route := ResearchRoute{StageIDs: []string{"question_refinement", "method_selection", "python_analysis", "result_interpretation", "independent_review", "delivery_gate"}}
	if !routeNeedsTabular(route) {
		t.Fatal("the current Python executor requires a declared task table")
	}
}

func TestStarterRouteProjectionNormalizesValidDataRouteAndKeepsFileImportAvailable(t *testing.T) {
	route := starterDataRoute()
	route.RouteID = "data_import_after_planning"
	route.StageIDs = []string{
		"question_refinement", "method_selection", "research_design",
		"literature_discovery", "candidate_review", "evidence_extraction",
		"data_preflight", "method_implementation", "dependency_preparation",
		"python_analysis", "result_interpretation", "report_drafting",
		"independent_review", "delivery_gate",
	}
	route.Layers = []ResearchRouteLayer{
		{LayerID: "strategy", Title: "战略与设计", Objective: "先形成研究设计", Stages: []ResearchRouteStage{
			{StageID: "question_refinement", Objective: "明确问题", Outputs: []string{"研究问题"}},
			{StageID: "method_selection", Objective: "选择方法", Outputs: []string{"方法蓝图"}},
			{StageID: "research_design", Objective: "形成设计", Outputs: []string{"研究设计"}},
		}},
		{LayerID: "evidence", Title: "证据", Objective: "补充证据", Stages: []ResearchRouteStage{
			{StageID: "literature_discovery", Objective: "发现文献", Outputs: []string{"候选文献"}},
			{StageID: "candidate_review", Objective: "筛选文献", Outputs: []string{"已选材料"}},
			{StageID: "evidence_extraction", Objective: "提取证据", Outputs: []string{"可信证据"}},
		}},
		{LayerID: "analysis", Title: "分析", Objective: "分析 CSV", Stages: []ResearchRouteStage{
			{StageID: "data_preflight", Objective: "检查数据", Outputs: []string{"数据快照"}},
			{StageID: "method_implementation", Objective: "实现方法", Outputs: []string{"分析代码"}},
			{StageID: "dependency_preparation", Objective: "准备依赖", Outputs: []string{"环境"}},
			{StageID: "python_analysis", Objective: "执行分析", Outputs: []string{"计算结果"}},
			{StageID: "result_interpretation", Objective: "解释结果", Outputs: []string{"结果解释"}},
		}},
		{LayerID: "delivery", Title: "交付", Objective: "形成并审查交付稿", Stages: []ResearchRouteStage{
			{StageID: "report_drafting", Objective: "形成交付稿", Outputs: []string{"交付稿"}},
			{StageID: "independent_review", Objective: "独立审查", Outputs: []string{"审查结论"}},
			{StageID: "delivery_gate", Objective: "核验交付", Outputs: []string{"交付许可"}},
		}},
	}
	plan := ResearchStarterPlan{Routes: []ResearchRoute{route}, RecommendedRouteID: route.RouteID}
	context := ResearchStarterContext{
		ResearchIdea:   "研究运动时长与大学生睡眠质量的关系，我有一份尚未导入的 CSV",
		PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog(),
	}
	if err := annotateStarterRoutesForSelection(&plan, context, nil); err != nil {
		t.Fatalf("route projection failed: %v", err)
	}
	projected := plan.Routes[0]
	if projected.Validation != "blocked" || projected.AvailableNow {
		t.Fatalf("missing CSV should be recoverable: validation=%q available=%v error=%q", projected.Validation, projected.AvailableNow, projected.ValidationError)
	}
	if got, want := strings.Join(projected.StageIDs, ","), "question_refinement,literature_discovery,candidate_review,evidence_extraction,method_selection,research_design,data_preflight,method_implementation,dependency_preparation,python_analysis,result_interpretation,report_drafting,independent_review,delivery_gate"; got != want {
		t.Fatalf("normalized stage order = %s, want %s", got, want)
	}
	template, initial, available, err := routeDefinition(projected, context)
	if err != nil {
		t.Fatalf("normalized route could not be compiled: %v", err)
	}
	if available {
		t.Fatal("route without an imported CSV became immediately available")
	}
	var filePort *Port
	for index := range template.Definition.Inputs {
		if template.Definition.Inputs[index].Name == "input_paths" {
			filePort = &template.Definition.Inputs[index]
			break
		}
	}
	if filePort == nil || filePort.FileKind != "tabular" || !filePort.Required || filePort.MinItems != 1 || filePort.MaxItems != 16 {
		t.Fatalf("recoverable route file input = %#v", filePort)
	}
	var prepared struct {
		InputPaths []string `json:"input_paths"`
	}
	if err := json.Unmarshal(initial, &prepared); err != nil || len(prepared.InputPaths) != 0 {
		t.Fatalf("initial blocked route inputs = %s, err=%v", initial, err)
	}
}

func TestStarterRouteProjectionIsolatesUnloadedSkillReference(t *testing.T) {
	valid := starterDesignRoute()
	valid.RouteID = "valid_skill_route"
	for layerIndex := range valid.Layers {
		for stageIndex := range valid.Layers[layerIndex].Stages {
			if valid.Layers[layerIndex].Stages[stageIndex].StageID == "method_selection" {
				valid.Layers[layerIndex].Stages[stageIndex].SkillNames = []string{"loaded-method"}
			}
		}
	}
	invalid := starterDataRoute()
	invalid.RouteID = "missing_skill_route"
	for layerIndex := range invalid.Layers {
		for stageIndex := range invalid.Layers[layerIndex].Stages {
			if invalid.Layers[layerIndex].Stages[stageIndex].StageID == "method_selection" {
				invalid.Layers[layerIndex].Stages[stageIndex].SkillNames = []string{"missing-method"}
			}
		}
	}
	plan := ResearchStarterPlan{
		SelectedSkills:     []ResearchSkillSelection{{Name: "loaded-method", Role: "提供研究方法"}},
		Routes:             []ResearchRoute{invalid, valid},
		RecommendedRouteID: valid.RouteID,
	}
	context := ResearchStarterContext{PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog()}
	loaded := []skillrun.Snapshot{{Name: "loaded-method", ContentHash: strings.Repeat("a", 64), PackageHash: strings.Repeat("b", 64)}}
	if err := annotateStarterRoutesForSelection(&plan, context, loaded); err != nil {
		t.Fatalf("route-local Skill projection failed: %v", err)
	}
	if plan.Routes[0].Validation != "invalid" || !strings.Contains(plan.Routes[0].ValidationError, "missing-method") {
		t.Fatalf("missing Skill route was not isolated: %#v", plan.Routes[0])
	}
	if plan.Routes[1].Validation != "ready" {
		t.Fatalf("loaded Skill route was contaminated by another route: %#v", plan.Routes[1])
	}
	selected, err := validatedResearchSkillsForRoute(plan, plan.Routes[1], loaded)
	if err != nil || len(selected) != 1 || selected[0].Name != "loaded-method" {
		t.Fatalf("valid route Skill snapshot = %#v, %v", selected, err)
	}
}

func TestStarterRouteProjectionDoesNotLetInvalidDuplicateShadowValidRoute(t *testing.T) {
	invalid := starterDesignRoute()
	invalid.RouteID = "invalid_duplicate"
	// Keep the same stage combination as the valid candidate, but make the
	// layer envelope invalid. The invalid candidate must not claim the
	// combination before the valid candidate is checked.
	invalid.Layers[0].Objective = ""
	valid := starterDesignRoute()
	valid.RouteID = "valid_duplicate"
	plan := ResearchStarterPlan{
		Routes:             []ResearchRoute{invalid, valid},
		RecommendedRouteID: valid.RouteID,
	}
	context := ResearchStarterContext{PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog()}
	if err := annotateStarterRoutesForSelection(&plan, context, nil); err != nil {
		t.Fatalf("projection rejected valid duplicate alternative: %v", err)
	}
	if plan.Routes[0].Validation != "invalid" {
		t.Fatalf("malformed duplicate route validation = %q", plan.Routes[0].Validation)
	}
	if plan.Routes[1].Validation != "ready" || !plan.Routes[1].AvailableNow {
		t.Fatalf("valid duplicate alternative was shadowed: validation=%q available=%v", plan.Routes[1].Validation, plan.Routes[1].AvailableNow)
	}
}

func TestStarterRouteProjectionLimitsSkillsPerRouteNotWholePlannerSnapshot(t *testing.T) {
	valid := starterDesignRoute()
	valid.RouteID = "one_skill_route"
	valid.Layers[1].Stages[0].SkillNames = []string{"method-0"}

	selected := make([]ResearchSkillSelection, 0, maxResearchStarterSelectedSkills+1)
	loaded := make([]skillrun.Snapshot, 0, maxResearchStarterSelectedSkills+1)
	for index := 0; index <= maxResearchStarterSelectedSkills; index++ {
		name := fmt.Sprintf("method-%d", index)
		selected = append(selected, ResearchSkillSelection{Name: name, Role: "互补方法"})
		loaded = append(loaded, skillrun.Snapshot{Name: name, ContentHash: strings.Repeat("a", 64), PackageHash: strings.Repeat("b", 64)})
	}
	plan := ResearchStarterPlan{SelectedSkills: selected, Routes: []ResearchRoute{valid}, RecommendedRouteID: valid.RouteID}
	context := ResearchStarterContext{PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog()}
	if err := annotateStarterRoutesForSelection(&plan, context, loaded); err != nil {
		t.Fatalf("route-local Skill limit rejected valid route: %v", err)
	}
	if plan.Routes[0].Validation != "ready" || !plan.Routes[0].AvailableNow {
		t.Fatalf("valid one-Skill route was blocked by unrelated snapshots: %#v", plan.Routes[0])
	}
	selectedForRoute, err := validatedResearchSkillsForRoute(plan, plan.Routes[0], loaded)
	if err != nil || len(selectedForRoute) != 1 || selectedForRoute[0].Name != "method-0" {
		t.Fatalf("route-local Skill selection = %#v, %v", selectedForRoute, err)
	}
}

func TestStarterRouteProjectionRejectsOnlyRouteThatUsesTooManySkills(t *testing.T) {
	route := starterDesignRoute()
	route.RouteID = "too_many_skills"
	names := make([]string, 0, maxResearchStarterSelectedSkills+1)
	selected := make([]ResearchSkillSelection, 0, maxResearchStarterSelectedSkills+1)
	loaded := make([]skillrun.Snapshot, 0, maxResearchStarterSelectedSkills+1)
	for index := 0; index <= maxResearchStarterSelectedSkills; index++ {
		name := fmt.Sprintf("method-%d", index)
		names = append(names, name)
		selected = append(selected, ResearchSkillSelection{Name: name, Role: "核心方法"})
		loaded = append(loaded, skillrun.Snapshot{Name: name, ContentHash: strings.Repeat("a", 64), PackageHash: strings.Repeat("b", 64)})
	}
	route.Layers[1].Stages[0].SkillNames = names
	plan := ResearchStarterPlan{SelectedSkills: selected, Routes: []ResearchRoute{route}, RecommendedRouteID: route.RouteID}
	context := ResearchStarterContext{PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog()}
	if err := annotateStarterRoutesForSelection(&plan, context, loaded); err != nil {
		t.Fatalf("projection failed instead of isolating over-limit route: %v", err)
	}
	if plan.Routes[0].Validation != "invalid" || !strings.Contains(plan.Routes[0].ValidationError, "核心 Skill 过多") {
		t.Fatalf("over-limit route was not isolated: %#v", plan.Routes[0])
	}
}

func TestApplyResearchTaskLinkKeepsPlanningAndExecutionUnderOneIdentity(t *testing.T) {
	starter := Run{ID: "starter-id", ResearchTaskID: "task-id", WorkflowPurpose: PurposeResearchStarter}
	ApplyResearchTaskLink(&starter)
	if starter.ResearchTaskID != "task-id" || starter.ResearchStarterID != "" {
		t.Fatalf("starter task link = %#v", starter)
	}
	formal := Run{ID: "formal-id", ResearchTaskID: "task-id", WorkflowPurpose: PurposeUserPlan, CreationKey: "research-route:starter-id:design_first"}
	ApplyResearchTaskLink(&formal)
	if formal.ResearchTaskID != "task-id" || formal.ResearchStarterID != starter.ID {
		t.Fatalf("formal task link = %#v", formal)
	}
}

func clarificationFixture() ResearchClarification {
	return ResearchClarification{
		NeedsUserInput: true,
		Questions: []ResearchClarificationQuestion{
			{
				ID: "population", Text: "研究对象？", Required: true,
				Options: []ResearchClarificationOption{{ID: "students", Label: "在校大学生"}, {ID: "adults", Label: "一般成年人"}},
			},
			{
				ID: "goal", Text: "研究目标？", Required: false,
				Options: []ResearchClarificationOption{{ID: "exploratory", Label: "探索性分析"}, {ID: "confirmatory", Label: "验证性设计"}},
			},
		},
	}
}

func TestResearchClarificationValidationRequiresFiniteDistinctOptions(t *testing.T) {
	valid := clarificationFixture()
	if err := validateResearchClarification(valid); err != nil {
		t.Fatalf("valid clarification rejected: %v", err)
	}
	cases := []struct {
		name   string
		value  ResearchClarification
		needle string
	}{
		{name: "missing questions", value: ResearchClarification{NeedsUserInput: true}, needle: "1-12"},
		{name: "invalid question id", value: ResearchClarification{NeedsUserInput: true, Questions: []ResearchClarificationQuestion{{ID: "bad id", Text: "对象", Options: []ResearchClarificationOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}}}}, needle: "question"},
		{name: "duplicate option", value: ResearchClarification{NeedsUserInput: true, Questions: []ResearchClarificationQuestion{{ID: "q", Text: "对象", Options: []ResearchClarificationOption{{ID: "a", Label: "A"}, {ID: "a", Label: "B"}}}}}, needle: "option"},
		{name: "questions on nonblocking", value: ResearchClarification{Questions: []ResearchClarificationQuestion{{ID: "q", Text: "对象", Options: []ResearchClarificationOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}}}}, needle: "non-blocking"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := validateResearchClarification(test.value)
			if err == nil || !strings.Contains(err.Error(), test.needle) {
				t.Fatalf("error = %v, want substring %q", err, test.needle)
			}
		})
	}
}

func TestResearchClarificationAnswersAreHostValidatedAndAuditable(t *testing.T) {
	clarification := clarificationFixture()
	answers, err := normalizeResearchClarificationAnswers(clarification, map[string][]string{
		"population": {"students"}, "goal": {"exploratory"},
	})
	if err != nil {
		t.Fatalf("valid answers rejected: %v", err)
	}
	if got := clarifiedResearchIdea("研究睡眠", clarification, answers); !strings.Contains(got, "在校大学生") || !strings.Contains(got, "探索性分析") {
		t.Fatalf("clarified idea did not preserve selected labels: %q", got)
	}
	invalid := []map[string][]string{
		{"unknown": {"students"}},
		{"population": {"unknown"}},
		{"population": {}},
		{"population": {"students", "adults"}},
	}
	for index, value := range invalid {
		if _, err := normalizeResearchClarificationAnswers(clarification, value); err == nil {
			t.Fatalf("invalid answer %d was accepted: %#v", index, value)
		}
	}
}

package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/skillrun"
)

func TestReferenceTemplatesCompileAgainstResearchToolContracts(t *testing.T) {
	registry := tool.NewRegistry()
	definitions := []tool.Definition{
		fullTextReadFixtureDefinition(),
		{QualifiedName: "builtin.resource.open", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["actionId"],"properties":{"actionId":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.resource.search", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["actionId","query"],"properties":{"actionId":{"type":"string"},"query":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.workspace.list", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.workspace.read_text", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)},
		{QualifiedName: "builtin.skill.load", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"category":{"type":"string"},"section":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string"},"loaded":{"type":"boolean"}}}`)},
		{QualifiedName: "builtin.skill.resource.list", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"resources":{"type":"array"}}}`)},
		{QualifiedName: "builtin.skill.resource.read_text", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["name","path"],"properties":{"name":{"type":"string"},"path":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"path":{"type":"string"}}}`)},
		{QualifiedName: "builtin.research.workflow.search", Version: "2", Risk: tool.RiskModerate, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"},"queries":{"type":"array"},"referencesOnly":{"type":"boolean"},"limit":{"type":"integer"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"queryText":{"type":"string"},"candidates":{"type":"array"}}}`)},
		{QualifiedName: "builtin.research.workflow.import", Version: "5", Risk: tool.RiskHigh, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["selectedCandidateIds"],"properties":{"selectedCandidateIds":{"type":"array"},"selectedAttachmentIds":{"type":"array"},"mode":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"attachmentIds":{"type":"array"}}}`)},
		{QualifiedName: "builtin.research.workflow.sync", Version: "1", Risk: tool.RiskModerate, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["attachmentIds"],"properties":{"attachmentIds":{"type":"array"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"documentIds":{"type":"array"}}}`)},
		{QualifiedName: "builtin.knowledge.search", Version: "3", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"},"limit":{"type":"integer"},"documentIds":{"type":"array"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"matches":{"type":"array"}}}`)},
		{QualifiedName: "builtin.research.workflow.python.ensure", Version: "1", Risk: tool.RiskHigh, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","properties":{"context":{}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"environmentFingerprint":{"type":"string"}}}`)},
		{QualifiedName: "builtin.research.workflow.python.prepare", Version: "1", Risk: tool.RiskHigh, Idempotent: false, InputSchema: json.RawMessage(`{"type":"object","required":["packages"],"properties":{"packages":{"type":"array"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"environmentFingerprint":{"type":"string"}}}`)},
		{QualifiedName: "builtin.research.workflow.review.gate", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["subject","review"],"properties":{"subject":{},"review":{"type":"object"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"approved":{"type":"boolean"}}}`)},
		{QualifiedName: "builtin.python.kernel.execute", Version: "1", Risk: tool.RiskHigh, Idempotent: false, InputSchema: json.RawMessage(`{"type":"object","required":["code"],"properties":{"code":{"type":"string"},"inputPaths":{"type":"array"},"inputData":{},"expectedEnvironmentFingerprint":{"type":"string"},"outputPaths":{"type":"array"},"timeoutSeconds":{"type":"integer"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"reproductionSha256":{"type":"string"}}}`)},
		{QualifiedName: "builtin.research.workflow.report", Version: "3", Risk: tool.RiskHigh, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["name","citations","reportDraft","reviewGate"],"properties":{"name":{"type":"string"},"citations":{"type":"array"},"reportDraft":{"type":"object"},"sourceArtifacts":{"type":"array"},"reviewGate":{"type":"object"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"artifact":{"type":"object"}}}`)},
	}
	for index := range definitions {
		definitions[index].Description = "Workflow template fixture tool"
	}
	for _, definition := range definitions {
		definition = withProviderQueryFixture(definition)
		if err := registry.Register(context.Background(), fixtureTool{definition: definition}); err != nil {
			t.Fatal(err)
		}
	}
	for _, template := range ReferenceTemplates() {
		t.Run(template.ID, func(t *testing.T) {
			compiled, err := NewCompiler(registry).Compile(context.Background(), template.Definition)
			if err != nil {
				preview := NewCompiler(registry).Preview(context.Background(), template.Definition)
				t.Fatalf("template does not compile: %v; diagnostics=%#v", err, preview.Diagnostics)
			}
			if len(compiled.Order) != len(template.Definition.Nodes) {
				t.Fatalf("compiled order=%#v", compiled.Order)
			}
			hasAI := false
			for _, node := range compiled.Nodes {
				if node.Kind != NodeAIAnalysis && node.Kind != NodeAgentStage {
					continue
				}
				hasAI = true
				if node.PromptVersion == "" || len(node.OutputSchema) == 0 || len(node.OutputSchemaSHA256) != 64 {
					t.Fatalf("AI node snapshot is incomplete: %#v", node)
				}
				if node.Kind == NodeAgentStage && (node.ReviewPolicy != AIReviewAuto || !node.SkillRouting) {
					t.Fatalf("reference Agent Stage is not autonomy-first: %#v", node)
				}
				if node.SkillRouting && (!hasFrozenTool(node.AllowedTools, "builtin.skill.load") || !hasFrozenTool(node.AllowedTools, "builtin.skill.resource.list") || !hasFrozenTool(node.AllowedTools, "builtin.skill.resource.read_text")) {
					t.Fatalf("Skill-assisted Agent Stage did not freeze read-only Skill tools: %#v", node.AllowedTools)
				}
			}
			if !hasAI {
				t.Fatal("reference research template has no AI collaboration stage")
			}
		})
	}
}

func TestDynamicResearchRoutesCompileFromTrustedStageCombinations(t *testing.T) {
	registry := referenceTemplateRegistry(t)
	for _, route := range []ResearchRoute{
		{
			RouteID: "design_first", Title: "动态研究设计", Reason: "形成课题设计", AvailableNow: true,
			Deliverables: []string{"研究设计"}, StageIDs: []string{"question_refinement", "method_selection", "research_design", "independent_review", "delivery_gate"}, ReviewCheckpoints: []string{"独立审查"},
			Layers: []ResearchRouteLayer{
				{LayerID: "strategy", Title: "问题", Objective: "明确问题", Stages: []ResearchRouteStage{{StageID: "question_refinement", Objective: "明确问题", Outputs: []string{"问题"}}}},
				{LayerID: "method", Title: "方法", Objective: "形成设计", Stages: []ResearchRouteStage{{StageID: "method_selection", Objective: "选择方法", Outputs: []string{"方法"}}, {StageID: "research_design", Objective: "形成设计", Outputs: []string{"设计"}}}},
				{LayerID: "delivery", Title: "交付", Objective: "核验交付", Stages: []ResearchRouteStage{{StageID: "independent_review", Objective: "独立审查", Outputs: []string{"审查"}}, {StageID: "delivery_gate", Objective: "交付门禁", Outputs: []string{"许可"}}}},
			},
		},
		{
			RouteID: "data", Title: "动态数据分析", Reason: "执行课题分析", AvailableNow: true,
			Deliverables: []string{"分析结果"}, StageIDs: []string{"question_refinement", "method_selection", "data_preflight", "method_implementation", "dependency_preparation", "python_analysis", "result_interpretation", "independent_review", "delivery_gate"}, ReviewCheckpoints: []string{"方法确认", "独立审查"},
			Layers: []ResearchRouteLayer{
				{LayerID: "strategy", Title: "问题", Objective: "明确问题", Stages: []ResearchRouteStage{{StageID: "question_refinement", Objective: "明确问题", Outputs: []string{"问题"}}}},
				{LayerID: "method", Title: "方法", Objective: "形成实现", Stages: []ResearchRouteStage{{StageID: "method_selection", Objective: "选择方法", Outputs: []string{"方法"}}, {StageID: "data_preflight", Objective: "预检数据", Outputs: []string{"预检"}}, {StageID: "method_implementation", Objective: "实现方法", Outputs: []string{"代码"}}}},
				{LayerID: "execution", Title: "执行", Objective: "执行分析", Stages: []ResearchRouteStage{{StageID: "dependency_preparation", Objective: "准备依赖", Outputs: []string{"环境"}}, {StageID: "python_analysis", Objective: "执行分析", Outputs: []string{"结果"}}, {StageID: "result_interpretation", Objective: "解释结果", Outputs: []string{"解释"}}}},
				{LayerID: "delivery", Title: "交付", Objective: "核验交付", Stages: []ResearchRouteStage{{StageID: "independent_review", Objective: "独立审查", Outputs: []string{"审查"}}, {StageID: "delivery_gate", Objective: "交付门禁", Outputs: []string{"许可"}}}},
			},
		},
	} {
		t.Run(route.RouteID, func(t *testing.T) {
			starter := ResearchStarterContext{ResearchIdea: "测试课题", PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog(), ResourceSnapshot: ResourceSnapshot{TabularFiles: []string{"data.csv"}}}
			template, _, _, err := dynamicResearchRouteTemplate(route, starter, true)
			if err != nil {
				t.Fatal(err)
			}
			compiler := NewCompiler(registry)
			if _, err := compiler.Compile(context.Background(), template.Definition); err != nil {
				t.Fatalf("dynamic route does not compile: %v; diagnostics=%#v", err, compiler.Preview(context.Background(), template.Definition).Diagnostics)
			}
			trusted := map[string]bool{}
			for _, stage := range dynamicResearchStageCatalog() {
				trusted[stage.StageID] = true
			}
			actual := make([]string, 0, len(route.StageIDs))
			for _, node := range template.Definition.Nodes {
				if trusted[node.ID] {
					actual = append(actual, node.ID)
				}
			}
			if !slices.Equal(actual, route.StageIDs) {
				t.Fatalf("compiled trusted stages = %v, route = %v", actual, route.StageIDs)
			}
		})
	}
}

func TestDynamicResearchRouteCarriesResearchDesignIntoEmpiricalDelivery(t *testing.T) {
	route := ResearchRoute{
		RouteID: "designed_empirical_report", Title: "设计约束下的实证分析", Reason: "设计、分析和报告形成一条因果链", AvailableNow: true,
		Deliverables:      []string{"研究设计", "实证研究报告"},
		StageIDs:          []string{"question_refinement", "method_selection", "research_design", "data_preflight", "method_implementation", "dependency_preparation", "python_analysis", "result_interpretation", "report_drafting", "independent_review", "delivery_gate"},
		ReviewCheckpoints: []string{"独立审查"},
		Layers: []ResearchRouteLayer{
			{LayerID: "question", Title: "问题", Objective: "冻结问题", Stages: []ResearchRouteStage{{StageID: "question_refinement", Objective: "冻结问题", Outputs: []string{"问题"}}}},
			{LayerID: "method", Title: "方法与设计", Objective: "形成设计", Stages: []ResearchRouteStage{{StageID: "method_selection", Objective: "选择方法", Outputs: []string{"方法"}}, {StageID: "research_design", Objective: "形成设计", Outputs: []string{"设计"}}}},
			{LayerID: "analysis", Title: "实证分析", Objective: "执行分析", Stages: []ResearchRouteStage{{StageID: "data_preflight", Objective: "预检数据", Outputs: []string{"预检"}}, {StageID: "method_implementation", Objective: "实现方法", Outputs: []string{"代码"}}, {StageID: "dependency_preparation", Objective: "准备依赖", Outputs: []string{"环境"}}, {StageID: "python_analysis", Objective: "执行分析", Outputs: []string{"结果"}}, {StageID: "result_interpretation", Objective: "解释结果", Outputs: []string{"解释"}}}},
			{LayerID: "delivery", Title: "交付", Objective: "报告与核验", Stages: []ResearchRouteStage{{StageID: "report_drafting", Objective: "形成报告", Outputs: []string{"报告"}}, {StageID: "independent_review", Objective: "独立审查", Outputs: []string{"审查"}}, {StageID: "delivery_gate", Objective: "核验交付", Outputs: []string{"许可"}}}},
		},
	}
	starter := ResearchStarterContext{ResearchIdea: "测试课题", PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog(), ResourceSnapshot: ResourceSnapshot{TabularFiles: []string{"data.csv"}}}
	template, _, _, err := dynamicResearchRouteTemplate(route, starter, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []Edge{
		{FromNode: "research_design", FromPort: "analysis", ToNode: "method_implementation", ToPort: "designContext"},
		{FromNode: "research_design", FromPort: "analysis", ToNode: "result_interpretation", ToPort: "designContext"},
		{FromNode: "research_design", FromPort: "analysis", ToNode: "report_drafting", ToPort: "designContext"},
	} {
		if !slices.Contains(template.Definition.Edges, expected) {
			t.Fatalf("dynamic empirical route does not carry design context over edge %#v", expected)
		}
	}
	compiler := NewCompiler(referenceTemplateRegistry(t))
	compiled, err := compiler.Compile(context.Background(), template.Definition)
	if err != nil {
		t.Fatalf("%v: %#v", err, compiler.Preview(context.Background(), template.Definition).Diagnostics)
	}
	if slices.Index(compiled.Order, "research_design") >= slices.Index(compiled.Order, "method_implementation") {
		t.Fatalf("research design must execute before method implementation: %v", compiled.Order)
	}
}

func TestDynamicResearchRouteAcceptsEvidenceDeliveryWithoutPython(t *testing.T) {
	route := ResearchRoute{
		RouteID: "evidence_synthesis", Title: "证据综合", Reason: "基于核验文献形成证据交付稿", AvailableNow: true,
		Deliverables: []string{"证据综合报告"}, ReviewCheckpoints: []string{"独立二次审查"},
		StageIDs: []string{"question_refinement", "literature_discovery", "candidate_review", "evidence_extraction", "method_selection", "report_drafting", "independent_review", "delivery_gate"},
		Layers: []ResearchRouteLayer{
			{LayerID: "question", Title: "问题", Objective: "明确研究边界", Stages: []ResearchRouteStage{{StageID: "question_refinement", Objective: "明确研究边界", Outputs: []string{"研究问题"}}}},
			{LayerID: "evidence", Title: "证据", Objective: "建立可核验的证据链", Stages: []ResearchRouteStage{
				{StageID: "literature_discovery", Objective: "发现候选文献", Outputs: []string{"候选文献"}},
				{StageID: "candidate_review", Objective: "确认研究材料", Outputs: []string{"已选材料"}},
				{StageID: "evidence_extraction", Objective: "提取可信证据", Outputs: []string{"证据摘录"}},
				{StageID: "method_selection", Objective: "选择综合方法", Outputs: []string{"方法蓝图"}},
				{StageID: "report_drafting", Objective: "形成证据交付稿", Outputs: []string{"交付稿"}},
			}},
			{LayerID: "delivery", Title: "交付", Objective: "独立复核后交付", Stages: []ResearchRouteStage{
				{StageID: "independent_review", Objective: "独立审查", Outputs: []string{"审查结论"}},
				{StageID: "delivery_gate", Objective: "核验交付条件", Outputs: []string{"交付许可"}},
			}},
		},
	}
	starterContext := ResearchStarterContext{ResearchIdea: "测试证据综合课题", PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog()}
	available, blockers, err := validateDynamicResearchRoute(route, starterContext)
	if err != nil || !available || len(blockers) != 0 {
		t.Fatalf("evidence-only route rejected: available=%v blockers=%v err=%v", available, blockers, err)
	}
	template, _, _, err := dynamicResearchRouteTemplate(route, starterContext, true)
	if err != nil {
		t.Fatalf("evidence-only route template failed: %v", err)
	}
	compiler := NewCompiler(referenceTemplateRegistry(t))
	if _, compileErr := compiler.Compile(context.Background(), template.Definition); compileErr != nil {
		t.Fatalf("AI-screened evidence route does not compile: %v; diagnostics=%#v", compileErr, compiler.Preview(context.Background(), template.Definition).Diagnostics)
	}
	order := make([]string, 0, len(template.Definition.Nodes))
	for _, node := range template.Definition.Nodes {
		order = append(order, node.ID)
	}
	for _, expected := range []string{"literature_discovery", "candidate_screening", "candidate_review", "evidence_search", "evidence_screening", "evidence_extraction", "method_selection"} {
		if !slices.Contains(order, expected) {
			t.Fatalf("evidence route is missing %s: %v", expected, order)
		}
	}
	if slices.Index(order, "candidate_screening") >= slices.Index(order, "candidate_review") || slices.Index(order, "evidence_screening") >= slices.Index(order, "evidence_extraction") {
		t.Fatalf("AI screening must precede user confirmation: %v", order)
	}
	for _, expected := range []Edge{
		{FromNode: "candidate_screening", FromPort: "analysis", ToNode: "candidate_review", ToPort: "screening"},
		{FromNode: "evidence_screening", FromPort: "analysis", ToNode: "evidence_extraction", ToPort: "screening"},
		{FromNode: "evidence_extraction", FromPort: "selectionAudit", ToNode: "independent_review", ToPort: "evidenceSelectionAudit"},
	} {
		if !slices.Contains(template.Definition.Edges, expected) {
			t.Fatalf("evidence route is missing audited edge %#v", expected)
		}
	}
}

func TestDynamicResearchRouteRejectsPartialEvidenceAndDataChains(t *testing.T) {
	base := ResearchRoute{
		RouteID: "partial", Title: "无效路线", Reason: "缺少阶段", AvailableNow: true,
		Deliverables: []string{"研究设计"}, ReviewCheckpoints: []string{"独立审查"},
	}
	context := ResearchStarterContext{PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog(), ResourceSnapshot: ResourceSnapshot{TabularFiles: []string{"data.csv"}}}
	for _, stages := range [][]string{
		{"question_refinement", "literature_discovery", "method_selection", "research_design", "independent_review", "delivery_gate"},
		{"question_refinement", "method_selection", "data_preflight", "research_design", "independent_review", "delivery_gate"},
	} {
		route := base
		route.StageIDs = stages
		route.Layers = []ResearchRouteLayer{
			{LayerID: "one", Title: "一", Objective: "一", Stages: []ResearchRouteStage{{StageID: stages[0], Objective: "一", Outputs: []string{"一"}}, {StageID: stages[1], Objective: "二", Outputs: []string{"二"}}}},
			{LayerID: "two", Title: "二", Objective: "二", Stages: []ResearchRouteStage{{StageID: stages[2], Objective: "三", Outputs: []string{"三"}}, {StageID: stages[3], Objective: "四", Outputs: []string{"四"}}}},
			{LayerID: "three", Title: "三", Objective: "三", Stages: []ResearchRouteStage{{StageID: stages[4], Objective: "五", Outputs: []string{"五"}}, {StageID: stages[5], Objective: "六", Outputs: []string{"六"}}}},
		}
		if _, _, err := validateDynamicResearchRoute(route, context); err == nil || !strings.Contains(err.Error(), "不完整") {
			t.Fatalf("partial route %v error = %v", stages, err)
		}
	}
}

func TestDynamicResearchRouteRejectsStagesOutsideTrustedCausalOrder(t *testing.T) {
	route := ResearchRoute{
		RouteID: "out_of_order", Title: "顺序错误", Reason: "报告先于设计", AvailableNow: true,
		Deliverables: []string{"研究设计"}, ReviewCheckpoints: []string{"独立审查"},
		StageIDs: []string{"question_refinement", "method_selection", "report_drafting", "research_design", "independent_review", "delivery_gate"},
		Layers: []ResearchRouteLayer{
			{LayerID: "one", Title: "一", Objective: "一", Stages: []ResearchRouteStage{{StageID: "question_refinement", Objective: "一", Outputs: []string{"一"}}, {StageID: "method_selection", Objective: "二", Outputs: []string{"二"}}}},
			{LayerID: "two", Title: "二", Objective: "二", Stages: []ResearchRouteStage{{StageID: "report_drafting", Objective: "三", Outputs: []string{"三"}}, {StageID: "research_design", Objective: "四", Outputs: []string{"四"}}}},
			{LayerID: "three", Title: "三", Objective: "三", Stages: []ResearchRouteStage{{StageID: "independent_review", Objective: "五", Outputs: []string{"五"}}, {StageID: "delivery_gate", Objective: "六", Outputs: []string{"六"}}}},
		},
	}
	context := ResearchStarterContext{PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog()}
	if _, _, err := validateDynamicResearchRoute(route, context); err == nil || !strings.Contains(err.Error(), "因果顺序") {
		t.Fatalf("out-of-order route error = %v", err)
	}
}

func TestResearchSkillsForRouteDoesNotLeakSkillsAcrossCandidateRoutes(t *testing.T) {
	skills := []ResearchSkillSelection{
		{Name: "design-method", Role: "设计方法", StageIDs: []string{"method_selection", "research_design"}},
		{Name: "data-method", Role: "数据方法", StageIDs: []string{"method_selection", "python_analysis"}},
	}
	route := ResearchRoute{
		StageIDs: []string{"question_refinement", "method_selection", "research_design", "independent_review", "delivery_gate"},
		Layers: []ResearchRouteLayer{{Stages: []ResearchRouteStage{
			{StageID: "method_selection", SkillNames: []string{"design-method"}},
			{StageID: "research_design", SkillNames: []string{"design-method"}},
		}}},
	}
	loaded := []skillrun.Snapshot{{Name: "design-method", ContentHash: "content-a", PackageHash: "package-a"}, {Name: "data-method", ContentHash: "content-b", PackageHash: "package-b"}}
	selected, err := validatedResearchSkillsForRoute(ResearchStarterPlan{SelectedSkills: skills}, route, loaded)
	if err != nil || len(selected) != 1 || selected[0].Name != "design-method" {
		t.Fatalf("selected route Skills = %#v", selected)
	}
	if len(selected[0].StageIDs) != 2 || selected[0].StageIDs[0] != "method_selection" || selected[0].StageIDs[1] != "research_design" {
		t.Fatalf("selected route stage bindings = %#v", selected[0].StageIDs)
	}
}

func TestDynamicSkillStageBindingsAreDerivedFromValidatedRoutes(t *testing.T) {
	plan := ResearchStarterPlan{
		SelectedSkills: []ResearchSkillSelection{
			{Name: "research-method", Role: "组织研究过程", StageIDs: []string{"method_selection"}},
			{Name: "evidence-method", Role: "核验研究证据"},
		},
		Routes: []ResearchRoute{{
			RouteID: "evidence_design",
			Layers: []ResearchRouteLayer{
				{LayerID: "evidence", Stages: []ResearchRouteStage{
					{StageID: "literature_discovery", SkillNames: []string{"research-method", "evidence-method"}},
					{StageID: "evidence_extraction", SkillNames: []string{"evidence-method"}},
				}},
				{LayerID: "method", Stages: []ResearchRouteStage{
					{StageID: "method_selection", SkillNames: []string{"research-method"}},
				}},
			},
		}},
	}
	loaded := []skillrun.Snapshot{
		{Name: "research-method", ContentHash: "content-a", PackageHash: "package-a"},
		{Name: "evidence-method", ContentHash: "content-b", PackageHash: "package-b"},
	}
	selected, err := validatedResearchSkillsForRoute(plan, plan.Routes[0], loaded)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].Name != "evidence-method" || selected[0].PackageHash != "package-b" || selected[1].Name != "research-method" || selected[1].ContentHash != "content-a" {
		t.Fatalf("route Skill snapshots = %#v", selected)
	}
	if want := []string{"literature_discovery", "evidence_extraction"}; !slices.Equal(selected[0].StageIDs, want) {
		t.Fatalf("derived evidence-method stages = %v, want %v", selected[0].StageIDs, want)
	}
	if want := []string{"literature_discovery", "method_selection"}; !slices.Equal(selected[1].StageIDs, want) {
		t.Fatalf("derived research-method stages = %v, want %v", selected[1].StageIDs, want)
	}
}

func TestDynamicSkillStageBindingsStillRejectUnloadedSkills(t *testing.T) {
	plan := ResearchStarterPlan{
		SelectedSkills: []ResearchSkillSelection{{Name: "invented-method", Role: "未加载的方法"}},
		Routes:         []ResearchRoute{{Layers: []ResearchRouteLayer{{Stages: []ResearchRouteStage{{StageID: "method_selection", SkillNames: []string{"invented-method"}}}}}}},
	}
	if _, err := validatedResearchSkillsForRoute(plan, plan.Routes[0], nil); err == nil || !strings.Contains(err.Error(), "未实际加载") {
		t.Fatalf("unloaded Skill error = %v", err)
	}
}

func TestDynamicResearchSkillLimitsStayAlignedAcrossSchemasAndValidation(t *testing.T) {
	starterSchema := decodeSchemaForTest(t, dynamicResearchStarterSchema())
	properties := starterSchema["properties"].(map[string]any)
	if got := int(properties["selectedSkills"].(map[string]any)["maxItems"].(float64)); got != maxResearchStarterSelectedSkills {
		t.Fatalf("starter selectedSkills maxItems = %d, want %d", got, maxResearchStarterSelectedSkills)
	}
	routes := properties["routes"].(map[string]any)["items"].(map[string]any)
	layers := routes["properties"].(map[string]any)["layers"].(map[string]any)["items"].(map[string]any)
	stages := layers["properties"].(map[string]any)["stages"].(map[string]any)["items"].(map[string]any)
	if got := int(stages["properties"].(map[string]any)["skillNames"].(map[string]any)["maxItems"].(float64)); got != maxResearchStarterSelectedSkills {
		t.Fatalf("stage skillNames maxItems = %d, want %d", got, maxResearchStarterSelectedSkills)
	}
	methodSchema := decodeSchemaForTest(t, dynamicMethodSchema())
	methodProperties := methodSchema["properties"].(map[string]any)
	if got := int(methodProperties["selectedSkills"].(map[string]any)["maxItems"].(float64)); got != maxResearchStarterSelectedSkills {
		t.Fatalf("method selectedSkills maxItems = %d, want %d", got, maxResearchStarterSelectedSkills)
	}

	selected, loaded := make([]ResearchSkillSelection, 0, maxResearchStarterSelectedSkills+1), make([]skillrun.Snapshot, 0, maxResearchStarterSelectedSkills+1)
	stageSkills := make([]string, 0, maxResearchStarterSelectedSkills+1)
	for index := 0; index <= maxResearchStarterSelectedSkills; index++ {
		name := fmt.Sprintf("method-%d", index)
		selected = append(selected, ResearchSkillSelection{Name: name, Role: "核心方法"})
		loaded = append(loaded, skillrun.Snapshot{Name: name, ContentHash: fmt.Sprintf("content-%d", index), PackageHash: fmt.Sprintf("package-%d", index)})
		stageSkills = append(stageSkills, name)
	}
	plan := ResearchStarterPlan{
		SelectedSkills: selected,
		Routes:         []ResearchRoute{{Layers: []ResearchRouteLayer{{Stages: []ResearchRouteStage{{StageID: "method_selection", SkillNames: stageSkills}}}}}},
	}
	if _, err := validatedResearchSkillsForRoute(plan, plan.Routes[0], loaded); err == nil || !strings.Contains(err.Error(), "核心 Skill 过多") {
		t.Fatalf("fifth core Skill error = %v", err)
	}
}

func decodeSchemaForTest(t *testing.T, schema json.RawMessage) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(schema, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func referenceTemplateRegistry(t *testing.T) *tool.MemoryRegistry {
	t.Helper()
	registry := tool.NewRegistry()
	for _, name := range []string{"builtin.web.search", "builtin.web.open", "builtin.browser.open", "builtin.mcp.list", "builtin.tools.search"} {
		if err := registry.Register(context.Background(), fixtureTool{definition: tool.Definition{QualifiedName: name, Version: "1", Description: "Web tool fixture", Risk: tool.RiskModerate, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	definitions := []tool.Definition{
		fullTextReadFixtureDefinition(),
		{QualifiedName: "builtin.resource.open", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["actionId"],"properties":{"actionId":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.resource.search", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["actionId","query"],"properties":{"actionId":{"type":"string"},"query":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.workspace.list", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.workspace.read_text", Version: "1", Risk: tool.RiskLow, Idempotent: true, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead}}, InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.knowledge.search", Version: "3", Risk: tool.RiskLow, Idempotent: true, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead}}, InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"documentIds":{"type":"array"},"limit":{"type":"integer"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"matches":{"type":"array"}}}`)},
		{QualifiedName: "builtin.skill.load", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.skill.resource.list", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.skill.resource.read_text", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.research.workflow.search", Version: "2", Risk: tool.RiskModerate, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"},"queries":{"type":"array"},"referencesOnly":{"type":"boolean"},"limit":{"type":"integer"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"queryText":{"type":"string"},"candidates":{"type":"array"},"partial":{"type":"boolean"}}}`)},
		{QualifiedName: "builtin.research.workflow.import", Version: "5", Risk: tool.RiskHigh, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["selectedCandidateIds"],"properties":{"selectedCandidateIds":{"type":"array"},"selectedAttachmentIds":{"type":"array"},"mode":{"type":"string"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"attachmentIds":{"type":"array"}}}`)},
		{QualifiedName: "builtin.research.workflow.sync", Version: "1", Risk: tool.RiskModerate, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["attachmentIds"],"properties":{"attachmentIds":{"type":"array"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"documentIds":{"type":"array"}}}`)},
		{QualifiedName: "builtin.research.workflow.python.ensure", Version: "2", Risk: tool.RiskHigh, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","properties":{"context":{}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"environmentFingerprint":{"type":"string"}}}`)},
		{QualifiedName: "builtin.research.workflow.python.prepare", Version: "1", Risk: tool.RiskHigh, Idempotent: false, InputSchema: json.RawMessage(`{"type":"object","required":["packages"],"properties":{"packages":{"type":"array"}}}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"environmentFingerprint":{"type":"string"}}}`)},
		{QualifiedName: "builtin.python.kernel.execute", Version: "1", Risk: tool.RiskHigh, Idempotent: false, InputSchema: json.RawMessage(`{"type":"object","required":["code"],"properties":{"code":{"type":"string"},"inputPaths":{"type":"array"},"inputData":{},"expectedEnvironmentFingerprint":{"type":"string"},"outputPaths":{"type":"array"},"timeoutSeconds":{"type":"integer"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{QualifiedName: "builtin.research.workflow.review.gate", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","required":["subject","review"],"properties":{"subject":{},"review":{"type":"object"}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	for _, definition := range definitions {
		definition = withProviderQueryFixture(definition)
		definition.Description = "fixture"
		if err := registry.Register(context.Background(), fixtureTool{definition: definition}); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func TestReferenceTemplatesKeepHumanInterventionToEvidenceCheckpoints(t *testing.T) {
	for _, template := range ReferenceTemplates() {
		humanKinds := make([]NodeKind, 0, 2)
		for _, node := range template.Definition.Nodes {
			switch node.Kind {
			case NodeHumanConfirmation, NodeCandidateSelection, NodeCitationSelection:
				humanKinds = append(humanKinds, node.Kind)
			}
			if node.Kind == NodeAgentStage && node.ReviewPolicy != AIReviewAuto {
				t.Fatalf("template %s requires routine review for Agent Stage %s", template.ID, node.ID)
			}
		}
		switch template.ID {
		case "trusted-research-closure":
			want := []NodeKind{NodeCandidateSelection, NodeCitationSelection}
			if !slices.Equal(humanKinds, want) {
				t.Fatalf("trusted closure checkpoints = %#v, want %#v", humanKinds, want)
			}
		default:
			if len(humanKinds) != 0 {
				t.Fatalf("autonomous template %s contains routine human checkpoints: %#v", template.ID, humanKinds)
			}
		}
	}
}

func TestReferenceTemplatesRequireIndependentReviewAndDeliveryGate(t *testing.T) {
	for _, template := range ReferenceTemplates() {
		hasReview, hasGate := false, false
		for _, node := range template.Definition.Nodes {
			hasReview = hasReview || node.Kind == NodeAIAnalysis && strings.Contains(node.ID, "review")
			hasGate = hasGate || node.ToolName == "builtin.research.workflow.review.gate"
		}
		if !hasReview || !hasGate {
			t.Fatalf("template %s review=%t gate=%t", template.ID, hasReview, hasGate)
		}
	}
}

func hasFrozenTool(values []ToolSnapshot, name string) bool {
	for _, value := range values {
		if value.QualifiedName == name {
			return true
		}
	}
	return false
}

func TestXLSXAnalysisTemplateBindsReadOnlyInputAndDeclaresReproducibleOutputs(t *testing.T) {
	var selected *Template
	for _, value := range ReferenceTemplates() {
		if value.ID == "xlsx-descriptive-analysis" {
			copy := value
			selected = &copy
			break
		}
	}
	if selected == nil {
		t.Fatal("XLSX analysis template is missing")
	}
	if len(selected.Definition.Inputs) != 2 || selected.Definition.Inputs[0].Name != "input_paths" || selected.Definition.Inputs[0].Type != TypeArray || selected.Definition.Inputs[0].FileKind != "xlsx" || selected.Definition.Inputs[0].MinItems != 1 || selected.Definition.Inputs[0].MaxItems != 1 || !selected.Definition.Inputs[0].Required || selected.Definition.Inputs[1].Name != "research_goal" || selected.Definition.Inputs[1].Type != TypeString || !selected.Definition.Inputs[1].Required {
		t.Fatalf("XLSX template input = %#v", selected.Definition.Inputs)
	}
	var analysis Node
	for _, node := range selected.Definition.Nodes {
		if node.ID == "analysis" {
			analysis = node
		}
	}
	var arguments struct {
		Code        string   `json:"code"`
		OutputPaths []string `json:"outputPaths"`
	}
	if json.Unmarshal(analysis.Arguments, &arguments) != nil || !strings.Contains(arguments.Code, "zipfile.ZipFile") || len(arguments.OutputPaths) != 4 {
		t.Fatalf("XLSX analysis arguments = %#v", analysis.Arguments)
	}
	if !slices.Contains(arguments.OutputPaths, "analysis-output/xlsx-{{runId}}-{{attempt}}-analysis.py") {
		t.Fatalf("XLSX reproducible script output is missing: %#v", arguments.OutputPaths)
	}
	foundInputEdge := false
	for _, edge := range selected.Definition.Edges {
		foundInputEdge = foundInputEdge || edge.FromNode == "$input" && edge.FromPort == "input_paths" && edge.ToNode == "analysis" && edge.ToPort == "inputPaths"
	}
	if !foundInputEdge {
		t.Fatal("XLSX input is not bound to the Kernel inputPaths contract")
	}
}

func TestReferenceTemplatesReturnIndependentDefinitions(t *testing.T) {
	first := ReferenceTemplates()
	first[0].Definition.Name = "changed"
	first[0].Definition.Nodes[0].Name = "changed"
	second := ReferenceTemplates()
	if second[0].Definition.Name == "changed" || second[0].Definition.Nodes[0].Name == "changed" {
		t.Fatal("reference templates share mutable state")
	}
}

func TestDelimitedAnalysisTemplateUsesStructuredGoalAndDeclaresReplayEvidence(t *testing.T) {
	var selected *Template
	for _, value := range ReferenceTemplates() {
		if value.ID == "python-analysis" {
			copy := value
			selected = &copy
			break
		}
	}
	if selected == nil || selected.Name != "数据探索与可复现分析" {
		t.Fatalf("delimited analysis template = %#v", selected)
	}
	if len(selected.Definition.Inputs) != 2 || selected.Definition.Inputs[0].Name != "input_paths" || selected.Definition.Inputs[0].FileKind != "delimited" || selected.Definition.Inputs[0].MinItems != 1 || selected.Definition.Inputs[0].MaxItems != 1 || selected.Definition.Inputs[1].Name != "analysis_request" || selected.Definition.Inputs[1].Control != "analysis_request" {
		t.Fatalf("delimited inputs = %#v", selected.Definition.Inputs)
	}
	var analysis Node
	for _, node := range selected.Definition.Nodes {
		if node.ID == "analysis" {
			analysis = node
		}
	}
	var arguments struct {
		Code        string   `json:"code"`
		OutputPaths []string `json:"outputPaths"`
	}
	if json.Unmarshal(analysis.Arguments, &arguments) != nil || !strings.Contains(arguments.Code, "SCIAIDE_DATA") || !strings.Contains(arguments.Code, "MAX_ROWS = 200000") || len(arguments.OutputPaths) != 5 {
		t.Fatalf("delimited analysis arguments = %#v", analysis.Arguments)
	}
	if !slices.Contains(arguments.OutputPaths, "analysis-output/data-{{runId}}-{{attempt}}-analysis.py") || !slices.Contains(arguments.OutputPaths, "analysis-output/data-{{runId}}-{{attempt}}-methods.md") {
		t.Fatalf("delimited replay outputs = %#v", arguments.OutputPaths)
	}
	foundData := false
	for _, edge := range selected.Definition.Edges {
		foundData = foundData || edge.FromNode == "$input" && edge.FromPort == "analysis_request" && edge.ToNode == "analysis" && edge.ToPort == "inputData"
	}
	if !foundData {
		t.Fatal("analysis request is not bound as structured Kernel input")
	}
}

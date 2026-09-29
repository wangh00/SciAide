package workflow

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/skillrun"
)

var researchRouteIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func dynamicResearchStageCatalog() []ResearchStage {
	return []ResearchStage{
		{StageID: "question_refinement", Name: "明确研究边界", Purpose: "冻结研究问题、目标、范围、假设状态与交付边界", Provides: []string{"research_question"}},
		{StageID: "literature_discovery", Name: "发现相关文献", Purpose: "检索公共科研数据库并保留来源记录", Requires: []string{"research_question"}, Provides: []string{"literature_candidates"}},
		{StageID: "candidate_review", Name: "确认文献材料", Purpose: "由用户确认进入本地证据链的候选材料", Requires: []string{"literature_candidates"}, Provides: []string{"selected_materials"}},
		{StageID: "evidence_extraction", Name: "提取可信证据", Purpose: "导入、索引、检索并人工确认可引用证据", Requires: []string{"selected_materials"}, Provides: []string{"verified_evidence"}},
		{StageID: "method_selection", Name: "综合方法 Skill", Purpose: "实际加载并综合适用的多个科研 Skill，形成方法蓝图", Requires: []string{"research_question"}, Provides: []string{"method_blueprint"}},
		{StageID: "research_design", Name: "形成研究设计", Purpose: "把方法蓝图转化为采样、变量、材料、质量和伦理计划", Requires: []string{"method_blueprint"}, Provides: []string{"research_design"}},
		{StageID: "data_preflight", Name: "检查研究数据", Purpose: "冻结表格输入并检查字段、质量、适用性与方法前提", Requires: []string{"research_question"}, Provides: []string{"frozen_tabular_data"}, ResourceKind: "tabular"},
		{StageID: "method_implementation", Name: "实现分析方法", Purpose: "依据方法 Skill 与数据预检生成结构化分析规范和受审查的 Python 实现", Requires: []string{"method_blueprint", "frozen_tabular_data"}, Provides: []string{"analysis_specification", "analysis_code"}},
		{StageID: "dependency_preparation", Name: "准备分析依赖", Purpose: "在项目 Python 环境内记录并安装方法实现声明的依赖", Requires: []string{"analysis_specification"}, Provides: []string{"frozen_python_environment"}},
		{StageID: "python_analysis", Name: "执行可复现分析", Purpose: "对冻结输入执行已确认的代码，记录环境、输入、代码和输出哈希", Requires: []string{"analysis_code", "frozen_python_environment"}, Provides: []string{"computed_results", "analysis_artifacts"}},
		{StageID: "result_interpretation", Name: "解释结果与局限", Purpose: "结合方法 Skill 区分计算事实、推断、替代解释与限制", Requires: []string{"computed_results"}, Provides: []string{"interpreted_results"}},
		{StageID: "report_drafting", Name: "形成交付稿", Purpose: "把冻结的设计、证据或计算结果组织为待审查交付稿", Provides: []string{"delivery_draft"}},
		{StageID: "independent_review", Name: "独立二次审查", Purpose: "独立检查主张、数字、引用、方法和结论强度", Provides: []string{"independent_review"}},
		{StageID: "delivery_gate", Name: "核验交付条件", Purpose: "由宿主确定性验证审查哈希和未解决问题", Requires: []string{"independent_review"}, Provides: []string{"delivery_approval"}},
		{StageID: "report_publication", Name: "发布可信报告", Purpose: "复核引用和产物快照后发布不可变报告", Requires: []string{"delivery_approval", "verified_evidence", "delivery_draft"}, Provides: []string{"trusted_report"}},
	}
}

func validRouteID(value string) bool {
	return researchRouteIDPattern.MatchString(strings.TrimSpace(value))
}

func researchStageCatalogEqual(left, right []ResearchStage) bool {
	return reflect.DeepEqual(left, right)
}

// materializeSemanticResearchRoute compiles the model's semantic stage
// selection into the host-owned executable route. Dependencies are expanded
// deterministically, then the trusted catalog supplies order, ports and
// checkpoints. A legacy route is first converted to semantic plans so old
// frozen Runs remain readable without making the planner depend on layers.
func materializeSemanticResearchRoute(route ResearchRoute, contexts ...ResearchStarterContext) (ResearchRoute, error) {
	semantic := len(route.StagePlans) > 0
	if len(route.StagePlans) == 0 {
		if len(route.Layers) > 0 {
			if err := validateLegacyRoutePresentation(route); err != nil {
				return route, err
			}
		}
		legacy := legacyStagePlans(route)
		if len(legacy) == 0 {
			return route, routeError("研究路线没有语义阶段计划")
		}
		route.StagePlans = legacy
	}

	catalog := dynamicResearchStageCatalog()
	byID := make(map[string]ResearchStage, len(catalog))
	for _, stage := range catalog {
		byID[stage.StageID] = stage
	}
	plans := make(map[string]ResearchStagePlan, len(route.StagePlans))
	for _, plan := range route.StagePlans {
		plan.StageID = strings.TrimSpace(plan.StageID)
		plan.Objective = strings.TrimSpace(plan.Objective)
		if _, ok := byID[plan.StageID]; !ok {
			return route, routeError("研究路线包含未知阶段 " + plan.StageID)
		}
		if plan.Objective == "" {
			return route, routeError("研究路线阶段缺少目标 " + plan.StageID)
		}
		if _, duplicate := plans[plan.StageID]; duplicate {
			return route, routeError("研究路线包含重复阶段 " + plan.StageID)
		}
		plans[plan.StageID] = plan
	}
	// New planners select the substantive work. The mandatory execution and
	// review spine is owned by the host, as promised by the semantic contract.
	// Historical presentations remain strict rather than silently rewritten.
	if semantic {
		for _, id := range []string{"question_refinement", "method_selection", "independent_review", "delivery_gate"} {
			if _, present := plans[id]; !present {
				plans[id] = ResearchStagePlan{StageID: id, Objective: byID[id].Purpose}
			}
		}
	}
	// Preserve the selected computational intent. Any analysis stage expands
	// the full input/preflight/implementation chain below; missing data blocks
	// execution rather than silently turning empirical work into a design.
	if _, ok := plans["question_refinement"]; !ok {
		return route, routeError("研究路线必须包含明确研究边界")
	}
	if _, ok := plans["method_selection"]; !ok {
		return route, routeError("研究路线必须包含方法综合阶段")
	}
	if _, ok := plans["independent_review"]; !ok {
		return route, routeError("研究路线必须包含独立二次审查")
	}
	if _, ok := plans["delivery_gate"]; !ok {
		return route, routeError("研究路线必须包含交付核验")
	}

	// Any selected member of a coupled chain promotes the complete chain. This
	// keeps execution inputs satisfiable while allowing the model to choose
	// between evidence, design and empirical branches.
	ensureChain := func(ids ...string) {
		for _, id := range ids {
			if _, selected := plans[id]; selected {
				for _, chainID := range ids {
					if _, exists := plans[chainID]; !exists {
						stage := byID[chainID]
						plans[chainID] = ResearchStagePlan{StageID: chainID, Objective: stage.Name}
					}
				}
				return
			}
		}
	}
	// Explicit local references satisfy the material source prerequisite. Do
	// not invent public discovery when the planner selected extraction only.
	evidenceChain := []string{"literature_discovery", "candidate_review", "evidence_extraction"}
	if len(contexts) > 0 && len(contexts[0].SelectedMaterials) > 0 {
		if _, discoveryRequested := plans["literature_discovery"]; !discoveryRequested {
			evidenceChain = []string{"candidate_review", "evidence_extraction"}
		}
	}
	ensureChain(evidenceChain...)
	ensureChain("data_preflight", "method_implementation", "dependency_preparation", "python_analysis", "result_interpretation")
	if _, selected := plans["report_publication"]; selected {
		for _, id := range []string{"report_drafting", "evidence_extraction"} {
			if _, exists := plans[id]; !exists {
				stage := byID[id]
				plans[id] = ResearchStagePlan{StageID: id, Objective: stage.Name}
			}
		}
		ensureChain(evidenceChain...)
	}
	if _, selected := plans["report_drafting"]; selected {
		// A report without design, computed results or a complete evidence chain
		// is rejected by the route validator; add the evidence chain only when
		// the model explicitly selected a report-only path.
		if _, design := plans["research_design"]; !design {
			if _, results := plans["result_interpretation"]; !results {
				ensureChain(evidenceChain...)
			}
		}
	}

	orderedIDs := make([]string, 0, len(plans))
	orderedStages := make([]ResearchRouteStage, 0, len(plans))
	for _, stage := range catalog {
		plan, included := plans[stage.StageID]
		if !included {
			continue
		}
		orderedIDs = append(orderedIDs, stage.StageID)
		objective := strings.TrimSpace(plan.Objective)
		if objective == "" {
			objective = stage.Name
		}
		checkpoint := stage.StageID == "candidate_review" || stage.StageID == "evidence_extraction"
		inputs := append([]string{}, stage.Requires...)
		if stage.StageID == "candidate_review" && len(evidenceChain) == 2 {
			inputs = []string{"selected_materials"}
			checkpoint = false
		}
		orderedStages = append(orderedStages, ResearchRouteStage{
			StageID: stage.StageID, Objective: objective,
			Methods: append([]string{}, plan.Methods...), SkillNames: append([]string{}, plan.SkillNames...),
			Inputs: inputs, Outputs: append([]string{}, stage.Provides...), HumanCheckpoint: checkpoint,
		})
	}
	if len(orderedIDs) == 0 {
		return route, routeError("研究路线没有可执行阶段")
	}
	route.StageIDs = orderedIDs
	route.Layers = materializeResearchRouteLayers(orderedStages)
	if len(evidenceChain) == 2 {
		for index := range route.Layers {
			if route.Layers[index].LayerID == "evidence" {
				route.Layers[index].Objective = "确认指定的本地材料，并提取可核验的研究证据。"
			}
		}
	}
	if len(route.ReviewCheckpoints) == 0 {
		route.ReviewCheckpoints = []string{"独立二次审查", "交付条件核验"}
	}
	return route, nil
}

func validateLegacyRoutePresentation(route ResearchRoute) error {
	if len(route.Layers) < 3 || len(route.Layers) > len(dynamicResearchStageCatalog()) {
		return routeError("历史研究路线的展示层数量无效")
	}
	declared := make(map[string]bool, len(route.StageIDs))
	for _, id := range route.StageIDs {
		if declared[id] {
			return routeError("历史研究路线包含重复阶段")
		}
		declared[id] = true
	}
	flattened := make([]string, 0, len(route.StageIDs))
	seen := map[string]bool{}
	for _, layer := range route.Layers {
		if !validRouteID(layer.LayerID) || strings.TrimSpace(layer.Title) == "" || strings.TrimSpace(layer.Objective) == "" || len(layer.Stages) == 0 {
			return routeError("历史研究路线包含无效展示层")
		}
		for _, stage := range layer.Stages {
			if !declared[stage.StageID] || seen[stage.StageID] || strings.TrimSpace(stage.Objective) == "" || len(stage.Outputs) == 0 {
				return routeError("历史研究路线包含无效展示阶段")
			}
			seen[stage.StageID] = true
			flattened = append(flattened, stage.StageID)
		}
	}
	if !reflect.DeepEqual(flattened, route.StageIDs) {
		return routeError("历史研究路线的展示阶段与执行顺序不一致")
	}
	return nil
}

func legacyStagePlans(route ResearchRoute) []ResearchStagePlan {
	descriptions := map[string]ResearchStagePlan{}
	for _, layer := range route.Layers {
		for _, stage := range layer.Stages {
			descriptions[stage.StageID] = ResearchStagePlan{StageID: stage.StageID, Objective: stage.Objective, Methods: append([]string(nil), stage.Methods...), SkillNames: append([]string(nil), stage.SkillNames...)}
		}
	}
	plans := make([]ResearchStagePlan, 0, len(route.StageIDs))
	if len(route.StageIDs) > 0 {
		for _, stageID := range route.StageIDs {
			if plan, ok := descriptions[stageID]; ok {
				plans = append(plans, plan)
			} else {
				// Preserve a missing historical description as invalid semantic
				// output; do not silently invent an objective during conversion.
				plans = append(plans, ResearchStagePlan{StageID: stageID})
			}
		}
		return plans
	}
	for _, plan := range descriptions {
		plans = append(plans, plan)
	}
	return plans
}

func materializeResearchRouteLayers(stages []ResearchRouteStage) []ResearchRouteLayer {
	groups := []struct {
		id, title, objective string
		ids                  []string
	}{
		{"question", "问题界定", "明确研究问题、范围与交付边界。", []string{"question_refinement"}},
		{"evidence", "证据基础", "发现、筛选并提取可核验的研究证据。", []string{"literature_discovery", "candidate_review", "evidence_extraction"}},
		{"method", "方法与设计", "综合科研方法并形成课题专属研究设计。", []string{"method_selection", "research_design"}},
		{"analysis", "数据与分析", "冻结数据，执行可复现分析并解释结果。", []string{"data_preflight", "method_implementation", "dependency_preparation", "python_analysis", "result_interpretation"}},
		{"delivery", "交付与核验", "形成交付稿并完成独立审查与交付核验。", []string{"report_drafting", "independent_review", "delivery_gate", "report_publication"}},
	}
	byID := make(map[string]ResearchRouteStage, len(stages))
	for _, stage := range stages {
		byID[stage.StageID] = stage
	}
	layers := make([]ResearchRouteLayer, 0, len(groups))
	for _, group := range groups {
		selected := make([]ResearchRouteStage, 0, len(group.ids))
		for _, id := range group.ids {
			if stage, ok := byID[id]; ok {
				selected = append(selected, stage)
			}
		}
		if len(selected) > 0 {
			layers = append(layers, ResearchRouteLayer{LayerID: group.id, Title: group.title, Objective: group.objective, Stages: selected})
		}
	}
	return layers
}

// normalizePlannerRouteOrder repairs a planner-only presentation error when
// the route contains exactly one well-formed declaration for every stage but
// lists those declarations outside the host's trusted causal order. The
// original AI output remains immutable on the Run; callers use this normalized
// projection for validation, display and compilation.
//
// This deliberately refuses partial, duplicate or unknown stage declarations.
// Those remain invalid instead of being silently completed by the host.
func normalizePlannerRouteOrder(route ResearchRoute) (ResearchRoute, bool) {
	catalog := dynamicResearchStageCatalog()
	catalogIndex := make(map[string]int, len(catalog))
	for index, stage := range catalog {
		catalogIndex[stage.StageID] = index
	}
	// Providers occasionally split the presentation into one layer per stage.
	// Accept that bounded intermediate shape here; the host folds it back into
	// the canonical 3-5 layer projection before the strict route validator runs.
	if len(route.StageIDs) < 5 || len(route.StageIDs) > len(catalog) || len(route.Layers) < 3 || len(route.Layers) > len(catalog) {
		return route, false
	}

	declared := make(map[string]bool, len(route.StageIDs))
	for _, stageID := range route.StageIDs {
		if _, trusted := catalogIndex[stageID]; !trusted || declared[stageID] {
			return route, false
		}
		declared[stageID] = true
	}

	stages := make(map[string]ResearchRouteStage, len(route.StageIDs))
	seenLayers := map[string]bool{}
	flattened := make([]string, 0, len(route.StageIDs))
	for _, layer := range route.Layers {
		if !validRouteID(layer.LayerID) || seenLayers[layer.LayerID] || strings.TrimSpace(layer.Title) == "" || strings.TrimSpace(layer.Objective) == "" || len(layer.Stages) == 0 {
			return route, false
		}
		seenLayers[layer.LayerID] = true
		for _, stage := range layer.Stages {
			if !declared[stage.StageID] || stages[stage.StageID].StageID != "" || strings.TrimSpace(stage.Objective) == "" || len(stage.Outputs) == 0 {
				return route, false
			}
			stages[stage.StageID] = stage
			flattened = append(flattened, stage.StageID)
		}
	}
	if len(stages) != len(route.StageIDs) {
		return route, false
	}

	expected := make([]string, 0, len(route.StageIDs))
	for _, stage := range catalog {
		if declared[stage.StageID] {
			expected = append(expected, stage.StageID)
		}
	}
	if len(route.Layers) <= 5 && reflect.DeepEqual(route.StageIDs, expected) && reflect.DeepEqual(flattened, expected) {
		return route, false
	}

	type layerSpec struct {
		id, title, objective string
		stageIDs             []string
	}
	specs := []layerSpec{
		{id: "question", title: "问题界定", objective: "明确并冻结研究问题、范围与交付边界。", stageIDs: []string{"question_refinement"}},
		{id: "evidence", title: "证据基础", objective: "发现、筛选并提取可核验的研究证据。", stageIDs: []string{"literature_discovery", "candidate_review", "evidence_extraction"}},
		{id: "method", title: "方法与设计", objective: "综合科研方法并形成与课题匹配的研究设计。", stageIDs: []string{"method_selection", "research_design"}},
		{id: "analysis", title: "数据与分析", objective: "冻结研究数据，执行可复现分析并解释结果。", stageIDs: []string{"data_preflight", "method_implementation", "dependency_preparation", "python_analysis", "result_interpretation"}},
		{id: "delivery", title: "交付与核验", objective: "形成交付稿并完成独立审查与确定性核验。", stageIDs: []string{"report_drafting", "independent_review", "delivery_gate", "report_publication"}},
	}
	normalizedLayers := make([]ResearchRouteLayer, 0, len(specs))
	for _, spec := range specs {
		layerStages := make([]ResearchRouteStage, 0, len(spec.stageIDs))
		for _, stageID := range spec.stageIDs {
			if stage, included := stages[stageID]; included {
				layerStages = append(layerStages, stage)
			}
		}
		if len(layerStages) > 0 {
			normalizedLayers = append(normalizedLayers, ResearchRouteLayer{LayerID: spec.id, Title: spec.title, Objective: spec.objective, Stages: layerStages})
		}
	}
	if len(normalizedLayers) < 3 || len(normalizedLayers) > 5 {
		return route, false
	}
	route.StageIDs = expected
	route.Layers = normalizedLayers
	return route, true
}

func routeNeedsTabular(route ResearchRoute) bool {
	needsData := func(id string) bool {
		switch id {
		case "data_preflight", "method_implementation", "dependency_preparation", "python_analysis", "result_interpretation":
			return true
		}
		return false
	}
	for _, stageID := range route.StageIDs {
		if needsData(stageID) {
			return true
		}
	}
	if len(route.StageIDs) == 0 {
		for _, plan := range route.StagePlans {
			if needsData(plan.StageID) {
				return true
			}
		}
	}
	return false
}

func validateDynamicResearchRoute(route ResearchRoute, context ResearchStarterContext) (bool, []string, error) {
	if len(route.StageIDs) < 5 || len(route.StageIDs) > len(dynamicResearchStageCatalog()) {
		return false, nil, routeError("动态研究路线的阶段数量无效")
	}
	catalog := map[string]ResearchStage{}
	for _, stage := range dynamicResearchStageCatalog() {
		catalog[stage.StageID] = stage
	}
	positions := map[string]int{}
	for index, stageID := range route.StageIDs {
		if _, ok := catalog[stageID]; !ok {
			return false, nil, routeError("动态研究路线包含不受信任的阶段")
		}
		if _, duplicate := positions[stageID]; duplicate {
			return false, nil, routeError("动态研究路线包含重复阶段")
		}
		positions[stageID] = index
	}
	if route.StageIDs[0] != "question_refinement" {
		return false, nil, routeError("动态研究路线必须从明确研究边界开始")
	}
	evidenceChain := []string{"literature_discovery", "candidate_review", "evidence_extraction"}
	_, hasDiscovery := positions["literature_discovery"]
	localMaterialsOnly := len(context.SelectedMaterials) > 0 && !hasDiscovery
	if localMaterialsOnly {
		evidenceChain = []string{"candidate_review", "evidence_extraction"}
	}
	for _, group := range [][]string{
		evidenceChain,
		{"data_preflight", "method_implementation", "dependency_preparation", "python_analysis", "result_interpretation"},
	} {
		included := 0
		for _, stageID := range group {
			if _, ok := positions[stageID]; ok {
				included++
			}
		}
		if included != 0 && included != len(group) {
			return false, nil, routeError("动态研究路线包含不完整的可信阶段链")
		}
	}
	expectedOrder := make([]string, 0, len(route.StageIDs))
	for _, stage := range dynamicResearchStageCatalog() {
		if _, included := positions[stage.StageID]; included {
			expectedOrder = append(expectedOrder, stage.StageID)
		}
	}
	if !reflect.DeepEqual(route.StageIDs, expectedOrder) {
		return false, nil, routeError("动态研究路线的阶段顺序与可信因果顺序不一致")
	}
	for _, required := range []string{"method_selection", "independent_review", "delivery_gate"} {
		if _, ok := positions[required]; !ok {
			return false, nil, routeError("动态研究路线缺少必要阶段 " + required)
		}
	}
	dependencies := map[string][]string{
		"candidate_review":       {"literature_discovery"},
		"evidence_extraction":    {"candidate_review"},
		"research_design":        {"method_selection"},
		"method_implementation":  {"method_selection", "data_preflight"},
		"dependency_preparation": {"method_implementation"},
		"python_analysis":        {"method_implementation", "dependency_preparation", "data_preflight"},
		"result_interpretation":  {"python_analysis"},
		"delivery_gate":          {"independent_review"},
		"report_publication":     {"report_drafting", "independent_review", "delivery_gate", "evidence_extraction"},
	}
	if localMaterialsOnly {
		dependencies["candidate_review"] = []string{"question_refinement"}
	}
	for stageID, required := range dependencies {
		stagePosition, included := positions[stageID]
		if !included {
			continue
		}
		for _, dependency := range required {
			dependencyPosition, ok := positions[dependency]
			if !ok || dependencyPosition >= stagePosition {
				return false, nil, routeError("动态研究路线的阶段依赖或顺序无效")
			}
		}
	}
	if positions["independent_review"] >= positions["delivery_gate"] {
		return false, nil, routeError("独立二次审查必须发生在交付门禁之前")
	}
	_, hasDesign := positions["research_design"]
	_, hasResults := positions["result_interpretation"]
	_, hasDraft := positions["report_drafting"]
	// A valid route must have a substantive deliverable. Evidence-only
	// research is a legitimate path, but a draft in that path is meaningful
	// only when it is backed by the complete evidence extraction chain.
	if !hasDesign && !hasResults && !hasDraft {
		return false, nil, routeError("动态研究路线必须形成研究设计、经过 Python 计算的结果或证据交付稿")
	}
	if hasDraft && !hasDesign && !hasResults {
		if _, hasEvidence := positions["evidence_extraction"]; !hasEvidence && len(context.SelectedMaterials) == 0 {
			return false, nil, routeError("仅有交付稿的动态研究路线必须包含证据提取阶段")
		}
	}
	if len(route.ReviewCheckpoints) == 0 {
		return false, nil, routeError("动态研究路线缺少独立审查检查点")
	}
	if err := validateDynamicRouteLayers(route, positions); err != nil {
		return false, nil, err
	}
	if _, needsData := positions["data_preflight"]; needsData {
		// The snapshot contains attachment display names, not staged task paths.
		// Even a relevant shared attachment must be explicitly selected before
		// StartAdoptedResearchRoute can freeze its bytes under this task owner.
		return false, []string{"请为本任务选择并确认与课题相关的 CSV、TSV 或 XLSX 研究数据"}, nil
	}
	return true, []string{}, nil
}

func validateDynamicRouteLayers(route ResearchRoute, positions map[string]int) error {
	if len(route.Layers) < 3 || len(route.Layers) > 5 {
		return routeError("动态研究路线必须按 3 至 5 个层次组织")
	}
	flattened := make([]string, 0, len(route.StageIDs))
	seenLayers, seenStages := map[string]bool{}, map[string]bool{}
	for _, layer := range route.Layers {
		if !validRouteID(layer.LayerID) || seenLayers[layer.LayerID] || strings.TrimSpace(layer.Title) == "" || strings.TrimSpace(layer.Objective) == "" || len(layer.Stages) == 0 {
			return routeError("动态研究路线包含无效或重复的层次")
		}
		seenLayers[layer.LayerID] = true
		for _, stage := range layer.Stages {
			if _, ok := positions[stage.StageID]; !ok || seenStages[stage.StageID] || strings.TrimSpace(stage.Objective) == "" || len(stage.Outputs) == 0 {
				return routeError("动态研究路线层次包含无效或重复阶段")
			}
			seenStages[stage.StageID] = true
			flattened = append(flattened, stage.StageID)
		}
	}
	if !reflect.DeepEqual(flattened, route.StageIDs) {
		return routeError("动态研究路线的分层阶段与执行顺序不一致")
	}
	return nil
}

// validateRouteSkillLimit applies the Skill cap to one candidate route. The
// planner may load a broader catalog snapshot than any single candidate uses;
// that unrelated context must not make an otherwise valid route unavailable.
func validateRouteSkillLimit(referencedCount int) error {
	if referencedCount > maxResearchStarterSelectedSkills {
		return researchSkillLimitError()
	}
	return nil
}

func researchSkillLimitError() error {
	return routeError("研究路线加载的核心 Skill 过多；请只保留对当前课题不可替代且互补的核心方法")
}

// validatedResearchSkillsForRoute validates only the Skill declarations that
// a candidate route actually uses. Planner output may contain several routes;
// an unrelated malformed or unused Skill declaration must not make every
// otherwise valid route unusable. The returned values carry the immutable
// content/package hashes needed by formal Workflow stages.
func validatedResearchSkillsForRoute(plan ResearchStarterPlan, route ResearchRoute, loaded []skillrun.Snapshot) ([]ResearchSkillSelection, error) {
	if len(route.Layers) == 0 && len(route.StagePlans) > 0 {
		if materialized, err := materializeSemanticResearchRoute(route); err == nil {
			route = materialized
		}
	}
	referenced := map[string]map[string]bool{}
	for _, layer := range route.Layers {
		for _, stage := range layer.Stages {
			for _, rawName := range stage.SkillNames {
				name := strings.TrimSpace(rawName)
				if !validResearchSkillName(name) {
					return nil, routeError("研究路线引用了无效的方法 Skill 名称")
				}
				if referenced[name] == nil {
					referenced[name] = map[string]bool{}
				}
				referenced[name][stage.StageID] = true
			}
		}
	}
	if err := validateRouteSkillLimit(len(referenced)); err != nil {
		return nil, err
	}
	if len(referenced) == 0 {
		return []ResearchSkillSelection{}, nil
	}

	loadedByName := map[string]skillrun.Snapshot{}
	for _, snapshot := range loaded {
		if strings.TrimSpace(snapshot.Name) == "" {
			continue
		}
		if _, duplicate := loadedByName[snapshot.Name]; duplicate {
			if referenced[snapshot.Name] != nil {
				return nil, routeError(fmt.Sprintf("路线所需的 Skill %q 存在重复快照", snapshot.Name))
			}
			continue
		}
		loadedByName[snapshot.Name] = snapshot
	}

	selectedByName := map[string]ResearchSkillSelection{}
	selectedCounts := map[string]int{}
	for _, selection := range plan.SelectedSkills {
		name := strings.TrimSpace(selection.Name)
		if name == "" {
			continue
		}
		selectedCounts[name]++
		if selectedCounts[name] > 1 && referenced[name] != nil {
			return nil, routeError(fmt.Sprintf("路线所需的 Skill %q 被重复声明", name))
		}
		if _, exists := selectedByName[name]; !exists {
			selection.Name = name
			selectedByName[name] = selection
		}
	}

	result := make([]ResearchSkillSelection, 0, len(referenced))
	for name, stages := range referenced {
		selection, selected := selectedByName[name]
		if !validResearchSkillName(name) {
			return nil, routeError(fmt.Sprintf("研究路线引用了无效的科研 Skill 名称 %q", name))
		}
		snapshot, loaded := loadedByName[name]
		if !loaded {
			return nil, routeError(fmt.Sprintf("研究阶段引用了未实际加载的 Skill %q", name))
		}
		// The run snapshot is authoritative proof that a Skill was loaded. If
		// the planner omitted it from top-level selectedSkills, synthesize only
		// a minimal descriptive entry instead of invalidating every route.
		if !selected {
			selection = ResearchSkillSelection{Name: name, Role: "当前路线已实际加载的科研 Skill", Limitations: []string{}}
		}
		if strings.TrimSpace(selection.Role) == "" {
			selection.Role = "当前路线已实际加载的科研 Skill"
		}
		if strings.TrimSpace(snapshot.ContentHash) == "" || strings.TrimSpace(snapshot.PackageHash) == "" {
			return nil, routeError(fmt.Sprintf("路线所需的 Skill %q 缺少完整快照", name))
		}
		selection.ContentHash, selection.PackageHash = snapshot.ContentHash, snapshot.PackageHash
		selection.StageIDs = nil
		for _, stage := range dynamicResearchStageCatalog() {
			if stages[stage.StageID] {
				selection.StageIDs = append(selection.StageIDs, stage.StageID)
			}
		}
		result = append(result, selection)
	}
	// Stable ordering makes the frozen route inputs deterministic even though
	// the referenced Skill set is collected through a map.
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result, nil
}

func validResearchSkillName(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || index > 0 && (character == '-' || character == '_') {
			continue
		}
		return false
	}
	return true
}

func dynamicResearchStarterTemplate() Template {
	return Template{
		ID: "research-starter", Name: "AI 研究启动", Description: "从课题语义和项目资源出发，动态加载多个科研 Skill 并生成分层研究路线。",
		Definition: Definition{
			SchemaVersion: SchemaVersion, Name: "AI 研究启动", Description: "动态 Skill 路由、方法综合、分层阶段编排和宿主依赖校验。",
			Inputs: []Port{{Name: "starter_context", Type: TypeObject, Description: "研究课题、资源快照与可信阶段目录", Required: true}},
			Nodes: []Node{{
				ID: "explore", Name: "AI 综合 Skill 并设计研究路线", Kind: NodeAgentStage, Arguments: raw(`{}`),
				PromptVersion: "research-starter-semantic-v6", ReviewPolicy: AIReviewAuto, SkillRouting: true,
				AllowedTools: []string{
					"builtin.mcp.list", "builtin.tools.search",
					"builtin.resource.open", "builtin.resource.search",
					"builtin.attachment.list", "builtin.document.inspect", "builtin.document.read", "builtin.document.search",
					"builtin.knowledge.search", "builtin.research.catalog", "builtin.research.search", "builtin.research.fetch",
					"builtin.web.search", "builtin.web.open", "builtin.browser.open",
					"builtin.workspace.list", "builtin.workspace.read_text",
				},
				Prompt: `你是 SciAide 科研模式的动态研究架构师。先依据课题、交付目标、资源快照和 available_skills 元数据完成候选重排，再从 resource_actions 的真实候选中选择加载操作，通过 builtin.resource.open 加载 1 至 4 个对当前课题不可替代且彼此互补的核心科研 Skill。不要把召回候选全部加载，不要为了凑数加载近义、重复或仅弱相关的 Skill，也不要声称采用未实际加载的 Skill。长 Skill 返回章节目录时，只读取规划当前路线所需的章节并读完相关章节。读取资料时只选择宿主签发的 actionId；Skill 名称、附件 ID、目录路径、章节定位和分页位置由宿主解析，不提交自由定位参数。综合核心 Skill 的理论、方法、适用条件和限制，生成 1 至 3 条真正适合当前课题的语义路线。

selectedMaterials 是用户明确指定的参考材料清单，不是实验数据，也不代表已经读取或可以直接据此下结论。按需通过资源检索和宿主签发的读取操作了解内容；不得仅凭文件名推断发现。后续宿主会安排这些资料的确认、索引和证据评估；是否还需公共文献检索按实际研究目标决定，不因已有一两份资料宣称证据充分。

工具调用期间可以不输出说明文字，工具结果会由宿主记录。完成必要的工具调用后，必须在同一响应末尾提交一个完整、可解析且符合下方 Schema 的路线 JSON。即使资料不足，也要在 missingInformation、blockers 或 limitations 中说明缺口，不得只返回普通文本或半截 JSON。

只返回每条路线的语义阶段计划 stagePlans。stagePlans 只描述课题需要的可信 stageId、该阶段目标 objective、可选 methods 和实际负责的 skillNames。不要返回 stageIds、layers、阶段 inputs、outputs、humanCheckpoint、执行顺序或展示分组；这些由宿主依据可信阶段目录和阶段依赖自动编译。路线必须能够形成研究设计、经过 Python 计算的结果，或有完整证据链支撑的交付稿；宿主会补齐必要的基础阶段、排序、依赖、输入输出、人工检查点和 3 至 5 个展示层。没有相关表格数据时，选择 data_preflight 及其完整分析链仍可作为待补充数据的路线，requiredResources 或 blockers 要面向用户说明需要 CSV、TSV 或 XLSX。只列真正负责该阶段的已加载 Skill；selectedSkills 只列实际加载过的核心 Skill。最终代码块必须是字面量 JSON；禁止输出 .replace(...)、函数调用、注释、尾随逗号或任何 JavaScript/Python 表达式。研究设计不得冒充实证结果；缺数据时优先形成可执行设计和数据采集计划，而不是编造分析结论。 无数据时，在符合用户目标的前提下优先提供一条不执行分析的研究设计路线，但不要把用户明确要求的数据分析替换成研究设计。不同研究设计可以使用相同的阶段组合，区别应写在 objective、methods 和交付边界中。availableNow 仅表示本轮路线能否开始执行，不代表研究已完成或已获伦理许可。招募、量表授权、伦理审批和未来采集数据是研究设计中必须披露的后续条件，不是撰写方案的前置条件；未确定且会改变本轮研究方向的选择使用 clarification。用户只需简单描述，统计方法与路线应由你解释推荐，不要要求用户预先提供专业模型方案。先从宿主当前任务资源目录选择相关资料读取；目录为空时只在 requiredResources/blockers 记录待补文件，不探索其他目录。用户说已有数据但尚未上传时，直接生成待补数据的分析路线，先由用户采纳路线，再由宿主文件选择器上传并绑定文件，不能编造梯度、字段、采样时间强迫选择，也不能擅自把分析任务改成生成演示数据。已有数据可确定的组别、字段和时间应读取，不要再用假设选项覆盖；无法从文件确认的实验单位、分配方式及目标才向用户询问。clarification 只在信息不足且会改变研究方向、研究对象或交付目标时按需询问；信息充分时省略 clarification 或返回 needsUserInput=false、questions=[]，不得为了确认而问答。每题 kind 必须为 research_direction，impact 必须解释不同回答如何改变研究方向。文件尚未上传、何时上传、绑定数据、安装依赖不是研究方向问题，禁止作为 clarification 或“立即上传/稍后上传”选项；这些由采纳路线后的任务输入流程处理。字段、组别、时间等可从稍后上传的数据中读取的信息先留待数据预检，不要求用户重复选择。clarification 每轮优先只问 1 至 3 个真正影响研究方向的易懂问题，每个问题只针对一个维度；不可将梯度、指标、时间混成一道题。每题必须设置 selectionMode：互斥方案为 single，可同时成立的同类指标为 multiple。题干不要重复“单选/多选”，由界面显示；选项不得混合多个分组或相互矛盾的配置。已知信息不足以给出穷尽选项时，应包含“先读取我上传的数据再确认”或“暂不确定，请推荐并说明”的适当出口，而非假定用户属于某个方案。不要向用户展示 question_refinement、data_preflight 等内部节点名。用户确认后保留其明确选择，不重复问已回答问题。任何计算环节都需要完整数据链和用户显式选择任务输入，公共附件名称不能作为可直接读取的路径。`,
				OutputSchema: semanticResearchStarterSchema(),
			}},
			Edges:   []Edge{{FromNode: "$input", FromPort: "starter_context", ToNode: "explore", ToPort: "context"}},
			Outputs: []Output{{Name: "plan", Type: TypeObject, FromNode: "explore", FromPort: "analysis", Required: true, Description: "Skill 辅助、分层且经过宿主校验的动态研究路线"}},
		},
	}
}

// legacyDynamicResearchStarterSchema is retained only for decoding historical
// planner Runs. New planner calls use semanticResearchStarterSchema below.
func legacyDynamicResearchStarterSchema() json.RawMessage {
	stageIDs := make([]string, 0, len(dynamicResearchStageCatalog()))
	for _, stage := range dynamicResearchStageCatalog() {
		stageIDs = append(stageIDs, stage.StageID)
	}
	stageEnum, _ := json.Marshal(stageIDs)
	schema := fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["normalizedQuestion","researchType","availableResources","missingInformation","selectedSkills","routes","recommendedRouteId","recommendationReason","confidence","limitations"],"properties":{"normalizedQuestion":{"type":"string","minLength":1,"maxLength":4000},"researchType":{"type":"string","enum":["evidence","data","design","mixed","uncertain"]},"availableResources":{"type":"array","maxItems":40,"items":{"type":"string","maxLength":1000}},"missingInformation":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":1000}},"selectedSkills":{"type":"array","maxItems":12,"items":{"type":"object","additionalProperties":false,"required":["name","role","limitations"],"properties":{"name":{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$"},"role":{"type":"string","minLength":1,"maxLength":2000},"limitations":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":1000}}}}},"routes":{"type":"array","minItems":1,"maxItems":3,"items":{"type":"object","additionalProperties":false,"required":["routeId","title","reason","availableNow","requiredResources","deliverables","blockers","stageIds","reviewCheckpoints","layers"],"properties":{"routeId":{"type":"string","pattern":"^[a-z][a-z0-9_-]{0,63}$"},"title":{"type":"string","minLength":1,"maxLength":160},"reason":{"type":"string","minLength":1,"maxLength":4000},"availableNow":{"type":"boolean"},"requiredResources":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":1000}},"deliverables":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":1000}},"blockers":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":1000}},"stageIds":{"type":"array","minItems":5,"maxItems":%d,"uniqueItems":true,"items":{"type":"string","enum":%s}},"reviewCheckpoints":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"string","maxLength":1000}},"layers":{"type":"array","minItems":3,"maxItems":5,"items":{"type":"object","additionalProperties":false,"required":["layerId","title","objective","stages"],"properties":{"layerId":{"type":"string","pattern":"^[a-z][a-z0-9_-]{0,63}$"},"title":{"type":"string","minLength":1,"maxLength":120},"objective":{"type":"string","minLength":1,"maxLength":2000},"stages":{"type":"array","minItems":1,"maxItems":8,"items":{"type":"object","additionalProperties":false,"required":["stageId","objective","methods","skillNames","inputs","outputs","humanCheckpoint"],"properties":{"stageId":{"type":"string","enum":%s},"objective":{"type":"string","minLength":1,"maxLength":2000},"methods":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":1000}},"skillNames":{"type":"array","maxItems":12,"uniqueItems":true,"items":{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$"}},"inputs":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":1000}},"outputs":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"string","maxLength":1000}},"humanCheckpoint":{"type":"boolean"}}}}}}}}}},"recommendedRouteId":{"type":"string","pattern":"^[a-z][a-z0-9_-]{0,63}$"},"recommendationReason":{"type":"string","minLength":1,"maxLength":4000},"confidence":{"type":"string","enum":["low","medium","high"]},"limitations":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":1000}}}}`, len(stageIDs), stageEnum, stageEnum)
	schema = strings.Replace(schema, `"selectedSkills":{"type":"array","maxItems":12`, fmt.Sprintf(`"selectedSkills":{"type":"array","maxItems":%d`, maxResearchStarterSelectedSkills), 1)
	schema = strings.Replace(schema, `"skillNames":{"type":"array","maxItems":12`, fmt.Sprintf(`"skillNames":{"type":"array","maxItems":%d`, maxResearchStarterSelectedSkills), 1)
	var envelope map[string]any
	if json.Unmarshal([]byte(schema), &envelope) == nil {
		properties, _ := envelope["properties"].(map[string]any)
		if properties == nil {
			properties = map[string]any{}
			envelope["properties"] = properties
		}
		properties["clarification"] = map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"needsUserInput", "questions"},
			"properties": map[string]any{
				"needsUserInput": map[string]any{"type": "boolean"},
				"intro":          map[string]any{"type": "string", "maxLength": 1000},
				"questions": map[string]any{
					"type": "array", "maxItems": 12,
					"items": map[string]any{
						"type": "object", "additionalProperties": false,
						"required": []string{"id", "text", "required", "options"},
						"properties": map[string]any{
							"id":       map[string]any{"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_-]{0,63}$"},
							"text":     map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
							"required": map[string]any{"type": "boolean"},
							"impact":   map[string]any{"type": "string", "maxLength": 2000},
							"options": map[string]any{"type": "array", "minItems": 2, "maxItems": 8, "items": map[string]any{
								"type": "object", "additionalProperties": false, "required": []string{"id", "label"},
								"properties": map[string]any{
									"id":    map[string]any{"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_-]{0,63}$"},
									"label": map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
								},
							}},
						},
					},
				},
			},
		}
		if encoded, err := json.Marshal(envelope); err == nil {
			return json.RawMessage(encoded)
		}
	}
	return json.RawMessage(schema)
}

// dynamicResearchStarterSchema remains the historical schema name used by
// compatibility tests and old Run readers. It must not be used by new nodes.
func dynamicResearchStarterSchema() json.RawMessage { return legacyDynamicResearchStarterSchema() }

func semanticResearchStarterSchema() json.RawMessage {
	stageIDs := make([]string, 0, len(dynamicResearchStageCatalog()))
	for _, stage := range dynamicResearchStageCatalog() {
		stageIDs = append(stageIDs, stage.StageID)
	}
	stringArray := func(max int) map[string]any {
		return map[string]any{"type": "array", "maxItems": max, "items": map[string]any{"type": "string", "maxLength": 2000}}
	}
	stagePlan := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"stageId", "objective"},
		"properties": map[string]any{
			"stageId":    map[string]any{"type": "string", "enum": stageIDs},
			"objective":  map[string]any{"type": "string", "minLength": 1, "maxLength": 2000},
			"methods":    stringArray(20),
			"skillNames": map[string]any{"type": "array", "maxItems": maxResearchStarterSelectedSkills, "uniqueItems": true, "items": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$"}},
		},
	}
	route := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"routeId", "title", "reason", "availableNow", "requiredResources", "deliverables", "blockers", "stagePlans"},
		"properties": map[string]any{
			"routeId":           map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_-]{0,63}$"},
			"title":             map[string]any{"type": "string", "minLength": 1, "maxLength": 160},
			"reason":            map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
			"availableNow":      map[string]any{"type": "boolean"},
			"requiredResources": stringArray(30), "deliverables": stringArray(30), "blockers": stringArray(30),
			"reviewCheckpoints": stringArray(20),
			"stagePlans":        map[string]any{"type": "array", "minItems": 1, "maxItems": len(stageIDs), "uniqueItems": true, "items": stagePlan},
		},
	}
	clarification := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"needsUserInput", "questions"},
		"properties": map[string]any{
			"needsUserInput": map[string]any{"type": "boolean"}, "intro": map[string]any{"type": "string", "maxLength": 1000},
			"questions": map[string]any{"type": "array", "maxItems": 12, "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"id", "kind", "text", "impact", "required", "selectionMode", "options"},
				"properties": map[string]any{
					"kind":          map[string]any{"type": "string", "const": "research_direction", "description": "Only an unresolved decision that changes the research direction. Never use clarification for file upload, file binding, installation, or permission to proceed; these are handled after route adoption."},
					"selectionMode": map[string]any{"type": "string", "enum": []string{"single", "multiple"}}, "id": map[string]any{"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_-]{0,63}$"}, "text": map[string]any{"type": "string", "minLength": 1, "maxLength": 1000}, "required": map[string]any{"type": "boolean"}, "impact": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000, "description": "Explain how the answer changes the research direction, not merely whether execution resources are ready."},
					"options": map[string]any{"type": "array", "minItems": 2, "maxItems": 8, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "label"}, "properties": map[string]any{"id": map[string]any{"type": "string", "pattern": "^[A-Za-z][A-Za-z0-9_-]{0,63}$"}, "label": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}}}},
				},
			}},
		},
	}
	root := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"normalizedQuestion", "researchType", "availableResources", "missingInformation", "selectedSkills", "routes", "recommendedRouteId", "recommendationReason", "confidence", "limitations"},
		"properties": map[string]any{
			"normalizedQuestion": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000}, "researchType": map[string]any{"type": "string", "enum": []string{"evidence", "data", "design", "mixed", "uncertain"}}, "availableResources": stringArray(40), "missingInformation": stringArray(30),
			"selectedSkills": map[string]any{"type": "array", "maxItems": maxResearchStarterSelectedSkills, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name", "role", "limitations"}, "properties": map[string]any{"name": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$"}, "role": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000}, "limitations": stringArray(20)}}},
			"routes":         map[string]any{"type": "array", "minItems": 1, "maxItems": 3, "items": route}, "recommendedRouteId": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_-]{0,63}$"}, "recommendationReason": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000}, "confidence": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}}, "limitations": stringArray(30), "clarification": clarification,
		},
	}
	encoded, _ := json.Marshal(root)
	return json.RawMessage(encoded)
}

func researchDesignSchema() json.RawMessage {
	return raw(`{"type":"object","additionalProperties":false,"required":["title","researchQuestion","objectives","hypotheses","populationAndSampling","variablesOrMaterials","dataCollectionPlan","analysisPlan","qualityControls","ethicsAndRisks","milestones","evidenceGaps","limitations","status"],"properties":{"title":{"type":"string","minLength":1,"maxLength":240},"researchQuestion":{"type":"string","minLength":1,"maxLength":4000},"objectives":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"hypotheses":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":2000}},"populationAndSampling":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"variablesOrMaterials":{"type":"array","minItems":1,"maxItems":40,"items":{"type":"string","maxLength":2000}},"dataCollectionPlan":{"type":"array","minItems":1,"maxItems":40,"items":{"type":"string","maxLength":2000}},"analysisPlan":{"type":"array","minItems":1,"maxItems":40,"items":{"type":"string","maxLength":2000}},"qualityControls":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"ethicsAndRisks":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"milestones":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"string","maxLength":2000}},"evidenceGaps":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":2000}},"limitations":{"type":"array","maxItems":30,"items":{"type":"string","maxLength":2000}},"status":{"type":"string","const":"research_design_not_empirical_result"}}}`)
}

type routeError string

func (e routeError) Error() string { return string(e) }

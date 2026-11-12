package workflow

import "encoding/json"

// Describe actual execution semantics, not what a stage's friendly name seems
// to promise. This is frozen navigation for planning, not another output schema
// or a keyword-driven override of the model's scientific judgement.
type revisionTargetCapability struct {
	Kind        NodeKind `json:"kind"`
	Work        string   `json:"work"`
	Limits      string   `json:"limits"`
	LookupTools []string `json:"lookupTools,omitempty"`
	Available   int      `json:"availableReferences,omitempty"`
	Selected    int      `json:"selectedReferences,omitempty"`
	Recommended int      `json:"recommendedReferences,omitempty"`
	Missing     int      `json:"unselectedRecommendedReferences"`
	Adoption    string   `json:"newEvidenceAdoption,omitempty"`
	LocalSearch *int     `json:"localSearchRoundsRemaining,omitempty"`
}

func revisionTargetCapabilities(d RunDetail, targets []ResearchRevisionTarget) map[string]revisionTargetCapability {
	result := map[string]revisionTargetCapability{}
	nodes := compilationNodeMap(d.Run.Compilation)
	for _, target := range targets {
		node := nodes[target.NodeID]
		capability := revisionTargetCapability{Kind: node.Kind, Work: "重新执行当前阶段，更新该阶段的派生结果。", Limits: "不会改变冻结研究目标、原始材料或阶段授权工具。"}
		for _, tool := range node.AllowedTools {
			switch tool.QualifiedName {
			case "builtin.research.full_text.read", "builtin.document.inspect", "builtin.knowledge.read", "builtin.resource.open", "builtin.resource.search", "builtin.web.open", "builtin.web.search":
				capability.LookupTools = append(capability.LookupTools, tool.QualifiedName)
			}
		}
		switch {
		case node.Kind == NodeCitationSelection:
			capability.Work = "仅重新选择已接受综合中的引用；没有模型补查或核读操作。"
			capability.Limits = "不能下载全文、发现新证据、修正综合或更新已有证据的核读范围；不能通过重复确认相同引用弥补缺失字段。"
			capability.LookupTools = nil
			if availability := evidenceSelectionAvailability(d); availability != nil && availability.NodeID == node.ID {
				capability.Available, capability.Recommended, capability.Missing = availability.AvailableCount, availability.RecommendedCount, availability.MissingRecommendedCount
				if step := findStepByNode(d.Steps, node.ID); step != nil {
					var output struct {
						Citations []json.RawMessage `json:"citations"`
					}
					_ = json.Unmarshal(step.Output, &output)
					capability.Selected = len(output.Citations)
				}
			}
		case node.ID == "evidence_screening" && node.Kind == NodeAgentStage && node.PromptVersion == selectedEvidenceVersion:
			capability.Work = "对已入选来源核读并更新证据综合；只能使用本阶段实际授权工具。"
			capability.Limits = "不重跑候选发现，不保证全文可得；已有读取可复用，失败或不可得来源不能靠重复执行变成已核实。"
			_, used := supplementaryEvidenceQueries(d)
			remaining := max(0, 2-used)
			search, screening := findStepByNode(d.Steps, "evidence_search"), findStepByNode(d.Steps, node.ID)
			var settings struct {
				Limit int `json:"limit"`
			}
			if search != nil {
				_ = json.Unmarshal(search.Input, &settings)
			}
			eligible := search != nil && screening != nil && search.Status == StepCompleted && screening.Ordinal == search.Ordinal+1 && settings.Limit == 20
			if !eligible {
				remaining = 0
			}
			capability.LocalSearch = &remaining
			if !eligible {
				capability.Limits += "当前路线没有可执行的自动本地关键词补查链路；不能借返修增加该能力。"
			} else if remaining > 0 {
				capability.Work += "剩余本地关键词补查额度可用于已导入材料。"
			} else {
				capability.Limits += "本地关键词补查额度已用尽，返修不会重置；仅能用实际授权的原文/全文读取入口核对。"
			}
			for _, tool := range node.AllowedTools {
				if tool.QualifiedName == "builtin.research.full_text.read" {
					capability.Adoption = "可尝试已有候选的全文入口；成功保存后由宿主刷新索引与引用，再重新综合和确认引用。普通网页查看不等于可信证据已采用。"
				}
			}
		case usesReportProvenance(node):
			capability.Work = "修改 AI 撰写的正文、方法说明与局限；程序管理附录由保存记录重新组装，不由 AI 删除或改写。"
			capability.Limits = "不能通过改正文补造证据或修改冻结的上游综合；不能产生缺失的历史执行日志或重新执行题录归并。只能呈现已保存的实际规则/决定与真实局限；不得把程序附录的存在当成 AI 复制错误。"
			capability.Limits += "从设计 deliverables 承接的材料也会原文重组，报告不能替换、补写或删除它们；修改这些材料须选择其真实设计产出阶段。仅据正确设计补充报告叙述则仍可只改报告。"
		}
		result[node.ID] = capability
	}
	return result
}

const revisionTargetCapabilityInstruction = `
Repair capabilities: stage_input.revisionTargetCapabilities describes what each offered stage ACTUALLY executes, including authorized lookup tools and citation-selection counts. Stage labels are not capabilities. A citation_selection stage only adopts existing references: use it only when a specific relevant available reference is not selected and adopting it resolves the defect. Zero unselectedRecommendedReferences means the recommendation set is already adopted, NOT that every source was read or every possible citation selected; inspect other candidates only if concretely relevant. Re-selecting the same references cannot acquire missing facts. Missing source passages, actual full-text checks, or incorrect synthesis belong to a capable evidence_screening stage when offered, with a specific source/question and its adoption path; web lookup alone does not import evidence. If tools/material cannot supply the requirement, disclose that limit and use the existing question tool only for an unresolved substantive user choice. Do not promise that repeating an unchanged stage will close a known gap. Do not add a separate plan-organizing or AI-check step.
`

func needsEvidenceRepairCapabilities(input json.RawMessage) bool {
	var stage struct {
		Capabilities map[string]revisionTargetCapability `json:"revisionTargetCapabilities"`
	}
	if json.Unmarshal(input, &stage) != nil {
		return false
	}
	for id, c := range stage.Capabilities {
		// Report-only/design routes still receive their explicit capability
		// inventory. Citation-selection/read-budget instructions apply only if
		// this route actually offers an evidence stage, not merely a report.
		if c.Kind == NodeCitationSelection || id == "evidence_screening" {
			return true
		}
	}
	return false
}

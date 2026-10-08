package workflow

import "strings"

// These are generation instructions, not a scientific validator or a new
// submission shape. Existing evidence/citation and acceptance checks remain.
const reportConfidenceInstruction = "分别判断原文主张的支持程度、证据覆盖完整性与应用推断的不确定性；confidence 是基于来源独立性、相关性、直接支持与冲突的模型定性判断，不是统计置信区间或正确率。引用片段数量、推荐采用比例和用户接受有限证据不能单独决定 confidence，也不要求机械降为 low 或自动升为 high。证据确有不足时降低相关主张强度并解释具体原因；已核对的原文主张可以有明确支持，而全文覆盖或外推仍有限。用户接受有限证据只授权继续形成有边界的初稿，不授权缩小原目标、补造结论或免除最终审查；是否完成原目标须另行逐项核对。用现有 claimSummary、limitations 和正文说明哪些结论受支持、哪些问题尚未完成，不添加输出契约之外的字段。"

const reviewCompletenessInstruction = "逐项拆解每个验收条件列举的子要求，分别指出当前结果实际完成的内容及依据；不能仅因表格有对应行、报告披露局限或上一轮问题已解决就把整项判为 met。原目标明确要求的内容如果已有证据但未采用、尚未核验或仅写成未来补充，必须进入 requiredCorrections，不能移到 suggestions；建议只用于原目标之外的可选改善。对于用户允许的原文未讨论或无法核对，按实际核读范围和原约定判断是否满足，不要求论文创造不存在的机制。"

const claimAssessmentVersion = "claim-assessment-v1"

// Record the effective prompt identity without changing the frozen route or
// sending full execution prompts to the presentation layer.
func reportExecutionPromptVersion(node CompiledNode, prompt string) string {
	if usesReportProvenance(node) && strings.Contains(prompt, reportConfidenceInstruction) && !strings.HasSuffix(node.PromptVersion, "-"+claimAssessmentVersion) {
		return node.PromptVersion + "-" + claimAssessmentVersion
	}
	return node.PromptVersion
}

// An exact legacy wording adaptation affects only a newly built execution
// prompt. It never modifies a frozen route, saved execution, report or score.
func reportAssessmentPrompt(node CompiledNode) string {
	text := node.Prompt
	if !usesReportProvenance(node) {
		return text
	}
	text = strings.Replace(text, "对已接受的有限证据，必须保持低置信并将交付物定位为范围受限的初稿。", reportConfidenceInstruction, 1)
	text = strings.Replace(text, "没有实证结果时明确标注为研究设计，不得伪装成已经完成的研究报告。", "没有实证结果时准确标明交付类型：文献比较、证据综述或研究设计；不得把文献研究一律改称研究设计，也不得伪装成已执行的实验研究。", 1)
	return text
}

func isDynamicAcceptanceReview(version string) bool {
	return strings.HasPrefix(version, "dynamic-independent-review-v5") || strings.HasPrefix(version, "dynamic-independent-review-v6") || strings.HasPrefix(version, dynamicResearchReviewVersion)
}

func isDynamicGapReview(version string) bool {
	return strings.HasPrefix(version, "dynamic-independent-review-v6") || strings.HasPrefix(version, dynamicResearchReviewVersion)
}

package workflow

import (
	"strings"

	"github.com/wangh00/SciAide/internal/app/chat"
)

// These are generation instructions, not a scientific validator or a new
// submission shape. Existing evidence/citation and acceptance checks remain.
const reportConfidenceInstruction = "分别判断原文主张的支持程度、证据覆盖完整性与应用推断的不确定性；confidence 是基于来源独立性、相关性、直接支持与冲突的模型定性判断，不是统计置信区间或正确率。引用片段数量、推荐采用比例和用户接受有限证据不能单独决定 confidence，也不要求机械降为 low 或自动升为 high。证据确有不足时降低相关主张强度并解释具体原因；已核对的原文主张可以有明确支持，而全文覆盖或外推仍有限。用户接受有限证据本身只授权继续形成有边界的初稿，不授权缩小交付验收、补造结论或免除最终审查；若 researchContract.deliveryScope 存在，只按用户已确认的当前 successCriteria 核对本轮交付，原问题与未完成的原目标仍须如实保留和披露。用现有 claimSummary、limitations 和正文区分受支持的结论、已承诺但缺失的内容以及未纳入本轮范围的可选工作，不把可选改进一律写成未完成，也不隐藏真实缺项；不添加输出契约之外的字段。"

const reviewCompletenessInstruction = "逐项拆解每个验收条件的本轮内容要求，按用户确认的交付成熟度核对；不能仅因表格有对应行、报告披露局限或上一轮问题已解决就判 met。本轮明确承诺的具体材料如果缺失、证据未采用或只写成未来待办，必须给出修正；多个条件涉及同一缺陷时共用问题与修正，不重复创建总括问题。约定允许的待核验草稿应有实际内容，但不能擅自升级为已完成量表验证、伦理审批或实施。后置交付核验由后续门禁执行，其尚未执行不构成当前稿件缺陷；仍需核对稿件一致性及是否虚构已通过。对于用户允许的原文未讨论或无法核对，按实际来源范围和原约定判断，不要求论文创造不存在的机制。"

const claimAssessmentVersion = "claim-assessment-v4"

const reportDeliverableVersion = "delivery-form-v1"

const reportDeliverableInstruction = `
User-confirmed delivery form takes precedence over generic method/Skill templates. Check the current requested language, audience, artifact form and approximate length against still-effective user statements and confirmed clarifications, not only a compressed successCriteria list. A method template is not authority to waive these requirements. When the user requests a concise main text, keep the substantive synthesis within that scale; put required detailed study tables, provenance and reproducibility records in clearly separated appendices instead of inflating the main text or dropping scientific evidence. Judge approximate limits reasonably, without inventing a rigid threshold, but do not call a substantial overrun "met" merely because a method asked for more tables. If genuinely incompatible requirements cannot both be met, disclose the conflict and request confirmation rather than silently reprioritizing. A confirmed deliveryScope amendment supersedes only what the user explicitly changed; do not restore deferred historical criteria. This is generation/review guidance, not a deterministic keyword gate, an automatic shortening transform, or permission to alter source quotations.
`

// Only report/review execution prompts receive these new rules, so upgrading
// cannot change an unrelated implementation checkpoint's frozen prompt hash.
const researchLiteratureAssessmentInstruction = `
For evidence syntheses, distinguish retrieved candidate records, deduplicated reports, independently conducted studies, user-selected source documents, and studies actually synthesized. They are not interchangeable counts. Trace every required candidate and selected source to its saved screening decision and actual use (included, merged with another report of the same study, background only, excluded with reason, or unresolved). Give explicit mappings when merging reports; never invent an exclusion or a full-text assessment. If a per-study table is promised, account for all eligible studies and document how their source reports map to rows; do not demand one independent-study row per uploaded or selected document. A missing promised mapping remains a defect even if headline counts agree or limitations are disclosed.
For literature-derived counts and proportions, identify the source operands, numerator, denominator, unit of analysis, population, time window and result version. Label a reproducible calculation as derived; its absence as a verbatim number in the source is not by itself fabrication. Assess arithmetic correctness separately from denominator validity, sampling limits, duplicated participants or reports, and the strength of the scientific interpretation. Correct arithmetic does not establish representativeness, efficacy or causality. Never relabel rated comments, responses or observations as all students or all records, and never silently pool incompatible sources. Preserve and correct genuine unsupported claims; this guidance does not relax acceptance criteria or authorize invented operands.
` + reportDeliverableInstruction

// Writing/review guidance, not a new acceptance condition or a text rewrite.
const researchReportScopeInstruction = `
Research scope and reader-facing writing:
Distinguish three things against the complete agreed requirements: delivered work, concrete promised work still missing, and optional analyses NOT INCLUDED in this round. Only the second is an unfulfilled requirement. Do not describe every possible robustness check, external validation or alternative model as "unfinished" merely because it could improve a future study; describe a real scope boundary and its effect on inference, without hiding genuinely promised missing work. Do not mechanically replace all absence/unfinished wording or remove a supported review finding.
In a thesis or research report, present the question, methods, results, conclusions and scientific limitations in the reader's requested style. Check design consistency internally, disclose actual deviations and their consequences, but do not add repeated self-grading sections about whether the program completed its stages. A limitation is neither an automatic approval nor an automatic rejection. Optional improvements belong in limitations or future work, not an invented failure list in the abstract. Preserve the original research question, methods, evidence and historical requirements. For this delivery, use researchContract.successCriteria as the current acceptance list: it is original unless researchContract.deliveryScope records the user's confirmed amendment. In that case previousCriteria is history, not an additional completion gate; disclose postponed original goals without claiming they were done. Neither limited-evidence acceptance nor ordinary repair changes the current list.
`

// Record the effective prompt identity without changing the frozen route or
// sending full execution prompts to the presentation layer.
func reportExecutionPromptVersion(node CompiledNode, prompt string) string {
	if IsNativePlannerProtocol(node.PromptVersion) || IsReviewSourceProtocol(node.PromptVersion) || IsCitationSourceProtocol(node.PromptVersion) {
		return node.PromptVersion // An already frozen execution is never upgraded.
	}
	limit := 128
	nativeSuffix := ""
	if usesNativePlannerProtocol(node) {
		nativeSuffix = nativePlannerVersionSuffix
		limit -= len(nativeSuffix)
	} else if usesReviewSourceProtocol(node) {
		nativeSuffix = reviewSourceVersionSuffix
		limit -= len(nativeSuffix)
	} else if usesCitationSourceProtocol(node) {
		nativeSuffix = "|" + citationSourceSchemaVersion(node.OutputSchema)
		limit -= len(nativeSuffix)
	}
	version := node.PromptVersion
	markers := []string{reviewRepairVersion, reviewScopeVersion, claimAssessmentVersion, implementationDecisionVersion, chat.ImplementationPartsVersion, chat.ImplementationPatchVersion, implementationEfficiencyVersion, computationAuditVersion, reviewKernelPathVersion, researchInputSharingVersion, researchHandoffVersion, stageInputPagesVersion, reportDeliverableVersion}
	// Keep transport/assessment markers verbatim because consumers use them.
	// Descriptive-only markers have bounded aliases for the extreme combination
	// that would otherwise exhaust even the base digest's 128-byte allowance.
	aliases := map[string]string{reviewRepairVersion: "rr1", reviewScopeVersion: "rs4", implementationDecisionVersion: "id1", implementationEfficiencyVersion: "ie1", computationAuditVersion: "da2", reviewKernelPathVersion: "kr1", researchInputSharingVersion: "ss1", researchHandoffVersion: "rh1", reportDeliverableVersion: "df1"}
	hasMarker := func(value, marker string) bool {
		for _, token := range []string{marker, aliases[marker]} {
			if token != "" && (strings.HasSuffix(value, "-"+token) || strings.Contains(value, "-"+token+"-")) {
				return true
			}
		}
		return false
	}
	appendVersion := func(marker string, enabled bool) {
		if enabled && !hasMarker(version, marker) {
			version += "-" + marker
		}
	}
	appendVersion(reviewRepairVersion, usesTrackedReview(node) && (strings.Contains(prompt, singleSourceReviewInstruction) || strings.Contains(prompt, ReviewSourceInstruction(node.PromptVersion))))
	appendVersion(reviewScopeVersion, isIndependentReviewSchema(node.OutputSchema) && strings.Contains(prompt, reviewScopeInstruction))
	appendVersion(claimAssessmentVersion, usesReportProvenance(node) && strings.Contains(prompt, reportConfidenceInstruction))
	appendVersion(reportDeliverableVersion, strings.Contains(prompt, reportDeliverableInstruction))
	appendVersion(implementationDecisionVersion, strings.Contains(prompt, implementationDecisionInstruction))
	appendVersion(chat.ImplementationPartsVersion, node.ID == "method_implementation" && strings.Contains(prompt, ImplementationPartsInstruction))
	appendVersion(chat.ImplementationPatchVersion, node.ID == "method_implementation" && strings.Contains(prompt, ImplementationPatchInstruction))
	appendVersion(implementationEfficiencyVersion, node.ID == "method_implementation" && strings.Contains(prompt, implementationEfficiencyInstruction))
	appendVersion(computationAuditVersion, strings.Contains(prompt, computationAuditInstruction))
	appendVersion(reviewKernelPathVersion, isIndependentReviewSchema(node.OutputSchema) && strings.Contains(prompt, kernelReviewNavigationInstruction))
	appendVersion(researchInputSharingVersion, strings.Contains(prompt, researchInputSharingInstruction))
	appendVersion(stageInputPagesVersion, strings.Contains(prompt, stageInputPagesInstruction))
	appendVersion(researchHandoffVersion, strings.Contains(prompt, researchHandoffInstruction))
	if len(version) > limit {
		// SQLite already bounds this descriptive execution identity at 128.
		// Compact only its base, keeping all host markers readable/idempotent.
		// The frozen node version and full PromptSHA256 remain unchanged.
		base := node.PromptVersion
		for {
			trimmed := false
			for _, marker := range markers {
				for _, token := range []string{marker, aliases[marker]} {
					if token != "" && strings.HasSuffix(base, "-"+token) {
						base = strings.TrimSuffix(base, "-"+token)
						trimmed = true
						break
					}
				}
				if trimmed {
					break
				}
			}
			if !trimmed {
				break
			}
		}
		suffix := ""
		for _, marker := range markers {
			if hasMarker(version, marker) {
				suffix += "-" + marker
			}
		}
		if len(suffix) > limit-18 {
			suffix = ""
			for _, marker := range markers {
				if hasMarker(version, marker) {
					token := marker
					if alias := aliases[marker]; alias != "" {
						token = alias
					}
					suffix += "-" + token
				}
			}
		}
		digest := hashStrings(base)
		if available := limit - len("p-") - len(suffix); len(digest) > available {
			digest = digest[:available]
		}
		version = "p-" + digest + suffix
	}
	return version + nativeSuffix
}

// An exact legacy wording adaptation affects only a newly built execution
// prompt. It never modifies a frozen route, saved execution, report or score.
func reportAssessmentPrompt(node CompiledNode) string {
	text := node.Prompt
	if !usesReportProvenance(node) {
		return text
	}
	text = strings.Replace(text, "对已接受的有限证据，必须保持低置信并将交付物定位为范围受限的初稿。", reportConfidenceInstruction, 1)
	text = strings.Replace(text, "必须说明实证结果是否符合原研究设计", "核对实证结果与已确认研究设计的一致性，正文披露实际偏离及其影响，不重复自评流程完成度", 1)
	text = strings.Replace(text, "没有实证结果时明确标注为研究设计，不得伪装成已经完成的研究报告。", "没有实证结果时准确标明交付类型：文献比较、证据综述或研究设计；不得把文献研究一律改称研究设计，也不得伪装成已执行的实验研究。", 1)
	if !strings.Contains(text, reportConfidenceInstruction) {
		text += "\n当前主张评估说明（取代冲突的宽泛完成度表述）：" + reportConfidenceInstruction
	}
	return text
}

func isDynamicAcceptanceReview(version string) bool {
	return strings.HasPrefix(version, "dynamic-independent-review-v5") || strings.HasPrefix(version, "dynamic-independent-review-v6") || strings.HasPrefix(version, dynamicResearchReviewVersion) || isRouteDeliveryReview(version)
}

func isDynamicGapReview(version string) bool {
	return strings.HasPrefix(version, "dynamic-independent-review-v6") || strings.HasPrefix(version, dynamicResearchReviewVersion) || isRouteDeliveryReview(version)
}

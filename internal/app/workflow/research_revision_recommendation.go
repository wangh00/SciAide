package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// A recommendation is advisory. Only an explicit Retry command may act on it.
type ResearchRevisionRecommendation struct {
	Status                string   `json:"status"`
	NodeID                string   `json:"nodeId,omitempty"`
	Label                 string   `json:"label,omitempty"`
	Summary               string   `json:"summary"`
	Reasons               []string `json:"reasons,omitempty"`
	RequiredInputs        []string `json:"requiredInputs,omitempty"`
	RepairableCorrections []string `json:"repairableCorrections,omitempty"`
	PendingStartLabel     string   `json:"pendingStartLabel,omitempty"`
	PendingAffectedStages []string `json:"pendingAffectedStages,omitempty"`
	AffectedStages        []string `json:"affectedStages,omitempty"`
	RepeatsSideEffects    bool     `json:"repeatsSideEffects"`
	ReviewOutputSHA256    string   `json:"reviewOutputSha256,omitempty"`
}

type researchRevisionPlanItem struct {
	CorrectionIndex int      `json:"correctionIndex"`
	NodeID          string   `json:"nodeId"`
	Reason          string   `json:"reason"`
	WhyNotLater     string   `json:"whyNotLater"`
	NeedsUserInput  bool     `json:"needsUserInput"`
	RequiredInput   string   `json:"requiredInput"`
	EditTargets     []string `json:"editTargets,omitempty"`
}

// Only newly generated templates acquire this optional extension. Existing
// persisted compilations and their schemas are never rewritten. New prompts
// require it, while legacy outputs remain readable without inventing a plan.
func withRevisionPlanSchema(base json.RawMessage) json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal(base, &schema)
	var plan any
	_ = json.Unmarshal(raw(`{"type":"array","maxItems":100,"items":{"type":"object","additionalProperties":false,"required":["correctionIndex","nodeId","reason","whyNotLater","needsUserInput","requiredInput"],"properties":{"correctionIndex":{"type":"integer","minimum":0,"maximum":99},"nodeId":{"type":"string","maxLength":200},"reason":{"type":"string","minLength":1,"maxLength":2000},"whyNotLater":{"type":"string","minLength":1,"maxLength":2000},"needsUserInput":{"type":"boolean"},"requiredInput":{"type":"string","maxLength":2000}}}}`), &plan)
	schema["properties"].(map[string]any)["revisionPlan"] = plan
	// Optional in the stored schema for old callers; a newly frozen ownership
	// contract requires explicit writes during input-aware validation.
	plan.(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["editTargets"] = decodeObject(raw(`{"type":"array","maxItems":100,"items":{"type":"string","minLength":1,"maxLength":80}}`))
	return rawObject(schema)
}

func revisionPlanTargetsForReview(detail RunDetail, reviewID string) []ResearchRevisionTarget {
	nodes := compilationNodeMap(detail.Run.Compilation)
	for _, edge := range detail.Run.Compilation.Edges {
		if edge.FromNode != reviewID || edge.ToPort != "review" || !isReviewGateNode(nodes[edge.ToNode]) {
			continue
		}
		gate := findStepByNode(detail.Steps, edge.ToNode)
		if gate == nil {
			continue
		}
		for _, subject := range detail.Run.Compilation.Edges {
			if subject.ToNode == gate.NodeID && subject.ToPort == "subject" {
				if producer := findStepByNode(detail.Steps, subject.FromNode); producer != nil {
					return researchRevisionCandidates(detail, *producer, *gate)
				}
			}
		}
	}
	return nil
}

func parseResearchRevisionPlan(output json.RawMessage, targets []ResearchRevisionTarget) ([]researchRevisionPlanItem, []string, bool, error) {
	var value struct {
		Corrections []string        `json:"requiredCorrections"`
		Plan        json.RawMessage `json:"revisionPlan"`
	}
	if err := json.Unmarshal(output, &value); err != nil {
		return nil, nil, false, fmt.Errorf("审查输出无法解码")
	}
	if len(value.Plan) == 0 {
		return nil, value.Corrections, false, nil
	}
	var plan []researchRevisionPlanItem
	if string(value.Plan) == "null" || json.Unmarshal(value.Plan, &plan) != nil || len(plan) != len(value.Corrections) {
		return nil, nil, true, fmt.Errorf("返修建议必须逐项覆盖 requiredCorrections，不能遗漏或增加修正项")
	}
	wanted := make(map[string]bool, len(targets))
	for _, target := range targets {
		wanted[target.NodeID] = true
	}
	seen := make(map[int]bool, len(plan))
	for _, item := range plan {
		if item.CorrectionIndex < 0 || item.CorrectionIndex >= len(value.Corrections) || seen[item.CorrectionIndex] {
			return nil, nil, true, fmt.Errorf("返修建议包含未知或重复的修正项序号")
		}
		seen[item.CorrectionIndex] = true
		if strings.TrimSpace(value.Corrections[item.CorrectionIndex]) == "" || strings.TrimSpace(item.Reason) == "" || strings.TrimSpace(item.WhyNotLater) == "" {
			return nil, nil, true, fmt.Errorf("返修建议必须说明修正依据，以及为何仅修改后续阶段不足")
		}
		if !wanted[item.NodeID] && !(item.NodeID == "" && item.NeedsUserInput) {
			return nil, nil, true, fmt.Errorf("返修建议阶段 %q 不属于当前可返修阶段", item.NodeID)
		}
		if item.NeedsUserInput != (strings.TrimSpace(item.RequiredInput) != "") {
			return nil, nil, true, fmt.Errorf("需要用户补充资料时必须说明 requiredInput，不能把缺失资料当作可自动修正内容")
		}
	}
	return plan, value.Corrections, true, nil
}

func validateResearchRevisionPlan(output, input json.RawMessage) error {
	var stage struct {
		Targets []ResearchRevisionTarget `json:"revisionTargets"`
	}
	if err := json.Unmarshal(input, &stage); err != nil {
		return fmt.Errorf("返修建议的冻结阶段输入无效")
	}
	plan, _, _, err := parseResearchRevisionPlan(output, stage.Targets)
	if err != nil {
		return err
	}
	return validateRevisionWriteObjects(plan, input)
}

// Validate the actual current review, not text guessed from gate error messages
// or a model's earlier attempt. The hash also serves as the user's retry token.
func currentRejectedReview(detail RunDetail, gate Step) (*Step, json.RawMessage, error) {
	producer, review, _, err := reviewRevisionContext(detail, gate)
	if err != nil {
		return nil, nil, err
	}
	output, err := currentReviewOutput(detail, *review)
	if err != nil {
		return nil, nil, err
	}
	if err := matchReviewGate(*review, output, gate); err != nil {
		return nil, nil, err
	}
	var decision struct {
		Approved bool   `json:"approved"`
		Hash     string `json:"reviewedInputSha256"`
	}
	if json.Unmarshal(output, &decision) != nil || decision.Approved || decision.Hash != review.InputSHA256 {
		return nil, nil, fmt.Errorf("审查并未拒绝当前冻结输入")
	}
	var reviewInput map[string]json.RawMessage
	assembled, ok := reviewAssembledInput(review.Input)
	if !ok {
		return nil, nil, fmt.Errorf("审查报告组成无法还原")
	}
	_ = json.Unmarshal(assembled, &reviewInput)
	linked := false
	for _, edge := range detail.Run.Compilation.Edges {
		if edge.FromNode == producer.NodeID && edge.ToNode == review.NodeID {
			produced, err := outputPort(producer.Output, edge.FromPort)
			if err == nil && rawJSONEqual(produced, reviewInput[edge.ToPort]) {
				linked = true
			}
		}
	}
	if !linked {
		return nil, nil, fmt.Errorf("审查未覆盖当前产出阶段的结果")
	}
	return review, output, nil
}

func researchRevisionRecommendation(detail RunDetail) *ResearchRevisionRecommendation {
	if detail.Run.Status != RunFailed {
		return nil
	}
	nodes := compilationNodeMap(detail.Run.Compilation)
	var gate *Step
	for i := range detail.Steps {
		if isRejectedReviewGate(nodes[detail.Steps[i].NodeID], detail.Steps[i]) {
			gate = &detail.Steps[i]
			break
		}
	}
	if gate == nil {
		return nil
	}
	result := &ResearchRevisionRecommendation{Status: "unavailable", Summary: "这次审查未提供修改位置，可手动选择返修起点；新建任务的审查支持返修建议。"}
	// Missing plans in historical reviews are a compatibility state, not an
	// excuse to fabricate a report-only recommendation or mutate their schema.
	_, review, _, contextErr := reviewRevisionContext(detail, *gate)
	if contextErr != nil {
		result.Summary = "当前审查与交付核验记录不完整，暂不能返修；这是程序记录问题，不需要重新描述研究要求。"
		result.Reasons = []string{contextErr.Error()}
		return result
	}
	output, err := outputPort(review.Output, "analysis")
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(output, &fields) != nil {
		result.Summary = "无法读取当前审查的冻结结果，请手动选择返修起点。"
		return result
	}
	_, output, err = currentRejectedReview(detail, *gate)
	if err != nil {
		result.Summary = "这次返修建议未通过一致性检查，请刷新或手动选择起点。"
		result.Reasons = []string{err.Error()}
		return result
	}
	result.ReviewOutputSHA256 = hashJSON(output)
	// Old reviews did not distinguish quoted evidence from the object to edit.
	// Do not guess a new target or rewrite their frozen plan/history. Manual
	// repair remains available without recreating the task or its materials.
	ownership, ownershipErr := currentRevisionWriteContract(detail, review.NodeID, review.Input, researchRevisionTargets(detail))
	if ownershipErr != nil {
		result.Summary = "当前返修写入对象与保存材料不一致，请刷新后核对。"
		result.Reasons = []string{ownershipErr.Error()}
		return result
	}
	if ownership != nil && decodeObject(review.Input)["revisionWriteContract"] == nil {
		result.Summary = "这次历史审查未绑定实际写入对象，不能确认报告阶段能修改冻结设计材料；请手动核对并选择返修起点，无需新建任务。"
		return result
	}
	if _, exists := fields["revisionPlan"]; !exists {
		return result
	}
	targets := researchRevisionTargets(detail)
	plan, corrections, _, err := parseResearchRevisionPlan(output, targets)
	if err != nil || len(plan) == 0 {
		result.Summary = "返修建议未覆盖可执行的修正项，请手动选择返修起点。"
		if err != nil {
			result.Reasons = []string{err.Error()}
		}
		return result
	}
	byID := make(map[string]ResearchRevisionTarget, len(targets))
	for _, target := range targets {
		byID[target.NodeID] = target
	}
	result.ReviewOutputSHA256 = hashJSON(output)
	needsInput := false
	missingInputs := []string{}
	seenInput := map[string]bool{}
	var earliest *Step
	for _, item := range plan {
		label := byID[item.NodeID].Label
		if item.NodeID == "" {
			label = "补充资料"
		}
		reason := fmt.Sprintf("修正 %d：%s；建议%s：%s；仅修改后续阶段不足：%s", item.CorrectionIndex+1, corrections[item.CorrectionIndex], label, item.Reason, item.WhyNotLater)
		if item.NeedsUserInput {
			needsInput = true
			reason += "；需要用户补充：" + item.RequiredInput
			missing := strings.TrimSpace(item.RequiredInput)
			if !seenInput[missing] {
				seenInput[missing] = true
				missingInputs = append(missingInputs, missing)
			}
		} else {
			result.RepairableCorrections = append(result.RepairableCorrections, corrections[item.CorrectionIndex])
		}
		result.Reasons = append(result.Reasons, reason)
		if item.NodeID != "" {
			stage := findStepByNode(detail.Steps, item.NodeID)
			if earliest == nil || stage.Ordinal < earliest.Ordinal {
				earliest = stage
			}
		}
	}
	if needsInput {
		result.Status = "needs_input"
		result.RequiredInputs = missingInputs
		if earliest != nil {
			result.PendingStartLabel = byID[earliest.NodeID].Label
			result.PendingAffectedStages = revisionStageLabels(detail, earliest.Ordinal)
		}
		result.Summary = "审查已列出仍未解决的必要输入；已有修正保留，不自动重跑，也不再启动一轮核查或整理方案。 需要补充：" + strings.Join(missingInputs, "；")
		return result
	}
	if earliest == nil {
		result.Summary = "未找到覆盖所有修正项的合法返修起点。"
		return result
	}
	target := byID[earliest.NodeID]
	// Progress is review context, not a human decision. Never turn an unchanged
	// history heuristic into a second planning pass or a missing-user-input gate.
	if reason := reviewRepairProgress(detail, earliest.NodeID); reason != "" {
		result.Reasons = append(result.Reasons, reason)
	}
	result.Status, result.NodeID, result.Label = "recommended", target.NodeID, target.Label
	result.RepeatsSideEffects = target.RepeatsSideEffects
	// Retry rewinds through the gate, then continues the queued downstream
	// publication steps. Show the whole execution suffix, not just corrections.
	result.AffectedStages = revisionStageLabels(detail, earliest.Ordinal)
	result.Summary = "建议从“" + target.Label + "”返修，以覆盖全部修正项；仅在你确认后执行。"
	var tracked struct {
		Findings []trackedReviewFinding `json:"reviewFindings"`
	}
	_ = json.Unmarshal(output, &tracked)
	repeated := 0
	for _, finding := range tracked.Findings {
		if finding.Status == "open" && finding.Origin == "existing" {
			repeated++
			result.Reasons = append(result.Reasons, fmt.Sprintf("尚未解决 %s：%s；本轮核对：%s", finding.ID, finding.Summary, finding.Reason))
		}
	}
	if repeated > 0 {
		result.Summary += fmt.Sprintf(" 其中 %d 项此前已提出，本轮仍未解决；请核对具体依据后再确认。", repeated)
	}
	return result
}

func revisionStageLabels(detail RunDetail, ordinal int) []string {
	nodes := compilationNodeMap(detail.Run.Compilation)
	stages := append([]Step(nil), detail.Steps...)
	sort.Slice(stages, func(i, j int) bool { return stages[i].Ordinal < stages[j].Ordinal })
	result := []string{}
	for _, stage := range stages {
		if stage.Ordinal >= ordinal {
			result = append(result, nonEmptyMessage(nodes[stage.NodeID].Name, stage.NodeID))
		}
	}
	return result
}

func resolveReviewRevisionRequest(detail RunDetail, gate Step, command RetryCommand) (string, error) {
	if !command.UseRecommendation {
		if strings.TrimSpace(command.RevisionNodeID) == "" {
			return "", fmt.Errorf("请明确选择返修起点，或确认采用当前审查的建议")
		}
		if target := evidenceReselectionStep(detail); target != nil && strings.TrimSpace(command.RevisionNodeID) == target.NodeID {
			_, output, err := currentRejectedReview(detail, gate)
			if err != nil || command.ExpectedReviewSHA256 == "" || hashJSON(output) != command.ExpectedReviewSHA256 {
				return "", fmt.Errorf("证据补选对应的审查已变化，请刷新后重新确认")
			}
		}
		return strings.TrimSpace(command.RevisionNodeID), nil
	}
	recommendation := researchRevisionRecommendation(detail)
	if recommendation == nil || recommendation.Status != "recommended" {
		return "", fmt.Errorf("当前审查没有可执行的返修建议；请补充资料或手动选择起点")
	}
	if strings.TrimSpace(command.ExpectedReviewSHA256) == "" || command.ExpectedReviewSHA256 != recommendation.ReviewOutputSHA256 {
		return "", fmt.Errorf("审查建议已变化，请刷新并重新确认")
	}
	if command.RevisionNodeID != "" && strings.TrimSpace(command.RevisionNodeID) != recommendation.NodeID {
		return "", fmt.Errorf("确认的返修起点与当前建议不一致，请刷新并重新确认")
	}
	review, output, err := currentRejectedReview(detail, gate)
	if err != nil || review == nil || hashJSON(output) != recommendation.ReviewOutputSHA256 {
		return "", fmt.Errorf("请求的失败阶段与当前审查建议不一致，请刷新并重新确认")
	}
	return recommendation.NodeID, nil
}

package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// A recommendation is advisory. Only an explicit Retry command may act on it.
type ResearchRevisionRecommendation struct {
	Status             string   `json:"status"`
	NodeID             string   `json:"nodeId,omitempty"`
	Label              string   `json:"label,omitempty"`
	Summary            string   `json:"summary"`
	Reasons            []string `json:"reasons,omitempty"`
	RequiredInputs     []string `json:"requiredInputs,omitempty"`
	AffectedStages     []string `json:"affectedStages,omitempty"`
	RepeatsSideEffects bool     `json:"repeatsSideEffects"`
	ReviewOutputSHA256 string   `json:"reviewOutputSha256,omitempty"`
}

type researchRevisionPlanItem struct {
	CorrectionIndex int    `json:"correctionIndex"`
	NodeID          string `json:"nodeId"`
	Reason          string `json:"reason"`
	WhyNotLater     string `json:"whyNotLater"`
	NeedsUserInput  bool   `json:"needsUserInput"`
	RequiredInput   string `json:"requiredInput"`
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
	_, _, _, err := parseResearchRevisionPlan(output, stage.Targets)
	return err
}

// Validate the actual current review, not text guessed from gate error messages
// or a model's earlier attempt. The hash also serves as the user's retry token.
func currentRejectedReview(detail RunDetail, gate Step) (*Step, json.RawMessage, error) {
	producer, review, _, err := reviewRevisionContext(detail, gate)
	if err != nil {
		return nil, nil, err
	}
	node := compilationNodeMap(detail.Run.Compilation)[review.NodeID]
	if !isIndependentReviewSchema(node.OutputSchema) || review.InputSHA256 == "" || hashJSON(review.Input) != review.InputSHA256 || gate.InputSHA256 == "" || hashJSON(gate.Input) != gate.InputSHA256 {
		return nil, nil, fmt.Errorf("审查或门禁输入快照校验失败")
	}
	output, err := outputPort(review.Output, "analysis")
	if err != nil {
		return nil, nil, err
	}
	if err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, output); err != nil {
		return nil, nil, fmt.Errorf("审查不符合冻结契约：%w", err)
	}
	if err := validateWorkflowAIStageOutput(node, output, review.Input); err != nil {
		return nil, nil, err
	}
	var decision struct {
		Approved bool   `json:"approved"`
		Hash     string `json:"reviewedInputSha256"`
	}
	if json.Unmarshal(output, &decision) != nil || decision.Approved || decision.Hash != review.InputSHA256 {
		return nil, nil, fmt.Errorf("审查并未拒绝当前冻结输入")
	}
	matched := false
	for _, execution := range detail.AIExecutions {
		if execution.WorkflowStepID == review.ID && execution.Attempt == review.Attempt && execution.Status == "completed" && execution.InputSHA256 == review.InputSHA256 && execution.OutputSHA256 != "" && hashJSON(execution.Output) == execution.OutputSHA256 && rawJSONEqual(execution.Output, output) {
			matched = true
		}
	}
	if !matched {
		return nil, nil, fmt.Errorf("当前审查尝试缺少一致的 AI 冻结输出")
	}
	var gateInput struct {
		Subject json.RawMessage `json:"subject"`
		Review  json.RawMessage `json:"review"`
	}
	core, err := reviewCoreForGate(output)
	if err != nil || json.Unmarshal(gate.Input, &gateInput) != nil || !reviewSubjectMatchesInput(gateInput.Subject, review.Input) || !rawJSONEqual(core, gateInput.Review) {
		return nil, nil, fmt.Errorf("门禁引用了过期或不同的审查")
	}
	var reviewInput map[string]json.RawMessage
	_ = json.Unmarshal(review.Input, &reviewInput)
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
		if detail.Steps[i].Status == StepFailed && isReviewGateNode(nodes[detail.Steps[i].NodeID]) {
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
		result.Summary = "无法验证这次审查的返修建议，请刷新或手动选择起点。"
		result.Reasons = []string{contextErr.Error()}
		return result
	}
	output, err := outputPort(review.Output, "analysis")
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(output, &fields) != nil {
		result.Summary = "无法读取当前审查的冻结结果，请手动选择返修起点。"
		return result
	}
	if _, exists := fields["revisionPlan"]; !exists {
		return result
	}
	_, output, err = currentRejectedReview(detail, *gate)
	if err != nil {
		result.Summary = "这次返修建议未通过一致性检查，请刷新或手动选择起点。"
		result.Reasons = []string{err.Error()}
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
		result.Summary = "需先补充或确认：" + strings.Join(missingInputs, "；") + "。当前审查未给出可直接执行的完整返修方案，不会默认只修改报告。"
		return result
	}
	if earliest == nil {
		result.Summary = "未找到覆盖所有修正项的合法返修起点。"
		return result
	}
	target := byID[earliest.NodeID]
	result.Status, result.NodeID, result.Label = "recommended", target.NodeID, target.Label
	result.RepeatsSideEffects = target.RepeatsSideEffects
	// Retry resets every step in this ordinal interval, not only the stages
	// named by individual corrections. Show the actual replay scope in order.
	stages := append([]Step(nil), detail.Steps...)
	sort.Slice(stages, func(i, j int) bool { return stages[i].Ordinal < stages[j].Ordinal })
	for _, stage := range stages {
		if stage.Ordinal >= earliest.Ordinal && stage.Ordinal <= gate.Ordinal {
			result.AffectedStages = append(result.AffectedStages, nonEmptyMessage(nodes[stage.NodeID].Name, stage.NodeID))
		}
	}
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

func resolveReviewRevisionRequest(detail RunDetail, gate Step, command RetryCommand) (string, error) {
	if !command.UseRecommendation {
		if strings.TrimSpace(command.RevisionNodeID) == "" {
			return "", fmt.Errorf("请明确选择返修起点，或确认采用当前审查的建议")
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

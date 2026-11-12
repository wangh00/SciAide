package workflow

import (
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/reviewcontract"
	"github.com/wangh00/SciAide/internal/app/tool"
)

// One current-review verifier for gate execution, rejection/revision and final
// delivery. No caller may treat completed status alone as a scientific verdict.
func currentReviewOutput(detail RunDetail, review Step) (json.RawMessage, error) {
	node := compilationNodeMap(detail.Run.Compilation)[review.NodeID]
	if review.Status != StepCompleted || !isIndependentReviewSchema(node.OutputSchema) || review.InputSHA256 == "" || hashJSON(review.Input) != review.InputSHA256 {
		return nil, fmt.Errorf("审查尚未完成或冻结输入不一致")
	}
	if err := validateReviewDependencies(detail, review); err != nil {
		return nil, err
	}
	output, err := outputPort(review.Output, "analysis")
	if err != nil {
		return nil, err
	}
	matched := false
	for _, execution := range detail.AIExecutions {
		if execution.WorkflowStepID == review.ID && execution.Attempt == review.Attempt && execution.Status == "completed" && execution.InputSHA256 == review.InputSHA256 && execution.OutputSHA256 != "" && hashJSON(execution.Output) == execution.OutputSHA256 && rawJSONEqual(execution.Output, output) {
			matched = true
		}
	}
	if !matched {
		return nil, fmt.Errorf("当前审查尝试缺少一致的 AI 冻结输出")
	}
	if err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, output); err != nil {
		return nil, fmt.Errorf("审查不符合冻结契约：%w", err)
	}
	if err := validateWorkflowAIStageOutput(node, output, review.Input); err != nil {
		return nil, err
	}
	return output, nil
}

// Gate inputs bind the FULL reviewed snapshot, never its display-only body.
func matchReviewGate(review Step, fullReview json.RawMessage, gate Step) error {
	if gate.InputSHA256 == "" || hashJSON(gate.Input) != gate.InputSHA256 {
		return fmt.Errorf("交付门禁输入摘要不一致")
	}
	var input struct {
		Subject json.RawMessage `json:"subject"`
		Review  json.RawMessage `json:"review"`
	}
	core, err := reviewCoreForGate(fullReview)
	if err != nil || json.Unmarshal(gate.Input, &input) != nil || !reviewcontract.SubjectMatchesInput(input.Subject, review.Input) || !reviewcontract.Equal(core, input.Review) {
		return fmt.Errorf("门禁引用了过期或不同的审查")
	}
	return nil
}

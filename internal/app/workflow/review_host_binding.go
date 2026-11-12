package workflow

import (
	"encoding/json"
	"fmt"
)

// The current stage already owns its immutable input. Asking a model to copy
// that digest adds no research evidence. Bind only an omitted field; supplied
// identities are never overwritten (a stale/mismatched one still fails).
func bindReviewSubmissionInput(text string, node CompiledNode, input json.RawMessage) (string, []AIStageNormalization, error) {
	if !isIndependentReviewSchema(node.OutputSchema) {
		return text, nil, nil
	}
	if !json.Valid(input) {
		return text, nil, fmt.Errorf("独立审查缺少有效冻结输入，无法绑定提交")
	}
	candidate, err := extractStageJSON(text, raw(`{"type":"object","additionalProperties":true}`))
	if err != nil {
		return text, nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(candidate, &fields); err != nil || fields == nil {
		return text, nil, fmt.Errorf("独立审查提交不是对象")
	}
	if supplied, exists := fields["reviewedInputSha256"]; exists {
		var digest string
		if json.Unmarshal(supplied, &digest) != nil || digest != hashJSON(input) {
			return text, nil, fmt.Errorf("reviewedInputSha256 与当前冻结输入不一致；请省略此宿主管理字段，不能用其他阶段的身份提交")
		}
		return text, nil, nil
	}
	fields["reviewedInputSha256"] = mustJSON(hashJSON(input))
	bound, err := json.Marshal(fields)
	if err != nil {
		return text, nil, err
	}
	return string(bound), []AIStageNormalization{{Path: "$.reviewedInputSha256", Rule: "bind_current_frozen_review_input", BeforeSHA256: hashJSON(candidate), AfterSHA256: hashJSON(bound)}}, nil
}

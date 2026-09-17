package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const dynamicResearchReviewVersion = "dynamic-independent-review-v4"

func isResearchAcceptanceSchema(schema json.RawMessage) bool {
	return isIndependentReviewSchema(schema) && schemaDeclaresProperty(schema, "acceptanceChecks")
}

type ResearchAcceptanceCriterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Checks are the reviewer's accountable assessment, not a claim that the host
// can establish scientific truth. The host verifies coverage and consistency.
type ResearchAcceptanceCheck struct {
	CriterionID string `json:"criterionId"`
	Status      string `json:"status"`
	Basis       string `json:"basis"`
}

func researchAcceptanceReviewSchema() json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal(independentReviewSchema(), &schema)
	schema["required"] = append(schema["required"].([]any), "acceptanceChecks")
	schema["properties"].(map[string]any)["acceptanceChecks"] = map[string]any{
		"type": "array", "minItems": 1, "maxItems": 30,
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"criterionId", "status", "basis"},
			"properties": map[string]any{
				"criterionId": map[string]any{"type": "string", "pattern": "^criterion-[1-9][0-9]*$"},
				"status":      map[string]any{"type": "string", "enum": []string{"met", "not_met"}},
				"basis":       map[string]any{"type": "string", "minLength": 1, "maxLength": 2000},
			},
		},
	}
	encoded, _ := json.Marshal(schema)
	return withRevisionPlanSchema(encoded)
}

func researchAcceptanceCriteria(contract json.RawMessage) ([]ResearchAcceptanceCriterion, error) {
	var value struct {
		SuccessCriteria []string `json:"successCriteria"`
	}
	if json.Unmarshal(contract, &value) != nil || len(value.SuccessCriteria) == 0 || len(value.SuccessCriteria) > 30 {
		return nil, fmt.Errorf("冻结研究目标缺少有效验收条件，不能开始独立审查")
	}
	criteria := make([]ResearchAcceptanceCriterion, 0, len(value.SuccessCriteria))
	for index, text := range value.SuccessCriteria {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, fmt.Errorf("研究验收条件不能为空")
		}
		criteria = append(criteria, ResearchAcceptanceCriterion{ID: fmt.Sprintf("criterion-%d", index+1), Text: text})
	}
	return criteria, nil
}

func validateResearchAcceptance(output, input json.RawMessage) error {
	var stage struct {
		Contract json.RawMessage               `json:"researchContract"`
		Criteria []ResearchAcceptanceCriterion `json:"acceptanceCriteria"`
	}
	if json.Unmarshal(input, &stage) != nil {
		return fmt.Errorf("科研验收输入无效")
	}
	expected, err := researchAcceptanceCriteria(stage.Contract)
	if err != nil {
		return err
	}
	if len(stage.Criteria) != len(expected) {
		return fmt.Errorf("验收清单与冻结研究目标不一致")
	}
	for index, criterion := range expected {
		if stage.Criteria[index] != criterion {
			return fmt.Errorf("验收清单与冻结研究目标不一致")
		}
	}
	var review struct {
		Approved    bool                      `json:"approved"`
		Checks      []ResearchAcceptanceCheck `json:"acceptanceChecks"`
		Corrections []string                  `json:"requiredCorrections"`
	}
	if json.Unmarshal(output, &review) != nil || len(review.Checks) != len(expected) {
		return fmt.Errorf("独立审查必须逐项覆盖冻结的验收条件，不能遗漏或增加条件")
	}
	wanted := make(map[string]bool, len(expected))
	for _, criterion := range expected {
		wanted[criterion.ID] = true
	}
	failed := false
	for _, check := range review.Checks {
		if !wanted[check.CriterionID] || strings.TrimSpace(check.Basis) == "" || (check.Status != "met" && check.Status != "not_met") {
			return fmt.Errorf("验收核对包含未知、重复条件或缺少判定依据")
		}
		delete(wanted, check.CriterionID)
		failed = failed || check.Status == "not_met"
	}
	if failed && (review.Approved || len(review.Corrections) == 0) {
		return fmt.Errorf("验收条件尚未满足：必须拒绝交付并给出可执行的修正要求")
	}
	return nil
}

// Preserve the existing tool contract: the full review (including criterion
// checks) remains in the immutable AI/step output. Only its core review fields
// are sent to the existing gate tool, after the extended contract is validated.
func reviewCoreForGate(output json.RawMessage) (json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(output, &value); err != nil || value == nil {
		return nil, fmt.Errorf("独立审查输出不是对象")
	}
	delete(value, "acceptanceChecks")
	delete(value, "revisionPlan")
	delete(value, "reviewFindings")
	delete(value, "suggestions")
	return json.Marshal(value)
}

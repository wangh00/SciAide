package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const dynamicResearchReviewVersion = "dynamic-independent-review-v7"

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
		"type": "array", "minItems": 1, "maxItems": 32,
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
		RequireOriginalDelivery bool                          `json:"requireOriginalDelivery"`
		RequireEvidenceGapCheck bool                          `json:"requireEvidenceGapCheck"`
		Contract                json.RawMessage               `json:"researchContract"`
		Criteria                []ResearchAcceptanceCriterion `json:"acceptanceCriteria"`
	}
	if json.Unmarshal(input, &stage) != nil {
		return fmt.Errorf("科研验收输入无效")
	}
	expected, err := researchAcceptanceCriteria(stage.Contract)
	if err != nil {
		return err
	}
	if stage.RequireOriginalDelivery {
		expected = appendOriginalDeliveryCriterion(expected)
	}
	originalDeliveryIndex := len(expected) - 1
	if stage.RequireEvidenceGapCheck {
		expected = appendEvidenceGapCriterion(expected)
		// Persisted review inputs retain their exact criterion wording across
		// upgrades. Do not invalidate an already frozen review/repair snapshot.
		if len(stage.Criteria) == len(expected) && stage.Criteria[len(expected)-1].Text == legacyEvidenceGapCriterion {
			expected[len(expected)-1].Text = legacyEvidenceGapCriterion
		}
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
	if stage.RequireOriginalDelivery {
		var scope struct {
			Screening struct {
				Coverage struct {
					Sufficient *bool `json:"sufficientForClaimedScope"`
				} `json:"coverage"`
			} `json:"evidenceScreening"`
		}
		_ = json.Unmarshal(input, &scope)
		if scope.Screening.Coverage.Sufficient != nil && !*scope.Screening.Coverage.Sufficient {
			for _, check := range review.Checks {
				if check.CriterionID == expected[originalDeliveryIndex].ID && check.Status != "not_met" {
					return fmt.Errorf("当前证据尚不足以支持原始范围；接受有限证据不等于完成原始交付，最后一项必须为 not_met")
				}
			}
		}
	}
	if stage.RequireEvidenceGapCheck {
		// A keyword occurrence cannot establish a scientific absence claim.
		// The reviewer evaluates meaning and provenance; the host checks only
		// the declared unresolved core gaps and review consistency below.
		var scope struct {
			Screening struct {
				Gaps []evidenceGap `json:"evidenceGaps"`
			} `json:"evidenceScreening"`
		}
		_ = json.Unmarshal(input, &scope)
		for _, g := range scope.Screening.Gaps {
			unresolved := g.Required && !evidenceGapResolvedForScope(g)
			if g.Status == "not_discussed_in_checked_scope" {
				if err := validateCheckedEvidenceScope(g, input); err != nil {
					// This is a defect in frozen upstream input, not something the
					// reviewer can repair. A consistent rejection must be saved so
					// the user can choose a responsible revision stage. Approval (or
					// calling this gap check met) still cannot legitimize the claim.
					if review.Approved {
						return err
					}
					unresolved = true
				}
			}
			if !unresolved {
				continue
			}
			for _, check := range review.Checks {
				if check.CriterionID == expected[len(expected)-1].ID && check.Status != "not_met" {
					return fmt.Errorf("缺项 %s 尚未核验或核读范围缺少依据；缺项审查必须为 not_met，不能把未检索到当原文未报告", g.Field)
				}
			}
		}
	}
	return nil
}

const legacyEvidenceGapCriterion = "逐项核对报告中的缺项断言（含自增表格列）：未检索到、无法读取、原文明示未报告必须区分；仅前两种不能写成原文未报告。对照 evidenceScreening.evidenceGaps、实际补查记录和当前原文，而非只复述报告。核心字段仍未核验则不通过并回到证据综合；非核心字段可省略或明确尚未核验。OR/RR/HR 与绝对发生率等不同指标不能混填。审查通过不证明通读全文或穷尽检索。"

func appendEvidenceGapCriterion(criteria []ResearchAcceptanceCriterion) []ResearchAcceptanceCriterion {
	return append(criteria, ResearchAcceptanceCriterion{ID: fmt.Sprintf("criterion-%d", len(criteria)+1), Text: "逐项核对报告中的缺项断言（含自增表格列）：区分未检索到、无法读取、原文明示未报告、已核读特定范围未讨论；检索失败不是缺失证明。对照 evidenceScreening.evidenceGaps、实际核读块范围和当前原文，而非只复述报告。范围结论只涵盖实际读取的片段，不自动等于整页或全文已读；不要求作者提供不存在机制的原句。否定、规程和引用反例不是作者缺项断言，应按语义判断。核心字段仍未核验则不通过；非核心字段可省略或明确尚未核验。OR/RR/HR 与绝对发生率等不同指标不能混填。审查通过不证明通读全文或穷尽检索。"})
}

func appendOriginalDeliveryCriterion(criteria []ResearchAcceptanceCriterion) []ResearchAcceptanceCriterion {
	return append(criteria, ResearchAcceptanceCriterion{ID: fmt.Sprintf("criterion-%d", len(criteria)+1), Text: "原始研究目标要求的核心交付已实际完成；未核验、待补充、空白字段以及仅接受有限证据初稿，均不能代替要求的结果。没有编造数据不等于已完成任务。"})
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

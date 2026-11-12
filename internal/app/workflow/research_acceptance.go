package workflow

import (
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/reviewcontract"
	"strings"
)

const dynamicResearchReviewVersion = "dynamic-independent-review-v7"
const scopedEvidenceAcceptanceVersion = "claim-scoped-evidence-v1"

// Evidence insufficiency is not itself a false claim or an unmet promised
// investigation. The reviewer judges that distinction against the contract.
const scopedEvidenceGapCriterion = "核对当前交付主张与证据边界：未检索到、无法读取、原文明示未报告、已核读范围未讨论必须区分；核读范围与原文定位仍需可验证。evidenceGaps.required 表示研究相关性，不自动等于交付失败。研究问题允许不确定结论时，准确披露尚不能确定及其依据是有效结果，不能要求必须证明有或无效应。若用户明确要求完成具体核验、全文阅读或数据分析，该工作未完成仍不满足原条件；披露局限不代替承诺的工作。不得声称未读全文已核验、把检索失败写成原文未报告或用不同指标替代。只把与当前具体主张或承诺相冲突的缺陷列为必改，其余保留为局限；不自行扩大或降低研究目标。"

func appendScopedEvidenceGapCriterion(criteria []ResearchAcceptanceCriterion) []ResearchAcceptanceCriterion {
	return append(criteria, ResearchAcceptanceCriterion{ID: fmt.Sprintf("criterion-%d", len(criteria)+1), Text: scopedEvidenceGapCriterion})
}

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
		EvidencePolicy          string                        `json:"evidenceAcceptancePolicy"`
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
		// Preserve already frozen review inputs byte-for-byte across upgrades.
		if len(stage.Criteria) >= len(expected) && stage.Criteria[len(expected)-1].Text == legacyOriginalDeliveryCriterion {
			expected[len(expected)-1].Text = legacyOriginalDeliveryCriterion
		}
	}
	originalDeliveryIndex := len(expected) - 1
	if stage.RequireEvidenceGapCheck {
		switch stage.EvidencePolicy {
		case scopedEvidenceAcceptanceVersion:
			expected = appendScopedEvidenceGapCriterion(expected)
		case "":
			expected = appendEvidenceGapCriterion(expected)
		default:
			return fmt.Errorf("未知的证据验收语义版本")
		}
		// Persisted review inputs retain their exact criterion wording across
		// upgrades. Do not invalidate an already frozen review/repair snapshot.
		if stage.EvidencePolicy == "" && len(stage.Criteria) == len(expected) && stage.Criteria[len(expected)-1].Text == legacyEvidenceGapCriterion {
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
	// Tracked reviews assess concrete claims and deliverables with current
	// evidence. An upstream coverage flag is not a whole-delivery verdict.
	// Retain the historical untracked protocol; never rewrite saved outcomes.
	if stage.RequireOriginalDelivery && decodeObject(input)["reviewHistory"] == nil {
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
			unresolved := stage.EvidencePolicy == "" && g.Required && !evidenceGapResolvedForScope(g)
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

const legacyOriginalDeliveryCriterion = "原始研究目标要求的核心交付已实际完成；未核验、待补充、空白字段以及仅接受有限证据初稿，均不能代替要求的结果。没有编造数据不等于已完成任务。"

func appendOriginalDeliveryCriterion(criteria []ResearchAcceptanceCriterion) []ResearchAcceptanceCriterion {
	return append(criteria, ResearchAcceptanceCriterion{ID: fmt.Sprintf("criterion-%d", len(criteria)+1), Text: "按本轮明确确认的交付范围和成熟度核对核心材料是否实际提供。明确允许的候选方案、待核验草稿不因尚未完成验证或实施而自动失败，但不得用空白、目录或未来待办代替承诺的具体内容；本轮明确要求完成核验的项目仍须有依据。本总括条件不扩大其他具体条件，冲突须说明并确认，不自行升级验收要求。没有编造数据不等于已完成任务；与其他条件相同的缺项复用已有问题。"})
}

// Preserve the existing tool contract: the full review (including criterion
// checks) remains in the immutable AI/step output. Only its core review fields
// are sent to the existing gate tool, after the extended contract is validated.
func reviewCoreForGate(output json.RawMessage) (json.RawMessage, error) {
	return reviewcontract.ProjectCore(output)
}

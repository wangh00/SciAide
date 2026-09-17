package workflow

import (
	"encoding/json"
	"fmt"
)

const maxConsecutivePythonRepairs = 3

// Count persisted repair requests since the last successful analysis or explicit
// restart. A process restart and a human approval do not replenish this budget.
func consecutivePythonRepairs(detail RunDetail, stepID string) int {
	count := 0
	for i := len(detail.Events) - 1; i >= 0; i-- {
		event := detail.Events[i]
		var payload struct {
			StepID       string `json:"stepId"`
			FailedStepID string `json:"failedStepId"`
			Automatic    bool   `json:"automatic"`
			Manual       bool   `json:"manual"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			continue
		}
		if (event.Type == "workflow.step_completed" || event.Type == "workflow.completed") && payload.StepID == stepID {
			break
		}
		if event.Type == "workflow.user_revision_queued" || event.Type == "workflow.review_revision_queued" {
			break
		}
		if event.Type == "workflow.step_retry_queued" && payload.StepID == stepID {
			break
		}
		if event.Type == "workflow.upstream_revision_queued" && payload.FailedStepID == stepID {
			if payload.Manual {
				break
			}
			if payload.Automatic {
				count++
			}
		}
	}
	return count
}

type implementationChange struct {
	Before string `json:"before"`
	After  string `json:"after"`
	Reason string `json:"reason"`
	Impact string `json:"impact"`
}

type implementationReview struct {
	Changes []implementationChange `json:"changes"`
}

func withImplementationChanges(schema json.RawMessage) json.RawMessage {
	var value map[string]any
	_ = json.Unmarshal(schema, &value)
	value["properties"].(map[string]any)["researchChanges"] = map[string]any{
		"type": "array", "maxItems": 20, "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"before", "after", "reason", "impact"},
			"properties": map[string]any{"before": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000}, "after": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000}, "reason": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000}, "impact": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000}},
		},
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

// Compare declared scientific metadata deterministically. This is not a proof
// of arbitrary Python semantics; independent result review remains mandatory.
func implementationConfirmation(node CompiledNode, input, output json.RawMessage) (*implementationReview, error) {
	if node.ID != "method_implementation" || node.PromptVersion != dynamicImplementationPromptVersion {
		return nil, nil
	}
	var proposed map[string]json.RawMessage
	if err := json.Unmarshal(output, &proposed); err != nil {
		return nil, err
	}
	var changes []implementationChange
	if len(proposed["researchChanges"]) == 0 || string(proposed["researchChanges"]) == "null" {
		return nil, fmt.Errorf("分析实现必须声明 researchChanges；仅技术修复且未改变研究约定时填 []，实质变化必须写明 before、after、reason、impact")
	}
	if err := json.Unmarshal(proposed["researchChanges"], &changes); err != nil {
		return nil, err
	}
	var bound struct {
		Repair *automaticPythonRepair `json:"_pythonRepair"`
	}
	if err := json.Unmarshal(input, &bound); err != nil {
		return nil, err
	}
	if bound.Repair != nil {
		var prior map[string]json.RawMessage
		if len(bound.Repair.PriorImplementation) == 0 || json.Unmarshal(bound.Repair.PriorImplementation, &prior) != nil {
			return nil, fmt.Errorf("自动修复缺少上一版分析实现快照")
		}
		for _, field := range []struct{ key, label string }{{"methodSummary", "分析方法"}, {"analysisInput", "分析参数与数据处理规则"}, {"assumptions", "研究假设"}, {"limitations", "研究局限"}, {"expectedOutputs", "预期交付"}, {"outputDeclarations", "产物声明"}} {
			before, after := prior[field.key], proposed[field.key]
			if len(before) == 0 && len(after) == 0 || rawJSONEqual(before, after) {
				continue
			}
			changes = append(changes, implementationChange{Before: field.label + "：" + string(before), After: field.label + "：" + string(after), Reason: "修订后的声明与上一版不一致", Impact: "可能改变分析解释或交付约定，请核对后再继续"})
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	return &implementationReview{Changes: changes}, nil
}

func previousImplementation(detail RunDetail, stepID string) json.RawMessage {
	for _, step := range detail.Steps {
		if step.ID != stepID {
			continue
		}
		var value struct {
			Analysis json.RawMessage `json:"analysis"`
		}
		if json.Unmarshal(step.Output, &value) == nil && len(value.Analysis) > 0 {
			return cloneRaw(value.Analysis)
		}
	}
	return nil
}

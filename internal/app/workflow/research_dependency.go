package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Binding and delivery checks must use the same citation-marker projection.
func resolveWorkflowEdgeValue(detail RunDetail, edge Edge) (json.RawMessage, error) {
	var value json.RawMessage
	if edge.FromNode == "$input" {
		var inputs map[string]json.RawMessage
		if err := json.Unmarshal(detail.Run.Inputs, &inputs); err != nil {
			return nil, fmt.Errorf("invalid workflow inputs: %w", err)
		}
		value = inputs[edge.FromPort]
	} else {
		step := findStepByNode(detail.Steps, edge.FromNode)
		if step == nil || step.Status != StepCompleted {
			return nil, fmt.Errorf("dependency %s has no committed output", edge.FromNode)
		}
		var err error
		value, err = outputPort(step.Output, edge.FromPort)
		if err != nil {
			return nil, err
		}
		source := compilationNodeMap(detail.Run.Compilation)[edge.FromNode]
		if source.Kind == NodeAIAnalysis || source.Kind == NodeAgentStage {
			for _, execution := range detail.AIExecutions {
				if execution.WorkflowStepID != step.ID || execution.Attempt != step.Attempt {
					continue
				}
				citations := workflowCitationSeedsFromSteps(detail.Run.Compilation, detail.Steps, *step)
				value = restoreWorkflowCitationMarkers(value, citations, workflowAIOutputChatRunID(step.Output, execution.ChatRunID))
				break
			}
		}
	}
	if len(value) == 0 {
		return nil, fmt.Errorf("edge %s.%s produced no value", edge.FromNode, edge.FromPort)
	}
	return value, nil
}

func validateReviewDependencies(detail RunDetail, review Step) error {
	var input map[string]json.RawMessage
	if json.Unmarshal(review.Input, &input) != nil || input == nil {
		return fmt.Errorf("独立审查输入无效，不能验证上游结果")
	}
	if usesTrackedReview(compilationNodeMap(detail.Run.Compilation)[review.NodeID]) && !rawJSONEqual(input["researchSourceContext"], mustJSON(researchSourceContext(detail))) {
		return fmt.Errorf("独立审查使用的检索来源记录已变化，请重新审查后交付")
	}
	for _, edge := range detail.Run.Compilation.Edges {
		if edge.ToNode != review.NodeID {
			continue
		}
		current, err := resolveWorkflowEdgeValue(detail, edge)
		if err != nil {
			return fmt.Errorf("独立审查依赖 %s 尚不可用，请完成相关阶段后重新审查：%w", edge.ToPort, err)
		}
		if !rawJSONEqual(input[edge.ToPort], current) {
			return fmt.Errorf("独立审查依赖 %s 与当前 %s.%s 不一致，请重新审查后交付", edge.ToPort, edge.FromNode, edge.FromPort)
		}
	}
	return validateResearchExecutionConsistency(review.Input)
}

// Verify code identity, not the scientific validity of the algorithm.
func validateResearchExecutionConsistency(input json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return fmt.Errorf("科研阶段输入无效")
	}
	implementation, hasImplementation := fields["implementationContext"]
	computed, hasComputed := fields["computedResults"]
	if !hasImplementation || !hasComputed {
		return nil
	}
	var method struct {
		Code string `json:"code"`
	}
	var result struct {
		Status     string `json:"status"`
		CodeSHA256 string `json:"codeSha256"`
	}
	if json.Unmarshal(implementation, &method) != nil || strings.TrimSpace(method.Code) == "" || json.Unmarshal(computed, &result) != nil || result.Status != "success" || result.CodeSHA256 != hashBytes([]byte(strings.TrimSpace(method.Code))) {
		return fmt.Errorf("实际方法代码与成功计算记录不一致，请重新执行分析后再形成报告和审查")
	}
	return nil
}

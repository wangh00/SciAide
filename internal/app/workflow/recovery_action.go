package workflow

import "strings"

// RecoveryAction is a read-only projection of the same rules used by Retry.
// It is not a new persisted task or a permission token. Every click is checked
// against the current durable state again; the UI cannot authorize a restart.
type RecoveryAction struct {
	StepID               string `json:"stepId"`
	Attempt              int    `json:"attempt"`
	Kind                 string `json:"kind"`
	FailureKind          string `json:"failureKind,omitempty"`
	Label                string `json:"label"`
	Summary              string `json:"summary"`
	RequiresConfirmation bool   `json:"requiresConfirmation"`
	CallsModel           bool   `json:"callsModel"`
}

func stepRecoveryAction(detail RunDetail, step Step) *RecoveryAction {
	if detail.Run.Status != RunFailed && detail.Run.Status != RunInterrupted && detail.Run.Status != RunCancelled {
		return nil
	}
	if step.Status != StepFailed && step.Status != StepInterrupted && step.Status != StepOutcomeUnknown && step.Status != StepCancelled {
		return nil
	}
	node, found := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
	if !found || node.Kind != step.NodeKind {
		return nil
	}
	if detail.Run.Status == RunCancelled {
		current := CancelledAIRecoveryStep(detail)
		if current == nil || current.ID != step.ID {
			return nil
		}
	} else if step.Status == StepCancelled {
		return nil
	}
	a := &RecoveryAction{StepID: step.ID, Attempt: step.Attempt, Kind: "retry_stage", Label: "重试当前阶段", Summary: "仅重试当前失败阶段，保留已完成结果；成功后按已确认路线继续。", RequiresConfirmation: node.SideEffect}
	if isRejectedReviewGate(node, step) {
		a.Kind, a.Label, a.Summary = "revise", "按建议修改", "独立审查明确要求修改；沿原返修卡确认起点和受影响范围。"
		return a
	}
	if step.ErrorCode == researchPrerequisiteBlockedCode {
		a.Kind, a.Label, a.Summary = "needs_input", "在对话中补充或补查", "当前执行明确缺少必要输入；先在原对话核对或补齐，再确认受影响阶段，不重复运行未修改的计算。"
		return a
	}
	if isReviewGateNode(node) {
		a.Kind, a.Label, a.Summary = "retry_gate", "重试交付核验", "报告和独立审查已保留。只重新执行本地交付核验，不重新调用模型进行审查；若核验确认报告需修改，再显示原返修入口。"
	} else if node.Tool != nil && node.Tool.QualifiedName == "builtin.research.workflow.report" {
		a.Kind, a.Label, a.Summary = "retry_publication", "重试发布报告", "只恢复报告发布及导出，保留已完成报告和独立审查，不重新检索或调用模型审查。"
	} else if node.Kind == NodeAgentStage || node.Kind == NodeAIAnalysis {
		a.CallsModel = true
		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = "当前 AI 阶段"
		}
		a.Summary = "保留仍有效的已完成阶段、原始资料和历史记录。不会自动恢复；点击后重新执行「" + name + "」，不是从断开的网络流续传；已保存且仍有效的实现分件可复用。成功后按已确认路线继续后续阶段，可能再次调用模型或审查并产生费用。"
		if step.ErrorCode == "MODEL_STREAM_INVALID" || step.ErrorCode == "MODEL_RESPONSE_INVALID" {
			a.FailureKind = "model_protocol"
			a.Summary = "模型响应协议异常，未接受本次残缺结果，不代表科研审查不通过。" + a.Summary
		}
		if step.ErrorCode == "WORKFLOW_AI_NATIVE_ACTION_MISSING" {
			a.FailureKind = "model_protocol"
			a.Summary = "模型接口未返回本阶段必选的原生调用，未进入内容修正或科学审查，不代表科研审查不通过。直接重试可能重复失败，请先核对接口的原生调用支持。" + a.Summary
		}
		if step.ErrorCode == "WORKFLOW_AI_READ_STALLED" {
			a.Summary = "重复阅读已停止，尚未形成可验收的阶段结果。请按诊断核对必需资料是否完整读取、当前模型的上下文配置或材料范围；直接重试可能再次遇到相同限制。" + a.Summary
		}
		if step.ErrorCode == "WORKFLOW_AI_MODEL_REFUSED" || step.ErrorCode == "MODEL_OUTPUT_REFUSED" {
			a.Summary = "模型明确拒绝本次请求，不会自动重发，拒绝诊断已保留。请先核对模型服务。" + a.Summary
		}
		if detail.Run.Status == RunInterrupted || detail.Run.Status == RunCancelled {
			a.Label = "恢复当前阶段"
		}
	}
	if producer, _ := invalidUpstreamImplementation(detail, step); producer != nil {
		a.Kind, a.Label, a.CallsModel = "repair_implementation", "修复实现并重试", true
		a.Summary = "冻结实现本身无法执行，将从实现阶段修复后重新计算；更早材料保留，可能产生模型和计算费用。"
	}
	return a
}

func recoveryAction(detail RunDetail) *RecoveryAction {
	if step := currentStep(detail); step != nil {
		if action := stepRecoveryAction(detail, *step); action != nil {
			return action
		}
	}
	// Interrupted/failed legacy runs may have advanced the ordinal when saved.
	// Select only an unambiguous recoverable step, never an arbitrary old error.
	var result *RecoveryAction
	for _, step := range detail.Steps {
		if action := stepRecoveryAction(detail, step); action != nil {
			if result != nil {
				return nil
			}
			result = action
		}
	}
	return result
}

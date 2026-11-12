package workflow

import (
	"encoding/json"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const researchPrerequisiteBlockedCode = "WORKFLOW_RESEARCH_PREREQUISITE_BLOCKED"

// This is an explicit protocol signal, not a scan of arbitrary research
// dicts/status strings/code. Valid partial, null or negative results stay valid.
func declaredResearchBlocker(d RunDetail, step Step, call tool.Call) (string, bool) {
	if !hasResearchAcceptance(d.Run.Compilation) || step.NodeID != "python_analysis" || step.NodeKind != NodePython || call.Result == nil || call.Result.Status != tool.ResultError {
		return "", false
	}
	var result struct {
		Status    string `json:"status"`
		Exception *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"exception"`
	}
	if json.Unmarshal(call.Result.Structured, &result) != nil || result.Status != "error" || result.Exception == nil || result.Exception.Type != "SciAideResearchBlocked" {
		return "", false
	}
	return boundedWorkflowText(nonEmptyMessage(result.Exception.Message, "研究计算明确声明缺少前置条件"), 4000), true
}

func prerequisiteRepairTargets(d RunDetail) []ResearchRevisionTarget {
	if d.Run.Status != RunFailed || d.Run.ErrorCode != researchPrerequisiteBlockedCode || len(d.RegisteredDeliverables) != 0 {
		return nil
	}
	failed := currentStep(d)
	if failed == nil || failed.NodeID != "python_analysis" || failed.NodeKind != NodePython || failed.Status != StepFailed || failed.ErrorCode != researchPrerequisiteBlockedCode || !hasResearchAcceptance(d.Run.Compilation) {
		return nil
	}
	nodes := compilationNodeMap(d.Run.Compilation)
	result := []ResearchRevisionTarget{}
	for _, id := range []string{"method_implementation", "method_selection", "data_preflight"} {
		stage := findStepByNode(d.Steps, id)
		if stage == nil || stage.Status != StepCompleted || stage.Ordinal >= failed.Ordinal || !workflowNodeReaches(d.Run.Compilation, id, failed.NodeID) {
			continue
		}
		if id == "data_preflight" && !revisionDataChain(d) {
			continue
		}
		if id != "data_preflight" && nodes[id].Kind != NodeAgentStage && nodes[id].Kind != NodeAIAnalysis {
			continue
		}
		result = append(result, ResearchRevisionTarget{NodeID: id, Label: nonEmptyMessage(nodes[id].Name, id), RepeatsSideEffects: revisionSuffixSideEffects(d, stage.Ordinal)})
	}
	return result
}

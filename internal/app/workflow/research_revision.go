package workflow

import "fmt"

type ResearchRevisionTarget struct {
	NodeID             string `json:"nodeId"`
	Label              string `json:"label"`
	RepeatsSideEffects bool   `json:"repeatsSideEffects"`
}

// Only explicit user-selected, completed method/result ancestors may be
// revised. The original question and frozen input data remain unchanged.
func researchRevisionTargets(detail RunDetail) []ResearchRevisionTarget {
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
	producer, _, _, err := reviewRevisionContext(detail, *gate)
	if err != nil {
		return nil
	}
	return researchRevisionCandidates(detail, *producer, *gate)
}

func researchRevisionCandidates(detail RunDetail, producer, gate Step) []ResearchRevisionTarget {
	nodes := compilationNodeMap(detail.Run.Compilation)
	allowed := stringSetOf([]string{producer.NodeID, "method_selection", "research_design", "method_implementation", "result_interpretation", "report_drafting"})
	result := []ResearchRevisionTarget{}
	for index := len(detail.Steps) - 1; index >= 0; index-- {
		step := detail.Steps[index]
		node := nodes[step.NodeID]
		if !allowed[step.NodeID] || step.Status != StepCompleted || step.Ordinal > producer.Ordinal || (node.Kind != NodeAIAnalysis && node.Kind != NodeAgentStage) || !workflowNodeReaches(detail.Run.Compilation, step.NodeID, gate.NodeID) {
			continue
		}
		target := ResearchRevisionTarget{NodeID: step.NodeID, Label: nonEmptyMessage(node.Name, step.NodeID)}
		for _, later := range detail.Steps {
			if later.Ordinal >= step.Ordinal && later.Ordinal <= gate.Ordinal && nodes[later.NodeID].SideEffect {
				target.RepeatsSideEffects = true
			}
		}
		result = append(result, target)
	}
	return result
}

func reviewRevisionFromTarget(detail RunDetail, gate Step, targetID string) (*Step, *Step, reviewRevision, error) {
	producer, review, revision, err := reviewRevisionContext(detail, gate)
	if err != nil {
		return nil, nil, reviewRevision{}, err
	}
	if targetID == "" {
		return nil, nil, reviewRevision{}, fmt.Errorf("请明确选择返修起点，或确认采用当前审查的建议")
	}
	valid := false
	for _, target := range researchRevisionTargets(detail) {
		if target.NodeID == targetID {
			valid = true
			break
		}
	}
	if !valid {
		return nil, nil, reviewRevision{}, fmt.Errorf("所选返修起点不属于当前审查失败的可修订阶段")
	}
	if targetID == producer.NodeID {
		return producer, review, revision, nil
	}
	target := findStepByNode(detail.Steps, targetID)
	prior, err := outputPort(target.Output, "analysis")
	if err != nil {
		return nil, nil, reviewRevision{}, err
	}
	revision.ProducerStepID, revision.ProducerNodeID, revision.ProducerAttempt = target.ID, target.NodeID, target.Attempt
	revision.NextProducerAttempt = target.Attempt + 1
	revision.PriorSubject, revision.PriorSubjectSHA256 = cloneRaw(prior), hashJSON(prior)
	return target, review, revision, nil
}

package workflow

import (
	"encoding/json"
	"fmt"
)

func validateRevisionWriteSnapshot(r reviewRevision) error {
	needed := r.WriteContractVersion != "" || len(r.WriteContract) > 0 || r.WriteContractSHA256 != ""
	if !needed {
		return nil
	} // legacy events never had this navigation
	var contract revisionWriteContract
	if r.WriteContractVersion != revisionWriteVersion || len(r.WriteContract) == 0 || r.WriteContractSHA256 == "" || hashJSON(r.WriteContract) != r.WriteContractSHA256 || json.Unmarshal(r.WriteContract, &contract) != nil || contract.Version != revisionWriteVersion || len(contract.Objects) == 0 {
		return fmt.Errorf("返修写入对象快照缺失或摘要不一致；已停止，不能丢弃绑定后继续生成")
	}
	return nil
}

// Validate before input binding, including downstream stages whose producer
// has already completed. A corrupt new receipt is a host error, not a missing
// checklist that a model should silently work around.
func validatePendingRevisionWriteSnapshot(d RunDetail, nodeID string) error {
	step := findStepByNode(d.Steps, nodeID)
	if step == nil || step.Status != StepQueued {
		return nil
	}
	for i := len(d.Events) - 1; i >= 0; i-- {
		ev := d.Events[i]
		if ev.Type == "workflow.user_revision_queued" {
			return nil
		}
		if ev.Type != "workflow.review_revision_queued" {
			continue
		}
		var r reviewRevision
		if json.Unmarshal(ev.Payload, &r) != nil {
			return fmt.Errorf("返修写入对象快照记录无法读取；已停止")
		}
		start, gate := findStep(d.Steps, r.ProducerStepID), findStep(d.Steps, r.GateStepID)
		if start == nil || gate == nil || step.Ordinal < start.Ordinal || step.Ordinal >= gate.Ordinal {
			return nil
		}
		return validateRevisionWriteSnapshot(r)
	}
	return nil
}

type stageReviewCorrection struct {
	Index       int      `json:"correctionIndex"`
	NodeID      string   `json:"nodeId"`
	Text        string   `json:"text"`
	EditTargets []string `json:"editTargets,omitempty"`
}
type stageReviewCorrections struct {
	DesignWriteIsolation            *designReviewIsolationReceipt `json:"designWriteIsolation,omitempty"`
	DesignWriteIsolationInstruction string                        `json:"designWriteIsolationInstruction,omitempty"`
	ReviewHash                      string                        `json:"reviewOutputSha256"`
	Corrections                     []stageReviewCorrection       `json:"corrections"`
	AllKnownCorrections             []string                      `json:"allKnownCorrections,omitempty"`
	PriorOutput                     json.RawMessage               `json:"priorOutput,omitempty"`
	WriteContract                   json.RawMessage               `json:"writeContract,omitempty"`
}

// Each responsible replayed stage receives its own generation checklist.
func pendingStageReviewCorrections(d RunDetail, nodeID string) *stageReviewCorrections {
	step := findStepByNode(d.Steps, nodeID)
	if step == nil || step.Status != StepQueued {
		return nil
	}
	for i := len(d.Events) - 1; i >= 0; i-- {
		ev := d.Events[i]
		if ev.Type == "workflow.user_revision_queued" {
			return nil
		}
		if ev.Type != "workflow.review_revision_queued" {
			continue
		}
		var r reviewRevision
		if json.Unmarshal(ev.Payload, &r) != nil || hashJSON(r.IndependentReview) != r.ReviewOutputSHA256 || hashJSON(r.PriorSubject) != r.PriorSubjectSHA256 {
			return nil
		}
		if validateRevisionWriteSnapshot(r) != nil {
			return nil
		}
		start, gate := findStepByNode(d.Steps, r.ProducerNodeID), findStep(d.Steps, r.GateStepID)
		if start == nil || gate == nil || start.ID != r.ProducerStepID || step.Ordinal < start.Ordinal || step.Ordinal >= gate.Ordinal {
			return nil
		}
		var review struct {
			Corrections []string                   `json:"requiredCorrections"`
			Plan        []researchRevisionPlanItem `json:"revisionPlan"`
		}
		if json.Unmarshal(r.IndependentReview, &review) != nil {
			return nil
		}
		result := &stageReviewCorrections{ReviewHash: r.ReviewOutputSHA256, Corrections: []stageReviewCorrection{}, AllKnownCorrections: review.Corrections}
		if r.DesignWriteIsolation != nil {
			receipt := *r.DesignWriteIsolation
			receipt.EditTargets = append([]string(nil), receipt.EditTargets...)
			result.DesignWriteIsolation = &receipt
			result.DesignWriteIsolationInstruction = designWriteIsolationInstruction
		}
		owners := map[string]string{}
		if len(r.WriteContract) > 0 {
			if r.WriteContractSHA256 == "" || hashJSON(r.WriteContract) != r.WriteContractSHA256 {
				return nil
			}
			result.WriteContract = cloneRaw(r.WriteContract)
			var contract revisionWriteContract
			if json.Unmarshal(r.WriteContract, &contract) != nil {
				return nil
			}
			for _, object := range contract.Objects {
				owners[object.ID] = object.OwnerNodeID
			}
		}
		for _, p := range review.Plan {
			local := []string{}
			for _, id := range p.EditTargets {
				if owners[id] == nodeID {
					local = append(local, id)
				}
			}
			if (p.NodeID == nodeID || len(local) > 0) && p.CorrectionIndex >= 0 && p.CorrectionIndex < len(review.Corrections) {
				result.Corrections = append(result.Corrections, stageReviewCorrection{Index: p.CorrectionIndex, NodeID: nodeID, Text: review.Corrections[p.CorrectionIndex], EditTargets: local})
			}
		}
		// Downstream synthesis/report/review must see the complete repair goal,
		// even when the original plan assigned every issue to selection/screening.
		if len(result.Corrections) == 0 && nodeID != "method_selection" && nodeID != "report_drafting" && nodeID != "independent_review" {
			return nil
		}
		// A completed collection/batch is not a replacement for the reviewed
		// stage. Keep both its output and its attempt out of the repair baseline.
		// In particular, changing priorOutput after each batch would change the
		// evidence sourceKey and invalidate the checkpoint just saved.
		executions := map[string]*AIExecution{}
		for j := range d.AIExecutions {
			e := &d.AIExecutions[j]
			if e.WorkflowStepID == step.ID {
				executions[e.ID] = e
			}
		}
		collections, collectionAttempts := map[string]bool{}, map[int]bool{}
		for index, event := range d.Events {
			switch event.Type {
			case "workflow.ai_completed", "workflow.completed", "workflow.agent_stage_review_requested":
				var done struct {
					ExecutionID string `json:"executionId"`
				}
				if index > i && json.Unmarshal(event.Payload, &done) == nil && done.ExecutionID != "" && executions[done.ExecutionID] != nil {
					return nil
				}
			case "workflow.selected_evidence_batch", "workflow.indexed_evidence_read", "workflow.local_evidence_requested":
				var part struct {
					ExecutionID string `json:"executionId"`
					StepID      string `json:"stepId"`
					Attempt     int    `json:"attempt"`
				}
				if json.Unmarshal(event.Payload, &part) == nil {
					if part.ExecutionID != "" {
						collections[part.ExecutionID] = true
					}
					if part.StepID == step.ID && part.Attempt > 0 {
						collectionAttempts[part.Attempt] = true
					}
				}
			}
		}
		if step.ID == r.ProducerStepID {
			// The user's confirmation already froze this exact prior result.
			// Neither retries nor intermediate continuations may replace it.
			result.PriorOutput = cloneRaw(r.PriorSubject)
			return result
		}
		var prior *AIExecution
		for j := range d.AIExecutions {
			e := &d.AIExecutions[j]
			completedAt := e.CreatedAt
			if e.CompletedAt != nil {
				completedAt = *e.CompletedAt
			}
			if e.WorkflowStepID != step.ID || e.Status != "completed" || len(e.Output) == 0 || rawJSONEqual(e.Output, raw(`{}`)) || collections[e.ID] || collectionAttempts[e.Attempt] || (!ev.CreatedAt.IsZero() && completedAt.After(ev.CreatedAt)) {
				continue
			}
			if prior == nil || e.Attempt > prior.Attempt {
				prior = e
			}
		}
		if prior != nil {
			result.PriorOutput = cloneRaw(prior.Output)
		}
		return result
	}
	return nil
}

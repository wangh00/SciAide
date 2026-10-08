package workflow

import "encoding/json"

type stageReviewCorrection struct {
	Index  int    `json:"correctionIndex"`
	NodeID string `json:"nodeId"`
	Text   string `json:"text"`
}
type stageReviewCorrections struct {
	ReviewHash          string                  `json:"reviewOutputSha256"`
	Corrections         []stageReviewCorrection `json:"corrections"`
	AllKnownCorrections []string                `json:"allKnownCorrections,omitempty"`
	PriorOutput         json.RawMessage         `json:"priorOutput,omitempty"`
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
		for _, p := range review.Plan {
			if p.NodeID == nodeID && p.CorrectionIndex >= 0 && p.CorrectionIndex < len(review.Corrections) {
				result.Corrections = append(result.Corrections, stageReviewCorrection{p.CorrectionIndex, nodeID, review.Corrections[p.CorrectionIndex]})
			}
		}
		// Downstream synthesis/report/review must see the complete repair goal,
		// even when the original plan assigned every issue to selection/screening.
		if len(result.Corrections) == 0 && nodeID != "method_selection" && nodeID != "report_drafting" && nodeID != "independent_review" {
			return nil
		}
		var latest *AIExecution
		for j := range d.AIExecutions {
			e := &d.AIExecutions[j]
			if e.WorkflowStepID == step.ID && e.Status == "completed" && !rawJSONEqual(e.Output, raw(`{}`)) && (latest == nil || e.Attempt > latest.Attempt) {
				latest = e
			}
		}
		if latest != nil {
			for _, event := range d.Events[i+1:] {
				var done struct {
					ExecutionID string `json:"executionId"`
				}
				if (event.Type == "workflow.ai_completed" || event.Type == "workflow.agent_stage_review_requested") && json.Unmarshal(event.Payload, &done) == nil && done.ExecutionID == latest.ID {
					return nil
				}
			}
			result.PriorOutput = cloneRaw(latest.Output)
		}
		return result
	}
	return nil
}

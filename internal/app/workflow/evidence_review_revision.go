package workflow

import "encoding/json"

// A newer user revision supersedes the old review repair. During a review
// repair, only explicitly checkpointed batch attempts may extend its lifetime.
func evidenceReviewRevision(detail RunDetail, nodeID string) (reviewRevision, bool) {
	step := findStepByNode(detail.Steps, nodeID)
	if step == nil || step.Status != StepQueued {
		return reviewRevision{}, false
	}
	for i := len(detail.Events) - 1; i >= 0; i-- {
		event := detail.Events[i]
		if event.Type == "workflow.user_revision_queued" {
			return reviewRevision{}, false
		}
		if event.Type != "workflow.review_revision_queued" {
			continue
		}
		var r reviewRevision
		if json.Unmarshal(event.Payload, &r) != nil || r.ProducerNodeID != nodeID || r.ProducerStepID != step.ID || r.NextProducerAttempt > step.Attempt+1 || r.NextProducerAttempt < 1 {
			return reviewRevision{}, false
		}
		if hashJSON(r.PriorSubject) != r.PriorSubjectSHA256 || hashJSON(r.IndependentReview) != r.ReviewOutputSHA256 {
			return reviewRevision{}, false
		}
		for attempt := r.NextProducerAttempt; attempt <= step.Attempt; attempt++ {
			found := false
			for _, execution := range detail.AIExecutions {
				if execution.WorkflowStepID != step.ID || execution.Attempt != attempt {
					continue
				}
				// A failed/cancelled batch may be retried without losing corrections.
				if execution.Status != "completed" {
					found = true
					break
				}
				for _, e := range detail.Events[i+1:] {
					if e.Type == "workflow.local_evidence_requested" {
						var continuation struct {
							StepID  string `json:"stepId"`
							Attempt int    `json:"attempt"`
						}
						if json.Unmarshal(e.Payload, &continuation) == nil && continuation.StepID == step.ID && continuation.Attempt == attempt {
							found = true
							break
						}
					}
					var batch struct {
						ExecutionID string `json:"executionId"`
					}
					if e.Type == "workflow.selected_evidence_batch" && json.Unmarshal(e.Payload, &batch) == nil && batch.ExecutionID == execution.ID {
						found = true
						break
					}
				}
			}
			if !found {
				return reviewRevision{}, false
			}
		}
		return r, true
	}
	return reviewRevision{}, false
}

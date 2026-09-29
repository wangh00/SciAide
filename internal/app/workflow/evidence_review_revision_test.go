package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEvidenceReviewTargetAndBatchCorrectionLifetime(t *testing.T) {
	d := revisionEvidenceFixture(t)
	d.Run.Status = RunFailed
	gate := findStepByNode(d.Steps, "review_gate")
	gate.Status = StepFailed
	// Use the rejected, frozen gate from the existing review fixture.
	f := reviewRevisionFixture()
	gate.Input = f.Steps[2].Input
	gate.InputSHA256 = hashJSON(gate.Input)
	s := findStepByNode(d.Steps, "evidence_screening")
	s.Output = raw(`{"analysis":{"summary":"wrong comparison"}}`)
	s.Attempt = 3
	found := false
	for _, target := range researchRevisionTargets(d) {
		if target.NodeID == s.NodeID {
			found = true
		}
	}
	if !found {
		t.Fatal("review cannot revise evidence synthesis")
	}
	planTargets := revisionPlanTargetsForReview(d, "review")
	found = false
	for _, target := range planTargets {
		if target.NodeID == s.NodeID {
			found = true
		}
	}
	if !found {
		t.Fatal("evidence target is not offered to the reviewer")
	}
	_, _, revision, err := reviewRevisionFromTarget(d, *gate, s.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(revision)
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.review_revision_queued", Payload: payload})
	s.Status = StepQueued
	first, ok := evidenceReviewRevision(d, s.NodeID)
	if !ok {
		t.Fatal("first revision missing")
	}
	n := compilationNodeMap(d.Run.Compilation)[s.NodeID]
	n.Arguments = raw(`{"documentIds":["doc"],"candidates":[]}`)
	// Isolate bound correction context from unrelated fixture edges.
	d.Run.Compilation.Edges = nil
	input, err := bindNodeInput(d, n)
	if err != nil || !strings.Contains(string(input), "_reviewRevision") {
		t.Fatal(string(input), err)
	}
	old := raw(`{"documentIds":["doc"],"candidates":[]}`)
	d.AIExecutions = append(d.AIExecutions, AIExecution{ID: "old", WorkflowStepID: s.ID, Attempt: 1, Status: "completed", Output: raw(`{"documentAnalyses":[{"documentId":"doc","finding":"old error"}]}`)})
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.selected_evidence_batch", Payload: rawObject(map[string]any{"executionId": "old", "sourceKey": hashJSON(old)})})
	prepared, err := prepareSelectedEvidence(d, input)
	if err != nil || strings.Contains(string(prepared), "old error") {
		t.Fatal("stale analysis reused", err)
	}
	// Continuation keeps the same correction and therefore the same batch key.
	s.Attempt++
	d.AIExecutions = append(d.AIExecutions, AIExecution{ID: "new", WorkflowStepID: s.ID, Attempt: s.Attempt, Status: "completed"})
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.selected_evidence_batch", Payload: raw(`{"executionId":"new"}`)})
	next, ok := evidenceReviewRevision(d, s.NodeID)
	if !ok || hashJSON(mustJSON(first)) != hashJSON(mustJSON(next)) {
		t.Fatal("batch lost correction")
	}
	input2, err := bindNodeInput(d, n)
	if err != nil || hashJSON(input) != hashJSON(input2) {
		t.Fatal("batch source identity changed", err)
	}
	// A completed coverage attempt is not a batch and ends this generation.
	s.Attempt++
	d.AIExecutions = append(d.AIExecutions, AIExecution{ID: "coverage", WorkflowStepID: s.ID, Attempt: s.Attempt, Status: "completed"})
	if _, ok := evidenceReviewRevision(d, s.NodeID); ok {
		t.Fatal("completed repair leaked into a later retry")
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued", Payload: raw(`{}`)})
	if _, ok := evidenceReviewRevision(d, s.NodeID); ok {
		t.Fatal("older review superseded user revision")
	}
}

func TestCompletedEvidenceTargetsAreNotDuplicated(t *testing.T) {
	d := revisionEvidenceFixture(t)
	count := 0
	for _, target := range completedRevisionTargets(d) {
		if target.NodeID == "evidence_screening" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("evidence target count", count)
	}
}

package chat

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func checkpointFixture() PlannerCheckpoint {
	return PlannerCheckpoint{Version: 1, RunID: "r", ExecutionID: "e", WorkflowRunID: "w", WorkflowStepID: "s", Attempt: 1, InputSHA256: strings.Repeat("a", 64), PromptSHA256: strings.Repeat("b", 64), SchemaSHA256: strings.Repeat("c", 64), TurnIndex: 1, RequestInFlight: true, UpdatedAt: time.Now()}
}

func TestPlannerCheckpointBudgetTransitionsAreMonotonicAndBounded(t *testing.T) {
	first := checkpointFixture()
	ready := first
	ready.RequestInFlight, ready.ContentCorrections = false, 1
	ready.Candidate, ready.CandidateTurnIndex, ready.Correction = "rejected baseline", 1, "specific format failure"
	ready.CandidateSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(ready.Candidate)))
	if err := ValidatePlannerCheckpoint(ready); err != nil || !ValidPlannerCheckpointTransition(first, ready) {
		t.Fatal("normal first correction rejected", err)
	}
	next := ready
	next.TurnIndex, next.RequestInFlight = 2, true
	if !ValidPlannerCheckpointTransition(ready, next) {
		t.Fatal("metered next request rejected")
	}
	for _, mutate := range []func(*PlannerCheckpoint){
		func(v *PlannerCheckpoint) { v.ContentCorrections = 0 },
		func(v *PlannerCheckpoint) { v.SkillCorrections = 1 },
		func(v *PlannerCheckpoint) { v.ExecutionID = "other" },
		func(v *PlannerCheckpoint) { v.Attempt++ },
		func(v *PlannerCheckpoint) { v.Candidate = "rewritten" },
		func(v *PlannerCheckpoint) { v.CandidateTurnIndex++ },
		func(v *PlannerCheckpoint) { v.TurnIndex++ },
	} {
		bad := next
		mutate(&bad)
		if ValidPlannerCheckpointTransition(ready, bad) {
			t.Fatal("request rewrote checkpoint or renewed allowance", bad)
		}
	}
	for _, mutate := range []func(*PlannerCheckpoint){
		func(v *PlannerCheckpoint) { v.ContentCorrections = -1 },
		func(v *PlannerCheckpoint) { v.ContentCorrections = 3 },
		func(v *PlannerCheckpoint) { v.SkillCorrections = 3 },
		func(v *PlannerCheckpoint) { v.ProgressCorrections = 4 },
		func(v *PlannerCheckpoint) { v.CandidateSHA256 = strings.Repeat("0", 64) },
		func(v *PlannerCheckpoint) { v.CandidateTurnIndex = 2 },
		func(v *PlannerCheckpoint) { v.SkillPending = true; v.SkillCorrections = 0 },
		func(v *PlannerCheckpoint) { v.Candidate = strings.Repeat("文", 200001) },
	} {
		bad := ready
		mutate(&bad)
		if ValidatePlannerCheckpoint(bad) == nil {
			t.Fatal("invalid checkpoint accepted")
		}
	}
	ready.SubmissionPhase = true
	next.SubmissionPhase = false
	if ValidPlannerCheckpointTransition(ready, next) {
		t.Fatal("submission phase reopened")
	}
}

func TestPlannerCheckpointResolvedSkillFeedbackDoesNotAcceptCandidateOrRefund(t *testing.T) {
	ready := checkpointFixture()
	ready.RequestInFlight = false
	ready.SkillPending = true
	ready.SkillCorrections = 1
	ready.Correction = "missing Skill"
	ready.Candidate = "still rejected"
	ready.CandidateTurnIndex = 1
	ready.CandidateSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(ready.Candidate)))
	next := ready
	next.RequestInFlight = true
	next.TurnIndex = 2
	next.SkillPending = false
	next.Correction = "actual snapshots now satisfy loads; resubmit"
	if !ValidPlannerCheckpointTransition(ready, next) {
		t.Fatal("actual Skill recheck could not update feedback")
	}
	next.Candidate = ""
	if ValidPlannerCheckpointTransition(ready, next) {
		t.Fatal("Skill recheck accepted or discarded rejected baseline")
	}
}

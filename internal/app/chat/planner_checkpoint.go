package chat

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"time"
	"unicode/utf8"
)

// PlannerCheckpoint is private host state, never an accepted research result.
// It belongs only to this Chat Run, not the next explicitly retried attempt.
type PlannerCheckpoint struct {
	Version             int    `json:"version"`
	RunID               string `json:"runId"`
	ExecutionID         string `json:"executionId"`
	WorkflowRunID       string `json:"workflowRunId"`
	WorkflowStepID      string `json:"workflowStepId"`
	Attempt             int    `json:"attempt"`
	InputSHA256         string `json:"inputSha256"`
	PromptSHA256        string `json:"promptSha256"`
	SchemaSHA256        string `json:"schemaSha256"`
	TurnIndex           int    `json:"turnIndex"`
	RequestInFlight     bool   `json:"requestInFlight"`
	ProgressCorrections int    `json:"progressCorrections"`
	ContentCorrections  int    `json:"contentCorrections"`
	SkillCorrections    int    `json:"skillCorrections"`
	Correction          string `json:"correction"`
	// Materialized only in memory from the referenced original journal. Never
	// JSON-escape a large candidate into the checkpoint event a second time.
	Candidate             string    `json:"-"`
	CandidateTurnIndex    int       `json:"candidateTurnIndex"`
	CandidateSHA256       string    `json:"candidateSha256"`
	SkillPending          bool      `json:"skillPending"`
	SubmissionPhase       bool      `json:"submissionPhase"`
	LastContentDiagnostic string    `json:"lastContentDiagnostic"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

type PlannerCheckpointRepository interface {
	PlannerCheckpoint(context.Context, string) (PlannerCheckpoint, bool, error)
	SavePlannerCheckpoint(context.Context, PlannerCheckpoint) error
	CheckPlannerCheckpointActive(context.Context, string, WorkflowAIExecution) error
}

func (v PlannerCheckpoint) Matches(runID string, c WorkflowAIExecution) bool {
	return v.RunID == runID && v.ExecutionID == c.ID && v.WorkflowRunID == c.WorkflowRunID && v.WorkflowStepID == c.WorkflowStepID && v.Attempt == c.Attempt &&
		v.InputSHA256 == c.InputSHA256 && v.PromptSHA256 == c.PromptSHA256 && v.SchemaSHA256 == c.OutputSchemaSHA256
}

func ValidatePlannerCheckpoint(v PlannerCheckpoint) error {
	if v.Version != 1 || v.RunID == "" || v.ExecutionID == "" || v.WorkflowRunID == "" || v.WorkflowStepID == "" || v.Attempt < 1 || v.TurnIndex < 1 || v.UpdatedAt.IsZero() ||
		len(v.InputSHA256) != 64 || len(v.PromptSHA256) != 64 || len(v.SchemaSHA256) != 64 ||
		v.ProgressCorrections < 0 || v.ProgressCorrections > 3 || v.ContentCorrections < 0 || v.ContentCorrections > 2 || v.SkillCorrections < 0 || v.SkillCorrections > 2 ||
		!utf8.ValidString(v.Candidate) || utf8.RuneCountInString(v.Candidate) > 200_000 || len(v.Correction) > 64_000 || len(v.LastContentDiagnostic) > 32_000 {
		return fmt.Errorf("invalid planner checkpoint bounds or identity")
	}
	if v.Candidate == "" {
		if v.CandidateTurnIndex != 0 || v.CandidateSHA256 != "" || v.SkillPending {
			return fmt.Errorf("planner checkpoint has a candidate reference without its rejected baseline")
		}
	} else if v.CandidateTurnIndex < 1 || v.CandidateTurnIndex > v.TurnIndex || v.CandidateSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(v.Candidate))) {
		return fmt.Errorf("planner checkpoint candidate identity changed")
	}
	if v.SkillPending && (v.SkillCorrections == 0 || v.Correction == "") {
		return fmt.Errorf("planner checkpoint has an unmetered Skill correction")
	}
	return nil
}

// The only normal transitions are a new paid request and its durable decision.
// In-flight state is never replayed as zero corrections after an interruption.
func ValidPlannerCheckpointTransition(previous, next PlannerCheckpoint) bool {
	if previous.RunID != next.RunID || previous.ExecutionID != next.ExecutionID || previous.WorkflowRunID != next.WorkflowRunID || previous.WorkflowStepID != next.WorkflowStepID ||
		previous.Attempt != next.Attempt || previous.InputSHA256 != next.InputSHA256 || previous.PromptSHA256 != next.PromptSHA256 || previous.SchemaSHA256 != next.SchemaSHA256 || previous.SubmissionPhase && !next.SubmissionPhase {
		return false
	}
	if !previous.RequestInFlight && next.RequestInFlight && next.TurnIndex == previous.TurnIndex+1 {
		if previous.ProgressCorrections != next.ProgressCorrections || previous.ContentCorrections != next.ContentCorrections || previous.SkillCorrections != next.SkillCorrections ||
			previous.Candidate != next.Candidate || previous.CandidateTurnIndex != next.CandidateTurnIndex || previous.CandidateSHA256 != next.CandidateSHA256 || previous.LastContentDiagnostic != next.LastContentDiagnostic {
			return false
		}
		// Actual Skill results can resolve a pending missing-Skill feedback, but
		// cannot accept the baseline or replenish any correction allowance.
		return previous.SkillPending && !next.SkillPending || previous.SkillPending == next.SkillPending && previous.Correction == next.Correction
	}
	if previous.RequestInFlight && !next.RequestInFlight && next.TurnIndex == previous.TurnIndex {
		a := next.ProgressCorrections - previous.ProgressCorrections
		b := next.ContentCorrections - previous.ContentCorrections
		c := next.SkillCorrections - previous.SkillCorrections
		return a >= 0 && b >= 0 && c >= 0 && a+b+c <= 1
	}
	return false
}

func SamePlannerCheckpoint(a, b PlannerCheckpoint) bool {
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/model"
)

const ImplementationPartsVersion = "impl-parts-v1"
const ImplementationPatchVersion = "impl-patch-v1"
const MaxImplementationPartCorrections = 2

// ImplementationDraft contains only source from completed model responses.
// It is not an accepted stage result and must never be sent to the Kernel.
// Each update is an immutable event, bound to the execution that saved it.
type ImplementationDraft struct {
	// Read-only host flag, not provider data or durable state.
	AdoptionPending bool            `json:"-"`
	RunID           string          `json:"runId"`
	ExecutionID     string          `json:"executionId"`
	WorkflowRunID   string          `json:"workflowRunId"`
	WorkflowStepID  string          `json:"workflowStepId"`
	Attempt         int             `json:"attempt"`
	InputSHA256     string          `json:"inputSha256"`
	PromptSHA256    string          `json:"promptSha256"`
	SchemaSHA256    string          `json:"schemaSha256"`
	GenerationID    string          `json:"generationId"`
	PartCount       int             `json:"partCount"`
	Metadata        json.RawMessage `json:"metadata"`
	Code            string          `json:"code"`
	Complete        bool            `json:"complete"`
	Rejected        bool            `json:"rejected"`
	CorrectionCount int             `json:"correctionCount"`
	// Format corrections belong to the next part, not to full scientific
	// validation or connection retries. Reset only after that part is saved.
	PartCorrectionCount   int                           `json:"partCorrectionCount,omitempty"`
	PartValidationError   string                        `json:"partValidationError,omitempty"`
	PartRejectedCandidate string                        `json:"partRejectedCandidate,omitempty"`
	PartControlName       string                        `json:"partControlName,omitempty"`
	SourcePatch           *ImplementationSourcePatch    `json:"sourcePatch,omitempty"`
	ValidationError       string                        `json:"validationError,omitempty"`
	SourceEvidence        []model.CompletedToolEvidence `json:"sourceEvidence,omitempty"`
	SourceTurnIndex       int                           `json:"sourceTurnIndex"`
	UpdatedAt             time.Time                     `json:"updatedAt"`
}

func ValidateImplementationDraftState(v ImplementationDraft) error {
	if v.PartCount < 0 || v.PartCount > 64 || len(v.Code) > 120_000 || !json.Valid(v.Metadata) || len(v.Metadata) > 100_000 || v.CorrectionCount < 0 || v.CorrectionCount > 2 || v.PartCorrectionCount < 0 || v.PartCorrectionCount > MaxImplementationPartCorrections || len(v.PartValidationError) > 16_000 || len(v.PartRejectedCandidate) > 33_000 || len(v.ValidationError) > 16_000 {
		return fmt.Errorf("invalid implementation checkpoint bounds")
	}
	if v.PartCorrectionCount > 0 {
		if v.Complete || v.Rejected || v.PartValidationError == "" || (v.PartControlName != "submit_implementation_part" && v.PartControlName != "submit_implementation_patch") || v.SourcePatch != nil {
			return fmt.Errorf("invalid implementation correction checkpoint")
		}
	} else if v.PartValidationError != "" || v.PartRejectedCandidate != "" || v.PartControlName != "" {
		return fmt.Errorf("implementation correction diagnostic without a consumed correction")
	}
	if v.PartCount == 0 {
		if v.Code != "" || v.Complete || v.SourcePatch != nil || (!v.Rejected && v.PartCorrectionCount == 0) || (v.Rejected && v.CorrectionCount == 0) {
			return fmt.Errorf("empty implementation checkpoint is neither a rejection nor a format correction")
		}
	} else if strings.TrimSpace(v.Code) == "" {
		return fmt.Errorf("saved implementation source is empty")
	}
	if v.SourcePatch != nil && (!v.Complete || v.PartCount != 1) {
		return fmt.Errorf("implementation source patch is not a complete atomic candidate")
	}
	return nil
}

type ImplementationDraftRepository interface {
	// Resume only the same execution or its immediately preceding failed/
	// cancelled attempt with the identical frozen input, prompt and schema.
	ImplementationDraft(context.Context, string) (ImplementationDraft, bool, error)
	SaveImplementationDraft(context.Context, ImplementationDraft) error
}

// Bounded, redacted diagnostics for each failed connection, even if a later
// retry succeeds or the user cancels. No draft text or reasoning is included.
type ModelRecoveryFailure struct {
	RunID         string    `json:"runId"`
	TurnIndex     int       `json:"turnIndex"`
	Phase         string    `json:"phase"`
	Attempt       int       `json:"attempt"`
	Code          string    `json:"code"`
	Details       string    `json:"details"`
	PartialChars  int       `json:"partialChars"`
	ElapsedMillis int64     `json:"elapsedMillis"`
	StartedAt     time.Time `json:"startedAt"`
	CompletedAt   time.Time `json:"completedAt"`
}

type ModelRecoveryFailureRepository interface {
	RecordModelRecoveryFailure(context.Context, ModelRecoveryFailure) error
}

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

func draftMatchesContract(v chat.ImplementationDraft, c chat.WorkflowAIExecution) bool {
	return v.ExecutionID == c.ID && v.WorkflowRunID == c.WorkflowRunID && v.WorkflowStepID == c.WorkflowStepID && v.Attempt == c.Attempt &&
		v.InputSHA256 == c.InputSHA256 && v.PromptSHA256 == c.PromptSHA256 && v.SchemaSHA256 == c.OutputSchemaSHA256 &&
		c.NodeID == "method_implementation" && strings.Contains(c.PromptVersion, chat.ImplementationPartsVersion)
}

type implementationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readImplementationDraft(ctx context.Context, q implementationQueryer, runID string) (chat.ImplementationDraft, bool, error) {
	var payload []byte
	err := q.QueryRowContext(ctx, `SELECT payload_json FROM run_events WHERE aggregate_id=? AND event_type='run.implementation_saved' ORDER BY sequence DESC LIMIT 1`, runID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return chat.ImplementationDraft{}, false, nil
	}
	if err != nil {
		return chat.ImplementationDraft{}, false, err
	}
	var v chat.ImplementationDraft
	if err := json.Unmarshal(payload, &v); err != nil || v.RunID != runID || v.GenerationID == "" || chat.ValidateImplementationDraftState(v) != nil {
		return chat.ImplementationDraft{}, false, fmt.Errorf("invalid persisted implementation checkpoint")
	}
	return v, true, nil
}

// No new table or migration: immutable bounded events are the checkpoints.
// Never search backwards past the immediately preceding execution. A failed
// content correction or a changed input must not resurrect an older good draft.
func (r *RunRepository) ImplementationDraft(ctx context.Context, runID string) (chat.ImplementationDraft, bool, error) {
	c, found, err := r.WorkflowAIContract(ctx, runID)
	if err != nil || !found {
		return chat.ImplementationDraft{}, false, err
	}
	v, found, err := readImplementationDraft(ctx, r.db, runID)
	if err != nil || found {
		if found && !draftMatchesContract(v, c) {
			return v, false, fmt.Errorf("implementation checkpoint contract mismatch")
		}
		return v, found, err
	}
	if c.Attempt < 2 || c.NodeID != "method_implementation" || !strings.Contains(c.PromptVersion, chat.ImplementationPartsVersion) {
		return v, false, nil
	}
	var previousRun, status, input, prompt, schema, failure string
	err = r.db.QueryRowContext(ctx, `SELECT b.chat_run_id,e.status,e.input_sha256,e.prompt_sha256,e.output_schema_sha256,e.error_code
		FROM workflow_ai_executions e JOIN workflow_ai_chat_runs b ON b.execution_id=e.id
		WHERE e.workflow_step_id=? AND e.attempt=?`, c.WorkflowStepID, c.Attempt-1).Scan(&previousRun, &status, &input, &prompt, &schema, &failure)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if (status != "failed" && status != "cancelled" && status != "interrupted") || input != c.InputSHA256 || prompt != c.PromptSHA256 || schema != c.OutputSchemaSHA256 {
		return v, false, nil
	}
	// A new attempt after terminal content rejection is a deliberate retry,
	// not transport recovery. Revalidating the same rejected Complete source
	// would make that button fail without ever asking the model to correct it.
	// Same-execution restart and interrupted corrections retain their budget.
	if failure == "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || failure == "WORKFLOW_AI_OUTPUT_INVALID" {
		return v, false, nil
	}
	priorContract, exists, err := r.WorkflowAIContract(ctx, previousRun)
	if err != nil || !exists {
		return v, false, err
	}
	v, found, err = readImplementationDraft(ctx, r.db, previousRun)
	if err != nil || !found {
		return v, false, err
	}
	if !draftMatchesContract(v, priorContract) {
		return v, false, fmt.Errorf("prior implementation checkpoint contract mismatch")
	}
	// Return a copy adopted to the new execution; the old event is immutable.
	v.RunID, v.ExecutionID, v.Attempt = runID, c.ID, c.Attempt
	v.SourceTurnIndex = 0
	v.AdoptionPending = true
	return v, true, nil
}

func (r *RunRepository) SaveImplementationDraft(ctx context.Context, v chat.ImplementationDraft) error {
	c, found, err := r.WorkflowAIContract(ctx, v.RunID)
	if err != nil {
		return err
	}
	marker := v.PartCount == 0 && v.Rejected && v.CorrectionCount > 0 && v.ValidationError != ""
	if !found || !draftMatchesContract(v, c) || v.GenerationID == "" || chat.ValidateImplementationDraftState(v) != nil || v.SourceTurnIndex < 0 || v.UpdatedAt.IsZero() || v.Rejected && v.ValidationError == "" {
		return fmt.Errorf("invalid implementation checkpoint")
	}
	if v.SourcePatch != nil {
		base, patchable, err := chat.FrozenImplementationPatchBase(c)
		if err != nil || !patchable {
			return fmt.Errorf("patch checkpoint has no frozen source baseline")
		}
		code, err := chat.ApplyImplementationSourcePatch(base.Code, *v.SourcePatch)
		if err != nil || code != v.Code || string(base.Metadata) != string(v.Metadata) {
			return fmt.Errorf("patch checkpoint differs from the atomic frozen-source replacement")
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// This transaction serializes against cancellation/rewind and prevents a
	// late completed response publishing a checkpoint into another attempt.
	var active int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runs r
		JOIN workflow_ai_chat_runs b ON b.chat_run_id=r.id JOIN workflow_ai_executions e ON e.id=b.execution_id
		JOIN workflow_steps s ON s.id=e.workflow_step_id JOIN workflow_runs w ON w.id=e.workflow_run_id
		WHERE r.id=? AND r.status='running' AND e.id=? AND e.status='running'
		AND s.status='running' AND s.attempt=e.attempt AND s.input_sha256=e.input_sha256 AND w.current_step_ordinal=s.ordinal AND w.status='running' AND w.cancel_requested=0
		AND (?=0 OR (r.model_turns=? AND EXISTS (SELECT 1 FROM model_turn_journal j WHERE j.run_id=r.id AND j.turn_index=? AND j.status='completed')))`, v.RunID, v.ExecutionID, v.SourceTurnIndex, v.SourceTurnIndex, v.SourceTurnIndex).Scan(&active)
	if err != nil || active != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("implementation checkpoint is not bound to an active completed response")
	}
	previous, exists, err := readImplementationDraft(ctx, tx, v.RunID)
	if err != nil {
		return err
	}
	if v.SourceTurnIndex == 0 {
		if exists || c.Attempt < 2 {
			return fmt.Errorf("invalid implementation checkpoint adoption")
		}
		var priorRun, status, input, prompt, schema, failure string
		err := tx.QueryRowContext(ctx, `SELECT b.chat_run_id,e.status,e.input_sha256,e.prompt_sha256,e.output_schema_sha256,e.error_code FROM workflow_ai_executions e JOIN workflow_ai_chat_runs b ON b.execution_id=e.id WHERE e.workflow_step_id=? AND e.attempt=?`, c.WorkflowStepID, c.Attempt-1).Scan(&priorRun, &status, &input, &prompt, &schema, &failure)
		if err != nil {
			return err
		}
		prior, ok, err := readImplementationDraft(ctx, tx, priorRun)
		if err != nil {
			return err
		}
		if !ok || (status != "failed" && status != "cancelled" && status != "interrupted") || input != c.InputSHA256 || prompt != c.PromptSHA256 || schema != c.OutputSchemaSHA256 ||
			failure == "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || failure == "WORKFLOW_AI_OUTPUT_INVALID" ||
			prior.GenerationID != v.GenerationID || !sameImplementationCandidate(prior, v) || prior.Rejected != v.Rejected || prior.CorrectionCount != v.CorrectionCount || prior.ValidationError != v.ValidationError || prior.PartCorrectionCount != v.PartCorrectionCount || prior.PartValidationError != v.PartValidationError || prior.PartRejectedCandidate != v.PartRejectedCandidate || prior.PartControlName != v.PartControlName || !sameImplementationEvidence(prior, v) {
			return fmt.Errorf("adopted checkpoint differs from the immediately preceding frozen attempt")
		}
	}
	if exists {
		if v.SourceTurnIndex != 0 && v.SourceTurnIndex <= previous.SourceTurnIndex {
			return fmt.Errorf("implementation checkpoint must belong to a newer completed response")
		}
		if !validImplementationTransition(previous, v) {
			return fmt.Errorf("implementation checkpoint cannot rewrite saved source or reset correction allowances")
		}
	} else if v.SourceTurnIndex != 0 && ((!marker && ((v.PartCount != 1 && !(v.PartCount == 0 && v.PartCorrectionCount == 1)) || v.Rejected || v.CorrectionCount != 0)) || (marker && v.CorrectionCount != 1)) {
		return fmt.Errorf("initial implementation checkpoint must be the first completed part")
	}
	event := events.New(eventID, v.RunID, "run", "run.implementation_saved", 0, data)
	if err := appendNextEventTx(ctx, tx, &event); err != nil {
		return err
	}
	return tx.Commit()
}

func sameImplementationCandidate(a, b chat.ImplementationDraft) bool {
	return a.PartCount == b.PartCount && a.Code == b.Code && string(a.Metadata) == string(b.Metadata) && a.Complete == b.Complete && reflect.DeepEqual(a.SourcePatch, b.SourcePatch)
}

func validImplementationTransition(previous, v chat.ImplementationDraft) bool {
	if !sameImplementationEvidence(previous, v) {
		return false
	}
	if previous.GenerationID != v.GenerationID {
		// A full validation rejection alone permits a new source generation.
		return previous.Rejected && !v.Rejected && v.SourcePatch == nil && v.CorrectionCount == previous.CorrectionCount && v.ValidationError == previous.ValidationError && (v.PartCount == 1 && v.PartCorrectionCount == 0 || v.PartCount == 0 && v.PartCorrectionCount == 1)
	}
	if previous.Rejected {
		return false
	}
	if v.Rejected {
		return previous.Complete && v.Complete && sameImplementationCandidate(previous, v) && v.CorrectionCount == previous.CorrectionCount+1 && v.ValidationError != ""
	}
	if previous.Complete || previous.CorrectionCount != v.CorrectionCount || previous.ValidationError != v.ValidationError {
		return false
	}
	if v.PartCount == previous.PartCount {
		// Persist only correction state for an unsaved part. Rejected source is
		// an audit payload, never appended to executable code or metadata.
		return sameImplementationCandidate(previous, v) && v.PartCorrectionCount == previous.PartCorrectionCount+1
	}
	return v.PartCount == previous.PartCount+1 && v.PartCorrectionCount == 0 && v.Code != previous.Code && strings.HasPrefix(v.Code, previous.Code) && (previous.PartCount == 0 || string(previous.Metadata) == string(v.Metadata)) && previous.SourcePatch == nil
}

func sameImplementationEvidence(a, b chat.ImplementationDraft) bool {
	if len(a.SourceEvidence) == 0 && len(b.SourceEvidence) == 0 {
		return true
	}
	return reflect.DeepEqual(a.SourceEvidence, b.SourceEvidence)
}

func (r *RunRepository) RecordModelRecoveryFailure(ctx context.Context, v chat.ModelRecoveryFailure) error {
	if v.RunID == "" || v.TurnIndex < 1 || v.Attempt < 1 || (v.Phase != "request" && v.Phase != "stream") || len(v.Details) > 40_000 {
		return fmt.Errorf("invalid recovery diagnostic")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	_, err = r.AppendNext(ctx, events.New(eventID, v.RunID, "run", "run.model_attempt_failed", 0, data))
	return err
}

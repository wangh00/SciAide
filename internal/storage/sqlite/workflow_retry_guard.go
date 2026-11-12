package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

func retryConflictError() error {
	return &apperr.Error{Code: "WORKFLOW_RETRY_CONFLICT", UserMessage: "当前阶段已被其他操作更新，未重复恢复或覆盖新尝试；请刷新任务。"}
}

func checkRetryGuard(ctx context.Context, tx *sql.Tx, runID, stepID, producerID string, expected workflow.RetryGuard) error {
	if expected.RunID != runID || expected.StepID != stepID || expected.ProducerStepID != producerID || expected.RunStatus == "" || expected.StepStatus == "" {
		return retryConflictError()
	}
	actual := workflow.RetryGuard{RunID: runID, StepID: stepID, ProducerStepID: producerID}
	err := tx.QueryRowContext(ctx, `SELECT r.status,r.current_step_ordinal,r.cancel_requested,r.resume_status,
		r.inputs_sha256,r.compilation_sha256,s.status,s.attempt,s.input_sha256,
		(SELECT COALESCE(MAX(sequence),0) FROM workflow_events WHERE workflow_run_id=r.id)
		FROM workflow_runs r JOIN workflow_steps s ON s.workflow_run_id=r.id WHERE r.id=? AND s.id=?`, runID, stepID).Scan(
		&actual.RunStatus, &actual.CurrentStep, &actual.CancelRequested, &actual.ResumeStatus,
		&actual.InputsSHA256, &actual.CompilationSHA256, &actual.StepStatus, &actual.Attempt, &actual.InputSHA256, &actual.EventSequence)
	if errors.Is(err, sql.ErrNoRows) {
		return retryConflictError()
	}
	if err != nil {
		return fmt.Errorf("inspect Workflow retry state: %w", err)
	}
	if producerID != "" {
		if err = tx.QueryRowContext(ctx, `SELECT attempt,input_sha256 FROM workflow_steps WHERE workflow_run_id=? AND id=?`, runID, producerID).Scan(&actual.ProducerAttempt, &actual.ProducerInputSHA256); errors.Is(err, sql.ErrNoRows) {
			return retryConflictError()
		}
		if err != nil {
			return fmt.Errorf("inspect upstream retry state: %w", err)
		}
	}
	if actual != expected {
		return retryConflictError()
	}
	return nil
}

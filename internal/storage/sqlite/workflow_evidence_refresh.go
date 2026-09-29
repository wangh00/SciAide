package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Reindex handoff, invalidation and audit are one compare-and-swap transaction.
// Original tool results, inputs and model text remain in the execution journal.
func (r *WorkflowRuntimeRepository) QueueEvidenceRefresh(ctx context.Context, runID, firstID, screeningID string, attempt int, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		var first, current int
		if err := tx.QueryRowContext(ctx, `SELECT ordinal FROM workflow_steps WHERE id=? AND workflow_run_id=? AND node_id='evidence_import' AND status='completed'`, firstID, runID).Scan(&first); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT ordinal FROM workflow_steps WHERE id=? AND workflow_run_id=? AND node_id='evidence_screening' AND status='running' AND attempt=?`, screeningID, runID, attempt).Scan(&current); err != nil {
			return err
		}
		if current-first != 3 {
			return fmt.Errorf("evidence refresh chain is not contiguous")
		}
		var valid int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_steps WHERE workflow_run_id=? AND ((ordinal=? AND node_id='evidence_sync' AND status='completed') OR (ordinal=? AND node_id='evidence_search' AND status='completed'))`, runID, first+1, first+2).Scan(&valid); err != nil || valid != 2 {
			return fmt.Errorf("evidence refresh dependencies changed: %v", err)
		}
		// Do not rewind a stage while its model is still executing or awaiting approval.
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_ai_executions e JOIN workflow_ai_chat_runs b ON b.execution_id=e.id JOIN runs r ON r.id=b.chat_run_id WHERE e.workflow_step_id=? AND e.attempt=? AND r.status IN ('queued','running','waiting_approval')`, screeningID, attempt).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return fmt.Errorf("evidence refresh model is still active")
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status='queued',current_step_ordinal=?,outputs_json='{}',updated_at=? WHERE id=? AND status='running' AND cancel_requested=0 AND current_step_ordinal=?`, first, formatTime(at), runID, current)
		if err := expectOne(result, err, "queue updated evidence"); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE workflow_ai_executions SET status='interrupted',error_code='WORKFLOW_EVIDENCE_UPDATED',error_message='补充材料已保存，本次旧证据综合被更新流程取代',completed_at=?,updated_at=? WHERE workflow_step_id=? AND attempt=? AND status IN ('prepared','running')`, formatTime(at), formatTime(at), screeningID, attempt)
		if err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_steps SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=? WHERE workflow_run_id=? AND ordinal BETWEEN ? AND ?`, formatTime(at), runID, first, current)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 4 {
			return fmt.Errorf("evidence refresh conflict")
		}
		return nil
	}, event)
}

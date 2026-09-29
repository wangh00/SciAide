package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"time"
)

func (r *WorkflowRuntimeRepository) QueueLocalEvidenceSearch(ctx context.Context, runID, searchID, screeningID string, attempt int, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		var first, current int
		if err := tx.QueryRowContext(ctx, `SELECT ordinal FROM workflow_steps WHERE id=? AND workflow_run_id=? AND node_id='evidence_search' AND status='completed'`, searchID, runID).Scan(&first); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT ordinal FROM workflow_steps WHERE id=? AND workflow_run_id=? AND node_id='evidence_screening' AND status='running' AND attempt=?`, screeningID, runID, attempt).Scan(&current); err != nil {
			return err
		}
		if current != first+1 {
			return fmt.Errorf("noncontiguous local evidence chain")
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_ai_executions e JOIN workflow_ai_chat_runs b ON b.execution_id=e.id JOIN runs r ON r.id=b.chat_run_id WHERE e.workflow_step_id=? AND e.attempt=? AND r.status IN ('queued','running','waiting_approval')`, screeningID, attempt).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return fmt.Errorf("local evidence model still active")
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status='queued',current_step_ordinal=?,outputs_json='{}',updated_at=? WHERE id=? AND status='running' AND cancel_requested=0 AND current_step_ordinal=?`, first, formatTime(at), runID, current)
		if err := expectOne(result, err, "queue local evidence"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_steps SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=? WHERE workflow_run_id=? AND ordinal BETWEEN ? AND ?`, formatTime(at), runID, first, current)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 2 {
			return fmt.Errorf("local evidence refresh conflict")
		}
		return nil
	}, event)
}

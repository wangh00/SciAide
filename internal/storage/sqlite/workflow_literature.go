package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

// The checkpoint event and step rewind share a transaction. Completed tools
// and model attempts remain immutable, including on pause/restart.
func (r *WorkflowRuntimeRepository) QueueLiteratureContinuation(ctx context.Context, runID, screeningID, discoveryID string, attempt int, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		var ordinal int
		if err := tx.QueryRowContext(ctx, "SELECT ordinal FROM workflow_steps WHERE id=? AND workflow_run_id=? AND status='running' AND attempt=?", screeningID, runID, attempt).Scan(&ordinal); err != nil {
			return err
		}
		first := ordinal
		if discoveryID != "" {
			if err := tx.QueryRowContext(ctx, "SELECT ordinal FROM workflow_steps WHERE id=? AND workflow_run_id=? AND status='completed'", discoveryID, runID).Scan(&first); err != nil {
				return err
			}
			if first+1 != ordinal {
				return fmt.Errorf("literature discovery and screening must be adjacent")
			}
		}
		result, err := tx.ExecContext(ctx, "UPDATE workflow_runs SET status='queued',current_step_ordinal=?,updated_at=? WHERE id=? AND status='running' AND cancel_requested=0 AND current_step_ordinal=?", first, formatTime(at), runID, ordinal)
		if err := expectOne(result, err, "queue literature continuation"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, "UPDATE workflow_steps SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=? WHERE workflow_run_id=? AND ordinal BETWEEN ? AND ?", formatTime(at), runID, first, ordinal)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != int64(ordinal-first+1) {
			return fmt.Errorf("literature continuation conflict")
		}
		return nil
	}, event)
}

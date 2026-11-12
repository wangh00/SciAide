package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

func freezeDesignReviewWriteSnapshot(ctx context.Context, tx *sql.Tx, runID string, event workflow.RuntimeEvent) error {
	if event.Type != "workflow.review_revision_queued" {
		return nil
	}
	var check struct {
		Receipt json.RawMessage `json:"designWriteIsolation"`
	}
	if json.Unmarshal(event.Payload, &check) != nil {
		return fmt.Errorf("invalid review revision event")
	}
	if len(check.Receipt) == 0 || string(check.Receipt) == "null" {
		return nil
	}
	run, err := scanWorkflowRun(tx.QueryRowContext(ctx, workflowRunSelect+` WHERE wr.id=?`, runID))
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, workflowStepSelect+` WHERE ws.workflow_run_id=? ORDER BY ws.ordinal`, runID)
	if err != nil {
		return err
	}
	steps := []workflow.Step{}
	for rows.Next() {
		step, err := scanWorkflowStep(rows)
		if err != nil {
			rows.Close()
			return err
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	d := workflow.RunDetail{Run: run, Steps: steps}
	valid, err := workflow.ValidateDesignReviewIsolationSnapshot(d, event)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("missing design isolation snapshot receipt")
	}
	snapshot, err := json.Marshal(map[string]any{"run": run, "steps": steps})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_revision_write_snapshots(event_id,workflow_run_id,snapshot_sha256,snapshot_json) VALUES(?,?,?,?)`, event.ID, runID, workflow.ResearchRevisionSnapshot(d), string(snapshot))
	return err
}

func (r *WorkflowRuntimeRepository) ReadDesignRevisionWriteSnapshot(ctx context.Context, runID, eventID string) (json.RawMessage, error) {
	var snapshot, digest string
	err := r.db.QueryRowContext(ctx, `SELECT snapshot_json,snapshot_sha256 FROM workflow_revision_write_snapshots WHERE workflow_run_id=? AND event_id=?`, runID, eventID).Scan(&snapshot, &digest)
	if err != nil {
		return nil, fmt.Errorf("read design revision write snapshot: %w", err)
	}
	var d workflow.RunDetail
	if json.Unmarshal([]byte(snapshot), &d) != nil || d.Run.ID != runID || workflow.ResearchRevisionSnapshot(d) != digest {
		return nil, fmt.Errorf("design revision write snapshot checksum mismatch")
	}
	return json.RawMessage(snapshot), nil
}

func (r *WorkflowRuntimeRepository) HasDesignRevisionWriteSnapshot(ctx context.Context, runID, eventID string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_revision_write_snapshots WHERE workflow_run_id=? AND event_id=?)`, runID, eventID).Scan(&exists)
	return exists, err
}

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// SweepOrphanRunEvents scans a bounded rowid window, then checks ownership again
// inside the DELETE. It never removes events of an existing run, another event
// aggregate type, or artifact provenance. No VACUUM/long exclusive transaction.
func SweepOrphanRunEvents(ctx context.Context, db *sql.DB, after int64, limit int) (next int64, removed int64, done bool, err error) {
	if limit < 1 || limit > 2000 {
		return after, 0, false, fmt.Errorf("invalid cleanup batch size")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return after, 0, false, err
	}
	defer tx.Rollback()
	var end sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT MAX(rowid) FROM (SELECT rowid FROM run_events WHERE rowid>? ORDER BY rowid LIMIT ?)`, after, limit).Scan(&end)
	if err != nil {
		return after, 0, false, err
	}
	if !end.Valid {
		return after, 0, true, nil
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM run_event_sequences WHERE aggregate_id IN (
	 SELECT aggregate_id FROM run_events WHERE rowid>? AND rowid<=? AND aggregate_type IN ('run','workflow_run'))
	 AND NOT EXISTS(SELECT 1 FROM runs WHERE id=run_event_sequences.aggregate_id)
	 AND NOT EXISTS(SELECT 1 FROM workflow_runs WHERE id=run_event_sequences.aggregate_id)
	 AND NOT EXISTS(SELECT 1 FROM run_events WHERE aggregate_id=run_event_sequences.aggregate_id AND aggregate_type NOT IN ('run','workflow_run'))`, after, end.Int64)
	if err != nil {
		return after, 0, false, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM run_events WHERE rowid>? AND rowid<=? AND (
	 (aggregate_type='run' AND NOT EXISTS(SELECT 1 FROM runs WHERE id=run_events.aggregate_id)) OR
	 (aggregate_type='workflow_run' AND NOT EXISTS(SELECT 1 FROM workflow_runs WHERE id=run_events.aggregate_id)))`, after, end.Int64)
	if err != nil {
		return after, 0, false, err
	}
	removed, err = result.RowsAffected()
	if err == nil {
		err = tx.Commit()
	}
	return end.Int64, removed, false, err
}

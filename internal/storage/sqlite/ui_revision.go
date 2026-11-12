package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// These versions are UI invalidation hints, never model checkpoints. This
// store owns one connection: total_changes observes its own writes, while
// data_version also observes another connection's commits. Read BEFORE the
// payload so a concurrent write can only cause an extra refresh, not suppress it.
func (r *RunRepository) UIRevision(ctx context.Context, runID, conversationID string) (string, error) {
	var id string
	var changes, version int64
	query := `SELECT id,total_changes(),(SELECT data_version FROM pragma_data_version) FROM runs WHERE id=?`
	key := strings.TrimSpace(runID)
	if conversationID != "" {
		query = `SELECT id,total_changes(),(SELECT data_version FROM pragma_data_version) FROM runs WHERE conversation_id=? ORDER BY created_at DESC,id DESC LIMIT 1`
		key = strings.TrimSpace(conversationID)
	}
	err := r.db.QueryRowContext(ctx, query, key).Scan(&id, &changes, &version)
	if err != nil {
		// Missing/deleted runs must still travel through the ordinary scoped
		// reader, which distinguishes a null latest run from an invalid ID.
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return uiVersion(id, changes, version), nil
}

func (r *WorkflowRuntimeRepository) UIRevision(ctx context.Context, projectID, runID string) (string, error) {
	var id, status string
	var changes, version int64
	if err := r.db.QueryRowContext(ctx, `SELECT id,status,total_changes(),(SELECT data_version FROM pragma_data_version) FROM workflow_runs WHERE project_id=? AND id=?`, strings.TrimSpace(projectID), strings.TrimSpace(runID)).Scan(&id, &status, &changes, &version); err != nil {
		return "", err
	}
	// Live process tails are not all persisted yet. Never suppress their UI
	// updates based only on a database revision.
	if status != "completed" && status != "failed" && status != "cancelled" && status != "interrupted" {
		return "", nil
	}
	return uiVersion(projectID+":"+id, changes, version), nil
}

func (r *WorkflowRuntimeRepository) ResearchTimelineRevision(ctx context.Context, projectID, taskID, before, after string, limit int) (string, error) {
	var id string
	var changes, version int64
	if err := r.db.QueryRowContext(ctx, `SELECT id,total_changes(),(SELECT data_version FROM pragma_data_version) FROM workflow_runs WHERE project_id=? AND research_task_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, projectID, taskID).Scan(&id, &changes, &version); err != nil {
		return "", err
	}
	return uiVersion(fmt.Sprintf("%s:%s:%s:%s:%s:%d", projectID, taskID, id, before, after, limit), changes, version), nil
}

func uiVersion(scope string, changes, version int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", scope, changes, version)))
	return hex.EncodeToString(digest[:])
}

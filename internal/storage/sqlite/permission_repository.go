package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

type PermissionRepository struct{ db *sql.DB }

func NewPermissionRepository(db *sql.DB) *PermissionRepository { return &PermissionRepository{db: db} }

func (r *PermissionRepository) ListActiveGrants(ctx context.Context, projectID, runID, toolName string, at time.Time) ([]permission.Grant, error) {
	rows, err := r.db.QueryContext(ctx, grantSelect+` WHERE project_id=? AND tool_name=? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?) AND ((scope='project' AND run_id IS NULL) OR (scope='run' AND run_id=?)) ORDER BY scope, created_at, id`, projectID, toolName, formatTime(at), runID)
	if err != nil {
		return nil, fmt.Errorf("list permission grants: %w", err)
	}
	defer rows.Close()
	values := make([]permission.Grant, 0)
	for rows.Next() {
		value, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *PermissionRepository) ListGrantedApprovals(ctx context.Context, toolCallID string) ([]permission.Approval, error) {
	rows, err := r.db.QueryContext(ctx, approvalSelect+` WHERE tool_call_id=? AND status='granted' ORDER BY created_at,id`, toolCallID)
	if err != nil {
		return nil, fmt.Errorf("list granted approvals: %w", err)
	}
	defer rows.Close()
	values := make([]permission.Approval, 0)
	for rows.Next() {
		value, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *PermissionRepository) CreateApprovalWithEvent(ctx context.Context, value permission.Approval, event events.Envelope) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO approvals(id,run_id,tool_call_id,project_id,tool_name,tool_version,permission_kind,resource,risk,status,requested_scope,resolved_scope,reason,created_at,resolved_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.RunID, value.ToolCallID, value.ProjectID, value.ToolName, value.ToolVersion, value.PermissionKind, value.Resource, value.Risk, value.Status, value.RequestedScope, nil, value.Reason, formatTime(value.CreatedAt), nil); err != nil {
		return fmt.Errorf("insert approval: %w", err)
	}
	if err := appendNextEventTx(ctx, tx, &event); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PermissionRepository) GetApproval(ctx context.Context, id string) (permission.Approval, error) {
	return scanApproval(r.db.QueryRowContext(ctx, approvalSelect+` WHERE id=?`, id))
}

func (r *PermissionRepository) ListApprovalsByRun(ctx context.Context, runID string) ([]permission.Approval, error) {
	rows, err := r.db.QueryContext(ctx, approvalSelect+` WHERE run_id=? ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	defer rows.Close()
	values := make([]permission.Approval, 0)
	for rows.Next() {
		value, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *PermissionRepository) ListPendingApprovals(ctx context.Context, runID string) ([]permission.Approval, error) {
	rows, err := r.db.QueryContext(ctx, approvalSelect+` WHERE run_id=? AND status='pending' ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()
	values := make([]permission.Approval, 0)
	for rows.Next() {
		value, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *PermissionRepository) ListGrantsByProject(ctx context.Context, projectID string) ([]permission.Grant, error) {
	rows, err := r.db.QueryContext(ctx, grantSelect+` WHERE project_id=? ORDER BY created_at DESC,id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list permission grants: %w", err)
	}
	defer rows.Close()
	values := make([]permission.Grant, 0)
	for rows.Next() {
		value, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *PermissionRepository) GetGrant(ctx context.Context, id string) (permission.Grant, error) {
	value, err := scanGrant(r.db.QueryRowContext(ctx, grantSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return permission.Grant{}, fmt.Errorf("permission grant not found")
	}
	return value, err
}

func (r *PermissionRepository) ResolveApprovalWithGrantAndEvent(ctx context.Context, approvalID string, expected, next permission.ApprovalStatus, scope permission.Scope, grant *permission.Grant, at time.Time, event events.Envelope) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE approvals SET status=?, resolved_scope=?, resolved_at=? WHERE id=? AND status=?`, next, scope, formatTime(at), approvalID, expected)
	if err != nil {
		return fmt.Errorf("resolve approval: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return permission.ErrApprovalConflict
	}
	if grant != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO permission_grants(id,project_id,run_id,tool_name,permission_kind,resource,scope,granted_by,created_at,expires_at,revoked_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, grant.ID, grant.ProjectID, nullableString(grant.RunID), grant.ToolName, grant.PermissionKind, grant.Resource, grant.Scope, grant.GrantedBy, formatTime(grant.CreatedAt), nullableTime(grant.ExpiresAt), nil); err != nil {
			return fmt.Errorf("insert permission grant: %w", err)
		}
	}
	if err := appendNextEventTx(ctx, tx, &event); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PermissionRepository) RevokeGrantWithEvent(ctx context.Context, id string, at time.Time, event events.Envelope) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE permission_grants SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("revoke permission grant: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("permission grant not found or already revoked")
	}
	if err := appendNextEventTx(ctx, tx, &event); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PermissionRepository) ExpirePending(ctx context.Context, at time.Time) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin expire pending approvals: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,run_id FROM approvals WHERE status='pending' ORDER BY created_at,id`)
	if err != nil {
		return 0, fmt.Errorf("list pending approvals: %w", err)
	}
	type pendingApproval struct{ id, runID string }
	values := make([]pendingApproval, 0)
	for rows.Next() {
		var value pendingApproval
		if err := rows.Scan(&value.id, &value.runID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, value := range values {
		result, err := tx.ExecContext(ctx, `UPDATE approvals SET status='expired', resolved_scope='call', resolved_at=? WHERE id=? AND status='pending'`, formatTime(at), value.id)
		if err != nil {
			return 0, fmt.Errorf("expire pending approval: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return 0, permission.ErrApprovalConflict
		}
		event, err := recoveryApprovalEvent(value.runID, value.id, at)
		if err != nil {
			return 0, err
		}
		if err := appendNextEventTx(ctx, tx, &event); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(values)), nil
}

func recoveryApprovalEvent(runID, approvalID string, at time.Time) (events.Envelope, error) {
	payload, err := json.Marshal(map[string]any{"approvalId": approvalID, "status": permission.ApprovalExpired, "reason": "application_restarted"})
	if err != nil {
		return events.Envelope{}, err
	}
	eventID, err := id.New()
	if err != nil {
		return events.Envelope{}, err
	}
	event := events.New(eventID, runID, "run", "approval.expired", 0, payload)
	event.Timestamp = at
	return event, nil
}

const approvalSelect = `SELECT id,run_id,tool_call_id,project_id,tool_name,tool_version,permission_kind,resource,risk,status,requested_scope,COALESCE(resolved_scope,''),reason,created_at,resolved_at FROM approvals`
const grantSelect = `SELECT id,project_id,COALESCE(run_id,''),tool_name,permission_kind,resource,scope,granted_by,created_at,expires_at,revoked_at FROM permission_grants`

func scanApproval(row rowScanner) (permission.Approval, error) {
	var value permission.Approval
	var createdAt string
	var resolvedAt sql.NullString
	if err := row.Scan(&value.ID, &value.RunID, &value.ToolCallID, &value.ProjectID, &value.ToolName, &value.ToolVersion, &value.PermissionKind, &value.Resource, &value.Risk, &value.Status, &value.RequestedScope, &value.ResolvedScope, &value.Reason, &createdAt, &resolvedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return permission.Approval{}, fmt.Errorf("approval not found")
		}
		return value, err
	}
	var err error
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return value, err
	}
	if resolvedAt.Valid {
		parsed, err := parseTime(resolvedAt.String)
		if err != nil {
			return value, err
		}
		value.ResolvedAt = &parsed
	}
	return value, nil
}

func scanGrant(row rowScanner) (permission.Grant, error) {
	var value permission.Grant
	var createdAt string
	var expiresAt, revokedAt sql.NullString
	if err := row.Scan(&value.ID, &value.ProjectID, &value.RunID, &value.ToolName, &value.PermissionKind, &value.Resource, &value.Scope, &value.GrantedBy, &createdAt, &expiresAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return permission.Grant{}, sql.ErrNoRows
		}
		return value, err
	}
	var err error
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return value, err
	}
	if expiresAt.Valid {
		parsed, err := parseTime(expiresAt.String)
		if err != nil {
			return value, err
		}
		value.ExpiresAt = &parsed
	}
	if revokedAt.Valid {
		parsed, err := parseTime(revokedAt.String)
		if err != nil {
			return value, err
		}
		value.RevokedAt = &parsed
	}
	return value, nil
}

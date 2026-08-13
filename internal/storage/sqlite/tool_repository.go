package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
)

type ToolRepository struct{ db *sql.DB }

func NewToolRepository(db *sql.DB) *ToolRepository { return &ToolRepository{db: db} }

func (r *ToolRepository) Create(ctx context.Context, value tool.Call) error {
	return r.create(ctx, r.db, value)
}

func (r *ToolRepository) create(ctx context.Context, executor sqlExecer, value tool.Call) error {
	if value.Permissions == nil {
		value.Permissions = []tool.PermissionRequirement{}
	}
	permissions, err := json.Marshal(value.Permissions)
	if err != nil {
		return fmt.Errorf("encode tool permissions: %w", err)
	}
	_, err = executor.ExecContext(ctx, `INSERT INTO tool_calls(id, run_id, provider_call_id, tool_name, tool_version, arguments_json, status, risk, permissions_json, idempotent, idempotency_key, error_code, error_message, created_at, started_at, completed_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.RunID, value.ProviderCallID, value.ToolName, value.ToolVersion, string(value.Arguments), value.Status, value.Risk, string(permissions), value.Idempotent, nullableString(value.IdempotencyKey), value.ErrorCode, value.ErrorMessage, formatTime(value.CreatedAt), nullableTime(value.StartedAt), nullableTime(value.CompletedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert tool call: %w", err)
	}
	return nil
}

func (r *ToolRepository) Get(ctx context.Context, id string) (tool.Call, error) {
	value, err := scanToolCall(r.db.QueryRowContext(ctx, toolCallSelect+` WHERE tc.id = ?`, id))
	if err != nil {
		return value, err
	}
	value.Result, err = r.getResult(ctx, value.ID)
	return value, err
}

func (r *ToolRepository) ListByRun(ctx context.Context, runID string) ([]tool.Call, error) {
	rows, err := r.db.QueryContext(ctx, toolCallSelect+` WHERE tc.run_id = ? ORDER BY tc.created_at, tc.id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list tool calls: %w", err)
	}
	values := make([]tool.Call, 0)
	for rows.Next() {
		value, err := scanToolCall(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range values {
		values[index].Result, err = r.getResult(ctx, values[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (r *ToolRepository) Transition(ctx context.Context, id string, expected, next tool.CallStatus, errorCode, errorMessage string, at time.Time) error {
	return transitionToolCall(ctx, r.db, id, expected, next, errorCode, errorMessage, at)
}

func transitionToolCall(ctx context.Context, executor sqlExecer, id string, expected, next tool.CallStatus, errorCode, errorMessage string, at time.Time) error {
	var started, completed any
	if next == tool.CallRunning {
		started = formatTime(at)
	}
	if next.Terminal() {
		completed = formatTime(at)
	}
	result, err := executor.ExecContext(ctx, `UPDATE tool_calls SET status=?, error_code=?, error_message=?, started_at=COALESCE(started_at, ?), completed_at=COALESCE(completed_at, ?), updated_at=? WHERE id=? AND status=?`,
		next, errorCode, errorMessage, started, completed, formatTime(at), id, expected)
	if err != nil {
		return fmt.Errorf("transition tool call: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return tool.ErrTransitionConflict
	}
	return nil
}

func (r *ToolRepository) Finish(ctx context.Context, id string, expected, next tool.CallStatus, value tool.Result, errorCode, errorMessage string, at time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tool finish: %w", err)
	}
	defer tx.Rollback()
	if err := finishToolCall(ctx, tx, id, expected, next, value, errorCode, errorMessage, at); err != nil {
		return err
	}
	return tx.Commit()
}

func finishToolCall(ctx context.Context, tx *sql.Tx, id string, expected, next tool.CallStatus, value tool.Result, errorCode, errorMessage string, at time.Time) error {
	if value.Artifacts == nil {
		value.Artifacts = []tool.ArtifactRef{}
	}
	if value.Citations == nil {
		value.Citations = []tool.CitationRef{}
	}
	artifacts, err := json.Marshal(value.Artifacts)
	if err != nil {
		return fmt.Errorf("encode tool artifacts: %w", err)
	}
	citations, err := json.Marshal(value.Citations)
	if err != nil {
		return fmt.Errorf("encode tool citations: %w", err)
	}
	meta, err := json.Marshal(value.Meta)
	if err != nil {
		return fmt.Errorf("encode tool result metadata: %w", err)
	}
	var structured any
	if len(value.Structured) > 0 {
		structured = string(value.Structured)
	}
	result, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status=?, error_code=?, error_message=?, completed_at=?, updated_at=? WHERE id=? AND status=?`, next, errorCode, errorMessage, formatTime(at), formatTime(at), id, expected)
	if err != nil {
		return fmt.Errorf("finish tool call: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return tool.ErrTransitionConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tool_results(tool_call_id, status, text_content, structured_json, artifacts_json, citations_json, truncated, meta_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, value.Status, value.Text, structured, string(artifacts), string(citations), value.Truncated, string(meta), formatTime(value.CreatedAt)); err != nil {
		return fmt.Errorf("insert tool result: %w", err)
	}
	return nil
}

func (r *ToolRepository) InterruptActive(ctx context.Context, at time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE tool_calls SET status='interrupted', error_code='APP_RESTARTED', error_message='应用退出时工具调用尚未完成', completed_at=?, updated_at=? WHERE status IN ('pending', 'awaiting_approval', 'running')`, formatTime(at), formatTime(at))
	if err != nil {
		return 0, fmt.Errorf("interrupt active tool calls: %w", err)
	}
	return result.RowsAffected()
}

func (r *ToolRepository) CreateWithEvent(ctx context.Context, value tool.Call, event events.Envelope) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.create(ctx, tx, value); err != nil {
		return err
	}
	if err := appendToolEvent(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *ToolRepository) TransitionWithEvent(ctx context.Context, id string, expected, next tool.CallStatus, errorCode, errorMessage string, at time.Time, event events.Envelope) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := transitionToolCall(ctx, tx, id, expected, next, errorCode, errorMessage, at); err != nil {
		return err
	}
	if err := appendToolEvent(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *ToolRepository) FinishWithEvent(ctx context.Context, id string, expected, next tool.CallStatus, value tool.Result, errorCode, errorMessage string, at time.Time, event events.Envelope) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := finishToolCall(ctx, tx, id, expected, next, value, errorCode, errorMessage, at); err != nil {
		return err
	}
	if err := appendToolEvent(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func appendToolEvent(ctx context.Context, tx *sql.Tx, event events.Envelope) error {
	return appendNextEventTx(ctx, tx, &event)
}

const toolCallSelect = `SELECT tc.id, tc.run_id, tc.provider_call_id, tc.tool_name, tc.tool_version, tc.arguments_json, tc.status, tc.risk, tc.permissions_json, tc.idempotent, COALESCE(tc.idempotency_key, ''), tc.error_code, tc.error_message, tc.created_at, tc.started_at, tc.completed_at, tc.updated_at FROM tool_calls tc`

func scanToolCall(row rowScanner) (tool.Call, error) {
	var value tool.Call
	var arguments, permissions, createdAt, updatedAt string
	var startedAt, completedAt sql.NullString
	if err := row.Scan(&value.ID, &value.RunID, &value.ProviderCallID, &value.ToolName, &value.ToolVersion, &arguments, &value.Status, &value.Risk, &permissions, &value.Idempotent, &value.IdempotencyKey, &value.ErrorCode, &value.ErrorMessage, &createdAt, &startedAt, &completedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tool.Call{}, fmt.Errorf("tool call not found")
		}
		return tool.Call{}, err
	}
	value.Arguments = json.RawMessage(arguments)
	if err := json.Unmarshal([]byte(permissions), &value.Permissions); err != nil {
		return value, err
	}
	var err error
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return value, err
	}
	if startedAt.Valid {
		parsed, err := parseTime(startedAt.String)
		if err != nil {
			return value, err
		}
		value.StartedAt = &parsed
	}
	if completedAt.Valid {
		parsed, err := parseTime(completedAt.String)
		if err != nil {
			return value, err
		}
		value.CompletedAt = &parsed
	}
	return value, nil
}

func (r *ToolRepository) getResult(ctx context.Context, callID string) (*tool.Result, error) {
	var value tool.Result
	var structured sql.NullString
	var artifacts, citations, meta, createdAt string
	err := r.db.QueryRowContext(ctx, `SELECT status, text_content, structured_json, artifacts_json, citations_json, truncated, meta_json, created_at FROM tool_results WHERE tool_call_id = ?`, callID).Scan(&value.Status, &value.Text, &structured, &artifacts, &citations, &value.Truncated, &meta, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if structured.Valid {
		value.Structured = json.RawMessage(structured.String)
	}
	if err := json.Unmarshal([]byte(artifacts), &value.Artifacts); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(citations), &value.Citations); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(meta), &value.Meta); err != nil {
		return nil, err
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

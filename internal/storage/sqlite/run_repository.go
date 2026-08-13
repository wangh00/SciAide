package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/events"
)

type RunRepository struct{ db *sql.DB }

func NewRunRepository(db *sql.DB) *RunRepository { return &RunRepository{db: db} }

func (r *RunRepository) CreateWithMessages(ctx context.Context, value chat.Run, userMessage, assistantMessage conversation.Message) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin run: %w", err)
	}
	defer tx.Rollback()
	if err := insertMessage(ctx, tx, userMessage); err != nil {
		return err
	}
	if err := insertMessage(ctx, tx, assistantMessage); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(id, conversation_id, user_message_id, assistant_message_id, model_profile_id, model_id, status, error_code, error_message, input_tokens, output_tokens, finish_reason, created_at, started_at, completed_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.ConversationID, value.UserMessageID, nullableString(value.AssistantMessageID), value.ModelProfileID, value.ModelID, value.Status, value.ErrorCode, value.ErrorMessage, value.InputTokens, value.OutputTokens, value.FinishReason, formatTime(value.CreatedAt), nullableTime(value.StartedAt), nullableTime(value.CompletedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET updated_at=? WHERE id=?`, formatTime(value.UpdatedAt), value.ConversationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *RunRepository) Get(ctx context.Context, id string) (chat.Run, error) {
	return scanRun(r.db.QueryRowContext(ctx, `SELECT id, conversation_id, user_message_id, COALESCE(assistant_message_id, ''), model_profile_id, model_id, status, error_code, error_message, input_tokens, output_tokens, finish_reason, created_at, started_at, completed_at, updated_at FROM runs WHERE id = ?`, id))
}

func (r *RunRepository) Update(ctx context.Context, value chat.Run) error {
	result, err := r.db.ExecContext(ctx, `UPDATE runs SET assistant_message_id=?, status=?, error_code=?, error_message=?, input_tokens=?, output_tokens=?, finish_reason=?, started_at=?, completed_at=?, updated_at=? WHERE id=?`,
		nullableString(value.AssistantMessageID), value.Status, value.ErrorCode, value.ErrorMessage, value.InputTokens, value.OutputTokens, value.FinishReason, nullableTime(value.StartedAt), nullableTime(value.CompletedAt), formatTime(value.UpdatedAt), value.ID)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("run not found")
	}
	return nil
}

func (r *RunRepository) InterruptActive(ctx context.Context, at time.Time) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin interrupt active runs: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status='incomplete', updated_at=? WHERE id IN (SELECT assistant_message_id FROM runs WHERE status IN ('queued', 'running'))`, formatTime(at)); err != nil {
		return 0, fmt.Errorf("interrupt assistant messages: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status='interrupted', error_code='APP_RESTARTED', error_message='应用退出时运行尚未完成', completed_at=?, updated_at=? WHERE status IN ('queued', 'running')`, formatTime(at), formatTime(at))
	if err != nil {
		return 0, fmt.Errorf("interrupt active runs: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

func (r *RunRepository) Append(ctx context.Context, event events.Envelope) error {
	payload := string(event.Payload)
	_, err := r.db.ExecContext(ctx, `INSERT INTO run_events(event_id, version, aggregate_id, aggregate_type, sequence, event_type, timestamp, payload_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.Version, event.AggregateID, event.AggregateType, event.Sequence, event.Type, formatTime(event.Timestamp), payload)
	if err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	return nil
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertMessage(ctx context.Context, tx sqlExecer, value conversation.Message) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, conversation_id, run_id, role, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.ConversationID, nullableString(value.RunID), value.Role, value.Status, formatTime(value.CreatedAt), formatTime(value.UpdatedAt)); err != nil {
		return fmt.Errorf("insert run message: %w", err)
	}
	for _, part := range value.Parts {
		var payload any
		if len(part.Payload) > 0 {
			payload = string(part.Payload)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_parts(id, message_id, ordinal, part_type, text_content, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			part.ID, value.ID, part.Ordinal, part.Type, part.Text, payload, formatTime(part.CreatedAt)); err != nil {
			return fmt.Errorf("insert run message part: %w", err)
		}
	}
	return nil
}

func scanRun(row rowScanner) (chat.Run, error) {
	var value chat.Run
	var createdAt, updatedAt string
	var startedAt, completedAt sql.NullString
	if err := row.Scan(&value.ID, &value.ConversationID, &value.UserMessageID, &value.AssistantMessageID, &value.ModelProfileID, &value.ModelID, &value.Status,
		&value.ErrorCode, &value.ErrorMessage, &value.InputTokens, &value.OutputTokens, &value.FinishReason, &createdAt, &startedAt, &completedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return chat.Run{}, fmt.Errorf("run not found")
		}
		return chat.Run{}, err
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

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

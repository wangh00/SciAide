package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
)

type ConversationRepository struct{ db *sql.DB }

func NewConversationRepository(db *sql.DB) *ConversationRepository {
	return &ConversationRepository{db: db}
}

func (r *ConversationRepository) CreateConversation(ctx context.Context, value conversation.Conversation) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO conversations(id, project_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		value.ID, value.ProjectID, value.Title, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert conversation: %w", err)
	}
	return nil
}

func (r *ConversationRepository) GetConversation(ctx context.Context, id string) (conversation.Conversation, error) {
	return scanConversation(r.db.QueryRowContext(ctx, `SELECT id, project_id, title, created_at, updated_at FROM conversations WHERE id = ?`, id))
}

func (r *ConversationRepository) ListConversations(ctx context.Context, projectID string) ([]conversation.Conversation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, project_id, title, created_at, updated_at FROM conversations WHERE project_id = ? ORDER BY updated_at DESC, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	values := make([]conversation.Conversation, 0)
	for rows.Next() {
		value, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *ConversationRepository) DeleteConversation(ctx context.Context, conversationID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin conversation delete: %w", err)
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE conversation_id = ? AND status IN ('queued', 'running', 'waiting_approval')`, conversationID).Scan(&active); err != nil {
		return fmt.Errorf("check active conversation runs: %w", err)
	}
	if active > 0 {
		return fmt.Errorf("conversation has an active chat run; stop it before removing the conversation")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_events WHERE aggregate_id IN (SELECT id FROM runs WHERE conversation_id = ?)`, conversationID); err != nil {
		return fmt.Errorf("delete conversation run events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE conversation_id = ?`, conversationID); err != nil {
		return fmt.Errorf("delete conversation runs: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, conversationID)
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("conversation not found")
	}
	return tx.Commit()
}

func (r *ConversationRepository) CreateMessage(ctx context.Context, value conversation.Message) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin message insert: %w", err)
	}
	defer tx.Rollback()
	var runID any
	if value.RunID != "" {
		runID = value.RunID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, conversation_id, run_id, role, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.ConversationID, runID, value.Role, value.Status, formatTime(value.CreatedAt), formatTime(value.UpdatedAt)); err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	for _, part := range value.Parts {
		var payload any
		if len(part.Payload) > 0 {
			payload = string(part.Payload)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_parts(id, message_id, ordinal, part_type, text_content, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			part.ID, value.ID, part.Ordinal, part.Type, part.Text, payload, formatTime(part.CreatedAt)); err != nil {
			return fmt.Errorf("insert message part: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET updated_at = ? WHERE id = ?`, formatTime(value.UpdatedAt), value.ConversationID); err != nil {
		return fmt.Errorf("touch conversation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit message: %w", err)
	}
	return nil
}

func (r *ConversationRepository) UpdateMessageText(ctx context.Context, messageID string, status conversation.MessageStatus, text string, updatedAt time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE messages SET status = ?, updated_at = ? WHERE id = ?`, status, formatTime(updatedAt), messageID)
	if err != nil {
		return fmt.Errorf("update message: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("message not found")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE message_parts SET text_content = ? WHERE message_id = ? AND ordinal = 0 AND part_type = 'text'`, text, messageID); err != nil {
		return fmt.Errorf("update message text: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit message update: %w", err)
	}
	return nil
}

func (r *ConversationRepository) ListMessages(ctx context.Context, conversationID string, limit int) ([]conversation.Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, conversation_id, COALESCE(run_id, ''), role, status, created_at, updated_at
		FROM (SELECT * FROM messages WHERE conversation_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)
		ORDER BY created_at, id`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	values := make([]conversation.Message, 0)
	for rows.Next() {
		var value conversation.Message
		var createdAt, updatedAt string
		if err := rows.Scan(&value.ID, &value.ConversationID, &value.RunID, &value.Role, &value.Status, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		value.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
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
	// Store deliberately uses one SQLite connection. Finish the message query
	// before loading parts so nested queries cannot wait on that same connection.
	for i := range values {
		parts, err := r.listParts(ctx, values[i].ID)
		if err != nil {
			return nil, err
		}
		values[i].Parts = parts
	}
	return values, nil
}

func (r *ConversationRepository) listParts(ctx context.Context, messageID string) ([]conversation.MessagePart, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, message_id, ordinal, part_type, text_content, COALESCE(payload_json, ''), created_at FROM message_parts WHERE message_id = ? ORDER BY ordinal`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parts := make([]conversation.MessagePart, 0)
	for rows.Next() {
		var part conversation.MessagePart
		var payload, createdAt string
		if err := rows.Scan(&part.ID, &part.MessageID, &part.Ordinal, &part.Type, &part.Text, &payload, &createdAt); err != nil {
			return nil, err
		}
		if payload != "" {
			part.Payload = []byte(payload)
		}
		part.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, rows.Err()
}

func scanConversation(row rowScanner) (conversation.Conversation, error) {
	var value conversation.Conversation
	var createdAt, updatedAt string
	if err := row.Scan(&value.ID, &value.ProjectID, &value.Title, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return conversation.Conversation{}, fmt.Errorf("conversation not found")
		}
		return conversation.Conversation{}, err
	}
	var err error
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return conversation.Conversation{}, err
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return conversation.Conversation{}, err
	}
	return value, nil
}

func formatTime(value time.Time) string         { return value.UTC().Format(time.RFC3339Nano) }
func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type ConversationRepository struct{ db *sql.DB }

func NewConversationRepository(db *sql.DB) *ConversationRepository {
	return &ConversationRepository{db: db}
}

func (r *ConversationRepository) CreateConversation(ctx context.Context, value conversation.Conversation) error {
	if !value.ReasoningLevel.Valid() {
		value.ReasoningLevel = modelcap.ReasoningMedium
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO conversations(id, project_id, title, model_profile_id, model_id, permission_mode, reasoning_level, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.ProjectID, value.Title, value.ModelProfileID, value.ModelID, value.PermissionMode, value.ReasoningLevel, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert conversation: %w", err)
	}
	return nil
}

func (r *ConversationRepository) GetConversation(ctx context.Context, id string) (conversation.Conversation, error) {
	return scanConversation(r.db.QueryRowContext(ctx, `SELECT id, project_id, title, model_profile_id, model_id, permission_mode, reasoning_level, created_at, updated_at FROM conversations WHERE id = ?`, id))
}

func (r *ConversationRepository) ListConversations(ctx context.Context, projectID string) ([]conversation.Conversation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, project_id, title, model_profile_id, model_id, permission_mode, reasoning_level, created_at, updated_at FROM conversations WHERE project_id = ? ORDER BY updated_at DESC, id`, projectID)
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

func (r *ConversationRepository) UpdatePermissionMode(ctx context.Context, conversationID string, mode conversation.PermissionMode, updatedAt time.Time) error {
	if !mode.Valid() {
		return fmt.Errorf("invalid permission mode")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE conversations SET permission_mode=?, updated_at=? WHERE id=? AND NOT EXISTS (SELECT 1 FROM runs WHERE conversation_id=? AND status IN ('queued','running','waiting_approval'))`, mode, formatTime(updatedAt), conversationID, conversationID)
	if err != nil {
		return fmt.Errorf("update conversation permission mode: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		var exists int
		if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE id=?`, conversationID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("conversation not found")
		}
		return fmt.Errorf("permission mode cannot change during an active run")
	}
	return nil
}

func (r *ConversationRepository) UpdateReasoningLevel(ctx context.Context, conversationID string, level modelcap.ReasoningLevel, updatedAt time.Time) error {
	if !level.Valid() {
		return fmt.Errorf("invalid reasoning level")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE conversations SET reasoning_level=?, updated_at=? WHERE id=? AND NOT EXISTS (SELECT 1 FROM runs WHERE conversation_id=? AND status IN ('queued','running','waiting_approval'))`, level, formatTime(updatedAt), conversationID, conversationID)
	if err != nil {
		return fmt.Errorf("update conversation reasoning level: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		var exists int
		if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE id=?`, conversationID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("conversation not found")
		}
		return fmt.Errorf("reasoning level cannot change during an active run")
	}
	return nil
}

func (r *ConversationRepository) UpdateModelSelection(ctx context.Context, conversationID, modelProfileID, modelID string, updatedAt time.Time) error {
	if modelProfileID == "" || modelID == "" {
		return fmt.Errorf("model profile and model are required")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE conversations SET model_profile_id=?, model_id=?, updated_at=? WHERE id=?`, modelProfileID, modelID, formatTime(updatedAt), conversationID)
	if err != nil {
		return fmt.Errorf("update conversation model selection: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("conversation not found")
	}
	return nil
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
	if limit <= 0 || limit > 2_000 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, conversation_id, COALESCE(run_id, ''), role, status, created_at, updated_at
		FROM (
			SELECT m.*,
				CASE WHEN m.role = 'user' THEN 0 WHEN m.role = 'assistant' THEN 1 ELSE 2 END AS role_order
			FROM messages m
			WHERE m.conversation_id = ?
			ORDER BY m.created_at DESC, role_order DESC, m.id DESC
			LIMIT ?
		)
		ORDER BY created_at, role_order, id`, conversationID, limit)
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
		citations, err := r.listMessageCitations(ctx, values[i].ID)
		if err != nil {
			return nil, err
		}
		values[i].Citations = citations
	}
	return values, nil
}

func completeAssistantMessageWithCitations(ctx context.Context, tx *sql.Tx, messageID, runID, text string, values []conversation.Citation, updatedAt time.Time) error {
	messageID, runID = strings.TrimSpace(messageID), strings.TrimSpace(runID)
	if messageID == "" || runID == "" {
		return fmt.Errorf("message and run ids are required")
	}
	if len(values) > 512 {
		return fmt.Errorf("message citation count exceeds limit")
	}
	var projectID string
	if err := tx.QueryRowContext(ctx, `
		SELECT c.project_id
		FROM messages m
		JOIN runs r ON r.id=m.run_id AND r.assistant_message_id=m.id AND r.conversation_id=m.conversation_id
		JOIN conversations c ON c.id=r.conversation_id
		WHERE m.id=? AND m.run_id=? AND m.role='assistant' AND m.status='streaming' AND r.status='running'`, messageID, runID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("running assistant message does not belong to run")
		}
		return fmt.Errorf("verify citation message: %w", err)
	}
	seenIDs := make(map[string]struct{}, len(values))
	seenReferences := make(map[string]struct{}, len(values))
	for index, value := range values {
		quoteSHA256 := citation.QuoteSHA256(value.Quote)
		if value.MessageID != messageID || value.RunID != runID || value.ID == "" || value.ToolCallID == "" || value.Ordinal != index || value.ProjectID != projectID || value.IndexVersionID == "" || value.DocumentID == "" || value.AttachmentID == "" || value.ChunkID == "" || value.SourceName == "" || value.Locator == "" || value.Quote == "" || value.QuoteSHA256 != quoteSHA256 || value.Reference != citation.KnowledgeReference(runID, value.IndexVersionID, value.ChunkID, quoteSHA256) || value.SourceStart < 0 || value.SourceEnd < value.SourceStart || !strings.Contains(text, value.Reference) || value.CreatedAt.IsZero() {
			return fmt.Errorf("invalid message citation")
		}
		if _, exists := seenIDs[value.ID]; exists {
			return fmt.Errorf("duplicate message citation id")
		}
		if _, exists := seenReferences[value.Reference]; exists {
			return fmt.Errorf("duplicate message citation reference")
		}
		seenIDs[value.ID] = struct{}{}
		seenReferences[value.Reference] = struct{}{}
		var trustedCall int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM tool_calls tc
			JOIN tool_results tr ON tr.tool_call_id=tc.id
			WHERE tc.id=? AND tc.run_id=? AND tc.tool_name=? AND tc.status='completed' AND tr.status='success'`, value.ToolCallID, runID, citation.KnowledgeToolName).Scan(&trustedCall); err != nil {
			return fmt.Errorf("verify citation tool call: %w", err)
		}
		if trustedCall != 1 {
			return fmt.Errorf("citation tool call is not a successful knowledge search")
		}
	}
	messageResult, err := tx.ExecContext(ctx, `UPDATE messages SET status='complete', updated_at=? WHERE id=? AND run_id=? AND role='assistant' AND status='streaming'`, formatTime(updatedAt), messageID, runID)
	if err != nil {
		return fmt.Errorf("complete assistant message: %w", err)
	}
	if affected, _ := messageResult.RowsAffected(); affected != 1 {
		return fmt.Errorf("assistant message completion conflict")
	}
	partResult, err := tx.ExecContext(ctx, `UPDATE message_parts SET text_content=? WHERE message_id=? AND ordinal=0 AND part_type='text'`, text, messageID)
	if err != nil {
		return fmt.Errorf("save assistant message text: %w", err)
	}
	if affected, _ := partResult.RowsAffected(); affected != 1 {
		return fmt.Errorf("assistant text part not found")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_citations WHERE message_id=?`, messageID); err != nil {
		return fmt.Errorf("clear message citations: %w", err)
	}
	for _, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_citations(id,message_id,run_id,tool_call_id,project_id,reference_key,ordinal,index_version_id,document_id,attachment_id,chunk_id,source_name,mime_type,locator,title,quote_text,quote_sha256,source_start,source_end,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			value.ID, value.MessageID, value.RunID, value.ToolCallID, value.ProjectID, value.Reference, value.Ordinal, value.IndexVersionID, value.DocumentID, value.AttachmentID, value.ChunkID, value.SourceName, value.MIMEType, value.Locator, value.Title, value.Quote, value.QuoteSHA256, value.SourceStart, value.SourceEnd, formatTime(value.CreatedAt)); err != nil {
			return fmt.Errorf("insert message citation: %w", err)
		}
	}
	return nil
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

func (r *ConversationRepository) listMessageCitations(ctx context.Context, messageID string) ([]conversation.Citation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,message_id,run_id,tool_call_id,project_id,reference_key,ordinal,index_version_id,document_id,attachment_id,chunk_id,source_name,mime_type,locator,title,quote_text,quote_sha256,source_start,source_end,created_at FROM message_citations WHERE message_id=? ORDER BY ordinal`, messageID)
	if err != nil {
		return nil, fmt.Errorf("list message citations: %w", err)
	}
	defer rows.Close()
	values := make([]conversation.Citation, 0)
	for rows.Next() {
		var value conversation.Citation
		var createdAt string
		if err := rows.Scan(&value.ID, &value.MessageID, &value.RunID, &value.ToolCallID, &value.ProjectID, &value.Reference, &value.Ordinal, &value.IndexVersionID, &value.DocumentID, &value.AttachmentID, &value.ChunkID, &value.SourceName, &value.MIMEType, &value.Locator, &value.Title, &value.Quote, &value.QuoteSHA256, &value.SourceStart, &value.SourceEnd, &createdAt); err != nil {
			return nil, err
		}
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanConversation(row rowScanner) (conversation.Conversation, error) {
	var value conversation.Conversation
	var createdAt, updatedAt string
	if err := row.Scan(&value.ID, &value.ProjectID, &value.Title, &value.ModelProfileID, &value.ModelID, &value.PermissionMode, &value.ReasoningLevel, &createdAt, &updatedAt); err != nil {
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

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
	rows, err := r.db.QueryContext(ctx, `
		SELECT conversation.id, conversation.project_id, conversation.title,
			conversation.model_profile_id, conversation.model_id,
			conversation.permission_mode, conversation.reasoning_level,
			conversation.created_at, conversation.updated_at
		FROM conversations conversation
		WHERE conversation.project_id = ?
		AND NOT EXISTS (
			SELECT 1 FROM workflow_conversations binding
			WHERE binding.conversation_id = conversation.id
		)
		ORDER BY conversation.updated_at DESC, conversation.id`, projectID)
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
	var researchRuns int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_conversations WHERE conversation_id=?`, conversationID).Scan(&researchRuns); err != nil {
		return fmt.Errorf("check research conversation binding: %w", err)
	}
	if researchRuns > 0 {
		return fmt.Errorf("research conversation is managed by its Workflow Run and cannot be removed separately")
	}
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
	result, err := r.db.ExecContext(ctx, `UPDATE conversations SET permission_mode=?, updated_at=? WHERE id=? AND NOT EXISTS (SELECT 1 FROM runs WHERE conversation_id=? AND status IN ('queued','running','waiting_approval')) AND NOT EXISTS (SELECT 1 FROM workflow_conversations WHERE conversation_id=?)`, mode, formatTime(updatedAt), conversationID, conversationID, conversationID)
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
		var researchRuns int
		if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_conversations WHERE conversation_id=?`, conversationID).Scan(&researchRuns); err != nil {
			return err
		}
		if researchRuns > 0 {
			return fmt.Errorf("research conversation permission mode is frozen by its Workflow Run")
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
	return r.listMessagesWhere(ctx, "m.conversation_id = ?", []any{conversationID}, limit)
}

func (r *ConversationRepository) ListMessagesForRun(ctx context.Context, conversationID, runID string) ([]conversation.Message, error) {
	return r.listMessagesWhere(ctx, "m.conversation_id = ? AND m.run_id = ?", []any{conversationID, runID}, -1)
}

func (r *ConversationRepository) GetTimelineMessage(ctx context.Context, messageID, conversationID string) (conversation.Message, error) {
	values, err := r.listMessagesWhere(ctx, "m.conversation_id = ? AND m.id = ?", []any{conversationID, messageID}, 1)
	if err != nil {
		return conversation.Message{}, err
	}
	if len(values) != 1 {
		return conversation.Message{}, fmt.Errorf("timeline message not found")
	}
	return values[0], nil
}

func (r *ConversationRepository) listMessagesWhere(ctx context.Context, condition string, args []any, limit int) ([]conversation.Message, error) {
	if limit < 0 {
		// Agent context and checkpoint construction must see the complete durable
		// history; SQLite still streams rows so this does not require one giant
		// intermediate query result.
		limit = 2_147_483_647
	} else if limit == 0 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT selected.id, selected.conversation_id, COALESCE(selected.run_id, ''), selected.role, selected.status, selected.created_at, selected.updated_at,
			COALESCE(r.status, ''), COALESCE(r.requested_reasoning_level, ''), COALESCE(r.resolved_reasoning_level, ''),
			COALESCE(r.reasoning_observed, 0), COALESCE(r.reasoning_signature_observed, 0), COALESCE(r.reasoning_tokens, 0), COALESCE(r.reasoning_summary, ''),
			COALESCE((SELECT workflow_run.status
				FROM workflow_ai_chat_runs binding
				JOIN workflow_ai_executions execution ON execution.id=binding.execution_id
				JOIN workflow_runs workflow_run ON workflow_run.id=execution.workflow_run_id
				WHERE binding.chat_run_id=selected.run_id LIMIT 1), ''),
			EXISTS(SELECT 1 FROM workflow_ai_chat_runs binding WHERE binding.chat_run_id=selected.run_id)
		FROM (
			SELECT m.*,
				CASE WHEN m.role = 'user' THEN 0 WHEN m.role = 'assistant' THEN 1 ELSE 2 END AS role_order
			FROM messages m
			WHERE `+condition+`
			ORDER BY m.created_at DESC, role_order DESC, m.id DESC
			LIMIT ?
		) selected
		LEFT JOIN runs r ON r.id = selected.run_id
		ORDER BY selected.created_at, selected.role_order, selected.id`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	values := make([]conversation.Message, 0)
	for rows.Next() {
		var value conversation.Message
		var reasoning conversation.MessageReasoning
		var createdAt, updatedAt string
		if err := rows.Scan(&value.ID, &value.ConversationID, &value.RunID, &value.Role, &value.Status, &createdAt, &updatedAt,
			&reasoning.Status, &reasoning.RequestedLevel, &reasoning.ResolvedLevel, &reasoning.Observed, &reasoning.SignatureObserved, &reasoning.Tokens, &reasoning.Summary, &value.WorkflowStatus, &value.Internal); err != nil {
			return nil, err
		}
		if value.Role == conversation.RoleAssistant && value.RunID != "" {
			value.Reasoning = &reasoning
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
			WHERE tc.id=? AND tc.run_id=? AND tc.tool_name IN (?,?) AND tc.status='completed' AND tr.status='success'`, value.ToolCallID, runID, citation.KnowledgeToolName, citation.WorkflowSeedToolName).Scan(&trustedCall); err != nil {
			return fmt.Errorf("verify citation tool call: %w", err)
		}
		if trustedCall != 1 {
			return fmt.Errorf("citation tool call is not a successful trusted evidence source")
		}
		bibliographyID, snapshot, level, snapshotErr := bibliographySnapshotForAttachment(ctx, tx, projectID, value.AttachmentID, updatedAt)
		if snapshotErr != nil {
			return snapshotErr
		}
		values[index].BibliographyID = bibliographyID
		values[index].Bibliography = snapshot
		values[index].EvidenceLevel = level
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
		snapshot := value.Bibliography
		if len(snapshot) == 0 {
			snapshot = json.RawMessage(`{}`)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_citations(id,message_id,run_id,tool_call_id,project_id,reference_key,ordinal,index_version_id,document_id,attachment_id,chunk_id,source_name,mime_type,locator,title,quote_text,quote_sha256,source_start,source_end,created_at,bibliography_id_snapshot,bibliography_snapshot_json,evidence_level) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			value.ID, value.MessageID, value.RunID, value.ToolCallID, value.ProjectID, value.Reference, value.Ordinal, value.IndexVersionID, value.DocumentID, value.AttachmentID, value.ChunkID, value.SourceName, value.MIMEType, value.Locator, value.Title, value.Quote, value.QuoteSHA256, value.SourceStart, value.SourceEnd, formatTime(value.CreatedAt), value.BibliographyID, string(snapshot), value.EvidenceLevel); err != nil {
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
	rows, err := r.db.QueryContext(ctx, `SELECT id,message_id,run_id,tool_call_id,project_id,reference_key,ordinal,index_version_id,document_id,attachment_id,chunk_id,source_name,mime_type,locator,title,quote_text,quote_sha256,source_start,source_end,created_at,bibliography_id_snapshot,bibliography_snapshot_json,evidence_level FROM message_citations WHERE message_id=? ORDER BY ordinal`, messageID)
	if err != nil {
		return nil, fmt.Errorf("list message citations: %w", err)
	}
	defer rows.Close()
	values := make([]conversation.Citation, 0)
	for rows.Next() {
		var value conversation.Citation
		var createdAt string
		var bibliographyJSON string
		if err := rows.Scan(&value.ID, &value.MessageID, &value.RunID, &value.ToolCallID, &value.ProjectID, &value.Reference, &value.Ordinal, &value.IndexVersionID, &value.DocumentID, &value.AttachmentID, &value.ChunkID, &value.SourceName, &value.MIMEType, &value.Locator, &value.Title, &value.Quote, &value.QuoteSHA256, &value.SourceStart, &value.SourceEnd, &createdAt, &value.BibliographyID, &bibliographyJSON, &value.EvidenceLevel); err != nil {
			return nil, err
		}
		if bibliographyJSON != "{}" {
			value.Bibliography = json.RawMessage(bibliographyJSON)
		}
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func bibliographySnapshotForAttachment(ctx context.Context, tx *sql.Tx, projectID, attachmentID string, at time.Time) (string, json.RawMessage, string, error) {
	var bibliographyID, canonicalJSON string
	var revision int
	var level string
	err := tx.QueryRowContext(ctx, `SELECT b.id,b.revision_number,b.canonical_json,m.evidence_level FROM research_bibliography_materials m JOIN research_bibliographies b ON b.id=m.bibliography_id AND b.project_id=m.project_id WHERE m.project_id=? AND (m.attachment_id=? OR m.attachment_id_snapshot=?) ORDER BY m.updated_at DESC,m.id LIMIT 1`, projectID, attachmentID, attachmentID).Scan(&bibliographyID, &revision, &canonicalJSON, &level)
	if errors.Is(err, sql.ErrNoRows) {
		return "", json.RawMessage(`{}`), "", nil
	}
	if err != nil {
		return "", nil, "", fmt.Errorf("load citation bibliography snapshot: %w", err)
	}
	var data json.RawMessage = json.RawMessage(canonicalJSON)
	if !json.Valid(data) {
		return "", nil, "", fmt.Errorf("citation bibliography snapshot is invalid")
	}
	snapshot, err := json.Marshal(struct {
		SchemaVersion  int             `json:"schemaVersion"`
		BibliographyID string          `json:"bibliographyId"`
		Revision       int             `json:"revision"`
		Data           json.RawMessage `json:"data"`
		CapturedAt     time.Time       `json:"capturedAt"`
	}{1, bibliographyID, revision, data, at.UTC()})
	if err != nil {
		return "", nil, "", err
	}
	return bibliographyID, snapshot, level, nil
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

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
)

type ContextCheckpointRepository struct{ db *sql.DB }

func NewContextCheckpointRepository(db *sql.DB) *ContextCheckpointRepository {
	return &ContextCheckpointRepository{db: db}
}

func (r *ContextCheckpointRepository) Latest(ctx context.Context, conversationID string) (contextmemory.Checkpoint, bool, error) {
	value, err := scanContextCheckpoint(r.db.QueryRowContext(ctx, `SELECT id, conversation_id, revision, through_message_id, summary_text, checkpoint_sha256, source_message_count, source_estimated_tokens, model_profile_id, model_id, api_protocol, created_at FROM conversation_context_checkpoints WHERE conversation_id=? ORDER BY revision DESC LIMIT 1`, conversationID))
	if errors.Is(err, sql.ErrNoRows) {
		return contextmemory.Checkpoint{}, false, nil
	}
	if err != nil {
		return contextmemory.Checkpoint{}, false, fmt.Errorf("load context checkpoint: %w", err)
	}
	if err := contextmemory.Verify(value); err != nil {
		return contextmemory.Checkpoint{}, false, err
	}
	return value, true, nil
}

func (r *ContextCheckpointRepository) Save(ctx context.Context, value contextmemory.Checkpoint) (contextmemory.Checkpoint, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return contextmemory.Checkpoint{}, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM conversation_context_checkpoints WHERE conversation_id=?`, value.ConversationID).Scan(&value.Revision); err != nil {
		return contextmemory.Checkpoint{}, fmt.Errorf("allocate context checkpoint revision: %w", err)
	}
	// ResearchTaskDiscussionMessages reads the same task across immutable runs.
	// Preserve the real boundary ID; never copy messages or substitute a later
	// local ID (which could skip history that the summary never covered).
	// Empty task identities remain conversation-local, as in the history reader.
	result, err := tx.ExecContext(ctx, `INSERT INTO conversation_context_checkpoints(id, conversation_id, revision, through_message_id, summary_text, checkpoint_sha256, source_message_count, source_estimated_tokens, model_profile_id, model_id, api_protocol, created_at) SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM messages m
		WHERE m.id=? AND (m.conversation_id=? OR EXISTS (
			SELECT 1 FROM workflow_conversations current_wc
			JOIN workflow_runs current_wr ON current_wr.id=current_wc.workflow_run_id
			JOIN workflow_runs source_wr ON source_wr.project_id=current_wr.project_id
				AND source_wr.research_task_id=current_wr.research_task_id
			JOIN workflow_conversations source_wc ON source_wc.workflow_run_id=source_wr.id
			WHERE current_wc.conversation_id=? AND source_wc.conversation_id=m.conversation_id
			AND COALESCE(current_wr.research_task_id,'')<>''
		))`,
		value.ID, value.ConversationID, value.Revision, value.ThroughMessageID, value.Summary, value.CheckpointSHA256, value.SourceMessageCount, value.SourceEstimatedTokens, value.ModelProfileID, value.ModelID, value.APIProtocol, formatTime(value.CreatedAt), value.ThroughMessageID, value.ConversationID, value.ConversationID)
	if err != nil {
		return contextmemory.Checkpoint{}, fmt.Errorf("save context checkpoint: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return contextmemory.Checkpoint{}, fmt.Errorf("context checkpoint boundary message does not belong to conversation or its bound research task")
	}
	if err := tx.Commit(); err != nil {
		return contextmemory.Checkpoint{}, fmt.Errorf("commit context checkpoint: %w", err)
	}
	return value, nil
}

func scanContextCheckpoint(row rowScanner) (contextmemory.Checkpoint, error) {
	var value contextmemory.Checkpoint
	var createdAt string
	if err := row.Scan(&value.ID, &value.ConversationID, &value.Revision, &value.ThroughMessageID, &value.Summary, &value.CheckpointSHA256, &value.SourceMessageCount, &value.SourceEstimatedTokens, &value.ModelProfileID, &value.ModelID, &value.APIProtocol, &createdAt); err != nil {
		return contextmemory.Checkpoint{}, err
	}
	var err error
	value.CreatedAt, err = parseTime(createdAt)
	return value, err
}

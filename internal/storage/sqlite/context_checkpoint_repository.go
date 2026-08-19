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
	result, err := tx.ExecContext(ctx, `INSERT INTO conversation_context_checkpoints(id, conversation_id, revision, through_message_id, summary_text, checkpoint_sha256, source_message_count, source_estimated_tokens, model_profile_id, model_id, api_protocol, created_at) SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM messages WHERE id=? AND conversation_id=?`,
		value.ID, value.ConversationID, value.Revision, value.ThroughMessageID, value.Summary, value.CheckpointSHA256, value.SourceMessageCount, value.SourceEstimatedTokens, value.ModelProfileID, value.ModelID, value.APIProtocol, formatTime(value.CreatedAt), value.ThroughMessageID, value.ConversationID)
	if err != nil {
		return contextmemory.Checkpoint{}, fmt.Errorf("save context checkpoint: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return contextmemory.Checkpoint{}, fmt.Errorf("context checkpoint boundary message does not belong to conversation")
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

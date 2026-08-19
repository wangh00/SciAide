package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/embedding"
)

type EmbeddingRepository struct{ db *sql.DB }

func NewEmbeddingRepository(db *sql.DB) *EmbeddingRepository { return &EmbeddingRepository{db: db} }

func (r *EmbeddingRepository) Get(ctx context.Context) (embedding.Config, error) {
	var value embedding.Config
	var enabled int
	var tested sql.NullString
	var updated string
	err := r.db.QueryRowContext(ctx, `SELECT enabled,base_url,model_id,dimensions,config_fingerprint,secret_ref,timeout_seconds,last_tested_at,updated_at FROM knowledge_embedding_config WHERE id=1`).Scan(
		&enabled, &value.BaseURL, &value.ModelID, &value.Dimensions, &value.Fingerprint, &value.SecretRef, &value.TimeoutSeconds, &tested, &updated,
	)
	if err != nil {
		return embedding.Config{}, fmt.Errorf("read Embedding configuration: %w", err)
	}
	value.Enabled = enabled == 1
	var parseErr error
	value.UpdatedAt, parseErr = parseTime(updated)
	if parseErr != nil {
		return embedding.Config{}, parseErr
	}
	if tested.Valid {
		parsed, err := parseTime(tested.String)
		if err != nil {
			return embedding.Config{}, err
		}
		value.LastTestedAt = &parsed
	}
	return value, nil
}

func (r *EmbeddingRepository) Save(ctx context.Context, value embedding.Config) error {
	var tested any
	if value.LastTestedAt != nil {
		tested = formatTime(*value.LastTestedAt)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE knowledge_embedding_config SET enabled=?,base_url=?,model_id=?,dimensions=?,config_fingerprint=?,secret_ref=?,timeout_seconds=?,last_tested_at=?,updated_at=? WHERE id=1`,
		value.Enabled, value.BaseURL, value.ModelID, value.Dimensions, value.Fingerprint, value.SecretRef, value.TimeoutSeconds, tested, formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save Embedding configuration: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("Embedding configuration row is missing")
	}
	return nil
}

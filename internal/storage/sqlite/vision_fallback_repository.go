package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/multimodal"
)

type VisionFallbackRepository struct{ db *sql.DB }

func NewVisionFallbackRepository(db *sql.DB) *VisionFallbackRepository {
	return &VisionFallbackRepository{db: db}
}

func (r *VisionFallbackRepository) Save(ctx context.Context, value multimodal.Channel) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO vision_fallback_channels(id,name,base_url,model_id,api_protocol,secret_ref,enabled,priority,timeout_seconds,max_tokens,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,base_url=excluded.base_url,model_id=excluded.model_id,
		api_protocol=excluded.api_protocol,enabled=excluded.enabled,priority=excluded.priority,
		timeout_seconds=excluded.timeout_seconds,max_tokens=excluded.max_tokens,updated_at=excluded.updated_at`,
		value.ID, value.Name, value.BaseURL, value.ModelID, value.Protocol(), value.SecretRef,
		value.Enabled, value.Priority, value.TimeoutSeconds, value.MaxTokens, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert vision fallback channel: %w", err)
	}
	return nil
}

func (r *VisionFallbackRepository) Get(ctx context.Context, id string) (multimodal.Channel, error) {
	return scanVisionFallbackChannel(r.db.QueryRowContext(ctx, visionFallbackSelect+` WHERE id=?`, id))
}

func (r *VisionFallbackRepository) List(ctx context.Context) ([]multimodal.Channel, error) {
	rows, err := r.db.QueryContext(ctx, visionFallbackSelect+` ORDER BY priority,created_at,id`)
	if err != nil {
		return nil, fmt.Errorf("list vision fallback channels: %w", err)
	}
	defer rows.Close()
	values := make([]multimodal.Channel, 0)
	for rows.Next() {
		value, err := scanVisionFallbackChannel(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *VisionFallbackRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM vision_fallback_channels WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete vision fallback channel: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("vision fallback channel not found")
	}
	return nil
}

const visionFallbackSelect = `SELECT id,name,base_url,model_id,api_protocol,secret_ref,enabled,priority,timeout_seconds,max_tokens,created_at,updated_at FROM vision_fallback_channels`

func scanVisionFallbackChannel(row rowScanner) (multimodal.Channel, error) {
	var value multimodal.Channel
	var protocol, createdAt, updatedAt string
	if err := row.Scan(&value.ID, &value.Name, &value.BaseURL, &value.ModelID, &protocol, &value.SecretRef, &value.Enabled, &value.Priority, &value.TimeoutSeconds, &value.MaxTokens, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, fmt.Errorf("vision fallback channel not found")
		}
		return value, err
	}
	value.APIFormat = protocol
	var err error
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return value, err
	}
	return value, nil
}

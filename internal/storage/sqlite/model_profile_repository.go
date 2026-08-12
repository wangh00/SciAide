package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
)

type ModelProfileRepository struct{ db *sql.DB }

func NewModelProfileRepository(db *sql.DB) *ModelProfileRepository {
	return &ModelProfileRepository{db: db}
}

func (r *ModelProfileRepository) Save(ctx context.Context, value modelprofile.Profile) error {
	headersJSON, err := modelprofile.EncodeHeaders(value.CustomHeaders)
	if err != nil {
		return fmt.Errorf("encode custom headers: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if value.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE model_profiles SET is_default = 0, updated_at = ? WHERE id <> ? AND is_default = 1`, formatTime(value.UpdatedAt), value.ID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO model_profiles(id, name, provider_type, base_url, model_id, secret_ref, timeout_seconds, temperature, max_output_tokens, custom_headers_json, enabled, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, provider_type=excluded.provider_type, base_url=excluded.base_url,
		model_id=excluded.model_id, timeout_seconds=excluded.timeout_seconds, temperature=excluded.temperature,
		max_output_tokens=excluded.max_output_tokens, custom_headers_json=excluded.custom_headers_json,
		enabled=excluded.enabled, is_default=excluded.is_default, updated_at=excluded.updated_at`,
		value.ID, value.Name, value.ProviderType, value.BaseURL, value.ModelID, value.SecretRef, value.TimeoutSeconds,
		value.Temperature, value.MaxOutputTokens, headersJSON, value.Enabled, value.IsDefault, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert model profile: %w", err)
	}
	return tx.Commit()
}

func (r *ModelProfileRepository) Get(ctx context.Context, id string) (modelprofile.Profile, error) {
	return scanModelProfile(r.db.QueryRowContext(ctx, `SELECT id, name, provider_type, base_url, model_id, secret_ref, timeout_seconds, temperature, max_output_tokens, custom_headers_json, enabled, is_default, created_at, updated_at FROM model_profiles WHERE id = ?`, id))
}

func (r *ModelProfileRepository) List(ctx context.Context) ([]modelprofile.Profile, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, provider_type, base_url, model_id, secret_ref, timeout_seconds, temperature, max_output_tokens, custom_headers_json, enabled, is_default, created_at, updated_at FROM model_profiles ORDER BY is_default DESC, updated_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("list model profiles: %w", err)
	}
	defer rows.Close()
	values := make([]modelprofile.Profile, 0)
	for rows.Next() {
		value, err := scanModelProfile(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *ModelProfileRepository) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM model_profiles WHERE id = ?`, id)
	return err
}

func scanModelProfile(row rowScanner) (modelprofile.Profile, error) {
	var value modelprofile.Profile
	var temperature sql.NullFloat64
	var maxOutput sql.NullInt64
	var headersJSON, createdAt, updatedAt string
	if err := row.Scan(&value.ID, &value.Name, &value.ProviderType, &value.BaseURL, &value.ModelID, &value.SecretRef, &value.TimeoutSeconds,
		&temperature, &maxOutput, &headersJSON, &value.Enabled, &value.IsDefault, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return modelprofile.Profile{}, fmt.Errorf("model profile not found")
		}
		return modelprofile.Profile{}, err
	}
	if temperature.Valid {
		v := temperature.Float64
		value.Temperature = &v
	}
	if maxOutput.Valid {
		v := int(maxOutput.Int64)
		value.MaxOutputTokens = &v
	}
	var err error
	value.CustomHeaders, err = modelprofile.DecodeHeaders(headersJSON)
	if err != nil {
		return value, err
	}
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

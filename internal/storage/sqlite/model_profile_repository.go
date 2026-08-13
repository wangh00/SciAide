package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
)

type ModelProfileRepository struct{ db *sql.DB }

func NewModelProfileRepository(db *sql.DB) *ModelProfileRepository {
	return &ModelProfileRepository{db: db}
}

func (r *ModelProfileRepository) Save(ctx context.Context, value modelprofile.Profile) error {
	if !value.APIProtocol.Valid() {
		value.APIProtocol = modelprofile.ProtocolOpenAIChat
	}
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
		INSERT INTO model_profiles(id, name, provider_type, api_protocol, base_url, model_id, secret_ref, timeout_seconds, temperature, max_output_tokens, custom_headers_json, enabled, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, provider_type=excluded.provider_type, base_url=excluded.base_url,
		api_protocol=excluded.api_protocol, model_id=excluded.model_id, timeout_seconds=excluded.timeout_seconds, temperature=excluded.temperature,
		max_output_tokens=excluded.max_output_tokens, custom_headers_json=excluded.custom_headers_json,
		enabled=excluded.enabled, is_default=excluded.is_default, updated_at=excluded.updated_at`,
		value.ID, value.Name, value.ProviderType, value.APIProtocol, value.BaseURL, value.ModelID, value.SecretRef, value.TimeoutSeconds,
		value.Temperature, value.MaxOutputTokens, headersJSON, value.Enabled, value.IsDefault, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert model profile: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_profile_models WHERE profile_id = ?`, value.ID); err != nil {
		return fmt.Errorf("replace profile models: %w", err)
	}
	for _, item := range value.Models {
		if item.ReasoningCapabilitySource == "" {
			item.ReasoningCapabilitySource = "unsupported"
		}
		reasoningJSON, err := json.Marshal(item.ReasoningLevels)
		if err != nil {
			return fmt.Errorf("encode model reasoning levels: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_profile_models(profile_id, model_id, owned_by, enabled, is_default, reasoning_levels_json, reasoning_capability_source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			value.ID, item.ID, item.OwnedBy, item.Enabled, item.IsDefault, string(reasoningJSON), item.ReasoningCapabilitySource, formatTime(value.CreatedAt), formatTime(value.UpdatedAt)); err != nil {
			return fmt.Errorf("insert profile model: %w", err)
		}
	}
	return tx.Commit()
}

func (r *ModelProfileRepository) Get(ctx context.Context, id string) (modelprofile.Profile, error) {
	value, err := scanModelProfile(r.db.QueryRowContext(ctx, `SELECT id, name, provider_type, api_protocol, base_url, model_id, secret_ref, timeout_seconds, temperature, max_output_tokens, custom_headers_json, enabled, is_default, created_at, updated_at FROM model_profiles WHERE id = ?`, id))
	if err != nil {
		return value, err
	}
	value.Models, err = r.listModels(ctx, value.ID, value.ModelID)
	return value, err
}

func (r *ModelProfileRepository) List(ctx context.Context) ([]modelprofile.Profile, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, provider_type, api_protocol, base_url, model_id, secret_ref, timeout_seconds, temperature, max_output_tokens, custom_headers_json, enabled, is_default, created_at, updated_at FROM model_profiles ORDER BY is_default DESC, updated_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("list model profiles: %w", err)
	}
	values := make([]modelprofile.Profile, 0)
	for rows.Next() {
		value, err := scanModelProfile(rows)
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
	// Store uses one SQLite connection, so the profile cursor must be closed
	// before loading child rows.
	for index := range values {
		values[index].Models, err = r.listModels(ctx, values[index].ID, values[index].ModelID)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (r *ModelProfileRepository) Delete(ctx context.Context, id string) error {
	var referenced int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE model_profile_id = ?`, id).Scan(&referenced); err != nil {
		return fmt.Errorf("check model profile references: %w", err)
	}
	if referenced > 0 {
		return fmt.Errorf("model profile is referenced by chat history; disable it instead of deleting it")
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM model_profiles WHERE id = ?`, id)
	if err == nil {
		if affected, _ := result.RowsAffected(); affected == 0 {
			return fmt.Errorf("model profile not found")
		}
	}
	return err
}

func (r *ModelProfileRepository) listModels(ctx context.Context, profileID, legacyDefault string) ([]modelprofile.ProfileModel, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT model_id, owned_by, enabled, is_default, reasoning_levels_json, reasoning_capability_source FROM model_profile_models WHERE profile_id = ? ORDER BY is_default DESC, model_id`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list profile models: %w", err)
	}
	defer rows.Close()
	values := make([]modelprofile.ProfileModel, 0)
	for rows.Next() {
		var value modelprofile.ProfileModel
		var reasoningJSON string
		if err := rows.Scan(&value.ID, &value.OwnedBy, &value.Enabled, &value.IsDefault, &reasoningJSON, &value.ReasoningCapabilitySource); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(reasoningJSON), &value.ReasoningLevels); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 && legacyDefault != "" {
		values = append(values, modelprofile.ProfileModel{ID: legacyDefault, Enabled: true, IsDefault: true})
	}
	return values, nil
}

func scanModelProfile(row rowScanner) (modelprofile.Profile, error) {
	var value modelprofile.Profile
	var temperature sql.NullFloat64
	var maxOutput sql.NullInt64
	var headersJSON, createdAt, updatedAt string
	if err := row.Scan(&value.ID, &value.Name, &value.ProviderType, &value.APIProtocol, &value.BaseURL, &value.ModelID, &value.SecretRef, &value.TimeoutSeconds,
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

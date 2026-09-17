package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/skill"
)

// SkillRepository reads immutable Skill snapshots stored with older Chat Runs.
// New Runs use internal/opensciskill. Historical snapshots are written by
// migrations or archive restoration, never by the active chat runtime.
type SkillRepository struct{ db *sql.DB }

func NewSkillRepository(db *sql.DB) *SkillRepository { return &SkillRepository{db: db} }

func (r *SkillRepository) GetRunContext(ctx context.Context, runID string) (skill.RunContext, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return skill.RunContext{}, fmt.Errorf("Run id is required")
	}
	var projectID, snapshotJSON, snapshotHash, createdAt string
	var schemaVersion int
	err := r.db.QueryRowContext(ctx, `SELECT project_id,schema_version,snapshot_json,snapshot_hash,created_at FROM run_skill_contexts WHERE run_id=?`, runID).Scan(&projectID, &schemaVersion, &snapshotJSON, &snapshotHash, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return skill.RunContext{}, skill.ErrRunContextNotFound
	}
	if err != nil {
		return skill.RunContext{}, fmt.Errorf("read Run Skill context: %w", err)
	}
	value, err := skill.DecodeRunContext([]byte(snapshotJSON), snapshotHash)
	if err != nil {
		return skill.RunContext{}, err
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return skill.RunContext{}, fmt.Errorf("parse Run Skill context time: %w", err)
	}
	if value.RunID != runID || value.ProjectID != projectID || value.SchemaVersion != schemaVersion || !value.CreatedAt.Equal(parsedCreatedAt) {
		return skill.RunContext{}, fmt.Errorf("persisted Run Skill context columns do not match its snapshot")
	}
	if err := verifyRunSkills(ctx, r.db, value); err != nil {
		return skill.RunContext{}, err
	}
	return value, nil
}

type runSkillQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func verifyRunSkills(ctx context.Context, queryer runSkillQueryer, value skill.RunContext) error {
	rows, err := queryer.QueryContext(ctx, `SELECT ordinal,skill_id,skill_version,content_hash,package_hash,created_at FROM run_skills WHERE run_id=? ORDER BY ordinal`, value.RunID)
	if err != nil {
		return fmt.Errorf("read Run Skill provenance: %w", err)
	}
	defer rows.Close()
	ordinal := 0
	for rows.Next() {
		if ordinal >= len(value.Skills) {
			return fmt.Errorf("Run Skill provenance contains unexpected rows")
		}
		var storedOrdinal int
		var id, version, contentHash, packageHash, createdAt string
		if err := rows.Scan(&storedOrdinal, &id, &version, &contentHash, &packageHash, &createdAt); err != nil {
			return err
		}
		selected := value.Skills[ordinal]
		parsedCreatedAt, err := parseTime(createdAt)
		if err != nil {
			return err
		}
		if storedOrdinal != ordinal || id != selected.Manifest.ID || version != selected.Manifest.Version || contentHash != selected.ContentHash || packageHash != selected.PackageHash || !parsedCreatedAt.Equal(value.CreatedAt) {
			return fmt.Errorf("Run Skill provenance does not match its immutable snapshot")
		}
		ordinal++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if ordinal != len(value.Skills) {
		return fmt.Errorf("Run Skill provenance is incomplete")
	}
	return nil
}

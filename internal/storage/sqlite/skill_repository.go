package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/skill"
)

// SkillRepository is a read-only compatibility repository for immutable P4
// Run snapshots. CreateRunContext remains only for archive restore and
// byte-identical fixture import; no production path creates legacy state.
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

func (r *SkillRepository) CreateRunContext(ctx context.Context, value skill.RunContext) error {
	encoded, snapshotHash, err := skill.EncodeRunContext(value)
	if err != nil {
		return err
	}
	if value.SnapshotHash != "" && value.SnapshotHash != snapshotHash {
		return fmt.Errorf("Run Skill context hash does not match its contents")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Run Skill context save: %w", err)
	}
	defer tx.Rollback()
	var projectID, status string
	if err := tx.QueryRowContext(ctx, `SELECT c.project_id,r.status FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE r.id=?`, value.RunID).Scan(&projectID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("Run not found for Skill context")
		}
		return fmt.Errorf("verify Run Skill context owner: %w", err)
	}
	if projectID != value.ProjectID {
		return fmt.Errorf("Run Skill context project does not match the Run")
	}
	if status != "queued" && status != "running" && status != "waiting_approval" {
		return fmt.Errorf("cannot create Skill context for a terminal Run")
	}
	var existingJSON, existingHash string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_json,snapshot_hash FROM run_skill_contexts WHERE run_id=?`, value.RunID).Scan(&existingJSON, &existingHash)
	if err == nil {
		if existingHash != snapshotHash || !bytes.Equal([]byte(existingJSON), encoded) {
			return fmt.Errorf("Run Skill context is immutable and conflicts with persisted state")
		}
		if err := verifyRunSkills(ctx, tx, value); err != nil {
			return err
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect existing Run Skill context: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_skill_contexts(run_id,project_id,schema_version,snapshot_json,snapshot_hash,created_at) VALUES (?,?,?,?,?,?)`, value.RunID, value.ProjectID, value.SchemaVersion, string(encoded), snapshotHash, formatTime(value.CreatedAt)); err != nil {
		return fmt.Errorf("insert Run Skill context: %w", err)
	}
	for ordinal, selected := range value.Skills {
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_skills(run_id,ordinal,skill_id,skill_version,content_hash,package_hash,created_at) VALUES (?,?,?,?,?,?,?)`, value.RunID, ordinal, selected.Manifest.ID, selected.Manifest.Version, selected.ContentHash, selected.PackageHash, formatTime(value.CreatedAt)); err != nil {
			return fmt.Errorf("insert Run Skill provenance: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Run Skill context: %w", err)
	}
	return nil
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

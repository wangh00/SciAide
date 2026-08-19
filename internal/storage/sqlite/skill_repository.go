package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/skill"
)

type SkillRepository struct{ db *sql.DB }

func NewSkillRepository(db *sql.DB) *SkillRepository { return &SkillRepository{db: db} }

func (r *SkillRepository) GetRunContext(ctx context.Context, runID string) (skill.RunContext, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return skill.RunContext{}, fmt.Errorf("Run id is required")
	}
	var projectID, snapshotJSON, snapshotHash, createdAt string
	var schemaVersion int
	err := r.db.QueryRowContext(ctx, `SELECT project_id,schema_version,snapshot_json,snapshot_hash,created_at FROM run_skill_contexts WHERE run_id=?`, runID).
		Scan(&projectID, &schemaVersion, &snapshotJSON, &snapshotHash, &createdAt)
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

// CreateRunContext writes the JSON snapshot and query-friendly run_skills
// provenance in one transaction. Existing snapshots are immutable; only a
// byte-identical idempotent retry is accepted.
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

	var projectID string
	var status string
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

	if _, err := tx.ExecContext(ctx, `INSERT INTO run_skill_contexts(run_id,project_id,schema_version,snapshot_json,snapshot_hash,created_at) VALUES (?,?,?,?,?,?)`,
		value.RunID, value.ProjectID, value.SchemaVersion, string(encoded), snapshotHash, formatTime(value.CreatedAt)); err != nil {
		return fmt.Errorf("insert Run Skill context: %w", err)
	}
	for ordinal, selected := range value.Skills {
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_skills(run_id,ordinal,skill_id,skill_version,content_hash,package_hash,created_at) VALUES (?,?,?,?,?,?,?)`,
			value.RunID, ordinal, selected.Manifest.ID, selected.Manifest.Version, selected.ContentHash, selected.PackageHash, formatTime(value.CreatedAt)); err != nil {
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

func (r *SkillRepository) Reconcile(ctx context.Context, packages []skill.InstalledSkill, diagnostics []skill.Diagnostic, seenPaths []string, at time.Time) (skill.ReconcileResult, error) {
	result := skill.ReconcileResult{RejectedChanges: []skill.Diagnostic{}}
	validByPath := make(map[string]struct{}, len(packages))
	for _, value := range packages {
		if err := validateInstalledSkill(value); err != nil {
			return result, err
		}
	}
	seen := make(map[string]struct{}, len(seenPaths))
	for _, value := range seenPaths {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if !validPackageRelativePath(value) {
			return result, fmt.Errorf("invalid discovered Skill package path")
		}
		seen[value] = struct{}{}
	}
	invalidByPath := make(map[string]string, len(diagnostics))
	for _, diagnostic := range diagnostics {
		path := filepath.ToSlash(strings.TrimSpace(diagnostic.PackageRelativePath))
		if !validPackageRelativePath(path) {
			continue
		}
		invalidByPath[path] = boundedError(diagnostic.Message)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin Skill reconciliation: %w", err)
	}
	defer tx.Rollback()
	for _, value := range packages {
		manifest, err := json.Marshal(value.Manifest)
		if err != nil {
			return result, err
		}
		var storedPath, storedManifestHash, storedContentHash, storedPackageHash string
		err = tx.QueryRowContext(ctx, `SELECT package_rel_path,manifest_hash,content_hash,package_hash FROM installed_skills WHERE skill_id=? AND skill_version=?`, value.Manifest.ID, value.Manifest.Version).
			Scan(&storedPath, &storedManifestHash, &storedContentHash, &storedPackageHash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, fmt.Errorf("inspect installed Skill %s@%s: %w", value.Manifest.ID, value.Manifest.Version, err)
		}
		if err == nil && (storedPath != value.PackageRelativePath || storedManifestHash != value.ManifestHash || storedContentHash != value.ContentHash || storedPackageHash != value.PackageHash) {
			// Discovery is deliberately not an update/install operation. Once a
			// package has been trusted, changed bytes must not silently replace
			// its recorded provenance (especially during automatic startup
			// refresh). P4.2's explicit installer will own accepting upgrades.
			message := "Skill package changed after installation; restore it or install the update explicitly"
			invalidByPath[value.PackageRelativePath] = message
			result.RejectedChanges = append(result.RejectedChanges, skill.Diagnostic{PackageRelativePath: value.PackageRelativePath, Message: message})
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO installed_skills(skill_id,skill_version,manifest_json,package_rel_path,manifest_hash,content_hash,package_hash,integrity_status,integrity_error,installed_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(skill_id,skill_version) DO UPDATE SET manifest_json=excluded.manifest_json,package_rel_path=excluded.package_rel_path,integrity_status='valid',integrity_error='',updated_at=excluded.updated_at`,
			value.Manifest.ID, value.Manifest.Version, string(manifest), value.PackageRelativePath, value.ManifestHash, value.ContentHash, value.PackageHash, skill.IntegrityValid, "", formatTime(value.InstalledAt), formatTime(at))
		if err != nil {
			return result, fmt.Errorf("upsert installed Skill %s@%s: %w", value.Manifest.ID, value.Manifest.Version, err)
		}
		validByPath[value.PackageRelativePath] = struct{}{}
	}
	rows, err := tx.QueryContext(ctx, `SELECT package_rel_path,integrity_status FROM installed_skills`)
	if err != nil {
		return result, err
	}
	type statusRow struct {
		path   string
		status skill.IntegrityStatus
	}
	statuses := make([]statusRow, 0)
	for rows.Next() {
		var value statusRow
		if err := rows.Scan(&value.path, &value.status); err != nil {
			rows.Close()
			return result, err
		}
		statuses = append(statuses, value)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	for _, value := range statuses {
		if _, valid := validByPath[value.path]; valid {
			continue
		}
		next, message := skill.IntegrityMissing, "Skill package is missing from the Skill directory"
		if _, exists := seen[value.path]; exists {
			next, message = skill.IntegrityInvalid, invalidByPath[value.path]
			if message == "" {
				message = "Skill package failed integrity validation"
			}
		} else {
			result.Missing++
		}
		if _, err := tx.ExecContext(ctx, `UPDATE installed_skills SET integrity_status=?,integrity_error=?,updated_at=? WHERE package_rel_path=?`, next, message, formatTime(at), value.path); err != nil {
			return result, fmt.Errorf("mark Skill package %s: %w", next, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit Skill reconciliation: %w", err)
	}
	return result, nil
}

func (r *SkillRepository) ListInstalled(ctx context.Context) ([]skill.InstalledSkill, error) {
	rows, err := r.db.QueryContext(ctx, installedSkillSelect+` ORDER BY i.skill_id,i.skill_version DESC`)
	if err != nil {
		return nil, fmt.Errorf("list installed Skills: %w", err)
	}
	defer rows.Close()
	values := make([]skill.InstalledSkill, 0)
	for rows.Next() {
		value, err := scanInstalledSkill(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	skill.SortInstalled(values)
	return values, nil
}

func (r *SkillRepository) GetInstalled(ctx context.Context, id, version string) (skill.InstalledSkill, error) {
	return scanInstalledSkill(r.db.QueryRowContext(ctx, installedSkillSelect+` WHERE i.skill_id=? AND i.skill_version=?`, strings.TrimSpace(id), strings.TrimSpace(version)))
}

func (r *SkillRepository) AcceptInstalled(ctx context.Context, value skill.InstalledSkill, source skill.PackageSource, at time.Time) error {
	if err := validateInstalledSkill(value); err != nil {
		return err
	}
	if err := validatePackageSource(source); err != nil {
		return err
	}
	manifest, err := json.Marshal(value.Manifest)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin explicit Skill installation: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO installed_skills(skill_id,skill_version,manifest_json,package_rel_path,manifest_hash,content_hash,package_hash,integrity_status,integrity_error,installed_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(skill_id,skill_version) DO UPDATE SET manifest_json=excluded.manifest_json,package_rel_path=excluded.package_rel_path,manifest_hash=excluded.manifest_hash,content_hash=excluded.content_hash,package_hash=excluded.package_hash,integrity_status='valid',integrity_error='',updated_at=excluded.updated_at`,
		value.Manifest.ID, value.Manifest.Version, string(manifest), value.PackageRelativePath, value.ManifestHash, value.ContentHash, value.PackageHash, skill.IntegrityValid, "", formatTime(at), formatTime(at)); err != nil {
		return fmt.Errorf("accept installed Skill provenance: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO skill_package_sources(skill_id,skill_version,source_kind,source_name,source_hash,archive_rel_path,installed_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(skill_id,skill_version) DO UPDATE SET source_kind=excluded.source_kind,source_name=excluded.source_name,source_hash=excluded.source_hash,archive_rel_path=excluded.archive_rel_path,updated_at=excluded.updated_at`,
		value.Manifest.ID, value.Manifest.Version, source.Kind, source.Name, source.Hash, source.ArchiveRelativePath, formatTime(at), formatTime(at)); err != nil {
		return fmt.Errorf("save Skill package source: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit explicit Skill installation: %w", err)
	}
	return nil
}

func (r *SkillRepository) CountProjectSkillReferences(ctx context.Context, id, version string) (int, error) {
	if !skill.ValidID(strings.TrimSpace(id)) || !skill.ValidVersion(strings.TrimSpace(version)) {
		return 0, fmt.Errorf("invalid Skill identity")
	}
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM project_skills WHERE skill_id=? AND skill_version=?`, strings.TrimSpace(id), strings.TrimSpace(version)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count project Skill references: %w", err)
	}
	return count, nil
}

func (r *SkillRepository) RemoveInstalled(ctx context.Context, id, version string, removeProjectLinks bool) (int, error) {
	id, version = strings.TrimSpace(id), strings.TrimSpace(version)
	if !skill.ValidID(id) || !skill.ValidVersion(version) {
		return 0, fmt.Errorf("invalid Skill identity")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin Skill uninstall: %w", err)
	}
	defer tx.Rollback()
	var references int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM project_skills WHERE skill_id=? AND skill_version=?`, id, version).Scan(&references); err != nil {
		return 0, err
	}
	if references > 0 && !removeProjectLinks {
		return 0, fmt.Errorf("Skill is still referenced by %d project(s)", references)
	}
	removedLinks := 0
	if removeProjectLinks && references > 0 {
		result, err := tx.ExecContext(ctx, `DELETE FROM project_skills WHERE skill_id=? AND skill_version=?`, id, version)
		if err != nil {
			return 0, fmt.Errorf("remove project Skill links: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		removedLinks = int(count)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM installed_skills WHERE skill_id=? AND skill_version=?`, id, version)
	if err != nil {
		return 0, fmt.Errorf("delete installed Skill: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if deleted != 1 {
		return 0, skill.ErrSkillNotFound
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit Skill uninstall: %w", err)
	}
	return removedLinks, nil
}

func (r *SkillRepository) SetProjectSkill(ctx context.Context, value skill.ProjectSkill) error {
	if strings.TrimSpace(value.ProjectID) == "" || !skill.ValidID(value.SkillID) || !skill.ValidVersion(value.Version) || value.Priority < 0 || value.Priority > 1000 {
		return fmt.Errorf("invalid project Skill selection")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO project_skills(project_id,skill_id,skill_version,enabled,priority,created_at,updated_at) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(project_id,skill_id) DO UPDATE SET skill_version=excluded.skill_version,enabled=excluded.enabled,priority=excluded.priority,updated_at=excluded.updated_at`,
		value.ProjectID, value.SkillID, value.Version, value.Enabled, value.Priority, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save project Skill: %w", err)
	}
	return nil
}

func (r *SkillRepository) GetProjectSkill(ctx context.Context, projectID, skillID string) (skill.ProjectSkill, error) {
	return scanProjectSkill(r.db.QueryRowContext(ctx, projectSkillSelect+` WHERE project_id=? AND skill_id=?`, strings.TrimSpace(projectID), strings.TrimSpace(skillID)))
}

func (r *SkillRepository) ListProjectSkills(ctx context.Context, projectID string) ([]skill.ProjectSkill, error) {
	rows, err := r.db.QueryContext(ctx, projectSkillSelect+` WHERE project_id=? ORDER BY priority,skill_id`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("list project Skills: %w", err)
	}
	defer rows.Close()
	values := make([]skill.ProjectSkill, 0)
	for rows.Next() {
		value, err := scanProjectSkill(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanInstalledSkill(row rowScanner) (skill.InstalledSkill, error) {
	var value skill.InstalledSkill
	var manifest, installedAt, updatedAt, sourceKind, sourceName, sourceHash, archiveRelativePath string
	if err := row.Scan(&manifest, &value.PackageRelativePath, &value.ManifestHash, &value.ContentHash, &value.PackageHash, &value.Integrity, &value.IntegrityError, &installedAt, &updatedAt, &sourceKind, &sourceName, &sourceHash, &archiveRelativePath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, skill.ErrSkillNotFound
		}
		return value, err
	}
	if err := json.Unmarshal([]byte(manifest), &value.Manifest); err != nil {
		return value, fmt.Errorf("decode installed Skill manifest: %w", err)
	}
	var err error
	value.InstalledAt, err = parseTime(installedAt)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return value, err
	}
	value.MissingRequiredTools = []string{}
	value.MissingOptionalTools = []string{}
	if sourceKind != "" {
		value.Source = skill.PackageSource{Kind: skill.SourceKind(sourceKind), Name: sourceName, Hash: sourceHash, Archived: archiveRelativePath != "", ArchiveRelativePath: archiveRelativePath}
	}
	return value, nil
}

func scanProjectSkill(row rowScanner) (skill.ProjectSkill, error) {
	var value skill.ProjectSkill
	var createdAt, updatedAt string
	if err := row.Scan(&value.ProjectID, &value.SkillID, &value.Version, &value.Enabled, &value.Priority, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, skill.ErrProjectSkillNotFound
		}
		return value, err
	}
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

func validateInstalledSkill(value skill.InstalledSkill) error {
	if err := skill.ValidateManifest(value.Manifest); err != nil {
		return err
	}
	if !validPackageRelativePath(value.PackageRelativePath) || !validSHA256(value.ManifestHash) || !validSHA256(value.ContentHash) || !validSHA256(value.PackageHash) || value.Integrity != skill.IntegrityValid {
		return fmt.Errorf("invalid installed Skill integrity metadata")
	}
	return nil
}

func validatePackageSource(value skill.PackageSource) error {
	if value.Kind != skill.SourceFolder && value.Kind != skill.SourceZIP && value.Kind != skill.SourceBuiltin {
		return fmt.Errorf("invalid Skill package source kind")
	}
	name := strings.TrimSpace(value.Name)
	if name == "" || len([]rune(name)) > 255 || strings.ContainsRune(name, 0) || strings.ContainsAny(name, "\r\n\t") || filepath.Base(name) != name {
		return fmt.Errorf("invalid Skill package source name")
	}
	if !validSHA256(value.Hash) || !validArchiveRelativePath(value.ArchiveRelativePath) {
		return fmt.Errorf("invalid Skill package source provenance")
	}
	return nil
}

func validArchiveRelativePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") || filepath.Clean(value) != filepath.FromSlash(value) {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) != 4 || parts[0] != "packages" || !skill.ValidID(parts[1]) || !skill.ValidVersion(parts[2]) || parts[3] != strings.TrimSuffix(parts[3], ".zip")+".zip" {
		return false
	}
	return validSHA256(strings.TrimSuffix(parts[3], ".zip"))
}

func validPackageRelativePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	parts := strings.Split(value, "/")
	return len(parts) == 2 && skill.ValidID(parts[0]) && skill.ValidVersion(parts[1])
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func boundedError(value string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 500 {
		value = string([]rune(value)[:500])
	}
	return value
}

const installedSkillSelect = `SELECT i.manifest_json,i.package_rel_path,i.manifest_hash,i.content_hash,i.package_hash,i.integrity_status,i.integrity_error,i.installed_at,i.updated_at,COALESCE(s.source_kind,''),COALESCE(s.source_name,''),COALESCE(s.source_hash,''),COALESCE(s.archive_rel_path,'') FROM installed_skills i LEFT JOIN skill_package_sources s ON s.skill_id=i.skill_id AND s.skill_version=i.skill_version`
const projectSkillSelect = `SELECT project_id,skill_id,skill_version,enabled,priority,created_at,updated_at FROM project_skills`

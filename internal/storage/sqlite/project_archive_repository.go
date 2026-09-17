package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

type ProjectArchiveRepository struct {
	db *sql.DB
}

const (
	archiveSkillBindingsTable = "sciaide_archive_skill_bindings"
	archiveModelBaseURL       = "https://archive.invalid/v1"
)

func NewProjectArchiveRepository(db *sql.DB) *ProjectArchiveRepository {
	return &ProjectArchiveRepository{db: db}
}

func (r *ProjectArchiveRepository) CreateSnapshot(ctx context.Context, projectID, destination string) (projectarchive.Snapshot, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || destination == "" {
		return projectarchive.Snapshot{}, fmt.Errorf("project archive snapshot identity is required")
	}
	if err := ensureProjectArchiveIdle(ctx, r.db, projectID); err != nil {
		return projectarchive.Snapshot{}, err
	}
	if err := BackupTo(ctx, r.db, destination); err != nil {
		return projectarchive.Snapshot{}, err
	}
	db, err := OpenExisting(ctx, destination, false)
	if err != nil {
		_ = os.Remove(destination)
		return projectarchive.Snapshot{}, err
	}
	defer db.Close()
	bindings, err := inspectArchiveSkillBindings(ctx, db, projectID)
	if err != nil {
		return projectarchive.Snapshot{}, err
	}
	if err := pruneArchiveSnapshot(ctx, db, projectID); err != nil {
		return projectarchive.Snapshot{}, err
	}
	if err := writeArchiveSkillBindings(ctx, db, bindings); err != nil {
		return projectarchive.Snapshot{}, err
	}
	snapshot, err := inspectArchiveSnapshot(ctx, db, projectID)
	if err != nil {
		return projectarchive.Snapshot{}, err
	}
	snapshot.SkillBindings = bindings
	stagingRoot := filepath.Dir(destination)
	for index := range snapshot.Files {
		if snapshot.Files[index].Kind != projectarchive.FileKnowledgeIndex {
			continue
		}
		source := filepath.Join(project.PrivateDataPath(snapshot.Project), filepath.FromSlash(snapshot.Files[index].StorageRelativePath))
		if info, err := os.Lstat(source); err != nil {
			if os.IsNotExist(err) && !snapshot.Files[index].Required {
				continue
			}
			return projectarchive.Snapshot{}, fmt.Errorf("inspect knowledge index for backup: %w", err)
		} else if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return projectarchive.Snapshot{}, fmt.Errorf("knowledge index is not a regular file")
		}
		consistent := filepath.Join(stagingRoot, fmt.Sprintf("knowledge-%04d.db", index))
		indexDB, err := OpenExisting(ctx, source, true)
		if err != nil {
			return projectarchive.Snapshot{}, fmt.Errorf("open knowledge index for backup: %w", err)
		}
		backupErr := BackupTo(ctx, indexDB, consistent)
		closeErr := indexDB.Close()
		if backupErr != nil {
			return projectarchive.Snapshot{}, backupErr
		}
		if closeErr != nil {
			return projectarchive.Snapshot{}, closeErr
		}
		consistentDB, err := OpenExisting(ctx, consistent, false)
		if err != nil {
			return projectarchive.Snapshot{}, err
		}
		if _, err := consistentDB.ExecContext(ctx, `DELETE FROM query_embedding_cache`); err != nil && !strings.Contains(err.Error(), "no such table") {
			consistentDB.Close()
			return projectarchive.Snapshot{}, fmt.Errorf("remove knowledge query cache from archive: %w", err)
		}
		if err := validateSQLite(ctx, consistentDB); err != nil {
			consistentDB.Close()
			return projectarchive.Snapshot{}, err
		}
		if _, err := consistentDB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE); VACUUM`); err != nil {
			consistentDB.Close()
			return projectarchive.Snapshot{}, err
		}
		if err := consistentDB.Close(); err != nil {
			return projectarchive.Snapshot{}, err
		}
		snapshot.Files[index].SourcePath = consistent
	}
	if err := validateSQLite(ctx, db); err != nil {
		return projectarchive.Snapshot{}, err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return projectarchive.Snapshot{}, fmt.Errorf("checkpoint project archive snapshot: %w", err)
	}
	return snapshot, nil
}

func (r *ProjectArchiveRepository) ValidateSnapshot(ctx context.Context, filePath, sourceProjectID string, databaseSchemaVersion int) (projectarchive.Snapshot, error) {
	db, err := OpenExisting(ctx, filePath, true)
	if err != nil {
		return projectarchive.Snapshot{}, fmt.Errorf("open archived project database: %w", err)
	}
	if err := validateSQLite(ctx, db); err != nil {
		db.Close()
		return projectarchive.Snapshot{}, err
	}
	current, err := CurrentSchemaVersion()
	if err != nil {
		db.Close()
		return projectarchive.Snapshot{}, err
	}
	if databaseSchemaVersion <= 0 || databaseSchemaVersion > current {
		db.Close()
		return projectarchive.Snapshot{}, fmt.Errorf("project archive database schema %d is unsupported", databaseSchemaVersion)
	}
	if err := validateArchiveMigrations(ctx, db, databaseSchemaVersion); err != nil {
		db.Close()
		return projectarchive.Snapshot{}, err
	}
	if err := validateArchiveSchemaForVersion(ctx, db, databaseSchemaVersion); err != nil {
		db.Close()
		return projectarchive.Snapshot{}, err
	}
	if _, err := readArchiveSkillBindings(ctx, db); err != nil {
		db.Close()
		return projectarchive.Snapshot{}, err
	}
	if err := validateArchiveSanitization(ctx, db); err != nil {
		db.Close()
		return projectarchive.Snapshot{}, err
	}
	if err := db.Close(); err != nil {
		return projectarchive.Snapshot{}, err
	}
	if databaseSchemaVersion < current {
		writable, err := OpenExisting(ctx, filePath, false)
		if err != nil {
			return projectarchive.Snapshot{}, fmt.Errorf("open archived project database for isolated migration: %w", err)
		}
		migrateErr := Migrate(ctx, writable)
		if migrateErr == nil {
			// New migrations may create default global configuration rows. Reapply
			// the current archive sanitization policy inside restore staging before
			// any migrated bytes are inspected, rewritten or merged.
			migrateErr = pruneArchiveSnapshot(ctx, writable, strings.TrimSpace(sourceProjectID))
		}
		closeErr := writable.Close()
		if migrateErr != nil {
			return projectarchive.Snapshot{}, fmt.Errorf("migrate archived project database: %w", migrateErr)
		}
		if closeErr != nil {
			return projectarchive.Snapshot{}, closeErr
		}
	}
	db, err = OpenExisting(ctx, filePath, true)
	if err != nil {
		return projectarchive.Snapshot{}, err
	}
	defer db.Close()
	if err := validateSQLite(ctx, db); err != nil {
		return projectarchive.Snapshot{}, err
	}
	if err := validateArchiveMigrations(ctx, db, current); err != nil {
		return projectarchive.Snapshot{}, err
	}
	if err := validateArchiveTables(ctx, db); err != nil {
		return projectarchive.Snapshot{}, err
	}
	snapshot, err := inspectArchiveSnapshot(ctx, db, strings.TrimSpace(sourceProjectID))
	if err != nil {
		return projectarchive.Snapshot{}, err
	}
	// Manifest comparison must retain the source archive version even though
	// the isolated extracted database is now upgraded for rewrite and merge.
	snapshot.DatabaseSchemaVersion = databaseSchemaVersion
	return snapshot, nil
}

func (r *ProjectArchiveRepository) RewriteSnapshot(ctx context.Context, filePath string, restored project.Project) (projectarchive.RewritePlan, error) {
	db, err := OpenExisting(ctx, filePath, false)
	if err != nil {
		return projectarchive.RewritePlan{}, err
	}
	defer db.Close()
	var sourceProjectID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM projects`).Scan(&sourceProjectID); err != nil {
		return projectarchive.RewritePlan{}, fmt.Errorf("read archived source project: %w", err)
	}
	maps := newArchiveIDMaps()
	if err := allocateArchiveIDs(ctx, db, maps); err != nil {
		return projectarchive.RewritePlan{}, err
	}
	plan := projectarchive.RewritePlan{Indexes: []projectarchive.IndexRewrite{}, TaskIDs: cloneStringMap(maps.values["research_tasks"])}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_profiles`).Scan(&plan.HistoricalProfiles); err != nil {
		return plan, err
	}
	if err := rewriteProjectSnapshot(ctx, db, sourceProjectID, restored, maps, &plan); err != nil {
		return plan, err
	}
	if err := validateSQLite(ctx, db); err != nil {
		return plan, err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return plan, err
	}
	return plan, nil
}

func (r *ProjectArchiveRepository) RewriteKnowledgeIndexes(ctx context.Context, privateRoot string, plan projectarchive.RewritePlan) error {
	guard, err := pathguard.Open(privateRoot)
	if err != nil {
		return fmt.Errorf("open restored project private storage: %w", err)
	}
	defer guard.Close()
	for _, item := range plan.Indexes {
		absolute, err := guard.Absolute(filepath.FromSlash(item.StorageRelativePath))
		if err != nil {
			return err
		}
		db, err := OpenExisting(ctx, absolute, false)
		if err != nil {
			return fmt.Errorf("open restored knowledge index: %w", err)
		}
		rewriteErr := rewriteKnowledgeIndex(ctx, db, item)
		if rewriteErr == nil {
			rewriteErr = validateSQLite(ctx, db)
		}
		if rewriteErr == nil {
			_, rewriteErr = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE); VACUUM`)
		}
		closeErr := db.Close()
		if rewriteErr != nil {
			return rewriteErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (r *ProjectArchiveRepository) MergeSnapshot(ctx context.Context, filePath string, bindings []projectarchive.SkillBinding) (projectarchive.MergeReport, error) {
	result := projectarchive.MergeReport{MissingSkillBindings: []projectarchive.SkillBinding{}}
	connection, err := r.db.Conn(ctx)
	if err != nil {
		return result, fmt.Errorf("open project archive merge connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `ATTACH DATABASE ? AS archive`, filePath); err != nil {
		return result, fmt.Errorf("attach restored project database: %w", err)
	}
	detached := false
	defer func() {
		if !detached {
			_, _ = connection.ExecContext(context.Background(), `DETACH DATABASE archive`)
		}
	}()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin project archive merge: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
		return result, err
	}
	if err := validateAttachedArchive(ctx, tx); err != nil {
		return result, err
	}
	for _, table := range archiveMergeOrder {
		columns, err := tableColumns(ctx, tx, "main", table)
		if err != nil {
			return result, err
		}
		archiveColumns, err := tableColumns(ctx, tx, "archive", table)
		if err != nil {
			return result, err
		}
		if strings.Join(columns, "\x00") != strings.Join(archiveColumns, "\x00") {
			return result, fmt.Errorf("project archive table %s schema does not match the current database", table)
		}
		quoted := quoteColumns(columns)
		selected := quoted
		if table == "research_timeline" {
			// Source rows may have fired capture triggers during merge. Replace only
			// this imported task's projection, preserving its durable relative order.
			if _, err := tx.ExecContext(ctx, `DELETE FROM main.research_timeline WHERE research_task_id IN (SELECT id FROM archive.research_tasks)`); err != nil {
				return result, err
			}
			columns = columns[1:] // Allocate new database-local sequence values.
			quoted = quoteColumns(columns)
			if _, err := tx.ExecContext(ctx, `INSERT INTO main.research_timeline(`+quoted+`) SELECT `+quoted+` FROM archive.research_timeline ORDER BY sequence`); err != nil {
				return result, err
			}
			continue
		}
		if table == "research_tasks" {
			parts := make([]string, len(columns))
			for index, column := range columns {
				parts[index] = quoteIdentifier(column)
				if column == "status" {
					parts[index] = `CASE WHEN status='archived' THEN 'active' ELSE status END`
				}
			}
			selected = strings.Join(parts, ",")
		}
		if table == "research_evidence_entries" {
			parts := make([]string, len(columns))
			for index, column := range columns {
				parts[index] = quoteIdentifier(column)
				if column == "review_status" {
					parts[index] = `CASE WHEN provenance='model' THEN 'pending' ELSE review_status END`
				}
			}
			selected = strings.Join(parts, ",")
		}
		if table == "workflows" {
			parts := make([]string, len(columns))
			for index, column := range columns {
				parts[index] = quoteIdentifier(column)
				if column == "current_version_id" {
					parts[index] = "NULL"
				}
			}
			selected = strings.Join(parts, ",")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO main.`+quoteIdentifier(table)+`(`+quoted+`) SELECT `+selected+` FROM archive.`+quoteIdentifier(table)); err != nil {
			return result, fmt.Errorf("merge project archive table %s: %w", table, err)
		}
		if table == "research_evidence_entries" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE main.research_evidence_entries
				SET review_status=(
					SELECT source.review_status FROM archive.research_evidence_entries AS source
					WHERE source.id=research_evidence_entries.id
				)
				WHERE id IN (SELECT id FROM archive.research_evidence_entries)`); err != nil {
				return result, fmt.Errorf("restore evidence review status: %w", err)
			}
		}
		if table == "workflow_versions" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE main.workflows
				SET current_version_id=(
					SELECT source.current_version_id FROM archive.workflows AS source
					WHERE source.id=workflows.id
				)
				WHERE id IN (SELECT id FROM archive.workflows)`); err != nil {
				return result, fmt.Errorf("restore Workflow current version: %w", err)
			}
		}
	}
	var projectID string
	// Run insertion refreshes task summaries. Restore the archived metadata
	// only after its owned history is present, including final archive status.
	if _, err := tx.ExecContext(ctx, `UPDATE main.research_tasks SET
		status=(SELECT source.status FROM archive.research_tasks source WHERE source.id=research_tasks.id),
		latest_run_id=(SELECT source.latest_run_id FROM archive.research_tasks source WHERE source.id=research_tasks.id),
		latest_run_status=(SELECT source.latest_run_status FROM archive.research_tasks source WHERE source.id=research_tasks.id),
		updated_at=(SELECT source.updated_at FROM archive.research_tasks source WHERE source.id=research_tasks.id)
		WHERE id IN (SELECT id FROM archive.research_tasks)`); err != nil {
		return result, fmt.Errorf("restore research task summaries: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM archive.projects`).Scan(&projectID); err != nil {
		return result, err
	}
	for _, binding := range bindings {
		var count int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM installed_skills WHERE skill_id=? AND skill_version=? AND content_hash=? AND package_hash=? AND integrity_status='valid'`, binding.SkillID, binding.Version, binding.ContentHash, binding.PackageHash).Scan(&count)
		if err != nil {
			return result, err
		}
		if count != 1 {
			result.MissingSkillBindings = append(result.MissingSkillBindings, binding)
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_skills(project_id,skill_id,skill_version,enabled,priority,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`,
			projectID, binding.SkillID, binding.Version, binding.Enabled, binding.Priority, formatTime(time.Now().UTC()), formatTime(time.Now().UTC())); err != nil {
			return result, err
		}
		result.RestoredSkillBindings++
	}
	if rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`); err != nil {
		return result, err
	} else {
		defer rows.Close()
		if rows.Next() {
			return result, fmt.Errorf("restored project graph violates a foreign key")
		}
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit restored project graph: %w", err)
	}
	if _, err := connection.ExecContext(ctx, `DETACH DATABASE archive`); err == nil {
		detached = true
	}
	return result, nil
}

func (r *ProjectArchiveRepository) ProjectExists(ctx context.Context, projectID string) (bool, error) {
	var found int
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=?)`, strings.TrimSpace(projectID)).Scan(&found); err != nil {
		return false, err
	}
	return found == 1, nil
}

func ensureProjectArchiveIdle(ctx context.Context, db *sql.DB, projectID string) error {
	var active int
	queries := []struct {
		query   string
		message string
	}{
		{`SELECT COUNT(*) FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE c.project_id=? AND r.status IN ('queued','running','waiting_approval')`, "project has an active chat Run"},
		{`SELECT COUNT(*) FROM knowledge_import_jobs WHERE project_id=? AND status IN ('queued','running')`, "project has active knowledge indexing"},
		{`SELECT COUNT(*) FROM research_candidates WHERE project_id=? AND import_status='importing'`, "project has an active research import"},
		{`SELECT COUNT(*) FROM python_environment_operations WHERE project_id=? AND state='running'`, "project has an active Python environment operation"},
		{`SELECT COUNT(*) FROM workflow_runs WHERE project_id=? AND status IN ('queued','running','waiting_approval','waiting_human_confirmation')`, "project has an active Workflow Run"},
	}
	for _, check := range queries {
		if err := db.QueryRowContext(ctx, check.query, projectID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("%s; wait for it to finish before exporting", check.message)
		}
	}
	return nil
}

func pruneArchiveSnapshot(ctx context.Context, db *sql.DB, projectID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`DELETE FROM projects WHERE id<>?`,
		`DELETE FROM run_events WHERE aggregate_id NOT IN (SELECT id FROM runs)`,
		`DELETE FROM run_event_sequences WHERE aggregate_id NOT IN (SELECT id FROM runs)`,
		`DELETE FROM model_profiles WHERE id NOT IN (
			SELECT model_profile_id FROM runs
			UNION SELECT model_profile_id FROM conversations WHERE model_profile_id<>''
			UNION SELECT model_profile_id FROM conversation_context_checkpoints WHERE model_profile_id<>''
		)`,
		`DELETE FROM project_skills`,
		`DELETE FROM approvals`,
		`DELETE FROM permission_grants`,
		`DELETE FROM mcp_servers`,
		`DELETE FROM vision_fallback_channels`,
		`DELETE FROM knowledge_embedding_config`,
		`DELETE FROM settings`,
		`DELETE FROM skill_policies`,
		`DELETE FROM retired_builtin_skill_packages`,
		`DELETE FROM skill_package_sources`,
		`DELETE FROM installed_skills`,
		`DELETE FROM python_environment_operations`,
	}
	for index, statement := range statements {
		arguments := []any{}
		if index == 0 {
			arguments = append(arguments, projectID)
		}
		if _, err := tx.ExecContext(ctx, statement, arguments...); err != nil {
			return fmt.Errorf("sanitize project archive: %w", err)
		}
	}
	var schemaVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		return err
	}
	if schemaVersion >= 82 {
		for _, table := range []string{"model_resource_actions", "model_resource_sessions"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+quoteIdentifier(table)); err != nil {
				return fmt.Errorf("sanitize resource capabilities: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET permission_mode='plan'`); err != nil {
		return err
	}
	if hasPermissionMode, err := sqliteTableHasColumn(ctx, tx, "workflow_runs", "permission_mode"); err != nil {
		return err
	} else if hasPermissionMode {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET permission_mode='plan'`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_profiles SET base_url=?,secret_ref='archive/profile/'||id,custom_headers_json='{}',enabled=0,is_default=0`, archiveModelBaseURL); err != nil {
		return err
	}
	pythonReset := `UPDATE python_environments SET state='absent',base_executable_path='',environment_python_path='',last_verified_at=NULL,error_message=''`
	if hasKind, err := sqliteTableHasColumn(ctx, tx, "python_environments", "environment_kind"); err != nil {
		return err
	} else if hasKind {
		pythonReset = `UPDATE python_environments SET state='absent',environment_kind='workspace_managed',base_executable_path='',environment_python_path='',last_verified_at=NULL,error_message=''`
	}
	if _, err := tx.ExecContext(ctx, pythonReset); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE); VACUUM`); err != nil {
		return fmt.Errorf("compact project archive snapshot: %w", err)
	}
	return nil
}

func inspectArchiveSnapshot(ctx context.Context, db *sql.DB, projectID string) (projectarchive.Snapshot, error) {
	if err := validateArchiveSanitization(ctx, db); err != nil {
		return projectarchive.Snapshot{}, err
	}
	var value project.Project
	var created, updated string
	if err := db.QueryRowContext(ctx, `SELECT id,name,description,workspace_path,workspace_kind,created_at,updated_at FROM projects WHERE id=?`, projectID).Scan(&value.ID, &value.Name, &value.Description, &value.WorkspacePath, &value.WorkspaceKind, &created, &updated); err != nil {
		return projectarchive.Snapshot{}, fmt.Errorf("read archived project: %w", err)
	}
	var err error
	value.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return projectarchive.Snapshot{}, err
	}
	value.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return projectarchive.Snapshot{}, err
	}
	var schemaVersion, projectCount int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		return projectarchive.Snapshot{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projectCount); err != nil || projectCount != 1 {
		return projectarchive.Snapshot{}, fmt.Errorf("project archive must contain exactly one project")
	}
	bindings, err := readArchiveSkillBindings(ctx, db)
	if err != nil {
		return projectarchive.Snapshot{}, err
	}
	snapshot := projectarchive.Snapshot{Project: value, DatabaseSchemaVersion: schemaVersion, Files: []projectarchive.FileSource{}, SkillBindings: bindings}
	for target, query := range map[*int]string{
		&snapshot.Stats.Conversations:  `SELECT COUNT(*) FROM conversations`,
		&snapshot.Stats.Runs:           `SELECT COUNT(*) FROM runs`,
		&snapshot.Stats.Attachments:    `SELECT COUNT(*) FROM attachments`,
		&snapshot.Stats.Documents:      `SELECT COUNT(*) FROM knowledge_documents`,
		&snapshot.Stats.Artifacts:      `SELECT COUNT(*) FROM artifacts`,
		&snapshot.Stats.Workflows:      `SELECT COUNT(*) FROM workflows`,
		&snapshot.Stats.WorkflowRuns:   `SELECT COUNT(*) FROM workflow_runs`,
		&snapshot.Stats.Candidates:     `SELECT COUNT(*) FROM research_candidates`,
		&snapshot.Stats.Bibliographies: `SELECT COUNT(*) FROM research_bibliographies`,
		&snapshot.Stats.Evidence:       `SELECT COUNT(*) FROM research_evidence_entries`,
	} {
		if err := db.QueryRowContext(ctx, query).Scan(target); err != nil {
			return snapshot, err
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_profiles`).Scan(&snapshot.HistoricalProfiles); err != nil {
		return snapshot, err
	}
	if err := appendAttachmentFiles(ctx, db, &snapshot); err != nil {
		return snapshot, err
	}
	if err := appendKnowledgeFiles(ctx, db, &snapshot); err != nil {
		return snapshot, err
	}
	if err := appendArtifactFiles(ctx, db, &snapshot); err != nil {
		return snapshot, err
	}
	if err := appendWorkflowInputFiles(ctx, db, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func appendAttachmentFiles(ctx context.Context, db *sql.DB, snapshot *projectarchive.Snapshot) error {
	rows, err := db.QueryContext(ctx, `SELECT storage_relative_path,cache_relative_path,size_bytes,sha256,document_format,status FROM attachments ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var storage, cache, hash, format, status string
		var size int64
		if err := rows.Scan(&storage, &cache, &size, &hash, &format, &status); err != nil {
			return err
		}
		snapshot.Files = append(snapshot.Files, projectarchive.FileSource{StorageRelativePath: filepath.ToSlash(storage), Kind: projectarchive.FileAttachment, Required: true, ExpectedSize: size, ExpectedSHA256: strings.ToLower(hash)})
		if format != "image" {
			snapshot.Files = append(snapshot.Files, projectarchive.FileSource{StorageRelativePath: filepath.ToSlash(cache), Kind: projectarchive.FileDocumentCache, Required: status == "ready", ExpectedSize: -1})
		}
	}
	return rows.Err()
}

func appendKnowledgeFiles(ctx context.Context, db *sql.DB, snapshot *projectarchive.Snapshot) error {
	rows, err := db.QueryContext(ctx, `SELECT storage_relative_path,status FROM knowledge_index_versions ORDER BY version_number`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var relative, status string
		if err := rows.Scan(&relative, &status); err != nil {
			return err
		}
		snapshot.Files = append(snapshot.Files, projectarchive.FileSource{StorageRelativePath: filepath.ToSlash(relative), Kind: projectarchive.FileKnowledgeIndex, Required: status == "ready" || status == "building", ExpectedSize: -1})
	}
	return rows.Err()
}

func inspectArchiveSkillBindings(ctx context.Context, db *sql.DB, projectID string) ([]projectarchive.SkillBinding, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT p.skill_id,p.skill_version,i.content_hash,i.package_hash,p.enabled,p.priority
		FROM project_skills p
		JOIN installed_skills i ON i.skill_id=p.skill_id AND i.skill_version=p.skill_version
		WHERE p.project_id=?
		ORDER BY p.priority,p.skill_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []projectarchive.SkillBinding{}
	for rows.Next() {
		var value projectarchive.SkillBinding
		if err := rows.Scan(&value.SkillID, &value.Version, &value.ContentHash, &value.PackageHash, &value.Enabled, &value.Priority); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func writeArchiveSkillBindings(ctx context.Context, db *sql.DB, bindings []projectarchive.SkillBinding) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE `+quoteIdentifier(archiveSkillBindingsTable)+` (
		skill_id TEXT NOT NULL,
		skill_version TEXT NOT NULL,
		content_hash TEXT NOT NULL,
		package_hash TEXT NOT NULL,
		enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
		priority INTEGER NOT NULL CHECK (priority BETWEEN 0 AND 1000),
		PRIMARY KEY (skill_id, skill_version)
	)`); err != nil {
		return fmt.Errorf("create archived Skill binding evidence: %w", err)
	}
	for _, binding := range bindings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+quoteIdentifier(archiveSkillBindingsTable)+`(skill_id,skill_version,content_hash,package_hash,enabled,priority) VALUES (?,?,?,?,?,?)`,
			binding.SkillID, binding.Version, binding.ContentHash, binding.PackageHash, binding.Enabled, binding.Priority); err != nil {
			return fmt.Errorf("record archived Skill binding evidence: %w", err)
		}
	}
	return tx.Commit()
}

func readArchiveSkillBindings(ctx context.Context, db *sql.DB) ([]projectarchive.SkillBinding, error) {
	columns, err := standaloneTableColumns(ctx, db, archiveSkillBindingsTable)
	if err != nil {
		return nil, fmt.Errorf("read archived Skill binding evidence: %w", err)
	}
	expected := []string{"skill_id", "skill_version", "content_hash", "package_hash", "enabled", "priority"}
	if strings.Join(columns, "\x00") != strings.Join(expected, "\x00") {
		return nil, fmt.Errorf("archived Skill binding evidence schema is invalid")
	}
	rows, err := db.QueryContext(ctx, `SELECT skill_id,skill_version,content_hash,package_hash,enabled,priority FROM `+quoteIdentifier(archiveSkillBindingsTable)+` ORDER BY priority,skill_id,skill_version`)
	if err != nil {
		return nil, fmt.Errorf("read archived Skill binding evidence: %w", err)
	}
	defer rows.Close()
	result := []projectarchive.SkillBinding{}
	for rows.Next() {
		var value projectarchive.SkillBinding
		if err := rows.Scan(&value.SkillID, &value.Version, &value.ContentHash, &value.PackageHash, &value.Enabled, &value.Priority); err != nil {
			return nil, err
		}
		if strings.TrimSpace(value.SkillID) == "" || strings.TrimSpace(value.Version) == "" || !validArchiveSHA256(value.ContentHash) || !validArchiveSHA256(value.PackageHash) {
			return nil, fmt.Errorf("archived Skill binding evidence is invalid")
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func validateArchiveSanitization(ctx context.Context, db *sql.DB) error {
	var schemaVersion int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		return err
	}
	for _, table := range archiveSanitizedTables {
		// Older archives are schema-verified before this check, then migrated
		// in isolation. Capabilities did not exist before migration 82.
		if schemaVersion < 82 && (table == "model_resource_actions" || table == "model_resource_sessions") {
			continue
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(table)).Scan(&count); err != nil {
			return fmt.Errorf("verify sanitized project archive table %s: %w", table, err)
		}
		if count != 0 {
			return fmt.Errorf("project archive contains forbidden configuration in %s", table)
		}
	}
	var unsafeProfiles int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_profiles
		WHERE base_url<>? OR secret_ref<>('archive/profile/'||id) OR custom_headers_json<>'{}' OR enabled<>0 OR is_default<>0`, archiveModelBaseURL).Scan(&unsafeProfiles); err != nil {
		return fmt.Errorf("verify sanitized historical model profiles: %w", err)
	}
	if unsafeProfiles != 0 {
		return fmt.Errorf("project archive contains an active or credential-bearing historical model profile")
	}
	var unsafeConversations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE permission_mode<>'plan'`).Scan(&unsafeConversations); err != nil {
		return fmt.Errorf("verify archived conversation permissions: %w", err)
	}
	if unsafeConversations != 0 {
		return fmt.Errorf("project archive contains reusable conversation permission state")
	}
	if hasPermissionMode, err := sqliteTableHasColumn(ctx, db, "workflow_runs", "permission_mode"); err != nil {
		return err
	} else if hasPermissionMode {
		var unsafeWorkflowRuns int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs WHERE permission_mode<>'plan'`).Scan(&unsafeWorkflowRuns); err != nil {
			return fmt.Errorf("verify archived Workflow permissions: %w", err)
		}
		if unsafeWorkflowRuns != 0 {
			return fmt.Errorf("project archive contains reusable Workflow permission state")
		}
	}
	pythonPredicate := `state<>'absent' OR base_executable_path<>'' OR environment_python_path<>''`
	if hasKind, err := sqliteTableHasColumn(ctx, db, "python_environments", "environment_kind"); err != nil {
		return err
	} else if hasKind {
		pythonPredicate += ` OR environment_kind<>'workspace_managed'`
	}
	var unsafeEnvironments int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM python_environments WHERE `+pythonPredicate).Scan(&unsafeEnvironments); err != nil {
		return fmt.Errorf("verify archived Python environments: %w", err)
	}
	if unsafeEnvironments != 0 {
		return fmt.Errorf("project archive contains a reusable Python environment path")
	}
	return nil
}

type sqliteQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func sqliteTableHasColumn(ctx context.Context, db sqliteQueryer, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func standaloneTableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	if err := requireRealTable(ctx, db, "main", table); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `PRAGMA table_xinfo(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var cid, notNull, primaryKey, hidden int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey, &hidden); err != nil {
			return nil, err
		}
		if hidden != 0 {
			return nil, fmt.Errorf("table %s contains hidden or generated columns", table)
		}
		result = append(result, name)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("table %s is missing", table)
	}
	return result, rows.Err()
}

func validArchiveSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func appendArtifactFiles(ctx context.Context, db *sql.DB, snapshot *projectarchive.Snapshot) error {
	rows, err := db.QueryContext(ctx, `
		SELECT storage_relative_path,size_bytes,sha256 FROM artifact_blobs
		UNION
		SELECT 'artifacts/objects/' || substr(json_extract(item.value,'$.sha256'),1,2) || '/' || json_extract(item.value,'$.sha256'),
			CAST(json_extract(item.value,'$.sizeBytes') AS INTEGER),json_extract(item.value,'$.sha256')
		FROM tool_calls tc
		JOIN tool_results tr ON tr.tool_call_id=tc.id
		JOIN json_each(tr.artifacts_json) item
		LEFT JOIN artifact_versions v ON v.source_key=('tool:' || tc.id || ':' || CAST(item.key AS TEXT))
		WHERE tc.status='completed' AND tr.status='success'
			AND length(COALESCE(json_extract(item.value,'$.sha256'),''))=64
			AND COALESCE(json_extract(item.value,'$.sizeBytes'),-1)>=0
			AND trim(COALESCE(json_extract(item.value,'$.workspacePath'),''))<>''
			AND v.id IS NULL
		ORDER BY sha256`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var relative, hash string
		var size int64
		if err := rows.Scan(&relative, &size, &hash); err != nil {
			return err
		}
		if !validArchiveSHA256(hash) || size < 0 || size > 1<<30 {
			return fmt.Errorf("pending Tool Artifact contains an invalid byte identity")
		}
		snapshot.Files = append(snapshot.Files, projectarchive.FileSource{StorageRelativePath: filepath.ToSlash(relative), Kind: projectarchive.FileArtifactObject, Required: true, ExpectedSize: size, ExpectedSHA256: strings.ToLower(hash)})
	}
	return rows.Err()
}

func appendWorkflowInputFiles(ctx context.Context, db *sql.DB, snapshot *projectarchive.Snapshot) error {
	rows, err := db.QueryContext(ctx, `SELECT COALESCE(research_task_id,''),inputs_json,compilation_json FROM workflow_runs ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	for rows.Next() {
		var taskID, inputsJSON, compilationJSON string
		if err := rows.Scan(&taskID, &inputsJSON, &compilationJSON); err != nil {
			return err
		}
		var compilation workflow.Compilation
		if json.Unmarshal([]byte(compilationJSON), &compilation) != nil {
			return fmt.Errorf("archived Workflow compilation is invalid")
		}
		paths, err := workflow.FrozenInputPaths(compilation, json.RawMessage(inputsJSON))
		if err != nil {
			return fmt.Errorf("inspect archived Workflow inputs: %w", err)
		}
		for _, relative := range paths {
			taskID = strings.TrimSpace(taskID)
			key := taskID + "\x00" + relative
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			expectedHash, ok := workflow.FrozenInputSHA256(relative)
			if !ok {
				return fmt.Errorf("archived Workflow input path is not content-addressed")
			}
			snapshot.Files = append(snapshot.Files, projectarchive.FileSource{
				StorageRelativePath: relative,
				SourcePath:          workflowInputSourcePath(snapshot.Project, taskID, relative),
				TaskID:              taskID,
				Kind:                projectarchive.FileWorkflowInput,
				Required:            true,
				ExpectedSize:        -1,
				ExpectedSHA256:      expectedHash,
			})
		}
	}
	return rows.Err()
}

func workflowInputSourcePath(value project.Project, taskID, relative string) string {
	root := value.WorkspacePath
	if strings.TrimSpace(taskID) != "" {
		if scoped, err := project.ResearchTaskWorkspacePath(value, taskID); err == nil {
			root = scoped
		}
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}

type archiveIDMaps struct {
	values map[string]map[string]string
}

func newArchiveIDMaps() *archiveIDMaps { return &archiveIDMaps{values: map[string]map[string]string{}} }

func (m *archiveIDMaps) allocate(table, old string) (string, error) {
	if strings.TrimSpace(old) == "" {
		return "", nil
	}
	if m.values[table] == nil {
		m.values[table] = map[string]string{}
	}
	if value := m.values[table][old]; value != "" {
		return value, nil
	}
	value, err := id.New()
	if err != nil {
		return "", err
	}
	m.values[table][old] = value
	return value, nil
}

func (m *archiveIDMaps) get(table, old string) string {
	if old == "" {
		return ""
	}
	if value := m.values[table][old]; value != "" {
		return value
	}
	return old
}

var idTables = []string{
	"research_revision_proposals",
	"model_profiles", "conversations", "messages", "message_parts", "runs", "tool_calls", "approvals", "permission_grants",
	"conversation_context_checkpoints", "attachments", "knowledge_index_versions", "knowledge_documents", "knowledge_import_jobs",
	"model_request_usage", "artifact_blobs", "artifacts", "artifact_versions", "artifact_lineage", "artifact_citations", "artifact_exports",
	"workflows", "workflow_versions", "workflow_runs", "workflow_steps", "workflow_human_decisions", "workflow_events", "workflow_ai_executions",
	"research_queries", "research_source_records", "research_candidates", "research_candidate_task_imports", "research_bibliographies", "research_bibliography_field_sources",
	"research_bibliography_revisions", "research_bibliography_materials", "research_evidence_entries", "message_citations",
	"python_environments", "python_kernel_executions",
	"research_tasks",
}

func allocateArchiveIDs(ctx context.Context, db *sql.DB, maps *archiveIDMaps) error {
	for _, table := range idTables {
		rows, err := db.QueryContext(ctx, `SELECT id FROM `+quoteIdentifier(table)+` ORDER BY id`)
		if err != nil {
			return fmt.Errorf("read %s identities: %w", table, err)
		}
		for rows.Next() {
			var old string
			if err := rows.Scan(&old); err != nil {
				rows.Close()
				return err
			}
			if _, err := maps.allocate(table, old); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func rewriteProjectSnapshot(ctx context.Context, db *sql.DB, oldProjectID string, restored project.Project, maps *archiveIDMaps, plan *projectarchive.RewritePlan) error {
	if err := dropArchiveTriggers(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range idTables {
		for old, next := range maps.values[table] {
			if _, err := tx.ExecContext(ctx, `UPDATE `+quoteIdentifier(table)+` SET id=? WHERE id=?`, next, old); err != nil {
				return fmt.Errorf("remap %s identity: %w", table, err)
			}
		}
	}
	updateMappings := []struct {
		table, column, target string
	}{
		{"model_profile_models", "profile_id", "model_profiles"},
		{"conversations", "model_profile_id", "model_profiles"},
		{"runs", "model_profile_id", "model_profiles"},
		{"model_request_usage", "model_profile_id", "model_profiles"},
		{"conversation_context_checkpoints", "model_profile_id", "model_profiles"},
		{"conversations", "id", "conversations"},
		{"messages", "conversation_id", "conversations"},
		{"runs", "conversation_id", "conversations"},
		{"conversation_context_checkpoints", "conversation_id", "conversations"},
		{"messages", "id", "messages"},
		{"message_parts", "message_id", "messages"},
		{"runs", "user_message_id", "messages"},
		{"runs", "assistant_message_id", "messages"},
		{"conversation_context_checkpoints", "through_message_id", "messages"},
		{"message_citations", "message_id", "messages"},
		{"runs", "id", "runs"},
		{"messages", "run_id", "runs"},
		{"tool_calls", "run_id", "runs"},
		{"tool_calls", "workflow_run_id", "workflow_runs"},
		{"run_skills", "run_id", "runs"},
		{"run_dynamic_skills", "run_id", "runs"},
		{"run_skill_routing", "run_id", "runs"},
		{"run_skill_routing_audits", "run_id", "runs"},
		{"run_skill_routing_candidates", "run_id", "runs"},
		{"run_skill_routing_candidates", "continuity_run_id", "runs"},
		{"provider_turn_items", "run_id", "runs"},
		{"run_steps", "run_id", "runs"},
		{"model_request_usage", "run_id", "runs"},
		{"model_turn_journal", "run_id", "runs"},
		{"message_citations", "run_id", "runs"},
		{"tool_calls", "id", "tool_calls"},
		{"tool_results", "tool_call_id", "tool_calls"},
		{"process_execution_audits", "tool_call_id", "tool_calls"},
		{"process_execution_audits", "run_id", "runs"},
		{"process_execution_audits", "workflow_run_id", "workflow_runs"},
		{"python_kernel_executions", "tool_call_id", "tool_calls"},
		{"python_kernel_executions", "run_id", "runs"},
		{"python_kernel_executions", "workflow_run_id", "workflow_runs"},
		{"message_citations", "tool_call_id", "tool_calls"},
		{"run_dynamic_skills", "tool_call_id", "tool_calls"},
		{"attachments", "id", "attachments"},
		{"knowledge_documents", "attachment_id", "attachments"},
		{"knowledge_import_jobs", "attachment_id", "attachments"},
		{"research_candidates", "attachment_id", "attachments"},
		{"research_candidate_task_imports", "id", "research_candidate_task_imports"},
		{"research_candidate_task_imports", "candidate_id", "research_candidates"},
		{"research_candidate_task_imports", "attachment_id", "attachments"},
		{"research_candidate_task_imports", "research_task_id", "research_tasks"},
		{"research_bibliography_materials", "attachment_id", "attachments"},
		{"research_bibliography_materials", "research_task_id", "research_tasks"},
		{"research_evidence_entries", "attachment_id", "attachments"},
		{"research_evidence_entries", "research_task_id", "research_tasks"},
		{"workflow_runs", "research_task_id", "research_tasks"},
		{"attachments", "research_task_id", "research_tasks"},
		{"knowledge_documents", "research_task_id", "research_tasks"},
		{"artifacts", "research_task_id", "research_tasks"},
		{"knowledge_index_versions", "id", "knowledge_index_versions"},
		{"knowledge_documents", "index_version_id", "knowledge_index_versions"},
		{"knowledge_import_jobs", "index_version_id", "knowledge_index_versions"},
		{"knowledge_documents", "id", "knowledge_documents"},
		{"knowledge_import_jobs", "document_id", "knowledge_documents"},
		{"research_bibliography_materials", "knowledge_document_id", "knowledge_documents"},
		{"research_evidence_entries", "knowledge_document_id", "knowledge_documents"},
		{"artifact_blobs", "id", "artifact_blobs"},
		{"artifact_versions", "blob_id", "artifact_blobs"},
		{"artifact_exports", "blob_id", "artifact_blobs"},
		{"artifacts", "id", "artifacts"},
		{"artifact_versions", "artifact_id", "artifacts"},
		{"artifact_versions", "id", "artifact_versions"},
		{"artifacts", "current_version_id", "artifact_versions"},
		{"artifact_lineage", "artifact_version_id", "artifact_versions"},
		{"artifact_lineage", "source_artifact_version_id", "artifact_versions"},
		{"artifact_lineage", "source_run_id", "runs"},
		{"artifact_lineage", "source_workflow_run_id", "workflow_runs"},
		{"artifact_lineage", "source_message_id", "messages"},
		{"artifact_lineage", "source_tool_call_id", "tool_calls"},
		{"artifact_citations", "artifact_version_id", "artifact_versions"},
		{"artifact_exports", "artifact_version_id", "artifact_versions"},
		{"workflows", "id", "workflows"},
		{"workflow_versions", "workflow_id", "workflows"},
		{"workflow_versions", "id", "workflow_versions"},
		{"workflows", "current_version_id", "workflow_versions"},
		{"workflow_runs", "id", "workflow_runs"},
		{"workflow_runs", "workflow_id", "workflows"},
		{"workflow_runs", "workflow_version_id", "workflow_versions"},
		{"workflow_conversations", "workflow_run_id", "workflow_runs"},
		{"workflow_conversations", "conversation_id", "conversations"},
		{"workflow_ai_executions", "id", "workflow_ai_executions"},
		{"workflow_ai_executions", "workflow_run_id", "workflow_runs"},
		{"workflow_ai_executions", "workflow_step_id", "workflow_steps"},
		{"workflow_ai_executions", "model_profile_id", "model_profiles"},
		{"workflow_ai_chat_runs", "execution_id", "workflow_ai_executions"},
		{"workflow_ai_chat_runs", "chat_run_id", "runs"},
		{"workflow_steps", "id", "workflow_steps"},
		{"workflow_steps", "workflow_run_id", "workflow_runs"},
		{"workflow_steps", "tool_call_id", "tool_calls"},
		{"workflow_human_decisions", "id", "workflow_human_decisions"},
		{"workflow_human_decisions", "workflow_run_id", "workflow_runs"},
		{"workflow_human_decisions", "workflow_step_id", "workflow_steps"},
		{"workflow_events", "id", "workflow_events"},
		{"workflow_events", "workflow_run_id", "workflow_runs"},
		{"research_revision_proposals", "id", "research_revision_proposals"},
		{"research_revision_proposals", "workflow_run_id", "workflow_runs"},
		{"research_revision_proposals", "chat_run_id", "runs"},
		{"research_revision_proposals", "user_message_id", "messages"},
		{"research_revision_proposals", "source_call_id", "tool_calls"},
		{"research_timeline", "research_task_id", "research_tasks"},
		{"research_timeline", "workflow_run_id", "workflow_runs"},
		{"research_queries", "id", "research_queries"},
		{"research_query_records", "query_id", "research_queries"},
		{"research_query_origins", "query_id", "research_queries"},
		{"research_query_origins", "task_id", "research_tasks"},
		{"research_query_candidates", "query_id", "research_queries"},
		{"research_query_candidates", "candidate_id", "research_candidates"},
		{"research_candidate_reviews", "candidate_id", "research_candidates"},
		{"research_candidate_reviews", "task_id", "research_tasks"},
		{"research_source_records", "id", "research_source_records"},
		{"research_candidate_records", "source_record_id", "research_source_records"},
		{"research_query_records", "source_record_id", "research_source_records"},
		{"research_bibliography_field_sources", "source_record_id", "research_source_records"},
		{"research_candidates", "id", "research_candidates"},
		{"research_candidate_records", "candidate_id", "research_candidates"},
		{"research_candidate_aliases", "candidate_id", "research_candidates"},
		{"research_bibliographies", "candidate_id", "research_candidates"},
		{"research_bibliographies", "id", "research_bibliographies"},
		{"research_bibliography_field_sources", "bibliography_id", "research_bibliographies"},
		{"research_bibliography_revisions", "bibliography_id", "research_bibliographies"},
		{"research_bibliography_materials", "bibliography_id", "research_bibliographies"},
		{"research_evidence_entries", "bibliography_id", "research_bibliographies"},
		{"python_environments", "id", "python_environments"},
		{"python_kernel_executions", "id", "python_kernel_executions"},
		{"python_kernel_executions", "environment_id", "python_environments"},
	}
	for _, mapping := range updateMappings {
		if err := updateMappedColumn(ctx, tx, mapping.table, mapping.column, maps.values[mapping.target]); err != nil {
			return err
		}
	}
	if err := remapProjectColumns(ctx, tx, oldProjectID, restored.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET id=?,name=?,description=?,workspace_path=?,workspace_kind='managed',updated_at=? WHERE id=?`, restored.ID, restored.Name, restored.Description, restored.WorkspacePath, formatTime(restored.UpdatedAt), oldProjectID); err != nil {
		return err
	}
	if err := rewriteMessagePayloads(ctx, tx, maps); err != nil {
		return err
	}
	if err := rewriteToolResultPayloads(ctx, tx, maps, restored.ID); err != nil {
		return err
	}
	if err := rewriteRunSkillContexts(ctx, tx, maps, restored.ID); err != nil {
		return err
	}
	if err := rewriteContextCheckpoints(ctx, tx, maps); err != nil {
		return err
	}
	if err := rewriteArtifactProvenance(ctx, tx, maps, restored.ID); err != nil {
		return err
	}
	if err := rewriteWorkflowRunSnapshots(ctx, tx, maps, oldProjectID, restored.ID); err != nil {
		return err
	}
	if err := rewriteArtifactSourceKeys(ctx, tx, maps, oldProjectID, restored.ID); err != nil {
		return err
	}
	if err := rewriteLiveCitationPointers(ctx, tx, maps, restored.ID); err != nil {
		return err
	}
	if err := rewriteResearchPointers(ctx, tx, maps); err != nil {
		return err
	}
	if err := rewriteDiscoverySnapshots(ctx, tx, maps, restored.ID); err != nil {
		return err
	}
	if err := rewriteRunEvents(ctx, tx, maps); err != nil {
		return err
	}
	if err := collectIndexRewritePlan(ctx, tx, maps, oldProjectID, restored.ID, plan); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='interrupted',error_code='ARCHIVE_RESTORED',error_message='已从项目归档恢复；历史 Run 不会自动继续',completed_at=COALESCE(completed_at,updated_at) WHERE status IN ('queued','running','waiting_approval')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status='incomplete' WHERE id IN (SELECT assistant_message_id FROM runs WHERE status='interrupted' AND error_code='ARCHIVE_RESTORED')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='interrupted',error_code='ARCHIVE_RESTORED',error_message='从项目归档恢复的运行中步骤不会自动继续',completed_at=COALESCE(completed_at,updated_at) WHERE status IN ('queued','running','waiting_approval','waiting_human_confirmation')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status='interrupted',error_code='ARCHIVE_RESTORED',error_message='从项目归档恢复的 Workflow Run 不会自动继续',completed_at=COALESCE(completed_at,updated_at),resume_status='' WHERE status IN ('queued','running','waiting_approval','waiting_human_confirmation','paused')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_ai_executions SET status='interrupted',error_code='ARCHIVE_RESTORED',error_message='从项目归档恢复的 AI 阶段不会自动继续',completed_at=COALESCE(completed_at,updated_at),updated_at=COALESCE(completed_at,updated_at) WHERE status IN ('prepared','running')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_import_jobs SET status='cancelled',stage='cancelled',error_message='restored historical job',completed_at=COALESCE(completed_at,updated_at) WHERE status IN ('queued','running')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_documents SET status='failed',error_message='restored interrupted indexing task' WHERE status='indexing'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE research_candidates SET import_status='failed',import_error='restored interrupted research import' WHERE import_status='importing'`); err != nil {
		return err
	}
	for old, next := range maps.values["model_profiles"] {
		if _, err := tx.ExecContext(ctx, `UPDATE model_profiles SET secret_ref=? WHERE id=?`, "archive/profile/"+next, next); err != nil {
			return fmt.Errorf("remap archived model profile secret placeholder from %s: %w", old, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	return nil
}

func updateMappedColumn(ctx context.Context, tx *sql.Tx, table, column string, values map[string]string) error {
	for old, next := range values {
		if _, err := tx.ExecContext(ctx, `UPDATE `+quoteIdentifier(table)+` SET `+quoteIdentifier(column)+`=? WHERE `+quoteIdentifier(column)+`=?`, next, old); err != nil {
			return fmt.Errorf("remap %s.%s: %w", table, column, err)
		}
	}
	return nil
}

var projectScopedTables = []string{
	"conversations", "attachments", "knowledge_index_versions", "knowledge_documents", "knowledge_import_jobs",
	"artifact_blobs", "artifacts", "artifact_exports", "research_queries", "research_source_records", "research_candidates",
	"research_candidate_aliases", "research_candidate_task_imports", "research_candidate_reviews", "research_bibliographies", "research_bibliography_materials", "research_evidence_entries", "message_citations",
	"run_dynamic_skills", "run_skill_routing", "run_skill_routing_audits",
	"process_execution_audits",
	"python_environments",
	"python_kernel_executions",
	"workflows",
	"workflow_runs",
	"research_tasks",
}

func remapProjectColumns(ctx context.Context, tx *sql.Tx, oldProjectID, newProjectID string) error {
	for _, table := range projectScopedTables {
		if _, err := tx.ExecContext(ctx, `UPDATE `+quoteIdentifier(table)+` SET project_id=? WHERE project_id=?`, newProjectID, oldProjectID); err != nil {
			return fmt.Errorf("remap project identity in %s: %w", table, err)
		}
	}
	return nil
}

func rewriteMessagePayloads(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,payload_json FROM message_parts WHERE part_type='media' AND payload_json IS NOT NULL`)
	if err != nil {
		return err
	}
	type update struct{ id, payload string }
	updates := []update{}
	for rows.Next() {
		var item update
		var value attachment.MessageReference
		if err := rows.Scan(&item.id, &item.payload); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal([]byte(item.payload), &value) != nil || value.AttachmentID == "" {
			rows.Close()
			return fmt.Errorf("archived media message payload is invalid")
		}
		value.AttachmentID = maps.get("attachments", value.AttachmentID)
		encoded, _ := json.Marshal(value)
		item.payload = string(encoded)
		updates = append(updates, item)
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE message_parts SET payload_json=? WHERE id=?`, item.payload, item.id); err != nil {
			return err
		}
	}
	return nil
}

func rewriteToolResultPayloads(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps, projectID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT tool_call_id,artifacts_json,citations_json FROM tool_results`)
	if err != nil {
		return err
	}
	type update struct{ id, artifacts, citations string }
	updates := []update{}
	for rows.Next() {
		var item update
		var artifacts []tool.ArtifactRef
		var citations []tool.CitationRef
		if err := rows.Scan(&item.id, &item.artifacts, &item.citations); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal([]byte(item.artifacts), &artifacts) != nil || json.Unmarshal([]byte(item.citations), &citations) != nil {
			rows.Close()
			return fmt.Errorf("archived tool result references are invalid")
		}
		for index := range artifacts {
			artifacts[index].ID = maps.getFirst(artifacts[index].ID, "attachments", "artifacts")
		}
		for index := range citations {
			citations[index].ID = maps.get("attachments", citations[index].ID)
			citations[index].ProjectID = projectID
			citations[index].IndexVersionID = maps.get("knowledge_index_versions", citations[index].IndexVersionID)
			citations[index].DocumentID = maps.get("knowledge_documents", citations[index].DocumentID)
			citations[index].AttachmentID = maps.get("attachments", citations[index].AttachmentID)
		}
		encodedArtifacts, _ := json.Marshal(artifacts)
		encodedCitations, _ := json.Marshal(citations)
		item.artifacts, item.citations = string(encodedArtifacts), string(encodedCitations)
		updates = append(updates, item)
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE tool_results SET artifacts_json=?,citations_json=? WHERE tool_call_id=?`, item.artifacts, item.citations, item.id); err != nil {
			return err
		}
	}
	return nil
}

func (m *archiveIDMaps) getFirst(old string, tables ...string) string {
	if old == "" {
		return ""
	}
	for _, table := range tables {
		if value := m.values[table][old]; value != "" {
			return value
		}
	}
	return old
}

func rewriteWorkflowRunSnapshots(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps, oldProjectID, projectID string) error {
	creationRows, err := tx.QueryContext(ctx, `SELECT id,creation_key FROM workflow_runs WHERE creation_key<>'' ORDER BY id`)
	if err != nil {
		return err
	}
	type creationUpdate struct{ id, key string }
	creationUpdates := []creationUpdate{}
	for creationRows.Next() {
		var item creationUpdate
		if err := creationRows.Scan(&item.id, &item.key); err != nil {
			creationRows.Close()
			return err
		}
		const prefix = "research-route:"
		if strings.HasPrefix(item.key, prefix) {
			parts := strings.SplitN(strings.TrimPrefix(item.key, prefix), ":", 2)
			if len(parts) == 2 {
				item.key = prefix + maps.get("workflow_runs", parts[0]) + ":" + parts[1]
			}
		}
		creationUpdates = append(creationUpdates, item)
	}
	if err := creationRows.Err(); err != nil {
		creationRows.Close()
		return err
	}
	creationRows.Close()
	for _, item := range creationUpdates {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET creation_key=? WHERE id=?`, item.key, item.id); err != nil {
			return fmt.Errorf("rewrite Workflow Run creation key: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,inputs_json,outputs_json FROM workflow_runs ORDER BY id`)
	if err != nil {
		return err
	}
	type update struct{ id, inputs, inputsSHA256, outputs string }
	updates := []update{}
	for rows.Next() {
		var item update
		if err := rows.Scan(&item.id, &item.inputs, &item.outputs); err != nil {
			rows.Close()
			return err
		}
		inputs, err := rewriteWorkflowPayload([]byte(item.inputs), maps, oldProjectID, projectID)
		if err != nil {
			rows.Close()
			return fmt.Errorf("rewrite Workflow Run inputs: %w", err)
		}
		outputs, err := rewriteWorkflowPayload([]byte(item.outputs), maps, oldProjectID, projectID)
		if err != nil {
			rows.Close()
			return fmt.Errorf("rewrite Workflow Run outputs: %w", err)
		}
		item.inputs, item.inputsSHA256, item.outputs = string(inputs), workflowPayloadSHA256(inputs), string(outputs)
		updates = append(updates, item)
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET inputs_json=?,inputs_sha256=?,outputs_json=? WHERE id=?`, item.inputs, item.inputsSHA256, item.outputs, item.id); err != nil {
			return err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,input_json,input_sha256,output_json FROM workflow_steps ORDER BY id`)
	if err != nil {
		return err
	}
	type stepUpdate struct{ id, input, inputSHA256, output string }
	stepUpdates := []stepUpdate{}
	for rows.Next() {
		var item stepUpdate
		if err := rows.Scan(&item.id, &item.input, &item.inputSHA256, &item.output); err != nil {
			rows.Close()
			return err
		}
		input, err := rewriteWorkflowPayload([]byte(item.input), maps, oldProjectID, projectID)
		if err != nil {
			rows.Close()
			return err
		}
		output, err := rewriteWorkflowPayload([]byte(item.output), maps, oldProjectID, projectID)
		if err != nil {
			rows.Close()
			return err
		}
		item.input, item.output = string(input), string(output)
		if item.inputSHA256 != "" {
			item.inputSHA256 = workflowPayloadSHA256(input)
		}
		stepUpdates = append(stepUpdates, item)
	}
	rows.Close()
	for _, item := range stepUpdates {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET input_json=?,input_sha256=?,output_json=? WHERE id=?`, item.input, item.inputSHA256, item.output, item.id); err != nil {
			return err
		}
	}
	for _, target := range []struct {
		query  string
		update string
		label  string
	}{
		{`SELECT id,arguments_json FROM tool_calls WHERE workflow_run_id IS NOT NULL ORDER BY id`, `UPDATE tool_calls SET arguments_json=? WHERE id=?`, "Workflow ToolCall arguments"},
		{`SELECT id,context_json FROM workflow_human_decisions ORDER BY id`, `UPDATE workflow_human_decisions SET context_json=? WHERE id=?`, "Workflow human decision context"},
		{`SELECT id,payload_json FROM workflow_events ORDER BY id`, `UPDATE workflow_events SET payload_json=? WHERE id=?`, "Workflow event payload"},
		{`SELECT id,proposal_json FROM research_revision_proposals ORDER BY id`, `UPDATE research_revision_proposals SET proposal_json=? WHERE id=?`, "Research revision proposal"},
		{`SELECT CAST(sequence AS TEXT),snapshot_json FROM research_timeline ORDER BY sequence`, `UPDATE research_timeline SET snapshot_json=? WHERE sequence=?`, "Research timeline snapshot"},
	} {
		if err := rewriteWorkflowJSONColumn(ctx, tx, target.query, target.update, target.label, maps, oldProjectID, projectID); err != nil {
			return err
		}
	}
	// Restored projects need fresh user consent. Historical delivery snapshots
	// retain their original identities as audit evidence, not executable state.
	for kind, table := range map[string]string{"message": "messages", "event": "workflow_events", "proposal": "research_revision_proposals", "registration": "artifact_lineage"} {
		for oldID, newID := range maps.values[table] {
			if _, err := tx.ExecContext(ctx, `UPDATE research_timeline SET source_id=? WHERE kind=? AND source_id=?`, newID, kind, oldID); err != nil {
				return err
			}
		}
	}
	for oldID, newID := range maps.values["workflow_steps"] {
		if _, err := tx.ExecContext(ctx, `UPDATE research_timeline SET snapshot_json=json_set(snapshot_json,'$.step.id',?) WHERE json_extract(snapshot_json,'$.step.id')=?`, newID, oldID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE research_revision_proposals SET status=CASE WHEN status='pending' THEN 'superseded' ELSE status END,
		proposal_json=json_set(proposal_json,'$.id',id,'$.runId',workflow_run_id,'$.chatRunId',chat_run_id,'$.userMessageId',user_message_id,'$.canConfirm',json('false'),'$.status',CASE WHEN status='pending' THEN 'superseded' ELSE status END)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workflow_events
		SET payload_json=json_set(
			payload_json,'$.inputsSha256',
			(SELECT wr.inputs_sha256 FROM workflow_runs wr WHERE wr.id=workflow_events.workflow_run_id)
		)
		WHERE event_type='workflow.created' AND json_type(payload_json,'$.inputsSha256')='text'`); err != nil {
		return fmt.Errorf("rewrite Workflow created input hash: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workflow_events
		SET payload_json=json_set(
			payload_json,'$.inputSha256',
			(SELECT ws.input_sha256 FROM workflow_steps ws
			 WHERE ws.id=json_extract(workflow_events.payload_json,'$.stepId'))
		)
		WHERE event_type='workflow.step_started'
		  AND json_type(payload_json,'$.stepId')='text'
		  AND json_type(payload_json,'$.inputSha256')='text'
		  AND EXISTS (SELECT 1 FROM workflow_steps ws WHERE ws.id=json_extract(workflow_events.payload_json,'$.stepId'))`); err != nil {
		return fmt.Errorf("rewrite Workflow step input hash: %w", err)
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT tr.tool_call_id,tr.structured_json
		FROM tool_results tr JOIN tool_calls tc ON tc.id=tr.tool_call_id
		WHERE tc.workflow_run_id IS NOT NULL AND tr.structured_json IS NOT NULL
		ORDER BY tr.tool_call_id`)
	if err != nil {
		return err
	}
	type structuredUpdate struct{ id, payload string }
	structuredUpdates := []structuredUpdate{}
	for rows.Next() {
		var item structuredUpdate
		if err := rows.Scan(&item.id, &item.payload); err != nil {
			rows.Close()
			return err
		}
		rewritten, err := rewriteWorkflowPayload([]byte(item.payload), maps, oldProjectID, projectID)
		if err != nil {
			rows.Close()
			return fmt.Errorf("rewrite Workflow ToolResult structured payload: %w", err)
		}
		item.payload = string(rewritten)
		structuredUpdates = append(structuredUpdates, item)
	}
	rows.Close()
	for _, item := range structuredUpdates {
		if _, err := tx.ExecContext(ctx, `UPDATE tool_results SET structured_json=? WHERE tool_call_id=?`, item.payload, item.id); err != nil {
			return err
		}
	}
	return nil
}

func rewriteWorkflowJSONColumn(ctx context.Context, tx *sql.Tx, query, updateQuery, label string, maps *archiveIDMaps, oldProjectID, projectID string) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	type update struct{ id, payload string }
	updates := []update{}
	for rows.Next() {
		var item update
		if err := rows.Scan(&item.id, &item.payload); err != nil {
			rows.Close()
			return err
		}
		rewritten, err := rewriteWorkflowPayload([]byte(item.payload), maps, oldProjectID, projectID)
		if err != nil {
			rows.Close()
			return fmt.Errorf("rewrite %s: %w", label, err)
		}
		item.payload = string(rewritten)
		updates = append(updates, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, updateQuery, item.payload, item.id); err != nil {
			return err
		}
	}
	return nil
}

func rewriteWorkflowPayload(raw []byte, maps *archiveIDMaps, oldProjectID, projectID string) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var walk func(any, string) any
	walk = func(current any, key string) any {
		switch typed := current.(type) {
		case map[string]any:
			for childKey, child := range typed {
				typed[childKey] = walk(child, childKey)
			}
			return typed
		case []any:
			for index, child := range typed {
				typed[index] = walk(child, key)
			}
			return typed
		case string:
			if key == "projectId" && typed == oldProjectID {
				return projectID
			}
			table := map[string]string{
				"proposalId": "research_revision_proposals", "userMessageId": "messages",
				"id": "artifacts", "artifactId": "artifacts", "artifactVersionId": "artifact_versions",
				"toolCallId": "tool_calls", "workflowId": "workflows", "workflowVersionId": "workflow_versions",
				"workflowRunId": "workflow_runs", "formalRunId": "workflow_runs", "workflowStepId": "workflow_steps", "stepId": "workflow_steps",
				"executionId": "workflow_ai_executions", "chatRunId": "runs", "revisionChatRunId": "runs", "adoptedChatRunId": "runs",
				"approvalId": "approvals", "decisionId": "workflow_human_decisions", "runId": "runs", "conversationId": "conversations", "modelProfileId": "model_profiles",
				"attachmentId": "attachments", "documentId": "knowledge_documents", "indexVersionId": "knowledge_index_versions",
				"bibliographyId": "research_bibliographies", "candidateId": "research_candidates",
			}[key]
			if table != "" {
				return maps.get(table, typed)
			}
			return typed
		default:
			return current
		}
	}
	return json.Marshal(walk(value, ""))
}

func workflowPayloadSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func rewriteRunSkillContexts(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps, projectID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT run_id,project_id,snapshot_json,snapshot_hash FROM run_skill_contexts`)
	if err != nil {
		return err
	}
	type update struct{ oldRunID, runID, encoded, hash string }
	updates := []update{}
	for rows.Next() {
		var item update
		var storedProjectID string
		if err := rows.Scan(&item.oldRunID, &storedProjectID, &item.encoded, &item.hash); err != nil {
			rows.Close()
			return err
		}
		decoded, err := skill.DecodeRunContext([]byte(item.encoded), item.hash)
		if err != nil {
			rows.Close()
			return err
		}
		if decoded.RunID != item.oldRunID || decoded.ProjectID != storedProjectID {
			rows.Close()
			return fmt.Errorf("archived Run Skill context columns do not match its snapshot")
		}
		decoded.RunID = maps.get("runs", item.oldRunID)
		decoded.ProjectID = projectID
		encoded, hash, err := skill.EncodeRunContext(decoded)
		if err != nil {
			rows.Close()
			return err
		}
		item.runID, item.encoded, item.hash = decoded.RunID, string(encoded), hash
		updates = append(updates, item)
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE run_skill_contexts SET run_id=?,project_id=?,snapshot_json=?,snapshot_hash=? WHERE run_id=?`, item.runID, projectID, item.encoded, item.hash, item.oldRunID); err != nil {
			return err
		}
	}
	return nil
}

func rewriteContextCheckpoints(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,conversation_id,revision,through_message_id,summary_text,source_message_count,source_estimated_tokens,model_profile_id,model_id,api_protocol,created_at FROM conversation_context_checkpoints`)
	if err != nil {
		return err
	}
	type update struct{ id, hash string }
	updates := []update{}
	for rows.Next() {
		var value contextmemory.Checkpoint
		var created string
		if err := rows.Scan(&value.ID, &value.ConversationID, &value.Revision, &value.ThroughMessageID, &value.Summary, &value.SourceMessageCount, &value.SourceEstimatedTokens, &value.ModelProfileID, &value.ModelID, &value.APIProtocol, &created); err != nil {
			rows.Close()
			return err
		}
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		contextmemory.RefreshIntegrity(&value)
		updates = append(updates, update{id: value.ID, hash: value.CheckpointSHA256})
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE conversation_context_checkpoints SET checkpoint_sha256=? WHERE id=?`, item.hash, item.id); err != nil {
			return err
		}
	}
	return nil
}

func rewriteArtifactProvenance(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps, projectID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,provenance_json FROM artifact_versions`)
	if err != nil {
		return err
	}
	type update struct{ id, encoded string }
	updates := []update{}
	for rows.Next() {
		var item update
		var value artifact.Provenance
		if err := rows.Scan(&item.id, &item.encoded); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal([]byte(item.encoded), &value) != nil {
			rows.Close()
			return fmt.Errorf("archived Artifact provenance is invalid")
		}
		value.ProjectID = projectID
		value.ConversationID = maps.get("conversations", value.ConversationID)
		value.RunID = maps.get("runs", value.RunID)
		value.WorkflowRunID = maps.get("workflow_runs", value.WorkflowRunID)
		value.MessageID = maps.get("messages", value.MessageID)
		value.ToolCallID = maps.get("tool_calls", value.ToolCallID)
		value.ModelProfileID = maps.get("model_profiles", value.ModelProfileID)
		if value.Extra["researchTaskId"] != "" {
			value.Extra["researchTaskId"] = maps.get("research_tasks", value.Extra["researchTaskId"])
		}
		encoded, _ := json.Marshal(value)
		item.encoded = string(encoded)
		updates = append(updates, item)
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE artifact_versions SET provenance_json=? WHERE id=?`, item.encoded, item.id); err != nil {
			return err
		}
	}
	return nil
}

func rewriteArtifactSourceKeys(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps, oldProjectID, projectID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(source_key,'') FROM artifact_versions WHERE source_key IS NOT NULL`)
	if err != nil {
		return err
	}
	type update struct{ id, key string }
	updates := []update{}
	for rows.Next() {
		var item update
		if err := rows.Scan(&item.id, &item.key); err != nil {
			rows.Close()
			return err
		}
		if strings.HasPrefix(item.key, "assistant:") {
			parts := strings.Split(item.key, ":")
			if len(parts) >= 2 {
				parts[1] = maps.get("messages", parts[1])
			}
			if len(parts) >= 4 && parts[2] == "artifact" {
				parts[3] = maps.get("artifacts", parts[3])
			}
			item.key = strings.Join(parts, ":")
		} else if strings.HasPrefix(item.key, "tool:") {
			parts := strings.Split(item.key, ":")
			if len(parts) >= 2 {
				parts[1] = maps.get("tool_calls", parts[1])
			}
			item.key = strings.Join(parts, ":")
		} else if strings.HasPrefix(item.key, "workspace:"+oldProjectID+":") {
			item.key = "workspace:" + projectID + strings.TrimPrefix(item.key, "workspace:"+oldProjectID)
		} else if strings.HasPrefix(item.key, "workflow-deliverable:") {
			parts := strings.Split(item.key, ":")
			if len(parts) == 4 {
				parts[1] = maps.get("workflow_runs", parts[1])
			}
			item.key = strings.Join(parts, ":")
		}
		updates = append(updates, item)
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE artifact_versions SET source_key=? WHERE id=?`, item.key, item.id); err != nil {
			return err
		}
	}
	return nil
}

func rewriteLiveCitationPointers(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps, projectID string) error {
	columns := []struct{ table, column, target string }{
		{"message_citations", "index_version_id", "knowledge_index_versions"}, {"message_citations", "document_id", "knowledge_documents"}, {"message_citations", "attachment_id", "attachments"},
		{"artifact_citations", "index_version_id", "knowledge_index_versions"}, {"artifact_citations", "document_id", "knowledge_documents"}, {"artifact_citations", "attachment_id", "attachments"},
	}
	for _, value := range columns {
		if err := updateMappedColumn(ctx, tx, value.table, value.column, maps.values[value.target]); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE message_citations SET project_id=?`, projectID)
	return err
}

func dropArchiveTriggers(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger' ORDER BY name`)
	if err != nil {
		return err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := db.ExecContext(ctx, `DROP TRIGGER `+quoteIdentifier(name)); err != nil {
			return fmt.Errorf("drop archived trigger %s: %w", name, err)
		}
	}
	return nil
}

func rewriteResearchPointers(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps) error {
	for _, value := range []struct{ table, column, target string }{
		{"research_bibliographies", "selected_sources_json", "research_source_records"},
	} {
		rows, err := tx.QueryContext(ctx, `SELECT id,`+quoteIdentifier(value.column)+` FROM `+quoteIdentifier(value.table))
		if err != nil {
			return err
		}
		type update struct{ id, encoded string }
		updates := []update{}
		for rows.Next() {
			var item update
			var selected map[string]string
			if err := rows.Scan(&item.id, &item.encoded); err != nil {
				rows.Close()
				return err
			}
			if json.Unmarshal([]byte(item.encoded), &selected) != nil {
				rows.Close()
				return fmt.Errorf("archived bibliography source selection is invalid")
			}
			for field, sourceID := range selected {
				prefix, rawID := "", sourceID
				if strings.HasPrefix(sourceID, "auto:") {
					prefix, rawID = "auto:", strings.TrimPrefix(sourceID, "auto:")
				} else if strings.HasPrefix(sourceID, "source:") {
					prefix, rawID = "source:", strings.TrimPrefix(sourceID, "source:")
				} else if sourceID == "user" {
					continue
				}
				mapped := maps.get(value.target, rawID)
				if mapped == rawID {
					rows.Close()
					return fmt.Errorf("archived bibliography source selection references an unknown record")
				}
				selected[field] = prefix + mapped
			}
			encoded, _ := json.Marshal(selected)
			item.encoded = string(encoded)
			updates = append(updates, item)
		}
		rows.Close()
		for _, item := range updates {
			if _, err := tx.ExecContext(ctx, `UPDATE `+quoteIdentifier(value.table)+` SET `+quoteIdentifier(value.column)+`=? WHERE id=?`, item.encoded, item.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func rewriteRunEvents(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps) error {
	rows, err := tx.QueryContext(ctx, `SELECT event_id,aggregate_id FROM run_events ORDER BY aggregate_id,sequence`)
	if err != nil {
		return err
	}
	type update struct{ oldEvent, newEvent, oldRun, newRun string }
	updates := []update{}
	for rows.Next() {
		var item update
		if err := rows.Scan(&item.oldEvent, &item.oldRun); err != nil {
			rows.Close()
			return err
		}
		item.newRun = maps.get("runs", item.oldRun)
		item.newEvent, err = id.New()
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, item)
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE run_events SET event_id=?,aggregate_id=? WHERE event_id=?`, item.newEvent, item.newRun, item.oldEvent); err != nil {
			return err
		}
	}
	for old, next := range maps.values["runs"] {
		if _, err := tx.ExecContext(ctx, `UPDATE run_event_sequences SET aggregate_id=? WHERE aggregate_id=?`, next, old); err != nil {
			return err
		}
	}
	return nil
}

func collectIndexRewritePlan(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps, oldProjectID, projectID string, plan *projectarchive.RewritePlan) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,storage_relative_path FROM knowledge_index_versions ORDER BY version_number`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var newIndexID, relative string
		if err := rows.Scan(&newIndexID, &relative); err != nil {
			return err
		}
		oldIndexID := ""
		for old, next := range maps.values["knowledge_index_versions"] {
			if next == newIndexID {
				oldIndexID = old
				break
			}
		}
		plan.Indexes = append(plan.Indexes, projectarchive.IndexRewrite{StorageRelativePath: filepath.ToSlash(relative), OldProjectID: oldProjectID, NewProjectID: projectID, OldIndexVersionID: oldIndexID, NewIndexVersionID: newIndexID, DocumentIDs: cloneStringMap(maps.values["knowledge_documents"]), AttachmentIDs: cloneStringMap(maps.values["attachments"])})
	}
	return rows.Err()
}

func rewriteKnowledgeIndex(ctx context.Context, db *sql.DB, item projectarchive.IndexRewrite) error {
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE index_metadata SET value=? WHERE key='project_id' AND value=?`, item.NewProjectID, item.OldProjectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE index_metadata SET value=? WHERE key='index_version_id' AND value=?`, item.NewIndexVersionID, item.OldIndexVersionID); err != nil {
		return err
	}
	for old, next := range item.DocumentIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE documents SET document_id=? WHERE document_id=?`, next, old); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE chunks SET document_id=? WHERE document_id=?`, next, old); err != nil {
			return err
		}
	}
	for old, next := range item.AttachmentIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE documents SET attachment_id=? WHERE attachment_id=?`, next, old); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE chunks SET attachment_id=? WHERE attachment_id=?`, next, old); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM query_embedding_cache`); err != nil && !strings.Contains(err.Error(), "no such table") {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	return err
}

func validateSQLite(ctx context.Context, db *sql.DB) error {
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("project archive SQLite integrity check failed: %s", integrity)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("project archive SQLite foreign key check failed")
	}
	return rows.Err()
}

func validateArchiveMigrations(ctx context.Context, db *sql.DB, declaredVersion int) error {
	if err := requireRealTable(ctx, db, "main", "schema_migrations"); err != nil {
		return fmt.Errorf("validate archived migrations: %w", err)
	}
	expected, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(expected) == 0 || declaredVersion <= 0 || declaredVersion > expected[len(expected)-1].version {
		return fmt.Errorf("project archive contains an unsupported database migration version")
	}
	rows, err := db.QueryContext(ctx, `SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("read archived migrations: %w", err)
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(expected) || expected[index].version > declaredVersion {
			return fmt.Errorf("project archive contains an unknown database migration")
		}
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return err
		}
		item := expected[index]
		if version != item.version || name != item.name || !migrationChecksumAccepted(item, checksum) {
			return fmt.Errorf("project archive migration %d does not match this SciAide build", version)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	expectedCount := 0
	for _, item := range expected {
		if item.version <= declaredVersion {
			expectedCount++
		}
	}
	if index != expectedCount {
		return fmt.Errorf("project archive migration history is incomplete")
	}
	return nil
}

type archiveSchemaObject struct {
	Type  string
	Name  string
	Table string
	SQL   string
}

func validateArchiveSchemaForVersion(ctx context.Context, archived *sql.DB, version int) error {
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return fmt.Errorf("open archive schema reference: %w", err)
	}
	reference.SetMaxOpenConns(1)
	defer reference.Close()
	if err := migrateToVersion(ctx, reference, version); err != nil {
		return fmt.Errorf("build archive schema reference: %w", err)
	}
	expected, err := listArchiveSchemaObjects(ctx, reference, false)
	if err != nil {
		return err
	}
	actual, err := listArchiveSchemaObjects(ctx, archived, true)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("project archive database schema does not match migration %d", version)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return fmt.Errorf("project archive database schema object %q does not match migration %d", actual[index].Name, version)
		}
	}
	return nil
}

func listArchiveSchemaObjects(ctx context.Context, db *sql.DB, excludeBindings bool) ([]archiveSchemaObject, error) {
	query := `SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`
	args := []any{}
	if excludeBindings {
		query += ` AND name<>?`
		args = append(args, archiveSkillBindingsTable)
	}
	query += ` ORDER BY type,name,tbl_name`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read project archive database schema: %w", err)
	}
	defer rows.Close()
	values := []archiveSchemaObject{}
	for rows.Next() {
		var value archiveSchemaObject
		if err := rows.Scan(&value.Type, &value.Name, &value.Table, &value.SQL); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func validateArchiveTables(ctx context.Context, db *sql.DB) error {
	tables := append(append(append([]string{}, archiveMergeOrder...), archiveSanitizedTables...), archiveSkillBindingsTable)
	for _, table := range tables {
		if err := requireRealTable(ctx, db, "main", table); err != nil {
			return fmt.Errorf("validate project archive structure: %w", err)
		}
	}
	return nil
}

func validateAttachedArchive(ctx context.Context, tx *sql.Tx) error {
	var integrity string
	if err := tx.QueryRowContext(ctx, `PRAGMA archive.integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("restored project database failed its final integrity check")
	}
	return nil
}

var archiveMergeOrder = []string{
	"projects", "model_profiles", "model_profile_models", "conversations", "messages", "message_parts", "runs", "run_events", "run_event_sequences",
	"research_tasks",
	"workflows", "workflow_versions", "workflow_runs", "workflow_conversations",
	"tool_calls", "tool_results", "process_execution_audits", "provider_turn_items", "run_steps", "model_turn_journal", "model_request_usage", "run_skill_contexts", "run_skills", "run_dynamic_skills", "run_skill_routing", "run_skill_routing_audits", "run_skill_routing_candidates",
	"conversation_context_checkpoints", "attachments", "knowledge_index_versions", "knowledge_documents", "knowledge_import_jobs", "message_citations",
	"artifact_blobs", "artifacts", "artifact_versions", "artifact_lineage", "artifact_citations", "artifact_exports",
	"workflow_steps", "workflow_human_decisions", "workflow_events", "workflow_ai_executions", "workflow_ai_chat_runs",
	"research_revision_proposals", "research_timeline",
	"research_queries", "research_source_records", "research_candidates", "research_candidate_records", "research_candidate_aliases", "research_candidate_task_imports", "research_query_records",
	"research_query_origins", "research_query_candidates", "research_candidate_reviews",
	"research_bibliographies", "research_bibliography_field_sources", "research_bibliography_revisions", "research_bibliography_materials", "research_evidence_entries",
	"python_environments", "python_kernel_executions",
}

var archiveSanitizedTables = []string{
	"model_resource_actions", "model_resource_sessions",
	"approvals", "permission_grants", "mcp_servers", "vision_fallback_channels", "knowledge_embedding_config", "settings",
	"project_skills", "installed_skills", "skill_package_sources", "retired_builtin_skill_packages", "skill_policies",
	"python_environment_operations",
}

func tableColumns(ctx context.Context, tx *sql.Tx, schema, table string) ([]string, error) {
	if err := requireRealTable(ctx, tx, schema, table); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA `+quoteIdentifier(schema)+`.table_xinfo(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var cid, notNull, primaryKey, hidden int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey, &hidden); err != nil {
			return nil, err
		}
		if hidden != 0 {
			return nil, fmt.Errorf("project archive table %s contains hidden or generated columns", table)
		}
		result = append(result, name)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("project archive table %s is missing", table)
	}
	return result, rows.Err()
}

type archiveSchemaQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireRealTable(ctx context.Context, queryer archiveSchemaQueryer, schema, table string) error {
	var objectType string
	query := `SELECT type FROM ` + quoteIdentifier(schema) + `.sqlite_schema WHERE name=? COLLATE BINARY`
	if err := queryer.QueryRowContext(ctx, query, table).Scan(&objectType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("project archive table %s is missing", table)
		}
		return err
	}
	if objectType != "table" {
		return fmt.Errorf("project archive object %s must be a table", table)
	}
	return nil
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func quoteColumns(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = quoteIdentifier(value)
	}
	return strings.Join(quoted, ",")
}

func cloneStringMap(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

var _ projectarchive.Repository = (*ProjectArchiveRepository)(nil)

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/id"
)

type KnowledgeRepository struct{ db *sql.DB }

func NewKnowledgeRepository(db *sql.DB) *KnowledgeRepository {
	return &KnowledgeRepository{db: db}
}

func (r *KnowledgeRepository) EnsureVersion(ctx context.Context, projectID string, spec knowledge.IndexSpec, at time.Time) (knowledge.IndexVersion, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return knowledge.IndexVersion{}, fmt.Errorf("project id is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return knowledge.IndexVersion{}, fmt.Errorf("begin knowledge index version: %w", err)
	}
	defer tx.Rollback()
	value, err := scanKnowledgeIndexVersion(tx.QueryRowContext(ctx, knowledgeIndexVersionSelect+`
		WHERE project_id=? AND status='building' ORDER BY version_number DESC LIMIT 1`, projectID))
	if err == nil && matchesCurrentKnowledgeVersion(value, spec) {
		if err := tx.Commit(); err != nil {
			return knowledge.IndexVersion{}, err
		}
		return value, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return knowledge.IndexVersion{}, fmt.Errorf("read active knowledge index version: %w", err)
	}
	if err == nil {
		if _, cancelErr := tx.ExecContext(ctx, `UPDATE knowledge_import_jobs SET status='cancelled',stage='cancelled',error_message='index version superseded',completed_at=?,updated_at=? WHERE project_id=? AND index_version_id=? AND status IN ('queued','running')`, formatTime(at), formatTime(at), projectID, value.ID); cancelErr != nil {
			return knowledge.IndexVersion{}, fmt.Errorf("cancel stale knowledge jobs: %w", cancelErr)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE knowledge_index_versions SET status='failed',error_message='superseded before activation',updated_at=? WHERE id=? AND project_id=?`, formatTime(at), value.ID, projectID); err != nil {
			return knowledge.IndexVersion{}, fmt.Errorf("fail stale knowledge index version: %w", err)
		}
	}
	value, err = scanKnowledgeIndexVersion(tx.QueryRowContext(ctx, knowledgeIndexVersionSelect+`
		WHERE project_id=? AND status='ready' ORDER BY version_number DESC LIMIT 1`, projectID))
	if err == nil && matchesCurrentKnowledgeVersion(value, spec) {
		if err := tx.Commit(); err != nil {
			return knowledge.IndexVersion{}, err
		}
		return value, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return knowledge.IndexVersion{}, fmt.Errorf("read ready knowledge index version: %w", err)
	}
	var versionNumber int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_number),0)+1 FROM knowledge_index_versions WHERE project_id=?`, projectID).Scan(&versionNumber); err != nil {
		return knowledge.IndexVersion{}, fmt.Errorf("allocate knowledge index version: %w", err)
	}
	versionID, err := id.New()
	if err != nil {
		return knowledge.IndexVersion{}, err
	}
	value = knowledge.IndexVersion{
		ID: versionID, ProjectID: projectID, VersionNumber: versionNumber,
		SchemaVersion: knowledge.IndexSchemaVersion, ParserSchemaVersion: document.SchemaVersion,
		ChunkingVersion: knowledge.ChunkingVersion, SearchKind: knowledge.SearchKind, RetrievalEngine: knowledge.RetrievalEngine,
		EmbeddingModel: spec.EmbeddingModel, EmbeddingDimensions: spec.EmbeddingDimensions, EmbeddingFingerprint: spec.EmbeddingFingerprint, HybridStrategy: spec.HybridStrategy,
		StorageRelativePath: filepath.ToSlash(filepath.Join("cache", "knowledge", fmt.Sprintf("index-v%d.db", versionNumber))),
		Status:              knowledge.IndexBuilding, CreatedAt: at, UpdatedAt: at,
	}
	if value.HybridStrategy == "" {
		value.HybridStrategy = knowledge.HybridBM25Only
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_index_versions(id,project_id,version_number,schema_version,parser_schema_version,chunking_version,search_kind,storage_relative_path,status,error_message,created_at,updated_at,retrieval_engine,embedding_model,embedding_dimensions,embedding_config_fingerprint,hybrid_strategy) VALUES (?,?,?,?,?,?,?,?,?,'',?,?,?,?,?,?,?)`,
		value.ID, value.ProjectID, value.VersionNumber, value.SchemaVersion, value.ParserSchemaVersion, value.ChunkingVersion, value.SearchKind, value.StorageRelativePath, value.Status, formatTime(value.CreatedAt), formatTime(value.UpdatedAt), value.RetrievalEngine, value.EmbeddingModel, value.EmbeddingDimensions, value.EmbeddingFingerprint, value.HybridStrategy); err != nil {
		return knowledge.IndexVersion{}, fmt.Errorf("insert knowledge index version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowledge.IndexVersion{}, err
	}
	return value, nil
}

func matchesCurrentKnowledgeVersion(value knowledge.IndexVersion, spec knowledge.IndexSpec) bool {
	if spec.HybridStrategy == "" {
		spec.HybridStrategy = knowledge.HybridBM25Only
	}
	return value.SchemaVersion == knowledge.IndexSchemaVersion &&
		value.ParserSchemaVersion == document.SchemaVersion &&
		value.ChunkingVersion == knowledge.ChunkingVersion &&
		value.SearchKind == knowledge.SearchKind &&
		value.RetrievalEngine == knowledge.RetrievalEngine &&
		value.EmbeddingModel == spec.EmbeddingModel &&
		value.EmbeddingDimensions == spec.EmbeddingDimensions &&
		value.EmbeddingFingerprint == spec.EmbeddingFingerprint &&
		value.HybridStrategy == spec.HybridStrategy
}

func (r *KnowledgeRepository) ReadyVersion(ctx context.Context, projectID string) (knowledge.IndexVersion, bool, error) {
	value, err := scanKnowledgeIndexVersion(r.db.QueryRowContext(ctx, knowledgeIndexVersionSelect+` WHERE project_id=? AND status='ready' ORDER BY version_number DESC LIMIT 1`, strings.TrimSpace(projectID)))
	if errors.Is(err, sql.ErrNoRows) {
		return knowledge.IndexVersion{}, false, nil
	}
	if err != nil {
		return knowledge.IndexVersion{}, false, fmt.Errorf("read ready knowledge index: %w", err)
	}
	return value, true, nil
}

func (r *KnowledgeRepository) ActiveVersions(ctx context.Context, projectID string) ([]knowledge.IndexVersion, error) {
	rows, err := r.db.QueryContext(ctx, knowledgeIndexVersionSelect+` WHERE project_id=? AND status IN ('building','ready') ORDER BY version_number`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("list active knowledge indexes: %w", err)
	}
	defer rows.Close()
	values := []knowledge.IndexVersion{}
	for rows.Next() {
		value, err := scanKnowledgeIndexVersion(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func (r *KnowledgeRepository) CanActivate(ctx context.Context, projectID, versionID string, expectedDocuments int) (bool, error) {
	if expectedDocuments < 0 {
		return false, fmt.Errorf("expected knowledge document count is invalid")
	}
	var readyDocuments, activeJobs, failedDocuments int
	if err := r.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(status='ready'),0),
		COALESCE(SUM(status IN ('pending','indexing')),0),
		COALESCE(SUM(status='failed'),0)
		FROM knowledge_documents WHERE project_id=? AND index_version_id=?`, strings.TrimSpace(projectID), strings.TrimSpace(versionID)).Scan(&readyDocuments, &activeJobs, &failedDocuments); err != nil {
		return false, fmt.Errorf("read knowledge index activation progress: %w", err)
	}
	var queuedOrRunning int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_import_jobs WHERE project_id=? AND index_version_id=? AND status IN ('queued','running')`, strings.TrimSpace(projectID), strings.TrimSpace(versionID)).Scan(&queuedOrRunning); err != nil {
		return false, fmt.Errorf("read knowledge index active jobs: %w", err)
	}
	return readyDocuments == expectedDocuments && activeJobs == 0 && failedDocuments == 0 && queuedOrRunning == 0, nil
}

func (r *KnowledgeRepository) MarkVersionReady(ctx context.Context, versionID, projectID string, at time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin knowledge index activation: %w", err)
	}
	defer tx.Rollback()
	versionID, projectID = strings.TrimSpace(versionID), strings.TrimSpace(projectID)
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_index_versions SET status='retired',updated_at=? WHERE project_id=? AND status='ready' AND id<>?`, formatTime(at), projectID, versionID); err != nil {
		return fmt.Errorf("retire prior knowledge index version: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE knowledge_index_versions SET status='ready',error_message='',activated_at=COALESCE(activated_at,?),updated_at=? WHERE id=? AND project_id=? AND status IN ('building','ready')`,
		formatTime(at), formatTime(at), versionID, projectID)
	if err != nil {
		return fmt.Errorf("activate knowledge index version: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("knowledge index version is not active")
	}
	return tx.Commit()
}

func (r *KnowledgeRepository) Enqueue(ctx context.Context, value attachment.Attachment, version knowledge.IndexVersion, force bool, at time.Time) (knowledge.ImportJob, bool, error) {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.ProjectID) == "" || value.ProjectID != version.ProjectID || value.Status != attachment.StatusReady {
		return knowledge.ImportJob{}, false, fmt.Errorf("only a ready attachment from the active project can be indexed")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return knowledge.ImportJob{}, false, fmt.Errorf("begin knowledge import enqueue: %w", err)
	}
	defer tx.Rollback()
	documentValue, err := scanKnowledgeDocument(tx.QueryRowContext(ctx, knowledgeDocumentSelect+` WHERE project_id=? AND attachment_id=?`, value.ProjectID, value.ID))
	if errors.Is(err, sql.ErrNoRows) {
		documentID, idErr := id.New()
		if idErr != nil {
			return knowledge.ImportJob{}, false, idErr
		}
		documentValue = knowledge.Document{
			ID: documentID, ProjectID: value.ProjectID, AttachmentID: value.ID, IndexVersionID: version.ID,
			Title: value.OriginalName, AttachmentSHA256: value.SHA256, Status: knowledge.DocumentPending,
			ParserSchemaVersion: version.ParserSchemaVersion, ChunkingVersion: version.ChunkingVersion,
			CreatedAt: at, UpdatedAt: at,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_documents(id,project_id,attachment_id,index_version_id,title,attachment_sha256,status,parser_schema_version,chunking_version,chunk_count,error_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,0,'',?,?)`,
			documentValue.ID, documentValue.ProjectID, documentValue.AttachmentID, documentValue.IndexVersionID, documentValue.Title, documentValue.AttachmentSHA256, documentValue.Status, documentValue.ParserSchemaVersion, documentValue.ChunkingVersion, formatTime(at), formatTime(at)); err != nil {
			return knowledge.ImportJob{}, false, fmt.Errorf("insert knowledge document: %w", err)
		}
	} else if err != nil {
		return knowledge.ImportJob{}, false, fmt.Errorf("read knowledge document: %w", err)
	} else {
		active, activeErr := scanKnowledgeJob(tx.QueryRowContext(ctx, knowledgeJobSelect+` WHERE document_id=? AND status IN ('queued','running') ORDER BY created_at DESC LIMIT 1`, documentValue.ID))
		if activeErr == nil {
			if err := tx.Commit(); err != nil {
				return knowledge.ImportJob{}, false, err
			}
			return active, false, nil
		}
		if !errors.Is(activeErr, sql.ErrNoRows) {
			return knowledge.ImportJob{}, false, fmt.Errorf("read active knowledge import job: %w", activeErr)
		}
		current := documentValue.Status == knowledge.DocumentReady && documentValue.IndexVersionID == version.ID &&
			documentValue.AttachmentSHA256 == value.SHA256 && documentValue.ParserSchemaVersion == version.ParserSchemaVersion &&
			documentValue.ChunkingVersion == version.ChunkingVersion
		if current && !force {
			if err := tx.Commit(); err != nil {
				return knowledge.ImportJob{}, false, err
			}
			return knowledge.ImportJob{}, false, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE knowledge_documents SET index_version_id=?,title=?,attachment_sha256=?,status='pending',parser_schema_version=?,chunking_version=?,error_message='',updated_at=? WHERE id=? AND project_id=?`,
			version.ID, value.OriginalName, value.SHA256, version.ParserSchemaVersion, version.ChunkingVersion, formatTime(at), documentValue.ID, value.ProjectID); err != nil {
			return knowledge.ImportJob{}, false, fmt.Errorf("reset knowledge document: %w", err)
		}
		documentValue.IndexVersionID = version.ID
		documentValue.Title = value.OriginalName
		documentValue.AttachmentSHA256 = value.SHA256
	}
	active, err := scanKnowledgeJob(tx.QueryRowContext(ctx, knowledgeJobSelect+` WHERE document_id=? AND status IN ('queued','running') ORDER BY created_at DESC LIMIT 1`, documentValue.ID))
	if err == nil {
		if err := tx.Commit(); err != nil {
			return knowledge.ImportJob{}, false, err
		}
		return active, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return knowledge.ImportJob{}, false, fmt.Errorf("read active knowledge import job: %w", err)
	}
	jobID, err := id.New()
	if err != nil {
		return knowledge.ImportJob{}, false, err
	}
	job := knowledge.ImportJob{
		ID: jobID, ProjectID: value.ProjectID, DocumentID: documentValue.ID, AttachmentID: value.ID,
		IndexVersionID: version.ID, Status: knowledge.JobQueued, Stage: "queued", CreatedAt: at, UpdatedAt: at,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_import_jobs(id,project_id,document_id,attachment_id,index_version_id,status,stage,attempt_count,error_message,created_at,updated_at) VALUES (?,?,?,?,?,?,'queued',0,'',?,?)`,
		job.ID, job.ProjectID, job.DocumentID, job.AttachmentID, job.IndexVersionID, job.Status, formatTime(at), formatTime(at)); err != nil {
		return knowledge.ImportJob{}, false, fmt.Errorf("insert knowledge import job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return knowledge.ImportJob{}, false, err
	}
	return job, true, nil
}

func (r *KnowledgeRepository) ListDocuments(ctx context.Context, projectID string) ([]knowledge.Document, error) {
	rows, err := r.db.QueryContext(ctx, knowledgeDocumentSelect+` WHERE project_id=? ORDER BY created_at DESC,id`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("list knowledge documents: %w", err)
	}
	defer rows.Close()
	values := []knowledge.Document{}
	for rows.Next() {
		value, err := scanKnowledgeDocument(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func (r *KnowledgeRepository) GetDocument(ctx context.Context, projectID, documentID string) (knowledge.Document, bool, error) {
	value, err := scanKnowledgeDocument(r.db.QueryRowContext(ctx, knowledgeDocumentSelect+` WHERE project_id=? AND id=?`, strings.TrimSpace(projectID), strings.TrimSpace(documentID)))
	if errors.Is(err, sql.ErrNoRows) {
		return knowledge.Document{}, false, nil
	}
	if err != nil {
		return knowledge.Document{}, false, fmt.Errorf("read knowledge document: %w", err)
	}
	return value, true, nil
}

func (r *KnowledgeRepository) RemoveDocument(ctx context.Context, projectID, documentID string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM knowledge_documents WHERE project_id=? AND id=?`, strings.TrimSpace(projectID), strings.TrimSpace(documentID))
	if err != nil {
		return false, fmt.Errorf("remove knowledge document: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (r *KnowledgeRepository) Recover(ctx context.Context, at time.Time) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin knowledge job recovery: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE knowledge_import_jobs SET status='queued',stage='queued',error_message='',started_at=NULL,updated_at=? WHERE status='running'`, formatTime(at))
	if err != nil {
		return 0, fmt.Errorf("recover knowledge jobs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_documents SET status='pending',error_message='',updated_at=? WHERE status='indexing'`, formatTime(at)); err != nil {
		return 0, fmt.Errorf("recover knowledge documents: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *KnowledgeRepository) ClaimNext(ctx context.Context, projectID string, at time.Time) (knowledge.Work, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return knowledge.Work{}, false, fmt.Errorf("begin knowledge job claim: %w", err)
	}
	defer tx.Rollback()
	query := knowledgeWorkSelect + ` WHERE j.status='queued' AND v.status IN ('building','ready')`
	args := []any{}
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		query += ` AND j.project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY j.created_at,j.id LIMIT 1`
	work, err := scanKnowledgeWork(tx.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return knowledge.Work{}, false, nil
	}
	if err != nil {
		return knowledge.Work{}, false, fmt.Errorf("read queued knowledge job: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE knowledge_import_jobs SET status='running',stage='loading',attempt_count=attempt_count+1,error_message='',started_at=?,completed_at=NULL,updated_at=? WHERE id=? AND project_id=? AND status='queued'`,
		formatTime(at), formatTime(at), work.Job.ID, work.Job.ProjectID)
	if err != nil {
		return knowledge.Work{}, false, fmt.Errorf("claim knowledge job: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return knowledge.Work{}, false, fmt.Errorf("knowledge job claim conflict")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_documents SET status='indexing',error_message='',updated_at=? WHERE id=? AND project_id=?`, formatTime(at), work.Document.ID, work.Document.ProjectID); err != nil {
		return knowledge.Work{}, false, fmt.Errorf("mark knowledge document indexing: %w", err)
	}
	work.Job.Status = knowledge.JobRunning
	work.Job.Stage = "loading"
	work.Job.AttemptCount++
	started := at
	work.Job.StartedAt = &started
	work.Job.UpdatedAt = at
	work.Document.Status = knowledge.DocumentIndexing
	if err := tx.Commit(); err != nil {
		return knowledge.Work{}, false, err
	}
	return work, true, nil
}

func (r *KnowledgeRepository) Complete(ctx context.Context, work knowledge.Work, chunkCount int, at time.Time) error {
	if chunkCount < 0 {
		return fmt.Errorf("knowledge chunk count is invalid")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE knowledge_import_jobs SET status='completed',stage='completed',error_message='',completed_at=?,updated_at=? WHERE id=? AND project_id=? AND status='running'`,
		formatTime(at), formatTime(at), work.Job.ID, work.Job.ProjectID)
	if err != nil {
		return fmt.Errorf("complete knowledge job: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("knowledge job completion conflict")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_documents SET status='ready',chunk_count=?,error_message='',indexed_at=?,updated_at=? WHERE id=? AND project_id=? AND index_version_id=?`,
		chunkCount, formatTime(at), formatTime(at), work.Document.ID, work.Document.ProjectID, work.Version.ID); err != nil {
		return fmt.Errorf("complete knowledge document: %w", err)
	}
	return tx.Commit()
}

func (r *KnowledgeRepository) UpdateStage(ctx context.Context, work knowledge.Work, stage string, at time.Time) error {
	stage = strings.TrimSpace(stage)
	if stage != "chunking" && stage != "indexing" {
		return fmt.Errorf("invalid knowledge import stage")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE knowledge_import_jobs SET stage=?,updated_at=? WHERE id=? AND project_id=? AND status='running'`, stage, formatTime(at), work.Job.ID, work.Job.ProjectID)
	if err != nil {
		return fmt.Errorf("update knowledge import stage: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("knowledge import stage conflict")
	}
	return nil
}

func (r *KnowledgeRepository) Fail(ctx context.Context, work knowledge.Work, message string, at time.Time) error {
	message = boundedKnowledgeError(message)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_import_jobs SET status='failed',stage='failed',error_message=?,completed_at=?,updated_at=? WHERE id=? AND project_id=? AND status='running'`, message, formatTime(at), formatTime(at), work.Job.ID, work.Job.ProjectID); err != nil {
		return fmt.Errorf("fail knowledge job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_documents SET status='failed',error_message=?,updated_at=? WHERE id=? AND project_id=?`, message, formatTime(at), work.Document.ID, work.Document.ProjectID); err != nil {
		return fmt.Errorf("fail knowledge document: %w", err)
	}
	return tx.Commit()
}

func (r *KnowledgeRepository) Requeue(ctx context.Context, work knowledge.Work, at time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_import_jobs SET status='queued',stage='queued',error_message='',started_at=NULL,updated_at=? WHERE id=? AND project_id=? AND status='running'`, formatTime(at), work.Job.ID, work.Job.ProjectID); err != nil {
		return fmt.Errorf("requeue knowledge job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_documents SET status='pending',error_message='',updated_at=? WHERE id=? AND project_id=?`, formatTime(at), work.Document.ID, work.Document.ProjectID); err != nil {
		return fmt.Errorf("requeue knowledge document: %w", err)
	}
	return tx.Commit()
}

func (r *KnowledgeRepository) ProjectStatus(ctx context.Context, projectID string) (knowledge.ProjectStatus, error) {
	var value knowledge.ProjectStatus
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(status='ready'),0),COALESCE(SUM(status='pending'),0),COALESCE(SUM(status='indexing'),0),COALESCE(SUM(status='failed'),0) FROM knowledge_documents WHERE project_id=?`, strings.TrimSpace(projectID)).Scan(
		&value.Documents, &value.Ready, &value.Pending, &value.Indexing, &value.Failed); err != nil {
		return value, fmt.Errorf("read project knowledge status: %w", err)
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='queued'),0),COALESCE(SUM(status='running'),0) FROM knowledge_import_jobs WHERE project_id=?`, strings.TrimSpace(projectID)).Scan(&value.QueuedJobs, &value.RunningJobs); err != nil {
		return value, fmt.Errorf("read project knowledge jobs: %w", err)
	}
	return value, nil
}

func boundedKnowledgeError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "knowledge indexing failed"
	}
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000])
	}
	return value
}

const knowledgeIndexVersionSelect = `SELECT id,project_id,version_number,schema_version,parser_schema_version,chunking_version,search_kind,storage_relative_path,status,error_message,created_at,activated_at,updated_at,retrieval_engine,embedding_model,embedding_dimensions,embedding_config_fingerprint,hybrid_strategy FROM knowledge_index_versions `
const knowledgeDocumentSelect = `SELECT id,project_id,attachment_id,index_version_id,title,attachment_sha256,status,parser_schema_version,chunking_version,chunk_count,error_message,created_at,indexed_at,updated_at FROM knowledge_documents `
const knowledgeJobSelect = `SELECT id,project_id,document_id,attachment_id,index_version_id,status,stage,attempt_count,error_message,created_at,started_at,completed_at,updated_at FROM knowledge_import_jobs `

const knowledgeWorkSelect = `SELECT
	d.id,d.project_id,d.attachment_id,d.index_version_id,d.title,d.attachment_sha256,d.status,d.parser_schema_version,d.chunking_version,d.chunk_count,d.error_message,d.created_at,d.indexed_at,d.updated_at,
	j.id,j.project_id,j.document_id,j.attachment_id,j.index_version_id,j.status,j.stage,j.attempt_count,j.error_message,j.created_at,j.started_at,j.completed_at,j.updated_at,
	v.id,v.project_id,v.version_number,v.schema_version,v.parser_schema_version,v.chunking_version,v.search_kind,v.storage_relative_path,v.status,v.error_message,v.created_at,v.activated_at,v.updated_at,v.retrieval_engine,v.embedding_model,v.embedding_dimensions,v.embedding_config_fingerprint,v.hybrid_strategy
	FROM knowledge_import_jobs j
	JOIN knowledge_documents d ON d.id=j.document_id AND d.project_id=j.project_id
	JOIN knowledge_index_versions v ON v.id=j.index_version_id AND v.project_id=j.project_id `

func scanKnowledgeIndexVersion(row rowScanner) (knowledge.IndexVersion, error) {
	var value knowledge.IndexVersion
	var createdAt, updatedAt string
	var activatedAt sql.NullString
	err := row.Scan(&value.ID, &value.ProjectID, &value.VersionNumber, &value.SchemaVersion, &value.ParserSchemaVersion, &value.ChunkingVersion, &value.SearchKind, &value.StorageRelativePath, &value.Status, &value.ErrorMessage, &createdAt, &activatedAt, &updatedAt, &value.RetrievalEngine, &value.EmbeddingModel, &value.EmbeddingDimensions, &value.EmbeddingFingerprint, &value.HybridStrategy)
	if err != nil {
		return value, err
	}
	var parseErr error
	if value.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
		return value, parseErr
	}
	if value.UpdatedAt, parseErr = parseTime(updatedAt); parseErr != nil {
		return value, parseErr
	}
	if activatedAt.Valid {
		parsed, err := parseTime(activatedAt.String)
		if err != nil {
			return value, err
		}
		value.ActivatedAt = &parsed
	}
	return value, nil
}

func scanKnowledgeDocument(row rowScanner) (knowledge.Document, error) {
	var value knowledge.Document
	var createdAt, updatedAt string
	var indexedAt sql.NullString
	err := row.Scan(&value.ID, &value.ProjectID, &value.AttachmentID, &value.IndexVersionID, &value.Title, &value.AttachmentSHA256, &value.Status, &value.ParserSchemaVersion, &value.ChunkingVersion, &value.ChunkCount, &value.ErrorMessage, &createdAt, &indexedAt, &updatedAt)
	if err != nil {
		return value, err
	}
	var parseErr error
	if value.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
		return value, parseErr
	}
	if value.UpdatedAt, parseErr = parseTime(updatedAt); parseErr != nil {
		return value, parseErr
	}
	if indexedAt.Valid {
		parsed, err := parseTime(indexedAt.String)
		if err != nil {
			return value, err
		}
		value.IndexedAt = &parsed
	}
	return value, nil
}

func scanKnowledgeJob(row rowScanner) (knowledge.ImportJob, error) {
	var value knowledge.ImportJob
	var createdAt, updatedAt string
	var startedAt, completedAt sql.NullString
	err := row.Scan(&value.ID, &value.ProjectID, &value.DocumentID, &value.AttachmentID, &value.IndexVersionID, &value.Status, &value.Stage, &value.AttemptCount, &value.ErrorMessage, &createdAt, &startedAt, &completedAt, &updatedAt)
	if err != nil {
		return value, err
	}
	var parseErr error
	if value.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
		return value, parseErr
	}
	if value.UpdatedAt, parseErr = parseTime(updatedAt); parseErr != nil {
		return value, parseErr
	}
	if startedAt.Valid {
		parsed, err := parseTime(startedAt.String)
		if err != nil {
			return value, err
		}
		value.StartedAt = &parsed
	}
	if completedAt.Valid {
		parsed, err := parseTime(completedAt.String)
		if err != nil {
			return value, err
		}
		value.CompletedAt = &parsed
	}
	return value, nil
}

func scanKnowledgeWork(row rowScanner) (knowledge.Work, error) {
	var work knowledge.Work
	var documentCreated, documentUpdated string
	var documentIndexed sql.NullString
	var jobCreated, jobUpdated string
	var jobStarted, jobCompleted sql.NullString
	var versionCreated, versionUpdated string
	var versionActivated sql.NullString
	err := row.Scan(
		&work.Document.ID, &work.Document.ProjectID, &work.Document.AttachmentID, &work.Document.IndexVersionID, &work.Document.Title, &work.Document.AttachmentSHA256, &work.Document.Status, &work.Document.ParserSchemaVersion, &work.Document.ChunkingVersion, &work.Document.ChunkCount, &work.Document.ErrorMessage, &documentCreated, &documentIndexed, &documentUpdated,
		&work.Job.ID, &work.Job.ProjectID, &work.Job.DocumentID, &work.Job.AttachmentID, &work.Job.IndexVersionID, &work.Job.Status, &work.Job.Stage, &work.Job.AttemptCount, &work.Job.ErrorMessage, &jobCreated, &jobStarted, &jobCompleted, &jobUpdated,
		&work.Version.ID, &work.Version.ProjectID, &work.Version.VersionNumber, &work.Version.SchemaVersion, &work.Version.ParserSchemaVersion, &work.Version.ChunkingVersion, &work.Version.SearchKind, &work.Version.StorageRelativePath, &work.Version.Status, &work.Version.ErrorMessage, &versionCreated, &versionActivated, &versionUpdated, &work.Version.RetrievalEngine, &work.Version.EmbeddingModel, &work.Version.EmbeddingDimensions, &work.Version.EmbeddingFingerprint, &work.Version.HybridStrategy,
	)
	if err != nil {
		return work, err
	}
	var parseErr error
	if work.Document.CreatedAt, parseErr = parseTime(documentCreated); parseErr != nil {
		return work, parseErr
	}
	if work.Document.UpdatedAt, parseErr = parseTime(documentUpdated); parseErr != nil {
		return work, parseErr
	}
	if work.Job.CreatedAt, parseErr = parseTime(jobCreated); parseErr != nil {
		return work, parseErr
	}
	if work.Job.UpdatedAt, parseErr = parseTime(jobUpdated); parseErr != nil {
		return work, parseErr
	}
	if work.Version.CreatedAt, parseErr = parseTime(versionCreated); parseErr != nil {
		return work, parseErr
	}
	if work.Version.UpdatedAt, parseErr = parseTime(versionUpdated); parseErr != nil {
		return work, parseErr
	}
	if documentIndexed.Valid {
		value, err := parseTime(documentIndexed.String)
		if err != nil {
			return work, err
		}
		work.Document.IndexedAt = &value
	}
	if jobStarted.Valid {
		value, err := parseTime(jobStarted.String)
		if err != nil {
			return work, err
		}
		work.Job.StartedAt = &value
	}
	if jobCompleted.Valid {
		value, err := parseTime(jobCompleted.String)
		if err != nil {
			return work, err
		}
		work.Job.CompletedAt = &value
	}
	if versionActivated.Valid {
		value, err := parseTime(versionActivated.String)
		if err != nil {
			return work, err
		}
		work.Version.ActivatedAt = &value
	}
	return work, nil
}

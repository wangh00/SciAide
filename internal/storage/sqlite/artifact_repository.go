package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type ArtifactRepository struct{ db *sql.DB }

func NewArtifactRepository(db *sql.DB) *ArtifactRepository { return &ArtifactRepository{db: db} }

func (r *ArtifactRepository) CreateVersion(ctx context.Context, record artifact.CreateVersionRecord) (artifact.SaveResult, error) {
	encoded, err := json.Marshal(record.Version.Provenance)
	if err != nil {
		return artifact.SaveResult{}, fmt.Errorf("encode Artifact provenance: %w", err)
	}
	if len(encoded) > 1<<20 {
		return artifact.SaveResult{}, fmt.Errorf("Artifact provenance exceeds size limit")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return artifact.SaveResult{}, fmt.Errorf("begin Artifact version: %w", err)
	}
	defer tx.Rollback()

	if key := strings.TrimSpace(record.Version.SourceKey); key != "" {
		var versionID string
		err := tx.QueryRowContext(ctx, `SELECT v.id FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id WHERE v.source_key=? AND a.project_id=?`, key, record.ProjectID).Scan(&versionID)
		if err == nil {
			result, loadErr := loadSaveResult(ctx, tx, record.ProjectID, versionID, false)
			if loadErr != nil {
				return artifact.SaveResult{}, loadErr
			}
			if result.Version.SHA256 != record.Version.SHA256 || result.Version.SizeBytes != record.Version.SizeBytes || result.Version.SourceKind != record.Version.SourceKind {
				return artifact.SaveResult{}, fmt.Errorf("Artifact idempotency key conflicts with different content")
			}
			if err := tx.Commit(); err != nil {
				return artifact.SaveResult{}, err
			}
			return result, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return artifact.SaveResult{}, fmt.Errorf("inspect Artifact idempotency key: %w", err)
		}
	}

	created := false
	var projectID string
	var kind artifact.Kind
	var status artifact.Status
	err = tx.QueryRowContext(ctx, `SELECT project_id,kind,status FROM artifacts WHERE id=?`, record.ArtifactID).Scan(&projectID, &kind, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO artifacts(id,project_id,name,kind,status,current_version_id,created_at,updated_at,trashed_at) VALUES (?,?,?,?,'active',NULL,?,?,NULL)`,
			record.ArtifactID, record.ProjectID, record.Name, record.Kind, formatTime(record.Version.CreatedAt), formatTime(record.Version.CreatedAt))
		if err != nil {
			return artifact.SaveResult{}, fmt.Errorf("insert Artifact: %w", err)
		}
		created = true
	case err != nil:
		return artifact.SaveResult{}, fmt.Errorf("inspect Artifact: %w", err)
	case projectID != record.ProjectID:
		return artifact.SaveResult{}, fmt.Errorf("Artifact does not belong to the current project")
	case status != artifact.StatusActive:
		return artifact.SaveResult{}, fmt.Errorf("restore the Artifact before adding a version")
	case kind != record.Kind:
		return artifact.SaveResult{}, fmt.Errorf("new version kind does not match the Artifact")
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_blobs(id,project_id,sha256,size_bytes,mime_type,storage_relative_path,created_at) VALUES (?,?,?,?,?,?,?) ON CONFLICT(project_id,sha256) DO NOTHING`,
		record.Blob.ID, record.ProjectID, record.Blob.SHA256, record.Blob.SizeBytes, record.Blob.MIMEType, record.Blob.StorageRelativePath, formatTime(record.Blob.CreatedAt))
	if err != nil {
		return artifact.SaveResult{}, fmt.Errorf("insert Artifact blob: %w", err)
	}
	var blob artifact.BlobRecord
	var blobCreatedAt string
	err = tx.QueryRowContext(ctx, `SELECT id,project_id,sha256,size_bytes,mime_type,storage_relative_path,created_at FROM artifact_blobs WHERE project_id=? AND sha256=?`, record.ProjectID, record.Blob.SHA256).
		Scan(&blob.ID, &blob.ProjectID, &blob.SHA256, &blob.SizeBytes, &blob.MIMEType, &blob.StorageRelativePath, &blobCreatedAt)
	if err != nil {
		return artifact.SaveResult{}, fmt.Errorf("read Artifact blob: %w", err)
	}
	if blob.SizeBytes != record.Blob.SizeBytes || blob.StorageRelativePath != record.Blob.StorageRelativePath {
		return artifact.SaveResult{}, fmt.Errorf("content-addressed Artifact blob conflicts with persisted metadata")
	}

	var versionNumber int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_number),0)+1 FROM artifact_versions WHERE artifact_id=?`, record.ArtifactID).Scan(&versionNumber); err != nil {
		return artifact.SaveResult{}, err
	}
	record.Version.ArtifactID = record.ArtifactID
	record.Version.BlobID = blob.ID
	record.Version.VersionNumber = versionNumber
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_versions(id,artifact_id,blob_id,version_number,file_name,mime_type,size_bytes,sha256,source_kind,source_key,provenance_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.Version.ID, record.ArtifactID, blob.ID, versionNumber, record.Version.FileName, record.Version.MIMEType,
		record.Version.SizeBytes, record.Version.SHA256, record.Version.SourceKind, nullableString(record.Version.SourceKey), string(encoded), formatTime(record.Version.CreatedAt))
	if err != nil {
		return artifact.SaveResult{}, fmt.Errorf("insert Artifact version: %w", err)
	}
	for _, value := range record.Version.Lineage {
		metadata := value.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO artifact_lineage(id,artifact_version_id,ordinal,relation_kind,source_id_snapshot,source_run_id,source_workflow_run_id,source_message_id,source_tool_call_id,source_artifact_version_id,label,metadata_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			value.ID, record.Version.ID, value.Ordinal, value.RelationKind, value.SourceIDSnapshot,
			nullableString(value.SourceRunID), nullableString(value.SourceWorkflowRunID), nullableString(value.SourceMessageID), nullableString(value.SourceToolCallID), nullableString(value.SourceArtifactVersionID),
			value.Label, string(metadata), formatTime(value.CreatedAt))
		if err != nil {
			return artifact.SaveResult{}, fmt.Errorf("insert Artifact lineage: %w", err)
		}
	}
	for _, value := range record.Version.Citations {
		snapshot := value.BibliographySnapshot
		if len(snapshot) == 0 {
			snapshot = json.RawMessage(`{}`)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO artifact_citations(id,artifact_version_id,ordinal,reference_key,source_run_id_snapshot,source_message_id_snapshot,source_tool_call_id_snapshot,index_version_id,document_id,attachment_id,chunk_id,source_name,mime_type,locator,title,quote_text,quote_sha256,source_start,source_end,created_at,bibliography_id_snapshot,bibliography_snapshot_json,evidence_level) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			value.ID, record.Version.ID, value.Ordinal, value.Reference, value.SourceRunIDSnapshot, value.SourceMessageIDSnapshot, value.SourceToolCallIDSnapshot,
			value.IndexVersionID, value.DocumentID, value.AttachmentID, value.ChunkID, value.SourceName, value.MIMEType, value.Locator, value.Title,
			value.Quote, value.QuoteSHA256, value.SourceStart, value.SourceEnd, formatTime(value.CreatedAt), value.BibliographyIDSnapshot, string(snapshot), value.EvidenceLevel)
		if err != nil {
			return artifact.SaveResult{}, fmt.Errorf("insert Artifact citation: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE artifacts SET current_version_id=?,updated_at=? WHERE id=? AND project_id=? AND status='active'`, record.Version.ID, formatTime(record.Version.CreatedAt), record.ArtifactID, record.ProjectID)
	if err != nil {
		return artifact.SaveResult{}, fmt.Errorf("publish Artifact version: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return artifact.SaveResult{}, fmt.Errorf("Artifact changed while its version was being saved")
	}
	saved, err := loadSaveResult(ctx, tx, record.ProjectID, record.Version.ID, created)
	if err != nil {
		return artifact.SaveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return artifact.SaveResult{}, fmt.Errorf("commit Artifact version: %w", err)
	}
	return saved, nil
}

func (r *ArtifactRepository) List(ctx context.Context, projectID string, includeTrashed bool) ([]artifact.Artifact, error) {
	query := artifactCurrentSelect + ` WHERE a.project_id=?`
	args := []any{projectID}
	if !includeTrashed {
		query += ` AND a.status='active'`
	}
	query += ` ORDER BY CASE a.status WHEN 'active' THEN 0 ELSE 1 END,a.updated_at DESC,a.id`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list Artifacts: %w", err)
	}
	defer rows.Close()
	values := make([]artifact.Artifact, 0)
	for rows.Next() {
		value, err := scanArtifactCurrent(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *ArtifactRepository) Get(ctx context.Context, projectID, artifactID string) (artifact.Detail, error) {
	value, err := scanArtifactCurrent(r.db.QueryRowContext(ctx, artifactCurrentSelect+` WHERE a.project_id=? AND a.id=?`, projectID, artifactID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return artifact.Detail{}, fmt.Errorf("Artifact not found")
		}
		return artifact.Detail{}, err
	}
	versions, err := r.listVersions(ctx, projectID, artifactID)
	if err != nil {
		return artifact.Detail{}, err
	}
	return artifact.Detail{Artifact: value, Versions: versions}, nil
}

func (r *ArtifactRepository) GetVersion(ctx context.Context, projectID, versionID string) (artifact.Version, artifact.BlobRecord, error) {
	version, blob, err := scanArtifactVersion(r.db.QueryRowContext(ctx, artifactVersionSelect+` WHERE a.project_id=? AND v.id=?`, projectID, versionID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return artifact.Version{}, artifact.BlobRecord{}, fmt.Errorf("Artifact version not found")
		}
		return artifact.Version{}, artifact.BlobRecord{}, err
	}
	if err := r.loadVersionRelations(ctx, &version); err != nil {
		return artifact.Version{}, artifact.BlobRecord{}, err
	}
	return version, blob, nil
}

func (r *ArtifactRepository) CreateExport(ctx context.Context, record artifact.CreateExportRecord) (artifact.ExportResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return artifact.ExportResult{}, fmt.Errorf("begin Artifact export: %w", err)
	}
	defer tx.Rollback()

	var sourceSHA256 string
	err = tx.QueryRowContext(ctx, `SELECT v.sha256 FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id WHERE v.id=? AND a.project_id=?`, record.Export.ArtifactVersionID, record.Export.ProjectID).Scan(&sourceSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.ExportResult{}, fmt.Errorf("Artifact version not found")
	}
	if err != nil {
		return artifact.ExportResult{}, fmt.Errorf("inspect Artifact export source: %w", err)
	}
	if sourceSHA256 != record.Export.SourceSHA256 {
		return artifact.ExportResult{}, fmt.Errorf("Artifact source changed before export was recorded")
	}

	existing, exists, err := findArtifactExport(ctx, tx, record.Export.ArtifactVersionID, record.Export.Format, record.Export.CitationStyle, record.Export.GeneratorVersion)
	if err != nil {
		return artifact.ExportResult{}, err
	}
	if exists {
		if existing.SHA256 != record.Export.SHA256 || existing.SizeBytes != record.Export.SizeBytes || existing.MIMEType != record.Export.MIMEType {
			return artifact.ExportResult{}, fmt.Errorf("Artifact export is not deterministic for the same source and options")
		}
		if err := tx.Commit(); err != nil {
			return artifact.ExportResult{}, err
		}
		return artifact.ExportResult{Export: existing}, nil
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_blobs(id,project_id,sha256,size_bytes,mime_type,storage_relative_path,created_at) VALUES (?,?,?,?,?,?,?) ON CONFLICT(project_id,sha256) DO NOTHING`,
		record.Blob.ID, record.Blob.ProjectID, record.Blob.SHA256, record.Blob.SizeBytes, record.Blob.MIMEType, record.Blob.StorageRelativePath, formatTime(record.Blob.CreatedAt))
	if err != nil {
		return artifact.ExportResult{}, fmt.Errorf("insert Artifact export blob: %w", err)
	}
	var blob artifact.BlobRecord
	var blobCreatedAt string
	err = tx.QueryRowContext(ctx, `SELECT id,project_id,sha256,size_bytes,mime_type,storage_relative_path,created_at FROM artifact_blobs WHERE project_id=? AND sha256=?`, record.Export.ProjectID, record.Blob.SHA256).
		Scan(&blob.ID, &blob.ProjectID, &blob.SHA256, &blob.SizeBytes, &blob.MIMEType, &blob.StorageRelativePath, &blobCreatedAt)
	if err != nil {
		return artifact.ExportResult{}, fmt.Errorf("read Artifact export blob: %w", err)
	}
	if blob.SizeBytes != record.Blob.SizeBytes || blob.StorageRelativePath != record.Blob.StorageRelativePath {
		return artifact.ExportResult{}, fmt.Errorf("content-addressed Artifact export blob conflicts with persisted metadata")
	}
	record.Export.BlobID = blob.ID
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_exports(id,project_id,artifact_version_id,blob_id,export_format,citation_style,generator_version,file_name,mime_type,size_bytes,sha256,source_sha256,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.Export.ID, record.Export.ProjectID, record.Export.ArtifactVersionID, blob.ID, record.Export.Format,
		record.Export.CitationStyle, record.Export.GeneratorVersion, record.Export.FileName, record.Export.MIMEType,
		record.Export.SizeBytes, record.Export.SHA256, record.Export.SourceSHA256, formatTime(record.Export.CreatedAt))
	if err != nil {
		return artifact.ExportResult{}, fmt.Errorf("insert Artifact export: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return artifact.ExportResult{}, fmt.Errorf("commit Artifact export: %w", err)
	}
	return artifact.ExportResult{Export: record.Export, Created: true}, nil
}

func (r *ArtifactRepository) GetExport(ctx context.Context, projectID, exportID string) (artifact.Export, artifact.BlobRecord, error) {
	value, blob, err := scanArtifactExport(r.db.QueryRowContext(ctx, artifactExportSelect+` WHERE e.project_id=? AND e.id=?`, projectID, exportID))
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.Export{}, artifact.BlobRecord{}, fmt.Errorf("Artifact export not found")
	}
	if err != nil {
		return artifact.Export{}, artifact.BlobRecord{}, err
	}
	return value, blob, nil
}

func (r *ArtifactRepository) Rename(ctx context.Context, projectID, artifactID, name string, at time.Time) (artifact.Artifact, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE artifacts SET name=?,updated_at=? WHERE id=? AND project_id=?`, name, formatTime(at), artifactID, projectID)
	if err != nil {
		return artifact.Artifact{}, fmt.Errorf("rename Artifact: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return artifact.Artifact{}, fmt.Errorf("Artifact not found")
	}
	return scanArtifactCurrent(r.db.QueryRowContext(ctx, artifactCurrentSelect+` WHERE a.project_id=? AND a.id=?`, projectID, artifactID))
}

func (r *ArtifactRepository) SetStatus(ctx context.Context, projectID, artifactID string, status artifact.Status, at time.Time) (artifact.Artifact, error) {
	if status != artifact.StatusActive && status != artifact.StatusTrashed {
		return artifact.Artifact{}, fmt.Errorf("invalid Artifact status")
	}
	var trashedAt any
	if status == artifact.StatusTrashed {
		trashedAt = formatTime(at)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE artifacts SET status=?,trashed_at=?,updated_at=? WHERE id=? AND project_id=? AND status<>?`, status, trashedAt, formatTime(at), artifactID, projectID, status)
	if err != nil {
		return artifact.Artifact{}, fmt.Errorf("update Artifact status: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		var exists int
		if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE id=? AND project_id=?`, artifactID, projectID).Scan(&exists); err != nil || exists == 0 {
			return artifact.Artifact{}, fmt.Errorf("Artifact not found")
		}
	}
	return scanArtifactCurrent(r.db.QueryRowContext(ctx, artifactCurrentSelect+` WHERE a.project_id=? AND a.id=?`, projectID, artifactID))
}

func (r *ArtifactRepository) AssistantSource(ctx context.Context, projectID, messageID string) (artifact.AssistantSource, error) {
	var value artifact.AssistantSource
	var protocol modelcap.APIProtocol
	err := r.db.QueryRowContext(ctx, `SELECT c.project_id,c.id,c.title,m.id,r.id,r.model_profile_id,COALESCE(p.name,''),r.model_id,r.api_protocol
		FROM messages m JOIN conversations c ON c.id=m.conversation_id JOIN runs r ON r.id=m.run_id
		LEFT JOIN model_profiles p ON p.id=r.model_profile_id
		WHERE c.project_id=? AND m.id=? AND m.role='assistant' AND m.status='complete' AND r.status='completed'`, projectID, messageID).
		Scan(&value.ProjectID, &value.ConversationID, &value.ConversationTitle, &value.MessageID, &value.RunID,
			&value.ModelProfileID, &value.ModelProfileName, &value.ModelID, &protocol)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, fmt.Errorf("only a completed assistant answer can be saved as an Artifact")
		}
		return value, fmt.Errorf("load assistant Artifact source: %w", err)
	}
	value.APIProtocol = protocol
	rows, err := r.db.QueryContext(ctx, `SELECT text_content FROM message_parts WHERE message_id=? AND part_type='text' ORDER BY ordinal`, messageID)
	if err != nil {
		return value, err
	}
	texts := make([]string, 0)
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			_ = rows.Close()
			return value, err
		}
		texts = append(texts, text)
	}
	if err := rows.Close(); err != nil {
		return value, err
	}
	value.Text = strings.Join(texts, "")
	if strings.TrimSpace(value.Text) == "" {
		return value, fmt.Errorf("assistant answer is empty")
	}
	value.Skills, err = loadSkillSnapshots(ctx, r.db, value.RunID)
	if err != nil {
		return value, err
	}
	value.Citations, err = loadMessageCitationSnapshots(ctx, r.db, messageID)
	return value, err
}

func (r *ArtifactRepository) ToolSource(ctx context.Context, callID string) (artifact.ToolSource, error) {
	var value artifact.ToolSource
	var permissionsJSON, artifactsJSON, citationsJSON string
	var protocol modelcap.APIProtocol
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(c.project_id,wr.project_id),COALESCE(c.id,''),COALESCE(c.title,''),COALESCE(tc.run_id,tc.workflow_run_id),
		CASE WHEN tc.workflow_run_id IS NULL THEN 'chat_run' ELSE 'workflow_run' END,tc.id,tc.tool_name,tc.tool_version,
		COALESCE(r.model_profile_id,''),COALESCE(p.name,''),COALESCE(r.model_id,''),COALESCE(r.api_protocol,''),tc.permissions_json,tr.artifacts_json,tr.citations_json
		FROM tool_calls tc JOIN tool_results tr ON tr.tool_call_id=tc.id LEFT JOIN runs r ON r.id=tc.run_id
		LEFT JOIN conversations c ON c.id=r.conversation_id LEFT JOIN model_profiles p ON p.id=r.model_profile_id
		LEFT JOIN workflow_runs wr ON wr.id=tc.workflow_run_id
		WHERE tc.id=? AND tc.status='completed' AND tr.status='success'`, callID).
		Scan(&value.ProjectID, &value.ConversationID, &value.ConversationTitle, &value.RunID, &value.SubjectKind, &value.CallID,
			&value.ToolName, &value.ToolVersion, &value.ModelProfileID, &value.ModelProfileName, &value.ModelID, &protocol, &permissionsJSON, &artifactsJSON, &citationsJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, fmt.Errorf("completed successful tool call not found")
		}
		return value, fmt.Errorf("load tool Artifact source: %w", err)
	}
	value.APIProtocol = protocol
	if err := json.Unmarshal([]byte(permissionsJSON), &value.Permissions); err != nil {
		return value, fmt.Errorf("decode tool permission snapshot: %w", err)
	}
	if err := json.Unmarshal([]byte(artifactsJSON), &value.Artifacts); err != nil {
		return value, fmt.Errorf("decode tool Artifact declarations: %w", err)
	}
	if err := json.Unmarshal([]byte(citationsJSON), &value.Citations); err != nil {
		return value, fmt.Errorf("decode tool citations: %w", err)
	}
	if tool.NormalizeSubjectKind(value.SubjectKind) == tool.SubjectChatRun {
		value.Skills, err = loadSkillSnapshots(ctx, r.db, value.RunID)
	} else {
		value.Skills = []artifact.SkillSnapshot{}
	}
	return value, err
}

func (r *ArtifactRepository) RecoverableToolCallIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT tc.id
		FROM tool_calls tc
		JOIN tool_results tr ON tr.tool_call_id=tc.id
		JOIN json_each(tr.artifacts_json) item
		LEFT JOIN artifact_versions v ON v.source_key=('tool:' || tc.id || ':' || CAST(item.key AS TEXT))
		WHERE tc.status='completed' AND tr.status='success'
			AND trim(COALESCE(json_extract(item.value,'$.workspacePath'),''))<>''
			AND length(COALESCE(json_extract(item.value,'$.sha256'),''))=64
			AND COALESCE(json_extract(item.value,'$.sizeBytes'),0)>=0
			AND v.id IS NULL
		ORDER BY tc.created_at,tc.id`)
	if err != nil {
		return nil, fmt.Errorf("list recoverable tool Artifacts: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *ArtifactRepository) BlobPaths(ctx context.Context, projectID string) (map[string]struct{}, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT storage_relative_path FROM artifact_blobs WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]struct{}{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values[value] = struct{}{}
	}
	return values, rows.Err()
}

func (r *ArtifactRepository) listVersions(ctx context.Context, projectID, artifactID string) ([]artifact.Version, error) {
	rows, err := r.db.QueryContext(ctx, artifactVersionSelect+` WHERE a.project_id=? AND v.artifact_id=? ORDER BY v.version_number DESC`, projectID, artifactID)
	if err != nil {
		return nil, fmt.Errorf("list Artifact versions: %w", err)
	}
	versions := make([]artifact.Version, 0)
	for rows.Next() {
		value, _, err := scanArtifactVersion(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		versions = append(versions, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range versions {
		if err := r.loadVersionRelations(ctx, &versions[index]); err != nil {
			return nil, err
		}
	}
	return versions, nil
}

func (r *ArtifactRepository) loadVersionRelations(ctx context.Context, version *artifact.Version) error {
	rows, err := r.db.QueryContext(ctx, `SELECT id,ordinal,relation_kind,source_id_snapshot,COALESCE(source_run_id,''),COALESCE(source_workflow_run_id,''),COALESCE(source_message_id,''),COALESCE(source_tool_call_id,''),COALESCE(source_artifact_version_id,''),label,metadata_json,created_at FROM artifact_lineage WHERE artifact_version_id=? ORDER BY ordinal`, version.ID)
	if err != nil {
		return err
	}
	version.Lineage = []artifact.Lineage{}
	for rows.Next() {
		var value artifact.Lineage
		var metadata, createdAt string
		value.ArtifactVersionID = version.ID
		if err := rows.Scan(&value.ID, &value.Ordinal, &value.RelationKind, &value.SourceIDSnapshot, &value.SourceRunID, &value.SourceWorkflowRunID, &value.SourceMessageID, &value.SourceToolCallID, &value.SourceArtifactVersionID, &value.Label, &metadata, &createdAt); err != nil {
			_ = rows.Close()
			return err
		}
		value.Metadata = json.RawMessage(metadata)
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			_ = rows.Close()
			return err
		}
		version.Lineage = append(version.Lineage, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	version.Citations, err = loadArtifactCitations(ctx, r.db, version.ID)
	if err != nil {
		return err
	}
	version.Exports, err = loadArtifactExports(ctx, r.db, version.ID)
	return err
}

type artifactQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadSkillSnapshots(ctx context.Context, queryer artifactQueryer, runID string) ([]artifact.SkillSnapshot, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT skill_id,skill_version,content_hash,package_hash FROM run_skills WHERE run_id=? ORDER BY ordinal`, runID)
	if err != nil {
		return nil, err
	}
	values := make([]artifact.SkillSnapshot, 0)
	for rows.Next() {
		var value artifact.SkillSnapshot
		if err := rows.Scan(&value.ID, &value.Version, &value.ContentHash, &value.PackageHash); err != nil {
			rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = queryer.QueryContext(ctx, `SELECT skill_name,origin,content_hash,package_hash FROM run_dynamic_skills WHERE run_id=? ORDER BY ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		value := artifact.SkillSnapshot{Version: "dynamic", Dynamic: true}
		if err := rows.Scan(&value.ID, &value.Origin, &value.ContentHash, &value.PackageHash); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadMessageCitationSnapshots(ctx context.Context, queryer artifactQueryer, messageID string) ([]artifact.Citation, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT reference_key,run_id,message_id,tool_call_id,index_version_id,document_id,attachment_id,chunk_id,source_name,mime_type,locator,title,quote_text,quote_sha256,source_start,source_end,created_at,bibliography_id_snapshot,bibliography_snapshot_json,evidence_level FROM message_citations WHERE message_id=? ORDER BY ordinal`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]artifact.Citation, 0)
	for rows.Next() {
		var value artifact.Citation
		var createdAt string
		value.Ordinal = len(values)
		var bibliographyJSON string
		if err := rows.Scan(&value.Reference, &value.SourceRunIDSnapshot, &value.SourceMessageIDSnapshot, &value.SourceToolCallIDSnapshot,
			&value.IndexVersionID, &value.DocumentID, &value.AttachmentID, &value.ChunkID, &value.SourceName, &value.MIMEType,
			&value.Locator, &value.Title, &value.Quote, &value.QuoteSHA256, &value.SourceStart, &value.SourceEnd, &createdAt, &value.BibliographyIDSnapshot, &bibliographyJSON, &value.EvidenceLevel); err != nil {
			return nil, err
		}
		if bibliographyJSON != "{}" {
			value.BibliographySnapshot = json.RawMessage(bibliographyJSON)
		}
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadArtifactCitations(ctx context.Context, queryer artifactQueryer, versionID string) ([]artifact.Citation, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT id,ordinal,reference_key,source_run_id_snapshot,source_message_id_snapshot,source_tool_call_id_snapshot,index_version_id,document_id,attachment_id,chunk_id,source_name,mime_type,locator,title,quote_text,quote_sha256,source_start,source_end,created_at,bibliography_id_snapshot,bibliography_snapshot_json,evidence_level FROM artifact_citations WHERE artifact_version_id=? ORDER BY ordinal`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]artifact.Citation, 0)
	for rows.Next() {
		var value artifact.Citation
		var createdAt string
		value.ArtifactVersionID = versionID
		var bibliographyJSON string
		if err := rows.Scan(&value.ID, &value.Ordinal, &value.Reference, &value.SourceRunIDSnapshot, &value.SourceMessageIDSnapshot, &value.SourceToolCallIDSnapshot,
			&value.IndexVersionID, &value.DocumentID, &value.AttachmentID, &value.ChunkID, &value.SourceName, &value.MIMEType,
			&value.Locator, &value.Title, &value.Quote, &value.QuoteSHA256, &value.SourceStart, &value.SourceEnd, &createdAt, &value.BibliographyIDSnapshot, &bibliographyJSON, &value.EvidenceLevel); err != nil {
			return nil, err
		}
		if bibliographyJSON != "{}" {
			value.BibliographySnapshot = json.RawMessage(bibliographyJSON)
		}
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadArtifactExports(ctx context.Context, queryer artifactQueryer, versionID string) ([]artifact.Export, error) {
	rows, err := queryer.QueryContext(ctx, artifactExportSelect+` WHERE e.artifact_version_id=? ORDER BY e.created_at DESC,e.id`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]artifact.Export, 0)
	for rows.Next() {
		value, _, err := scanArtifactExport(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type artifactRowScanner interface{ Scan(...any) error }

func scanArtifactCurrent(row artifactRowScanner) (artifact.Artifact, error) {
	var value artifact.Artifact
	var version artifact.Version
	var currentVersionID, versionID, blobID, fileName, mimeType, sha256, sourceKind, provenance, createdAt, updatedAt string
	var versionNumber int
	var sizeBytes int64
	var trashedAt, versionCreatedAt sql.NullString
	err := row.Scan(&value.ID, &value.ProjectID, &value.Name, &value.Kind, &value.Status, &currentVersionID, &createdAt, &updatedAt, &trashedAt,
		&versionID, &blobID, &versionNumber, &fileName, &mimeType, &sizeBytes, &sha256, &sourceKind, &provenance, &versionCreatedAt)
	if err != nil {
		return value, err
	}
	value.CurrentVersionID = currentVersionID
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return value, err
	}
	if trashedAt.Valid {
		parsed, parseErr := parseTime(trashedAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.TrashedAt = &parsed
	}
	if versionID != "" {
		version = artifact.Version{ID: versionID, ArtifactID: value.ID, BlobID: blobID, VersionNumber: versionNumber, FileName: fileName, MIMEType: mimeType, SizeBytes: sizeBytes, SHA256: sha256, SourceKind: artifact.SourceKind(sourceKind), Lineage: []artifact.Lineage{}, Citations: []artifact.Citation{}, Exports: []artifact.Export{}}
		if err := json.Unmarshal([]byte(provenance), &version.Provenance); err != nil {
			return value, fmt.Errorf("decode Artifact provenance: %w", err)
		}
		version.CreatedAt, err = parseTime(versionCreatedAt.String)
		if err != nil {
			return value, err
		}
		value.CurrentVersion = &version
	}
	return value, nil
}

func scanArtifactVersion(row artifactRowScanner) (artifact.Version, artifact.BlobRecord, error) {
	var version artifact.Version
	var blob artifact.BlobRecord
	var sourceKey, provenance, versionCreatedAt, blobCreatedAt string
	err := row.Scan(&version.ID, &version.ArtifactID, &version.BlobID, &version.VersionNumber, &version.FileName, &version.MIMEType,
		&version.SizeBytes, &version.SHA256, &version.SourceKind, &sourceKey, &provenance, &versionCreatedAt,
		&blob.ID, &blob.ProjectID, &blob.SHA256, &blob.SizeBytes, &blob.MIMEType, &blob.StorageRelativePath, &blobCreatedAt)
	if err != nil {
		return version, blob, err
	}
	version.SourceKey = sourceKey
	if err := json.Unmarshal([]byte(provenance), &version.Provenance); err != nil {
		return version, blob, err
	}
	version.CreatedAt, err = parseTime(versionCreatedAt)
	if err != nil {
		return version, blob, err
	}
	blob.CreatedAt, err = parseTime(blobCreatedAt)
	return version, blob, err
}

func scanArtifactExport(row artifactRowScanner) (artifact.Export, artifact.BlobRecord, error) {
	var value artifact.Export
	var blob artifact.BlobRecord
	var createdAt, blobCreatedAt string
	err := row.Scan(&value.ID, &value.ProjectID, &value.ArtifactVersionID, &value.BlobID, &value.Format, &value.CitationStyle,
		&value.GeneratorVersion, &value.FileName, &value.MIMEType, &value.SizeBytes, &value.SHA256, &value.SourceSHA256, &createdAt,
		&blob.ID, &blob.ProjectID, &blob.SHA256, &blob.SizeBytes, &blob.MIMEType, &blob.StorageRelativePath, &blobCreatedAt)
	if err != nil {
		return value, blob, err
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return value, blob, err
	}
	blob.CreatedAt, err = parseTime(blobCreatedAt)
	return value, blob, err
}

func findArtifactExport(ctx context.Context, queryer artifactTxQueryer, versionID string, format artifact.ExportFormat, style artifact.CitationStyle, generator string) (artifact.Export, bool, error) {
	value, _, err := scanArtifactExport(queryer.QueryRowContext(ctx, artifactExportSelect+` WHERE e.artifact_version_id=? AND e.export_format=? AND e.citation_style=? AND e.generator_version=?`, versionID, format, style, generator))
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.Export{}, false, nil
	}
	if err != nil {
		return artifact.Export{}, false, err
	}
	return value, true, nil
}

type artifactTxQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadSaveResult(ctx context.Context, queryer artifactTxQueryer, projectID, versionID string, created bool) (artifact.SaveResult, error) {
	version, _, err := scanArtifactVersion(queryer.QueryRowContext(ctx, artifactVersionSelect+` WHERE a.project_id=? AND v.id=?`, projectID, versionID))
	if err != nil {
		return artifact.SaveResult{}, err
	}
	if err := loadVersionRelationsFrom(ctx, queryer, &version); err != nil {
		return artifact.SaveResult{}, err
	}
	value, err := scanArtifactCurrent(queryer.QueryRowContext(ctx, artifactCurrentSelect+` WHERE a.project_id=? AND a.id=?`, projectID, version.ArtifactID))
	if err != nil {
		return artifact.SaveResult{}, err
	}
	return artifact.SaveResult{Artifact: value, Version: version, Created: created}, nil
}

func loadVersionRelationsFrom(ctx context.Context, queryer artifactTxQueryer, version *artifact.Version) error {
	rows, err := queryer.QueryContext(ctx, `SELECT id,ordinal,relation_kind,source_id_snapshot,COALESCE(source_run_id,''),COALESCE(source_workflow_run_id,''),COALESCE(source_message_id,''),COALESCE(source_tool_call_id,''),COALESCE(source_artifact_version_id,''),label,metadata_json,created_at FROM artifact_lineage WHERE artifact_version_id=? ORDER BY ordinal`, version.ID)
	if err != nil {
		return err
	}
	version.Lineage = []artifact.Lineage{}
	for rows.Next() {
		var value artifact.Lineage
		var metadata, createdAt string
		value.ArtifactVersionID = version.ID
		if err := rows.Scan(&value.ID, &value.Ordinal, &value.RelationKind, &value.SourceIDSnapshot, &value.SourceRunID, &value.SourceWorkflowRunID, &value.SourceMessageID, &value.SourceToolCallID, &value.SourceArtifactVersionID, &value.Label, &metadata, &createdAt); err != nil {
			_ = rows.Close()
			return err
		}
		value.Metadata = json.RawMessage(metadata)
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			_ = rows.Close()
			return err
		}
		version.Lineage = append(version.Lineage, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	version.Citations, err = loadArtifactCitations(ctx, queryer, version.ID)
	if err != nil {
		return err
	}
	version.Exports, err = loadArtifactExports(ctx, queryer, version.ID)
	return err
}

const artifactCurrentSelect = `SELECT a.id,a.project_id,a.name,a.kind,a.status,COALESCE(a.current_version_id,''),a.created_at,a.updated_at,a.trashed_at,
	COALESCE(v.id,''),COALESCE(v.blob_id,''),COALESCE(v.version_number,0),COALESCE(v.file_name,''),COALESCE(v.mime_type,''),COALESCE(v.size_bytes,0),COALESCE(v.sha256,''),COALESCE(v.source_kind,''),COALESCE(v.provenance_json,'{}'),v.created_at
	FROM artifacts a LEFT JOIN artifact_versions v ON v.id=a.current_version_id`

const artifactVersionSelect = `SELECT v.id,v.artifact_id,v.blob_id,v.version_number,v.file_name,v.mime_type,v.size_bytes,v.sha256,v.source_kind,COALESCE(v.source_key,''),v.provenance_json,v.created_at,
	b.id,b.project_id,b.sha256,b.size_bytes,b.mime_type,b.storage_relative_path,b.created_at
	FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id JOIN artifact_blobs b ON b.id=v.blob_id`

const artifactExportSelect = `SELECT e.id,e.project_id,e.artifact_version_id,e.blob_id,e.export_format,e.citation_style,e.generator_version,e.file_name,e.mime_type,e.size_bytes,e.sha256,e.source_sha256,e.created_at,
	b.id,b.project_id,b.sha256,b.size_bytes,b.mime_type,b.storage_relative_path,b.created_at
	FROM artifact_exports e JOIN artifact_blobs b ON b.id=e.blob_id`

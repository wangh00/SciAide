package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/attachment"
)

type AttachmentRepository struct{ db *sql.DB }

func NewAttachmentRepository(db *sql.DB) *AttachmentRepository {
	return &AttachmentRepository{db: db}
}

func (r *AttachmentRepository) Create(ctx context.Context, value attachment.Attachment) error {
	metadata, err := json.Marshal(normalizeParseMetadata(value.ParseMetadata))
	if err != nil {
		return fmt.Errorf("encode attachment parse metadata: %w", err)
	}
	if value.ScopeKind == "" {
		value.ScopeKind = attachment.ScopeProjectShared
	}
	if value.SourceKind == "" {
		value.SourceKind = attachment.SourceUnknown
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO attachments(id,project_id,scope_kind,research_task_id,source_kind,original_name,mime_type,document_format,size_bytes,sha256,storage_relative_path,cache_relative_path,status,unit_count,extracted_runes,truncated,parse_metadata_json,error_message,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		value.ID, value.ProjectID, value.ScopeKind, value.ResearchTaskID, value.SourceKind, value.OriginalName, value.MIMEType, value.Format, value.SizeBytes, value.SHA256, value.StorageRelativePath, value.CacheRelativePath, value.Status, value.UnitCount, value.ExtractedRunes, value.Truncated, string(metadata), value.ErrorMessage, formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert attachment: %w", err)
	}
	return nil
}

func (r *AttachmentRepository) Get(ctx context.Context, id string) (attachment.Attachment, error) {
	return scanAttachment(r.db.QueryRowContext(ctx, attachmentSelect+` WHERE id=?`, id))
}

func (r *AttachmentRepository) FindByHash(ctx context.Context, projectID, sha256 string) (attachment.Attachment, bool, error) {
	value, err := scanAttachment(r.db.QueryRowContext(ctx, attachmentSelect+` WHERE project_id=? AND sha256=?`, projectID, sha256))
	if errors.Is(err, sql.ErrNoRows) {
		return attachment.Attachment{}, false, nil
	}
	return value, err == nil, err
}

func (r *AttachmentRepository) FindByHashInScope(ctx context.Context, projectID, sha256 string, scopeKind attachment.ScopeKind, researchTaskID string) (attachment.Attachment, bool, error) {
	value, err := scanAttachment(r.db.QueryRowContext(ctx, attachmentSelect+` WHERE project_id=? AND sha256=? AND scope_kind=? AND research_task_id=?`, projectID, sha256, scopeKind, researchTaskID))
	if errors.Is(err, sql.ErrNoRows) {
		return attachment.Attachment{}, false, nil
	}
	return value, err == nil, err
}

func (r *AttachmentRepository) ListByProject(ctx context.Context, projectID string) ([]attachment.Attachment, error) {
	rows, err := r.db.QueryContext(ctx, attachmentSelect+` WHERE project_id=? ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]attachment.Attachment, 0)
	for rows.Next() {
		value, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *AttachmentRepository) UpdateParse(ctx context.Context, value attachment.Attachment) error {
	metadata, err := json.Marshal(normalizeParseMetadata(value.ParseMetadata))
	if err != nil {
		return fmt.Errorf("encode attachment parse metadata: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE attachments SET status=?,unit_count=?,extracted_runes=?,truncated=?,parse_metadata_json=?,error_message=?,updated_at=? WHERE id=? AND project_id=?`,
		value.Status, value.UnitCount, value.ExtractedRunes, value.Truncated, string(metadata), value.ErrorMessage, formatTime(value.UpdatedAt), value.ID, value.ProjectID)
	if err != nil {
		return fmt.Errorf("update attachment parse state: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("attachment not found")
	}
	return nil
}

const attachmentSelect = `SELECT id,project_id,scope_kind,research_task_id,COALESCE(source_kind,'unknown'),original_name,mime_type,document_format,size_bytes,sha256,storage_relative_path,cache_relative_path,status,unit_count,extracted_runes,truncated,parse_metadata_json,error_message,created_at,updated_at FROM attachments`

func scanAttachment(row rowScanner) (attachment.Attachment, error) {
	var value attachment.Attachment
	var createdAt, updatedAt, metadataJSON string
	if err := row.Scan(&value.ID, &value.ProjectID, &value.ScopeKind, &value.ResearchTaskID, &value.SourceKind, &value.OriginalName, &value.MIMEType, &value.Format, &value.SizeBytes, &value.SHA256, &value.StorageRelativePath, &value.CacheRelativePath, &value.Status, &value.UnitCount, &value.ExtractedRunes, &value.Truncated, &metadataJSON, &value.ErrorMessage, &createdAt, &updatedAt); err != nil {
		return attachment.Attachment{}, err
	}
	if err := json.Unmarshal([]byte(metadataJSON), &value.ParseMetadata); err != nil {
		return attachment.Attachment{}, fmt.Errorf("decode attachment parse metadata: %w", err)
	}
	value.ParseMetadata = normalizeParseMetadata(value.ParseMetadata)
	var err error
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return attachment.Attachment{}, err
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	return value, err
}

func normalizeParseMetadata(value map[string]string) map[string]string {
	if value == nil {
		return map[string]string{}
	}
	return value
}

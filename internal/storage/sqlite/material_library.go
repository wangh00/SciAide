package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/wangh00/SciAide/internal/app/attachment"
)

func (r *AttachmentRepository) MaterialContentKind(ctx context.Context, id string) (string, error) {
	var level string
	err := r.db.QueryRowContext(ctx, `SELECT evidence_level FROM research_bibliography_materials WHERE attachment_id=? ORDER BY updated_at DESC LIMIT 1`, id).Scan(&level)
	if errors.Is(err, sql.ErrNoRows) {
		return "research_material", nil
	}
	if err != nil {
		return "", err
	}
	if level == "metadata_abstract" {
		return "metadata_abstract", nil
	}
	if level == "full_text" {
		return "full_text", nil
	}
	return "research_material", nil
}

func (r *AttachmentRepository) MaterialMetadata(ctx context.Context, id string) (attachment.MaterialMetadata, error) {
	var v attachment.MaterialMetadata
	err := r.db.QueryRowContext(ctx, `SELECT title,notes,archived,collected,content_kind,origin_task_title FROM material_library WHERE attachment_id=?`, id).Scan(&v.Title, &v.Notes, &v.Archived, &v.Collected, &v.StoredContentKind, &v.OriginTaskTitle)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	return v, err
}
func (r *AttachmentRepository) SaveMaterialMetadata(ctx context.Context, id string, v attachment.MaterialMetadata) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO material_library(attachment_id,title,notes,archived,collected,content_kind,origin_task_title) VALUES(?,?,?,?,?,?,?) ON CONFLICT(attachment_id) DO UPDATE SET title=excluded.title,notes=excluded.notes,archived=excluded.archived,collected=excluded.collected,content_kind=excluded.content_kind,origin_task_title=excluded.origin_task_title`, id, v.Title, v.Notes, v.Archived, v.Collected, v.StoredContentKind, v.OriginTaskTitle)
	return err
}

func (r *AttachmentRepository) MaterialTitle(ctx context.Context, id string) (string, error) {
	var title string
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(json_extract(b.canonical_json,'$.title'),'') FROM research_bibliography_materials m JOIN research_bibliographies b ON b.id=m.bibliography_id WHERE m.attachment_id=? ORDER BY m.updated_at DESC LIMIT 1`, id).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return title, err
}

func (r *AttachmentRepository) CollectMaterial(ctx context.Context, source attachment.Attachment, meta attachment.MaterialMetadata, newID string) (attachment.Attachment, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return attachment.Attachment{}, err
	}
	defer tx.Rollback()
	var target string
	err = tx.QueryRowContext(ctx, `SELECT id FROM attachments WHERE project_id=? AND sha256=? AND scope_kind='project_shared' AND research_task_id=''`, source.ProjectID, source.SHA256).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		target = newID
		_, err = tx.ExecContext(ctx, `INSERT INTO attachments(id,project_id,scope_kind,research_task_id,source_kind,original_name,mime_type,document_format,size_bytes,sha256,storage_relative_path,cache_relative_path,status,unit_count,extracted_runes,truncated,parse_metadata_json,error_message,created_at,updated_at)
SELECT ?,project_id,'project_shared','',source_kind,original_name,mime_type,document_format,size_bytes,sha256,storage_relative_path,cache_relative_path,status,unit_count,extracted_runes,truncated,parse_metadata_json,error_message,created_at,updated_at FROM attachments WHERE id=? AND project_id=?`, target, source.ID, source.ProjectID)
	}
	if err != nil {
		return attachment.Attachment{}, err
	}
	if source.ResearchTaskID != "" {
		err = tx.QueryRowContext(ctx, `SELECT title FROM research_tasks WHERE id=? AND project_id=?`, source.ResearchTaskID, source.ProjectID).Scan(&meta.OriginTaskTitle)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return attachment.Attachment{}, err
		}
	}
	// Keep user-edited titles/notes when collecting the same bytes again.
	_, err = tx.ExecContext(ctx, `INSERT INTO material_library(attachment_id,title,notes,archived,collected,content_kind,origin_task_title) VALUES(?,?,?,0,1,?,?) ON CONFLICT(attachment_id) DO UPDATE SET archived=0,collected=1,content_kind=CASE WHEN material_library.content_kind='' THEN excluded.content_kind ELSE material_library.content_kind END`, target, meta.Title, meta.Notes, meta.StoredContentKind, meta.OriginTaskTitle)
	if err != nil {
		return attachment.Attachment{}, err
	}
	if err = tx.Commit(); err != nil {
		return attachment.Attachment{}, err
	}
	return r.Get(ctx, target)
}

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/researchtask"
)

type ResearchTaskRepository struct{ db *sql.DB }

func NewResearchTaskRepository(db *sql.DB) *ResearchTaskRepository {
	return &ResearchTaskRepository{db: db}
}

func (r *ResearchTaskRepository) Upsert(ctx context.Context, command researchtask.UpsertCommand) error {
	command.ID = strings.TrimSpace(command.ID)
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.Title = strings.TrimSpace(command.Title)
	command.ResearchQuestion = strings.TrimSpace(command.ResearchQuestion)
	if command.ID == "" || command.ProjectID == "" || command.Title == "" {
		return fmt.Errorf("research task id, project and title are required")
	}
	if command.OriginKind != researchtask.OriginAI && command.OriginKind != researchtask.OriginTemplate {
		return fmt.Errorf("research task origin is invalid")
	}
	if command.At.IsZero() {
		command.At = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO research_tasks(id,project_id,title,research_question,origin_kind,status,created_at,updated_at)
		VALUES (?,?,?,?,?,'active',?,?)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title,
			research_question=CASE WHEN trim(excluded.research_question)<>'' THEN excluded.research_question ELSE research_tasks.research_question END,
			origin_kind=excluded.origin_kind,
			updated_at=excluded.updated_at
		WHERE research_tasks.project_id=excluded.project_id
		  AND research_tasks.status<>'archived'`,
		command.ID, command.ProjectID, command.Title, command.ResearchQuestion, command.OriginKind, formatTime(command.At), formatTime(command.At))
	if err != nil {
		return fmt.Errorf("upsert research task: %w", err)
	}
	var projectID string
	var status researchtask.Status
	if err := r.db.QueryRowContext(ctx, `SELECT project_id,status FROM research_tasks WHERE id=?`, command.ID).Scan(&projectID, &status); err != nil {
		return fmt.Errorf("verify research task upsert: %w", err)
	}
	if projectID != command.ProjectID {
		return fmt.Errorf("research task belongs to another project")
	}
	if status == researchtask.StatusArchived {
		return fmt.Errorf("archived research task cannot be reopened")
	}
	return nil
}

func (r *ResearchTaskRepository) Exists(ctx context.Context, projectID, taskID string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_tasks WHERE project_id=? AND id=? AND status<>'archived'`, strings.TrimSpace(projectID), strings.TrimSpace(taskID)).Scan(&count)
	return count > 0, err
}

func (r *ResearchTaskRepository) ExistsIncludingArchived(ctx context.Context, projectID, taskID string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_tasks WHERE project_id=? AND id=?`, strings.TrimSpace(projectID), strings.TrimSpace(taskID)).Scan(&count)
	return count > 0, err
}

func (r *ResearchTaskRepository) List(ctx context.Context, projectID string, limit int) ([]researchtask.Task, error) {
	projectID = strings.TrimSpace(projectID)
	if limit < 1 || limit > 500 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT t.id,t.project_id,t.title,t.research_question,t.origin_kind,t.status,
		       t.latest_run_id,t.latest_run_status,
		       (SELECT COUNT(*) FROM attachments a WHERE a.project_id=t.project_id AND a.scope_kind='task' AND a.research_task_id=t.id),
		       (SELECT COUNT(*) FROM knowledge_documents d WHERE d.project_id=t.project_id AND d.scope_kind='task' AND d.research_task_id=t.id),
		       (SELECT COUNT(*) FROM artifacts a WHERE a.project_id=t.project_id AND a.scope_kind='task' AND a.research_task_id=t.id),
		       (SELECT COUNT(*) FROM research_evidence_entries e WHERE e.project_id=t.project_id AND e.research_task_id=t.id),
		       (SELECT COUNT(*) FROM research_bibliography_materials m WHERE m.project_id=t.project_id AND m.research_task_id=t.id),
		       t.created_at,t.updated_at,t.archived_at
		FROM research_tasks t WHERE t.project_id=?
		ORDER BY CASE t.status WHEN 'active' THEN 0 ELSE 1 END,t.updated_at DESC,t.id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list research tasks: %w", err)
	}
	defer rows.Close()
	values := make([]researchtask.Task, 0)
	for rows.Next() {
		var value researchtask.Task
		var created, updated string
		var archived sql.NullString
		if err := rows.Scan(&value.ID, &value.ProjectID, &value.Title, &value.ResearchQuestion, &value.OriginKind, &value.Status,
			&value.LatestRunID, &value.LatestRunStatus, &value.AttachmentCount, &value.KnowledgeCount, &value.ArtifactCount,
			&value.EvidenceCount, &value.BibliographyCount, &created, &updated, &archived); err != nil {
			return nil, err
		}
		var err error
		value.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		value.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, err
		}
		if archived.Valid && strings.TrimSpace(archived.String) != "" {
			parsed, parseErr := parseTime(archived.String)
			if parseErr != nil {
				return nil, parseErr
			}
			value.ArchivedAt = &parsed
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *ResearchTaskRepository) Archive(ctx context.Context, projectID, taskID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE research_tasks SET status='archived',archived_at=?,updated_at=? WHERE project_id=? AND id=?`, formatTime(at), formatTime(at), strings.TrimSpace(projectID), strings.TrimSpace(taskID))
	if err != nil {
		return fmt.Errorf("archive research task: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("research task not found")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM research_queries WHERE project_id=? AND id IN (SELECT query_id FROM research_query_origins WHERE task_id=?)`, strings.TrimSpace(projectID), strings.TrimSpace(taskID)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM research_candidate_reviews WHERE project_id=? AND task_id=?`, strings.TrimSpace(projectID), strings.TrimSpace(taskID)); err != nil {
		return err
	}
	return tx.Commit()
}

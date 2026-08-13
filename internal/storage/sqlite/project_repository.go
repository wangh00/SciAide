package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
)

type ProjectRepository struct {
	db *sql.DB
}

func NewProjectRepository(db *sql.DB) *ProjectRepository {
	return &ProjectRepository{db: db}
}

func (r *ProjectRepository) Create(ctx context.Context, value project.Project) error {
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO projects(id, name, description, workspace_path, workspace_kind, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.Name, value.Description, value.WorkspacePath, value.WorkspaceKind,
		value.CreatedAt.UTC().Format(time.RFC3339Nano),
		value.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

func (r *ProjectRepository) Get(ctx context.Context, projectID string) (project.Project, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, workspace_path, workspace_kind, created_at, updated_at
        FROM projects WHERE id = ?`, projectID)
	value, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return project.Project{}, fmt.Errorf("project not found")
	}
	return value, err
}

func (r *ProjectRepository) List(ctx context.Context) ([]project.Project, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, workspace_path, workspace_kind, created_at, updated_at
        FROM projects ORDER BY updated_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	values := make([]project.Project, 0)
	for rows.Next() {
		value, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return values, nil
}

func (r *ProjectRepository) UpdateWorkspace(ctx context.Context, projectID, path, kind string, updatedAt time.Time) error {
	result, err := r.db.ExecContext(ctx, `UPDATE projects SET workspace_path = ?, workspace_kind = ?, updated_at = ? WHERE id = ?`, path, kind, formatTime(updatedAt), projectID)
	if err != nil {
		return fmt.Errorf("update project workspace: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

func (r *ProjectRepository) Delete(ctx context.Context, projectID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project delete: %w", err)
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE conversation_id IN (SELECT id FROM conversations WHERE project_id = ?) AND status IN ('queued', 'running', 'waiting_approval')`, projectID).Scan(&active); err != nil {
		return fmt.Errorf("check active project runs: %w", err)
	}
	if active > 0 {
		return fmt.Errorf("project has an active chat run; stop it before removing the project")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_events WHERE aggregate_id IN (SELECT id FROM runs WHERE conversation_id IN (SELECT id FROM conversations WHERE project_id = ?))`, projectID); err != nil {
		return fmt.Errorf("delete project run events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE conversation_id IN (SELECT id FROM conversations WHERE project_id = ?)`, projectID); err != nil {
		return fmt.Errorf("delete project runs: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, projectID)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("project not found")
	}
	return tx.Commit()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProject(row rowScanner) (project.Project, error) {
	var value project.Project
	var createdAt, updatedAt string
	if err := row.Scan(&value.ID, &value.Name, &value.Description, &value.WorkspacePath, &value.WorkspaceKind, &createdAt, &updatedAt); err != nil {
		return project.Project{}, err
	}
	var err error
	value.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return project.Project{}, fmt.Errorf("parse project created_at: %w", err)
	}
	value.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return project.Project{}, fmt.Errorf("parse project updated_at: %w", err)
	}
	return value, nil
}

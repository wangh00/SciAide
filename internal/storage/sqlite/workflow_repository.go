package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

type WorkflowRepository struct{ db *sql.DB }

func NewWorkflowRepository(db *sql.DB) *WorkflowRepository { return &WorkflowRepository{db: db} }

func (r *WorkflowRepository) SaveVersion(ctx context.Context, record workflow.SaveRecord) (workflow.SaveResult, error) {
	value, version := record.Workflow, record.Version
	value.ID, value.ProjectID = strings.TrimSpace(value.ID), strings.TrimSpace(value.ProjectID)
	version.ID, version.WorkflowID = strings.TrimSpace(version.ID), strings.TrimSpace(version.WorkflowID)
	record.ExpectedCurrentVersionID = strings.TrimSpace(record.ExpectedCurrentVersionID)
	if value.ID == "" || value.ProjectID == "" || version.ID == "" || version.WorkflowID != value.ID {
		return workflow.SaveResult{}, fmt.Errorf("invalid Workflow version record")
	}
	if err := workflow.VerifyVersionSnapshot(version); err != nil {
		return workflow.SaveResult{}, err
	}
	definitionJSON, err := json.Marshal(version.Definition)
	if err != nil {
		return workflow.SaveResult{}, fmt.Errorf("encode Workflow definition: %w", err)
	}
	compilationJSON, err := json.Marshal(version.Compilation)
	if err != nil {
		return workflow.SaveResult{}, fmt.Errorf("encode Workflow compilation: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return workflow.SaveResult{}, fmt.Errorf("begin Workflow version: %w", err)
	}
	defer tx.Rollback()

	var persisted workflow.Workflow
	var createdAt, updatedAt string
	err = tx.QueryRowContext(ctx, workflowSelect+` WHERE w.id=?`, value.ID).Scan(
		&persisted.ID, &persisted.ProjectID, &persisted.Name, &persisted.Description,
		&persisted.CurrentVersionID, &persisted.Version, &createdAt, &updatedAt,
	)
	created := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if record.ExpectedCurrentVersionID != "" {
			return workflow.SaveResult{}, fmt.Errorf("Workflow no longer exists")
		}
		created = true
		persisted = value
		persisted.CurrentVersionID = ""
		persisted.Version = 1
		persisted.CreatedAt = value.CreatedAt.UTC()
		persisted.UpdatedAt = value.UpdatedAt.UTC()
		_, err = tx.ExecContext(ctx, `INSERT INTO workflows(id,project_id,name,description,current_version_id,version,created_at,updated_at) VALUES (?,?,?,?,NULL,1,?,?)`,
			persisted.ID, persisted.ProjectID, value.Name, value.Description, formatTime(persisted.CreatedAt), formatTime(persisted.UpdatedAt))
		if err != nil {
			return workflow.SaveResult{}, fmt.Errorf("insert Workflow: %w", err)
		}
	case err != nil:
		return workflow.SaveResult{}, fmt.Errorf("inspect Workflow: %w", err)
	default:
		persisted.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return workflow.SaveResult{}, err
		}
		persisted.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return workflow.SaveResult{}, err
		}
		if persisted.ProjectID != value.ProjectID {
			return workflow.SaveResult{}, fmt.Errorf("Workflow does not belong to the current project")
		}
		if record.ExpectedCurrentVersionID == "" || persisted.CurrentVersionID != record.ExpectedCurrentVersionID {
			return workflow.SaveResult{}, fmt.Errorf("Workflow changed since it was opened")
		}
		var currentHash string
		if err := tx.QueryRowContext(ctx, `SELECT definition_sha256 FROM workflow_versions WHERE id=? AND workflow_id=?`, persisted.CurrentVersionID, persisted.ID).Scan(&currentHash); err != nil {
			return workflow.SaveResult{}, fmt.Errorf("read current Workflow version: %w", err)
		}
		if currentHash == version.DefinitionSHA256 {
			current, loadErr := loadWorkflowVersion(ctx, tx, persisted.CurrentVersionID)
			if loadErr != nil {
				return workflow.SaveResult{}, loadErr
			}
			if err := tx.Commit(); err != nil {
				return workflow.SaveResult{}, err
			}
			return workflow.SaveResult{Workflow: persisted, Version: current, Created: false}, nil
		}
	}

	version.WorkflowID = persisted.ID
	version.Version = persisted.Version
	if !created {
		version.Version++
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_versions(id,workflow_id,version_number,definition_json,definition_sha256,compilation_json,compilation_sha256,created_at) VALUES (?,?,?,?,?,?,?,?)`,
		version.ID, version.WorkflowID, version.Version, string(definitionJSON), version.DefinitionSHA256,
		string(compilationJSON), version.CompilationSHA256, formatTime(version.CreatedAt))
	if err != nil {
		return workflow.SaveResult{}, fmt.Errorf("insert Workflow version: %w", err)
	}
	if created {
		result, updateErr := tx.ExecContext(ctx, `UPDATE workflows SET current_version_id=?,version=?,updated_at=? WHERE id=? AND current_version_id IS NULL AND version=1`,
			version.ID, version.Version, formatTime(value.UpdatedAt), persisted.ID)
		if updateErr != nil {
			return workflow.SaveResult{}, fmt.Errorf("publish Workflow version: %w", updateErr)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return workflow.SaveResult{}, fmt.Errorf("Workflow changed while its first version was saved")
		}
	} else {
		result, updateErr := tx.ExecContext(ctx, `UPDATE workflows SET name=?,description=?,current_version_id=?,version=?,updated_at=? WHERE id=? AND project_id=? AND current_version_id=? AND version=?`,
			value.Name, value.Description, version.ID, version.Version, formatTime(value.UpdatedAt), persisted.ID,
			persisted.ProjectID, record.ExpectedCurrentVersionID, persisted.Version)
		if updateErr != nil {
			return workflow.SaveResult{}, fmt.Errorf("publish Workflow version: %w", updateErr)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return workflow.SaveResult{}, fmt.Errorf("Workflow changed while its version was saved")
		}
	}
	persisted.Name = value.Name
	persisted.Description = value.Description
	persisted.CurrentVersionID = version.ID
	persisted.Version = version.Version
	persisted.UpdatedAt = value.UpdatedAt.UTC()
	if err := tx.Commit(); err != nil {
		return workflow.SaveResult{}, fmt.Errorf("commit Workflow version: %w", err)
	}
	return workflow.SaveResult{Workflow: persisted, Version: version, Created: created}, nil
}

func (r *WorkflowRepository) List(ctx context.Context, projectID string) ([]workflow.Workflow, error) {
	rows, err := r.db.QueryContext(ctx, workflowSelect+` WHERE w.project_id=? ORDER BY w.updated_at DESC,w.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list Workflows: %w", err)
	}
	defer rows.Close()
	result := make([]workflow.Workflow, 0)
	for rows.Next() {
		value, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *WorkflowRepository) Get(ctx context.Context, projectID, workflowID string) (workflow.Detail, error) {
	value, err := scanWorkflow(r.db.QueryRowContext(ctx, workflowSelect+` WHERE w.project_id=? AND w.id=?`, projectID, workflowID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workflow.Detail{}, fmt.Errorf("Workflow not found")
		}
		return workflow.Detail{}, err
	}
	rows, err := r.db.QueryContext(ctx, workflowVersionSelect+` WHERE v.workflow_id=? ORDER BY v.version_number DESC`, workflowID)
	if err != nil {
		return workflow.Detail{}, fmt.Errorf("list Workflow versions: %w", err)
	}
	defer rows.Close()
	versions := make([]workflow.Version, 0)
	for rows.Next() {
		version, err := scanWorkflowVersion(rows)
		if err != nil {
			return workflow.Detail{}, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return workflow.Detail{}, err
	}
	return workflow.Detail{Workflow: value, Versions: versions}, nil
}

func (r *WorkflowRepository) Delete(ctx context.Context, projectID, workflowID string) error {
	projectID, workflowID = strings.TrimSpace(projectID), strings.TrimSpace(workflowID)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Workflow deletion: %w", err)
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs WHERE project_id=? AND workflow_id=? AND status IN ('queued','running','waiting_approval','waiting_human_confirmation','paused')`, projectID, workflowID).Scan(&active); err != nil {
		return fmt.Errorf("inspect Workflow Runs: %w", err)
	}
	if active > 0 {
		return fmt.Errorf("研究方案仍有未结束的科研任务；请先取消任务再删除")
	}
	// Delete terminal Runs first. workflow_versions is ON DELETE RESTRICT from
	// Runs, so relying on sibling cascades from workflows is order-dependent.
	if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_runs WHERE project_id=? AND workflow_id=?`, projectID, workflowID); err != nil {
		return fmt.Errorf("delete Workflow Run history: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM workflows WHERE project_id=? AND id=?`, projectID, workflowID)
	if err != nil {
		return fmt.Errorf("delete Workflow: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("Workflow not found")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Workflow deletion: %w", err)
	}
	return nil
}

const workflowSelect = `SELECT w.id,w.project_id,w.name,w.description,COALESCE(w.current_version_id,''),w.version,w.created_at,w.updated_at FROM workflows w`
const workflowVersionSelect = `SELECT v.id,v.workflow_id,v.version_number,v.definition_json,v.definition_sha256,v.compilation_json,v.compilation_sha256,v.created_at FROM workflow_versions v`

type workflowScanner interface{ Scan(...any) error }

func scanWorkflow(scanner workflowScanner) (workflow.Workflow, error) {
	var value workflow.Workflow
	var createdAt, updatedAt string
	if err := scanner.Scan(&value.ID, &value.ProjectID, &value.Name, &value.Description, &value.CurrentVersionID, &value.Version, &createdAt, &updatedAt); err != nil {
		return workflow.Workflow{}, err
	}
	var err error
	if value.CreatedAt, err = parseTime(createdAt); err != nil {
		return workflow.Workflow{}, err
	}
	if value.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return workflow.Workflow{}, err
	}
	return value, nil
}

func scanWorkflowVersion(scanner workflowScanner) (workflow.Version, error) {
	var value workflow.Version
	var definitionJSON, compilationJSON, createdAt string
	if err := scanner.Scan(&value.ID, &value.WorkflowID, &value.Version, &definitionJSON, &value.DefinitionSHA256, &compilationJSON, &value.CompilationSHA256, &createdAt); err != nil {
		return workflow.Version{}, err
	}
	if err := json.Unmarshal([]byte(definitionJSON), &value.Definition); err != nil {
		return workflow.Version{}, fmt.Errorf("decode Workflow definition: %w", err)
	}
	if err := json.Unmarshal([]byte(compilationJSON), &value.Compilation); err != nil {
		return workflow.Version{}, fmt.Errorf("decode Workflow compilation: %w", err)
	}
	var err error
	if value.CreatedAt, err = parseTime(createdAt); err != nil {
		return workflow.Version{}, err
	}
	if err := workflow.VerifyVersionSnapshot(value); err != nil {
		return workflow.Version{}, fmt.Errorf("persisted Workflow snapshot is invalid: %w", err)
	}
	return value, nil
}

func loadWorkflowVersion(ctx context.Context, tx *sql.Tx, versionID string) (workflow.Version, error) {
	value, err := scanWorkflowVersion(tx.QueryRowContext(ctx, workflowVersionSelect+` WHERE v.id=?`, versionID))
	if err != nil {
		return workflow.Version{}, fmt.Errorf("load Workflow version: %w", err)
	}
	return value, nil
}

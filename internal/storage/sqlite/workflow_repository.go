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
	if value.Purpose == "" {
		value.Purpose = workflow.PurposeUserPlan
	}
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
		&persisted.ID, &persisted.ProjectID, &persisted.Purpose, &persisted.Name, &persisted.Description,
		&persisted.CurrentVersionID, &persisted.CurrentDefinitionSHA256, &persisted.Version, &createdAt, &updatedAt,
	)
	created := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if record.ExpectedCurrentVersionID != "" {
			return workflow.SaveResult{}, fmt.Errorf("Workflow no longer exists")
		}
		// Reuse the plan identity for an unchanged template, but only reuse
		// its version when the compiled tool contracts are unchanged too.
		var existingID, existingVersionID string
		duplicateErr := tx.QueryRowContext(ctx, `
			SELECT w.id,w.current_version_id
			FROM workflows w
			JOIN workflow_versions v ON v.id=w.current_version_id
			WHERE w.project_id=? AND w.purpose=? AND v.definition_sha256=?
			ORDER BY w.updated_at DESC,w.id
			LIMIT 1`, value.ProjectID, value.Purpose, version.DefinitionSHA256).Scan(&existingID, &existingVersionID)
		if duplicateErr == nil {
			existing, loadErr := scanWorkflow(tx.QueryRowContext(ctx, workflowSelect+` WHERE w.project_id=? AND w.id=?`, value.ProjectID, existingID))
			if loadErr != nil {
				return workflow.SaveResult{}, loadErr
			}
			current, loadErr := loadWorkflowVersion(ctx, tx, existingVersionID)
			if loadErr != nil {
				return workflow.SaveResult{}, loadErr
			}
			if current.CompilationSHA256 == version.CompilationSHA256 {
				if err := tx.Commit(); err != nil {
					return workflow.SaveResult{}, err
				}
				return workflow.SaveResult{Workflow: existing, Version: current, Created: false}, nil
			}
			persisted = existing
			record.ExpectedCurrentVersionID = existing.CurrentVersionID
			break
		}
		if !errors.Is(duplicateErr, sql.ErrNoRows) {
			return workflow.SaveResult{}, fmt.Errorf("inspect duplicate Workflow definition: %w", duplicateErr)
		}
		created = true
		persisted = value
		persisted.CurrentVersionID = ""
		persisted.Version = 1
		persisted.CreatedAt = value.CreatedAt.UTC()
		persisted.UpdatedAt = value.UpdatedAt.UTC()
		_, err = tx.ExecContext(ctx, `INSERT INTO workflows(id,project_id,purpose,name,description,current_version_id,version,created_at,updated_at) VALUES (?,?,?,?,?,NULL,1,?,?)`,
			persisted.ID, persisted.ProjectID, persisted.Purpose, value.Name, value.Description, formatTime(persisted.CreatedAt), formatTime(persisted.UpdatedAt))
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
		if persisted.Purpose != value.Purpose {
			return workflow.SaveResult{}, fmt.Errorf("Workflow purpose cannot be changed")
		}
		if record.ExpectedCurrentVersionID == "" || persisted.CurrentVersionID != record.ExpectedCurrentVersionID {
			return workflow.SaveResult{}, fmt.Errorf("Workflow changed since it was opened")
		}
		var currentHash, currentCompilationHash string
		if err := tx.QueryRowContext(ctx, `SELECT definition_sha256,compilation_sha256 FROM workflow_versions WHERE id=? AND workflow_id=?`, persisted.CurrentVersionID, persisted.ID).Scan(&currentHash, &currentCompilationHash); err != nil {
			return workflow.SaveResult{}, fmt.Errorf("read current Workflow version: %w", err)
		}
		if currentHash == version.DefinitionSHA256 && currentCompilationHash == version.CompilationSHA256 {
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
	persisted.CurrentDefinitionSHA256 = version.DefinitionSHA256
	persisted.Version = version.Version
	persisted.UpdatedAt = value.UpdatedAt.UTC()
	if err := tx.Commit(); err != nil {
		return workflow.SaveResult{}, fmt.Errorf("commit Workflow version: %w", err)
	}
	return workflow.SaveResult{Workflow: persisted, Version: version, Created: created}, nil
}

func (r *WorkflowRepository) List(ctx context.Context, projectID string) ([]workflow.Workflow, error) {
	rows, err := r.db.QueryContext(ctx, workflowSelect+` WHERE w.project_id=? AND w.purpose='user_plan' ORDER BY w.updated_at DESC,w.id`, projectID)
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

// DeleteUnstarted removes one exact Workflow version only when it is still the
// current version and no runtime has been created for the Workflow. This is a
// compensating action for research-route adoption: SaveVersion and runtime
// creation are separate repositories/transactions, so a failed launch must
// not leave a ghost plan behind. It deliberately does not apply the broader
// equivalent-plan deletion semantics of Delete.
func (r *WorkflowRepository) DeleteUnstarted(ctx context.Context, projectID, workflowID, versionID string) error {
	projectID, workflowID, versionID = strings.TrimSpace(projectID), strings.TrimSpace(workflowID), strings.TrimSpace(versionID)
	if projectID == "" || workflowID == "" || versionID == "" {
		return fmt.Errorf("invalid unstarted Workflow rollback")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin unstarted Workflow rollback: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		DELETE FROM workflows
		WHERE project_id=? AND id=? AND current_version_id=?
		  AND NOT EXISTS (SELECT 1 FROM workflow_runs WHERE project_id=? AND workflow_id=?)`,
		projectID, workflowID, versionID, projectID, workflowID)
	if err != nil {
		return fmt.Errorf("delete unstarted Workflow: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return fmt.Errorf("Workflow rollback was not applied because it was changed or already used")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit unstarted Workflow rollback: %w", err)
	}
	return nil
}

func (r *WorkflowRepository) Delete(ctx context.Context, projectID, workflowID string) error {
	projectID, workflowID = strings.TrimSpace(projectID), strings.TrimSpace(workflowID)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Workflow deletion: %w", err)
	}
	defer tx.Rollback()
	var definitionSHA256 string
	var purpose workflow.Purpose
	if err := tx.QueryRowContext(ctx, `
		SELECT current.definition_sha256,w.purpose
		FROM workflows w
		JOIN workflow_versions current ON current.id=w.current_version_id
		WHERE w.project_id=? AND w.id=?`, projectID, workflowID).Scan(&definitionSHA256, &purpose); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("Workflow not found")
		}
		return fmt.Errorf("inspect Workflow definition: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT w.id
		FROM workflows w
		JOIN workflow_versions current ON current.id=w.current_version_id
		WHERE w.project_id=? AND w.purpose=? AND current.definition_sha256=?
		ORDER BY w.id`, projectID, purpose, definitionSHA256)
	if err != nil {
		return fmt.Errorf("list equivalent Workflows for deletion: %w", err)
	}
	workflowIDs := make([]string, 0, 1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read equivalent Workflow for deletion: %w", err)
		}
		workflowIDs = append(workflowIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("list equivalent Workflows for deletion: %w", err)
	}
	_ = rows.Close()
	if len(workflowIDs) == 0 {
		return fmt.Errorf("Workflow not found")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(workflowIDs)), ",")
	queryArgs := make([]any, 0, len(workflowIDs)+1)
	queryArgs = append(queryArgs, projectID)
	for _, id := range workflowIDs {
		queryArgs = append(queryArgs, id)
	}
	var active int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM workflow_runs WHERE project_id=? AND workflow_id IN (%s) AND status IN ('queued','running','waiting_approval','waiting_human_confirmation','paused')`, placeholders), queryArgs...).Scan(&active); err != nil {
		return fmt.Errorf("inspect Workflow Runs: %w", err)
	}
	if active > 0 {
		return fmt.Errorf("研究方案仍有未结束的科研任务；请先取消任务再删除")
	}
	var activeChats int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*)
		FROM runs r
		JOIN workflow_conversations wc ON wc.conversation_id=r.conversation_id
		JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
		WHERE wr.project_id=? AND wr.workflow_id IN (%s)
		  AND r.status IN ('queued','running','waiting_approval')`, placeholders), queryArgs...).Scan(&activeChats); err != nil {
		return fmt.Errorf("inspect research conversation runs: %w", err)
	}
	if activeChats > 0 {
		return fmt.Errorf("研究方案仍有正在运行的 AI 协作；请先停止生成再删除")
	}
	rows, err = tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT wc.conversation_id
		FROM workflow_conversations wc
		JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
		WHERE wr.project_id=? AND wr.workflow_id IN (%s)`, placeholders), queryArgs...)
	if err != nil {
		return fmt.Errorf("list research conversations for deletion: %w", err)
	}
	conversationIDs := make([]string, 0)
	for rows.Next() {
		var conversationID string
		if err := rows.Scan(&conversationID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read research conversation for deletion: %w", err)
		}
		conversationIDs = append(conversationIDs, conversationID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("list research conversations for deletion: %w", err)
	}
	_ = rows.Close()
	// Delete terminal Runs first. workflow_versions is ON DELETE RESTRICT from
	// Runs, so relying on sibling cascades from workflows is order-dependent.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM workflow_runs WHERE project_id=? AND workflow_id IN (%s)`, placeholders), queryArgs...); err != nil {
		return fmt.Errorf("delete Workflow Run history: %w", err)
	}
	for _, conversationID := range conversationIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id=?`, conversationID); err != nil {
			return fmt.Errorf("delete research conversation: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM workflows WHERE project_id=? AND id IN (%s)`, placeholders), queryArgs...)
	if err != nil {
		return fmt.Errorf("delete Workflow: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != int64(len(workflowIDs)) {
		return fmt.Errorf("delete equivalent Workflows: removed %d of %d", affected, len(workflowIDs))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Workflow deletion: %w", err)
	}
	return nil
}

const workflowSelect = `SELECT w.id,w.project_id,w.purpose,w.name,w.description,COALESCE(w.current_version_id,''),COALESCE(current.definition_sha256,''),w.version,w.created_at,w.updated_at FROM workflows w LEFT JOIN workflow_versions current ON current.id=w.current_version_id`
const workflowVersionSelect = `SELECT v.id,v.workflow_id,v.version_number,v.definition_json,v.definition_sha256,v.compilation_json,v.compilation_sha256,v.created_at FROM workflow_versions v`

type workflowScanner interface{ Scan(...any) error }

func scanWorkflow(scanner workflowScanner) (workflow.Workflow, error) {
	var value workflow.Workflow
	var createdAt, updatedAt string
	if err := scanner.Scan(&value.ID, &value.ProjectID, &value.Purpose, &value.Name, &value.Description, &value.CurrentVersionID, &value.CurrentDefinitionSHA256, &value.Version, &createdAt, &updatedAt); err != nil {
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

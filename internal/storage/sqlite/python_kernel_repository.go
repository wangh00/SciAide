package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type PythonKernelRepository struct{ db *sql.DB }

func NewPythonKernelRepository(db *sql.DB) *PythonKernelRepository {
	return &PythonKernelRepository{db: db}
}

func (r *PythonKernelRepository) SaveKernelExecution(ctx context.Context, value pythonenv.KernelExecutionAudit) error {
	input, err := json.Marshal(value.InputSHA256)
	if err != nil {
		return err
	}
	output, err := json.Marshal(value.OutputSHA256)
	if err != nil {
		return err
	}
	var runID, workflowRunID any
	var subjectKind tool.SubjectKind
	if scanErr := r.db.QueryRowContext(ctx, `SELECT CASE WHEN workflow_run_id IS NULL THEN 'chat_run' ELSE 'workflow_run' END FROM tool_calls WHERE id=?`, value.ToolCallID).Scan(&subjectKind); scanErr != nil {
		return fmt.Errorf("resolve Python Kernel execution subject: %w", scanErr)
	}
	if subjectKind == tool.SubjectWorkflowRun {
		workflowRunID = value.RunID
	} else {
		runID = value.RunID
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO python_kernel_executions(
		id,project_id,tool_call_id,run_id,workflow_run_id,environment_id,environment_fingerprint,kernel_id,execution_id,sequence,status,
		code_sha256,input_sha256_json,output_sha256_json,reproduction_sha256,stdout_truncated,stderr_truncated,exception_type,error_message,started_at,completed_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.ProjectID, value.ToolCallID, runID, workflowRunID, value.EnvironmentID,
		value.EnvironmentFingerprint, value.KernelID, value.ExecutionID, value.Sequence, value.Status, value.CodeSHA256,
		string(input), string(output), value.ReproductionSHA256, value.StdoutTruncated, value.StderrTruncated, value.ExceptionType, value.ErrorMessage,
		formatTime(value.StartedAt), formatTime(value.CompletedAt))
	if err != nil {
		return fmt.Errorf("insert Python Kernel execution audit: %w", err)
	}
	return nil
}

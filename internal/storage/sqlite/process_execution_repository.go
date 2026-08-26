package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/platform/localexec"
)

type ProcessExecutionRepository struct{ db *sql.DB }

func NewProcessExecutionRepository(db *sql.DB) *ProcessExecutionRepository {
	return &ProcessExecutionRepository{db: db}
}

func (r *ProcessExecutionRepository) PrepareProcessExecution(ctx context.Context, audit localexec.Audit, at time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("process execution repository is not configured")
	}
	environment, err := json.Marshal(audit.EnvironmentNames)
	if err != nil {
		return err
	}
	var runID, workflowRunID any
	var subjectKind tool.SubjectKind
	if scanErr := r.db.QueryRowContext(ctx, `SELECT CASE WHEN workflow_run_id IS NULL THEN 'chat_run' ELSE 'workflow_run' END FROM tool_calls WHERE id=?`, strings.TrimSpace(audit.CallID)).Scan(&subjectKind); scanErr != nil {
		return fmt.Errorf("resolve process execution subject: %w", scanErr)
	}
	if subjectKind == tool.SubjectWorkflowRun {
		workflowRunID = strings.TrimSpace(audit.RunID)
	} else {
		runID = strings.TrimSpace(audit.RunID)
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO process_execution_audits(
		tool_call_id,run_id,workflow_run_id,project_id,tool_name,executable_path,executable_version,executable_sha256,script_path,script_sha256,command_sha256,
		workdir,timeout_millis,environment_names_json,state,prepared_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		strings.TrimSpace(audit.CallID), runID, workflowRunID, strings.TrimSpace(audit.ProjectID), strings.TrimSpace(audit.ToolName),
		audit.ExecutablePath, audit.ExecutableVersion, audit.ExecutableSHA256, audit.ScriptPath, audit.ScriptSHA256, audit.CommandSHA256,
		audit.Workdir, audit.TimeoutMillis, string(environment), "prepared", formatTime(at))
	if err != nil {
		return fmt.Errorf("insert process execution audit: %w", err)
	}
	return nil
}

func (r *ProcessExecutionRepository) MarkProcessExecutionStarted(ctx context.Context, callID string, pid int, at time.Time) error {
	result, err := r.db.ExecContext(ctx, `UPDATE process_execution_audits SET process_id=?,state='running',started_at=? WHERE tool_call_id=? AND state='prepared'`, pid, formatTime(at), strings.TrimSpace(callID))
	if err != nil {
		return fmt.Errorf("mark process execution started: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("process execution audit start transition conflict")
	}
	return nil
}

func (r *ProcessExecutionRepository) FinishProcessExecution(ctx context.Context, callID string, outcome localexec.AuditOutcome) error {
	state := "failed"
	if outcome.Reason == localexec.ReasonCompleted {
		state = "completed"
	}
	result, err := r.db.ExecContext(ctx, `UPDATE process_execution_audits SET
        process_id=COALESCE(process_id,?),state=?,termination_reason=?,exit_code=?,stdout_bytes=?,stdout_sha256=?,stdout_truncated=?,
        stderr_bytes=?,stderr_sha256=?,stderr_truncated=?,error_message=?,completed_at=?
        WHERE tool_call_id=? AND state IN ('prepared','running')`,
		nullablePositiveInt(outcome.PID), state, outcome.Reason, outcome.ExitCode, outcome.StdoutBytes, outcome.StdoutSHA256, outcome.StdoutTruncated,
		outcome.StderrBytes, outcome.StderrSHA256, outcome.StderrTruncated, outcome.ErrorMessage, formatTime(outcome.CompletedAt), strings.TrimSpace(callID))
	if err != nil {
		return fmt.Errorf("finish process execution audit: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("process execution audit finish transition conflict")
	}
	return nil
}

func (r *ProcessExecutionRepository) InterruptActive(ctx context.Context, at time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE process_execution_audits SET state='failed',termination_reason='app_shutdown',error_message='应用退出或重启时本地进程执行尚未完成',completed_at=? WHERE state IN ('prepared','running')`, formatTime(at))
	if err != nil {
		return 0, fmt.Errorf("interrupt active process execution audits: %w", err)
	}
	return result.RowsAffected()
}

func nullablePositiveInt(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

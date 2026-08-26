package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

type WorkflowRuntimeRepository struct{ db *sql.DB }

func NewWorkflowRuntimeRepository(db *sql.DB) *WorkflowRuntimeRepository {
	return &WorkflowRuntimeRepository{db: db}
}

func (r *WorkflowRuntimeRepository) CreateRun(ctx context.Context, run workflow.Run, steps []workflow.Step, event workflow.RuntimeEvent) error {
	compilation, err := json.Marshal(run.Compilation)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_runs(
		id,project_id,workflow_id,workflow_version_id,status,permission_mode,inputs_json,inputs_sha256,compilation_json,compilation_sha256,
		outputs_json,current_step_ordinal,error_code,error_message,cancel_requested,resume_status,created_at,started_at,completed_at,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, run.ID, run.ProjectID, run.WorkflowID, run.WorkflowVersionID, run.Status, run.PermissionMode,
		string(run.Inputs), run.InputsSHA256, string(compilation), run.CompilationSHA256, string(run.Outputs), run.CurrentStep,
		run.ErrorCode, run.ErrorMessage, run.CancelRequested, run.ResumeStatus, formatTime(run.CreatedAt), nullableTime(run.StartedAt), nullableTime(run.CompletedAt), formatTime(run.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert Workflow Run: %w", err)
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_steps(
			id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,tool_call_id,idempotency_key,
			error_code,error_message,started_at,completed_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, step.ID, step.WorkflowRunID, step.NodeID, step.Ordinal, step.NodeKind, step.Status,
			step.Attempt, string(step.Input), step.InputSHA256, string(step.Output), nullableString(step.ToolCallID), step.IdempotencyKey,
			step.ErrorCode, step.ErrorMessage, nullableTime(step.StartedAt), nullableTime(step.CompletedAt), formatTime(step.UpdatedAt)); err != nil {
			return fmt.Errorf("insert Workflow step: %w", err)
		}
	}
	if err := appendWorkflowEvent(ctx, tx, &event); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *WorkflowRuntimeRepository) GetRun(ctx context.Context, projectID, runID string) (workflow.RunDetail, error) {
	run, err := scanWorkflowRun(r.db.QueryRowContext(ctx, workflowRunSelect+` WHERE wr.project_id=? AND wr.id=?`, strings.TrimSpace(projectID), strings.TrimSpace(runID)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workflow.RunDetail{}, fmt.Errorf("Workflow Run not found")
		}
		return workflow.RunDetail{}, err
	}
	steps, err := r.listSteps(ctx, run.ID)
	if err != nil {
		return workflow.RunDetail{}, err
	}
	events, err := r.listEvents(ctx, run.ID)
	if err != nil {
		return workflow.RunDetail{}, err
	}
	return workflow.RunDetail{Run: run, Steps: steps, Events: events, PendingApprovals: []permission.Approval{}}, nil
}

func (r *WorkflowRuntimeRepository) ListRuns(ctx context.Context, projectID, workflowID string, limit int) ([]workflow.Run, error) {
	query := workflowRunSelect + ` WHERE wr.project_id=?`
	args := []any{strings.TrimSpace(projectID)}
	if workflowID = strings.TrimSpace(workflowID); workflowID != "" {
		query += ` AND wr.workflow_id=?`
		args = append(args, workflowID)
	}
	query += ` ORDER BY wr.created_at DESC,wr.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []workflow.Run{}
	for rows.Next() {
		value, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *WorkflowRuntimeRepository) MarkRunStarted(ctx context.Context, runID string, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status='running',started_at=COALESCE(started_at,?),updated_at=? WHERE id=? AND status='queued' AND cancel_requested=0`, formatTime(at), formatTime(at), runID)
		return expectOne(result, err, "start Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) BeginStep(ctx context.Context, runID, stepID string, input json.RawMessage, inputSHA256, idempotencyKey string, attempt int, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='running',attempt=?,input_json=?,input_sha256=?,output_json='{}',tool_call_id=NULL,idempotency_key=?,error_code='',error_message='',started_at=?,completed_at=NULL,updated_at=? WHERE id=? AND workflow_run_id=? AND status='queued'`, attempt, string(input), inputSHA256, idempotencyKey, formatTime(at), formatTime(at), stepID, runID)
		return expectOne(result, err, "start Workflow step")
	}, event)
}

func (r *WorkflowRuntimeRepository) AttachToolCall(ctx context.Context, runID, stepID, callID string, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET tool_call_id=?,updated_at=? WHERE id=? AND workflow_run_id=? AND status='running' AND tool_call_id IS NULL`, callID, formatTime(at), stepID, runID)
		return expectOne(result, err, "attach Workflow ToolCall")
	}, event)
}

func (r *WorkflowRuntimeRepository) WaitStep(ctx context.Context, runID, stepID string, stepStatus workflow.StepStatus, runStatus workflow.RunStatus, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status=?,updated_at=? WHERE id=? AND workflow_run_id=? AND status='running'`, stepStatus, formatTime(at), stepID, runID)
		if err := expectOne(result, err, "wait Workflow step"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status=?,updated_at=? WHERE id=? AND status='running' AND cancel_requested=0`, runStatus, formatTime(at), runID)
		return expectOne(result, err, "wait Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) ResumeApprovalStep(ctx context.Context, runID, stepID string, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='running',updated_at=? WHERE id=? AND workflow_run_id=? AND status='waiting_approval'`, formatTime(at), stepID, runID)
		if err := expectOne(result, err, "resume approved Workflow step"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='queued',resume_status='',updated_at=? WHERE id=? AND status='waiting_approval' AND cancel_requested=0`, formatTime(at), runID)
		return expectOne(result, err, "resume approved Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) CompleteStep(ctx context.Context, runID, stepID string, output json.RawMessage, nextOrdinal int, final bool, finalOutputs json.RawMessage, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='completed',output_json=?,error_code='',error_message='',completed_at=?,updated_at=? WHERE id=? AND workflow_run_id=? AND status IN ('running','waiting_human_confirmation')`, string(output), formatTime(at), formatTime(at), stepID, runID)
		if err := expectOne(result, err, "complete Workflow step"); err != nil {
			return err
		}
		if !final {
			result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='queued',current_step_ordinal=?,updated_at=? WHERE id=? AND status IN ('running','waiting_human_confirmation') AND cancel_requested=0`, nextOrdinal, formatTime(at), runID)
			return expectOne(result, err, "advance Workflow Run")
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='completed',outputs_json=?,current_step_ordinal=?,completed_at=?,updated_at=? WHERE id=? AND status IN ('running','waiting_human_confirmation') AND cancel_requested=0`, string(finalOutputs), nextOrdinal, formatTime(at), formatTime(at), runID)
		return expectOne(result, err, "complete Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) FailStep(ctx context.Context, runID, stepID string, stepStatus workflow.StepStatus, runStatus workflow.RunStatus, code, message string, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status=?,error_code=?,error_message=?,completed_at=?,updated_at=? WHERE id=? AND workflow_run_id=? AND status NOT IN ('completed','failed','cancelled','interrupted','outcome_unknown')`, stepStatus, code, message, formatTime(at), formatTime(at), stepID, runID)
		if err := expectOne(result, err, "fail Workflow step"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status=?,error_code=?,error_message=?,completed_at=?,updated_at=? WHERE id=? AND status NOT IN ('completed','failed','cancelled','interrupted')`, runStatus, code, message, formatTime(at), formatTime(at), runID)
		return expectOne(result, err, "fail Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) FailBlockedStep(ctx context.Context, runID, stepID, code, message string, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		approvals, err := loadPendingWorkflowApprovals(ctx, tx, runID)
		if err != nil {
			return err
		}
		calls, err := loadActiveWorkflowToolCalls(ctx, tx, runID)
		if err != nil {
			return err
		}
		for _, approval := range approvals {
			result, err := tx.ExecContext(ctx, `UPDATE approvals SET status='expired',resolved_scope='call',resolved_at=? WHERE id=? AND status='pending'`, formatTime(at), approval.ID)
			if err := expectOne(result, err, "expire invalid-input approval"); err != nil {
				return err
			}
			approval.Status, approval.ResolvedScope, approval.ResolvedAt = permission.ApprovalExpired, permission.ScopeCall, &at
			if err := appendWorkflowAggregateEvent(ctx, tx, runID, "approval.expired", map[string]any{"approval": approval, "grant": nil}, at); err != nil {
				return err
			}
		}
		for _, call := range calls {
			callStatus := tool.CallInterrupted
			callMessage := "Workflow 执行前置校验失败，工具不会继续执行"
			eventType := "tool.interrupted"
			if code == "TOOL_PERMISSION_DENIED" {
				callStatus, callMessage, eventType = tool.CallDenied, "用户拒绝了 Workflow 工具权限", "tool.denied"
			}
			if err := transitionToolCall(ctx, tx, call.ID, call.Status, callStatus, code, callMessage, at); err != nil {
				return err
			}
			call.Status, call.ErrorCode, call.ErrorMessage, call.CompletedAt, call.UpdatedAt = callStatus, code, callMessage, &at, at
			if err := appendWorkflowAggregateEvent(ctx, tx, runID, eventType, map[string]any{"toolCall": call}, at); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='failed',error_code=?,error_message=?,completed_at=?,updated_at=? WHERE id=? AND workflow_run_id=? AND status NOT IN ('completed','failed','cancelled','interrupted','outcome_unknown')`, code, message, formatTime(at), formatTime(at), stepID, runID)
		if err := expectOne(result, err, "fail invalid-input Workflow step"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='failed',error_code=?,error_message=?,completed_at=?,updated_at=? WHERE id=? AND status NOT IN ('completed','failed','cancelled','interrupted')`, code, message, formatTime(at), formatTime(at), runID)
		return expectOne(result, err, "fail invalid-input Workflow Run")
	}, event)
}

func loadPendingWorkflowApprovals(ctx context.Context, tx *sql.Tx, runID string) ([]permission.Approval, error) {
	rows, err := tx.QueryContext(ctx, approvalSelect+` WHERE workflow_run_id=? AND status='pending' ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []permission.Approval{}
	for rows.Next() {
		value, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadActiveWorkflowToolCalls(ctx context.Context, tx *sql.Tx, runID string) ([]tool.Call, error) {
	rows, err := tx.QueryContext(ctx, toolCallSelect+` WHERE workflow_run_id=? AND status IN ('pending','awaiting_approval','running') ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []tool.Call{}
	for rows.Next() {
		value, err := scanToolCall(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func appendWorkflowAggregateEvent(ctx context.Context, tx *sql.Tx, runID, eventType string, payload any, at time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	event := events.New(eventID, runID, tool.SubjectWorkflowRun.AggregateType(), eventType, 0, encoded)
	event.Timestamp = at
	return appendNextEventTx(ctx, tx, &event)
}

func (r *WorkflowRuntimeRepository) SetRunStatus(ctx context.Context, runID string, expected []workflow.RunStatus, next, resume workflow.RunStatus, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		placeholders := make([]string, len(expected))
		args := []any{next, resume, formatTime(at), runID}
		for index, value := range expected {
			placeholders[index] = "?"
			args = append(args, value)
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status=?,resume_status=?,updated_at=? WHERE id=? AND status IN (`+strings.Join(placeholders, ",")+`)`, args...)
		return expectOne(result, err, "transition Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) RecordDecision(ctx context.Context, decision workflow.HumanDecision, output json.RawMessage, nextOrdinal int, final bool, finalOutputs json.RawMessage, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, decision.WorkflowRunID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_human_decisions(id,workflow_run_id,workflow_step_id,decision_kind,attempt,approved,note,context_json,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, decision.ID, decision.WorkflowRunID, decision.WorkflowStepID, decision.Kind, decision.Attempt, decision.Approved, decision.Note, string(decision.Context), formatTime(decision.CreatedAt)); err != nil {
			return fmt.Errorf("record Workflow human decision: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='completed',output_json=?,completed_at=?,updated_at=? WHERE id=? AND workflow_run_id=? AND status='waiting_human_confirmation'`, string(output), formatTime(at), formatTime(at), decision.WorkflowStepID, decision.WorkflowRunID)
		if err := expectOne(result, err, "complete human Workflow step"); err != nil {
			return err
		}
		if !final {
			result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='queued',current_step_ordinal=?,updated_at=? WHERE id=? AND status='waiting_human_confirmation'`, nextOrdinal, formatTime(at), decision.WorkflowRunID)
			return expectOne(result, err, "advance human Workflow Run")
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='completed',outputs_json=?,current_step_ordinal=?,completed_at=?,updated_at=? WHERE id=? AND status='waiting_human_confirmation'`, string(finalOutputs), nextOrdinal, formatTime(at), formatTime(at), decision.WorkflowRunID)
		return expectOne(result, err, "complete human Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) RejectDecision(ctx context.Context, decision workflow.HumanDecision, stepStatus workflow.StepStatus, runStatus workflow.RunStatus, code, message string, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, decision.WorkflowRunID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_human_decisions(id,workflow_run_id,workflow_step_id,decision_kind,attempt,approved,note,context_json,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, decision.ID, decision.WorkflowRunID, decision.WorkflowStepID, decision.Kind, decision.Attempt, decision.Approved, decision.Note, string(decision.Context), formatTime(decision.CreatedAt)); err != nil {
			return fmt.Errorf("record rejected Workflow decision: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status=?,error_code=?,error_message=?,completed_at=?,updated_at=? WHERE id=? AND workflow_run_id=? AND status='waiting_human_confirmation'`, stepStatus, code, message, formatTime(at), formatTime(at), decision.WorkflowStepID, decision.WorkflowRunID)
		if err := expectOne(result, err, "reject human Workflow step"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status=?,error_code=?,error_message=?,completed_at=?,updated_at=? WHERE id=? AND status='waiting_human_confirmation'`, runStatus, code, message, formatTime(at), formatTime(at), decision.WorkflowRunID)
		return expectOne(result, err, "reject human Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) ResetStepForRetry(ctx context.Context, runID, stepID string, decision *workflow.HumanDecision, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		if decision != nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_human_decisions(id,workflow_run_id,workflow_step_id,decision_kind,attempt,approved,note,context_json,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, decision.ID, decision.WorkflowRunID, decision.WorkflowStepID, decision.Kind, decision.Attempt, decision.Approved, decision.Note, string(decision.Context), formatTime(decision.CreatedAt)); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=? WHERE id=? AND workflow_run_id=? AND status IN ('failed','interrupted','outcome_unknown','running','waiting_approval')`, formatTime(at), stepID, runID)
		if err := expectOne(result, err, "reset Workflow step"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='queued',error_code='',error_message='',completed_at=NULL,cancel_requested=0,resume_status='',updated_at=? WHERE id=? AND status IN ('failed','interrupted','running','waiting_approval','queued')`, formatTime(at), runID)
		return expectOne(result, err, "reset Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) RequestCancel(ctx context.Context, runID string, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		approvals, err := loadPendingWorkflowApprovals(ctx, tx, runID)
		if err != nil {
			return err
		}
		calls, err := loadActiveWorkflowToolCalls(ctx, tx, runID)
		if err != nil {
			return err
		}
		for _, approval := range approvals {
			result, err := tx.ExecContext(ctx, `UPDATE approvals SET status='expired',resolved_scope='call',resolved_at=? WHERE id=? AND status='pending'`, formatTime(at), approval.ID)
			if err := expectOne(result, err, "expire cancelled Workflow approval"); err != nil {
				return err
			}
			approval.Status, approval.ResolvedScope, approval.ResolvedAt = permission.ApprovalExpired, permission.ScopeCall, &at
			if err := appendWorkflowAggregateEvent(ctx, tx, runID, "approval.expired", map[string]any{"approval": approval, "grant": nil, "reason": "workflow_cancelled"}, at); err != nil {
				return err
			}
		}
		for _, call := range calls {
			if err := transitionToolCall(ctx, tx, call.ID, call.Status, tool.CallCancelled, "WORKFLOW_CANCELLED", "用户取消了 Workflow Run", at); err != nil {
				return err
			}
			call.Status, call.ErrorCode, call.ErrorMessage, call.CompletedAt, call.UpdatedAt = tool.CallCancelled, "WORKFLOW_CANCELLED", "用户取消了 Workflow Run", &at, at
			if err := appendWorkflowAggregateEvent(ctx, tx, runID, "tool.cancelled", map[string]any{"toolCall": call}, at); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='cancelled',error_code='WORKFLOW_CANCELLED',error_message='用户取消了 Workflow Run',completed_at=?,updated_at=? WHERE workflow_run_id=? AND status IN ('queued','running','waiting_approval','waiting_human_confirmation')`, formatTime(at), formatTime(at), runID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET status='cancelled',cancel_requested=1,error_code='WORKFLOW_CANCELLED',error_message='用户取消了 Workflow Run',completed_at=?,updated_at=? WHERE id=? AND status NOT IN ('completed','failed','cancelled','interrupted')`, formatTime(at), formatTime(at), runID)
		return expectOne(result, err, "cancel Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) RecoverableRuns(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM workflow_runs WHERE status IN ('queued','running','waiting_approval','waiting_human_confirmation','paused') ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *WorkflowRuntimeRepository) ProjectIDForWorkflowRun(ctx context.Context, runID string) (string, error) {
	var projectID string
	if err := r.db.QueryRowContext(ctx, `SELECT project_id FROM workflow_runs WHERE id=?`, strings.TrimSpace(runID)).Scan(&projectID); err != nil {
		return "", fmt.Errorf("Workflow Run not found")
	}
	return projectID, nil
}

func (r *WorkflowRuntimeRepository) ProjectIDForSubject(ctx context.Context, subjectKind tool.SubjectKind, subjectID string) (string, error) {
	if tool.NormalizeSubjectKind(subjectKind) != tool.SubjectWorkflowRun {
		return "", fmt.Errorf("unsupported Workflow subject kind")
	}
	return r.ProjectIDForWorkflowRun(ctx, subjectID)
}

func (r *WorkflowRuntimeRepository) listSteps(ctx context.Context, runID string) ([]workflow.Step, error) {
	rows, err := r.db.QueryContext(ctx, workflowStepSelect+` WHERE ws.workflow_run_id=? ORDER BY ws.ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []workflow.Step{}
	for rows.Next() {
		value, err := scanWorkflowStep(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *WorkflowRuntimeRepository) listEvents(ctx context.Context, runID string) ([]workflow.RuntimeEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,workflow_run_id,sequence,event_type,payload_json,created_at FROM workflow_events WHERE workflow_run_id=? ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []workflow.RuntimeEvent{}
	for rows.Next() {
		var value workflow.RuntimeEvent
		var payload, createdAt string
		if err := rows.Scan(&value.ID, &value.WorkflowRunID, &value.Sequence, &value.Type, &payload, &createdAt); err != nil {
			return nil, err
		}
		value.Payload = json.RawMessage(payload)
		value.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *WorkflowRuntimeRepository) transition(ctx context.Context, runID string, mutation func(*sql.Tx) error, event workflow.RuntimeEvent) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := mutation(tx); err != nil {
		return err
	}
	if err := appendWorkflowEvent(ctx, tx, &event); err != nil {
		return err
	}
	return tx.Commit()
}

func appendWorkflowEvent(ctx context.Context, tx *sql.Tx, event *workflow.RuntimeEvent) error {
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM workflow_events WHERE workflow_run_id=?`, event.WorkflowRunID).Scan(&event.Sequence); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_events(id,workflow_run_id,sequence,event_type,payload_json,created_at) VALUES (?,?,?,?,?,?)`, event.ID, event.WorkflowRunID, event.Sequence, event.Type, string(event.Payload), formatTime(event.CreatedAt))
	return err
}

func expectOne(result sql.Result, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("%s transition conflict", operation)
	}
	return nil
}

type workflowRuntimeScanner interface{ Scan(...any) error }

func scanWorkflowRun(scanner workflowRuntimeScanner) (workflow.Run, error) {
	var value workflow.Run
	var inputs, compilation, outputs, createdAt, updatedAt string
	var startedAt, completedAt sql.NullString
	if err := scanner.Scan(&value.ID, &value.ProjectID, &value.WorkflowID, &value.WorkflowVersionID, &value.Status, &value.PermissionMode, &inputs, &value.InputsSHA256, &compilation, &value.CompilationSHA256, &outputs, &value.CurrentStep, &value.ErrorCode, &value.ErrorMessage, &value.CancelRequested, &value.ResumeStatus, &createdAt, &startedAt, &completedAt, &updatedAt); err != nil {
		return value, err
	}
	value.Inputs, value.Outputs = json.RawMessage(inputs), json.RawMessage(outputs)
	if err := json.Unmarshal([]byte(compilation), &value.Compilation); err != nil {
		return value, err
	}
	var err error
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return value, err
	}
	if startedAt.Valid {
		parsed, parseErr := parseTime(startedAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.StartedAt = &parsed
	}
	if completedAt.Valid {
		parsed, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.CompletedAt = &parsed
	}
	return value, nil
}

func scanWorkflowStep(scanner workflowRuntimeScanner) (workflow.Step, error) {
	var value workflow.Step
	var input, output, updatedAt string
	var startedAt, completedAt sql.NullString
	if err := scanner.Scan(&value.ID, &value.WorkflowRunID, &value.NodeID, &value.Ordinal, &value.NodeKind, &value.Status, &value.Attempt, &input, &value.InputSHA256, &output, &value.ToolCallID, &value.IdempotencyKey, &value.ErrorCode, &value.ErrorMessage, &startedAt, &completedAt, &updatedAt); err != nil {
		return value, err
	}
	value.Input, value.Output = json.RawMessage(input), json.RawMessage(output)
	var err error
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return value, err
	}
	if startedAt.Valid {
		parsed, parseErr := parseTime(startedAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.StartedAt = &parsed
	}
	if completedAt.Valid {
		parsed, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.CompletedAt = &parsed
	}
	return value, nil
}

const workflowRunSelect = `SELECT wr.id,wr.project_id,wr.workflow_id,wr.workflow_version_id,wr.status,wr.permission_mode,wr.inputs_json,wr.inputs_sha256,wr.compilation_json,wr.compilation_sha256,wr.outputs_json,wr.current_step_ordinal,wr.error_code,wr.error_message,wr.cancel_requested,wr.resume_status,wr.created_at,wr.started_at,wr.completed_at,wr.updated_at FROM workflow_runs wr`
const workflowStepSelect = `SELECT ws.id,ws.workflow_run_id,ws.node_id,ws.ordinal,ws.node_kind,ws.status,ws.attempt,ws.input_json,ws.input_sha256,ws.output_json,COALESCE(ws.tool_call_id,''),ws.idempotency_key,ws.error_code,ws.error_message,ws.started_at,ws.completed_at,ws.updated_at FROM workflow_steps ws`

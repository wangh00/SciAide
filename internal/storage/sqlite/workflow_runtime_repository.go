package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

type WorkflowRuntimeRepository struct{ db *sql.DB }

func NewWorkflowRuntimeRepository(db *sql.DB) *WorkflowRuntimeRepository {
	return &WorkflowRuntimeRepository{db: db}
}

// Creation order identifies descendants even if an older attempt receives a
// later audit/status update. UI updated_at ordering is not a mutation guard.
func (r *WorkflowRuntimeRepository) GetLatestTaskRun(ctx context.Context, projectID, taskID string) (workflow.Run, error) {
	if strings.TrimSpace(taskID) == "" {
		return workflow.Run{}, fmt.Errorf("research task id is required")
	}
	return scanWorkflowRun(r.db.QueryRowContext(ctx, workflowRunSelect+` WHERE wr.project_id=? AND wr.research_task_id=? ORDER BY wr.created_at DESC,wr.id DESC LIMIT 1`, strings.TrimSpace(projectID), strings.TrimSpace(taskID)))
}

func (r *WorkflowRuntimeRepository) CreateResearchRun(ctx context.Context, run workflow.Run, steps []workflow.Step, event workflow.RuntimeEvent, researchConversation conversation.Conversation) error {
	compilation, err := json.Marshal(run.Compilation)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if researchConversation.ID == "" || researchConversation.ID != run.ConversationID || researchConversation.ProjectID != run.ProjectID || researchConversation.PermissionMode != run.PermissionMode {
		return fmt.Errorf("invalid research conversation binding")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO conversations(id,project_id,title,model_profile_id,model_id,permission_mode,reasoning_level,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		researchConversation.ID, researchConversation.ProjectID, researchConversation.Title, researchConversation.ModelProfileID, researchConversation.ModelID, researchConversation.PermissionMode, researchConversation.ReasoningLevel, formatTime(researchConversation.CreatedAt), formatTime(researchConversation.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert research conversation: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_runs(
		id,creation_key,research_task_id,project_id,workflow_id,workflow_version_id,workflow_name,workflow_purpose,status,permission_mode,inputs_json,inputs_sha256,compilation_json,compilation_sha256,
		outputs_json,current_step_ordinal,error_code,error_message,cancel_requested,resume_status,created_at,started_at,completed_at,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, run.ID, run.CreationKey, run.ResearchTaskID, run.ProjectID, run.WorkflowID, run.WorkflowVersionID, run.WorkflowName, run.WorkflowPurpose, run.Status, run.PermissionMode,
		string(run.Inputs), run.InputsSHA256, string(compilation), run.CompilationSHA256, string(run.Outputs), run.CurrentStep,
		run.ErrorCode, run.ErrorMessage, run.CancelRequested, run.ResumeStatus, formatTime(run.CreatedAt), nullableTime(run.StartedAt), nullableTime(run.CompletedAt), formatTime(run.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert Workflow Run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_conversations(workflow_run_id,conversation_id,created_at) VALUES (?,?,?)`, run.ID, researchConversation.ID, formatTime(run.CreatedAt)); err != nil {
		return fmt.Errorf("bind Workflow Run conversation: %w", err)
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

func (r *WorkflowRuntimeRepository) GetRunByCreationKey(ctx context.Context, projectID, creationKey string) (workflow.RunDetail, bool, error) {
	projectID, creationKey = strings.TrimSpace(projectID), strings.TrimSpace(creationKey)
	if projectID == "" || creationKey == "" {
		return workflow.RunDetail{}, false, nil
	}
	var runID string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM workflow_runs WHERE project_id=? AND creation_key=?`, projectID, creationKey).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.RunDetail{}, false, nil
	}
	if err != nil {
		return workflow.RunDetail{}, false, fmt.Errorf("read Workflow Run creation key: %w", err)
	}
	detail, err := r.GetRun(ctx, projectID, runID)
	return detail, err == nil, err
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
	aiExecutions, err := r.listAIExecutions(ctx, run.ID)
	if err != nil {
		return workflow.RunDetail{}, err
	}
	var artifactCount int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT version.artifact_id)
		FROM artifact_lineage lineage
		JOIN artifact_versions version ON version.id=lineage.artifact_version_id
		JOIN artifacts artifact ON artifact.id=version.artifact_id
		WHERE lineage.source_workflow_run_id=? AND artifact.status='active'`, run.ID).Scan(&artifactCount); err != nil {
		return workflow.RunDetail{}, fmt.Errorf("count Workflow Run Artifacts: %w", err)
	}
	registeredDeliverables, err := r.listRegisteredWorkflowDeliverables(ctx, run.ID)
	if err != nil {
		return workflow.RunDetail{}, err
	}
	proposals, err := r.ListResearchRevisionProposals(ctx, run.ID)
	if err != nil {
		return workflow.RunDetail{}, err
	}
	return workflow.RunDetail{Run: run, Steps: steps, Events: events, PendingApprovals: []permission.Approval{}, AIExecutions: aiExecutions, ArtifactCount: artifactCount, RegisteredDeliverables: registeredDeliverables, RevisionProposals: proposals}, nil
}

func (r *WorkflowRuntimeRepository) listRegisteredWorkflowDeliverables(ctx context.Context, workflowRunID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT json_extract(version.provenance_json,'$.extra.workflowDeliverable')
		FROM artifact_lineage lineage
		JOIN artifact_versions version ON version.id=lineage.artifact_version_id
		JOIN artifacts artifact ON artifact.id=version.artifact_id
		WHERE lineage.source_workflow_run_id=?
		  AND artifact.status='active'
		  AND json_extract(version.provenance_json,'$.extra.workflowDeliverable') IN ('research_design','report_draft')
		ORDER BY json_extract(version.provenance_json,'$.extra.workflowDeliverable')`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("list registered Workflow deliverables: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0, 2)
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan registered Workflow deliverable: %w", err)
		}
		if name.Valid && strings.TrimSpace(name.String) != "" {
			values = append(values, name.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read registered Workflow deliverables: %w", err)
	}
	return values, nil
}

func (r *WorkflowRuntimeRepository) GetRunByConversation(ctx context.Context, conversationID string) (workflow.RunDetail, bool, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return workflow.RunDetail{}, false, nil
	}
	var projectID, runID string
	err := r.db.QueryRowContext(ctx, `SELECT wr.project_id,wc.workflow_run_id FROM workflow_conversations wc JOIN workflow_runs wr ON wr.id=wc.workflow_run_id WHERE wc.conversation_id=?`, conversationID).Scan(&projectID, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.RunDetail{}, false, nil
	}
	if err != nil {
		return workflow.RunDetail{}, false, fmt.Errorf("read research conversation binding: %w", err)
	}
	detail, err := r.GetRun(ctx, projectID, runID)
	return detail, err == nil, err
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

func (r *WorkflowRuntimeRepository) WaitAgentStage(ctx context.Context, runID, stepID string, output json.RawMessage, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='waiting_human_confirmation',output_json=?,updated_at=? WHERE id=? AND workflow_run_id=? AND status='running'`, string(output), formatTime(at), stepID, runID)
		if err := expectOne(result, err, "wait Agent Stage confirmation"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='waiting_human_confirmation',updated_at=? WHERE id=? AND status='running' AND cancel_requested=0`, formatTime(at), runID)
		return expectOne(result, err, "wait Agent Stage Workflow confirmation")
	}, event)
}

func (r *WorkflowRuntimeRepository) RecordEvent(ctx context.Context, event workflow.RuntimeEvent) error {
	return r.transition(ctx, event.WorkflowRunID, func(*sql.Tx) error { return nil }, event)
}

func (r *WorkflowRuntimeRepository) GetAIExecutionForStep(ctx context.Context, stepID string, attempt int) (workflow.AIExecution, bool, error) {
	value, err := scanWorkflowAIExecution(r.db.QueryRowContext(ctx, workflowAIExecutionSelect+` WHERE execution.workflow_step_id=? AND execution.attempt=?`, strings.TrimSpace(stepID), attempt))
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.AIExecution{}, false, nil
	}
	if err != nil {
		return workflow.AIExecution{}, false, err
	}
	return value, true, nil
}

func (r *WorkflowRuntimeRepository) FinishAIExecution(ctx context.Context, execution workflow.AIExecution) error {
	output := execution.Output
	if len(output) == 0 {
		output = json.RawMessage(`{}`)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE workflow_ai_executions SET status=?,output_text=?,output_json=?,output_sha256=?,input_tokens=?,output_tokens=?,reasoning_tokens=?,model_turns=?,error_code=?,error_message=?,started_at=COALESCE(started_at,?),completed_at=?,updated_at=? WHERE id=? AND status IN ('prepared','running')`,
		execution.Status, execution.OutputText, string(output), execution.OutputSHA256, execution.InputTokens, execution.OutputTokens, execution.ReasoningTokens, execution.ModelTurns, execution.ErrorCode, execution.ErrorMessage,
		nullableTime(execution.StartedAt), nullableTime(execution.CompletedAt), formatTime(execution.UpdatedAt), execution.ID)
	if err != nil {
		return fmt.Errorf("finish Workflow AI execution: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("Workflow AI execution is not active")
	}
	return nil
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
		if _, err := tx.ExecContext(ctx, `
			UPDATE workflow_ai_executions
			SET status='interrupted',output_json=CASE WHEN json_valid(output_json) AND json_type(output_json)='object' THEN output_json ELSE '{}' END,
				error_code='WORKFLOW_AI_RETRY_SUPERSEDED',error_message='该 AI 阶段尝试已由用户重试取代',completed_at=?,updated_at=?
			WHERE workflow_step_id=? AND status IN ('prepared','running')`, formatTime(at), formatTime(at), stepID); err != nil {
			return fmt.Errorf("finish superseded Workflow AI execution: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=? WHERE id=? AND workflow_run_id=? AND status IN ('failed','interrupted','outcome_unknown','running','waiting_approval')`, formatTime(at), stepID, runID)
		if err := expectOne(result, err, "reset Workflow step"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='queued',error_code='',error_message='',completed_at=NULL,cancel_requested=0,resume_status='',updated_at=? WHERE id=? AND status IN ('failed','interrupted','running','waiting_approval','queued')`, formatTime(at), runID)
		return expectOne(result, err, "reset Workflow Run")
	}, event)
}

func (r *WorkflowRuntimeRepository) ResetStepsForUpstreamRetry(ctx context.Context, runID, producerStepID, failedStepID string, decision *workflow.HumanDecision, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		var producerOrdinal, failedOrdinal int
		var producerStatus, failedStatus string
		if err := tx.QueryRowContext(ctx, `SELECT ordinal,status FROM workflow_steps WHERE id=? AND workflow_run_id=?`, producerStepID, runID).Scan(&producerOrdinal, &producerStatus); err != nil {
			return fmt.Errorf("inspect upstream Workflow producer: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT ordinal,status FROM workflow_steps WHERE id=? AND workflow_run_id=?`, failedStepID, runID).Scan(&failedOrdinal, &failedStatus); err != nil {
			return fmt.Errorf("inspect failed Workflow consumer: %w", err)
		}
		if producerStatus != string(workflow.StepCompleted) || producerOrdinal >= failedOrdinal ||
			(failedStatus != string(workflow.StepFailed) && failedStatus != string(workflow.StepInterrupted) && failedStatus != string(workflow.StepOutcomeUnknown)) {
			return fmt.Errorf("upstream Workflow revision state changed; refresh the task before retrying")
		}
		if decision != nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_human_decisions(id,workflow_run_id,workflow_step_id,decision_kind,attempt,approved,note,context_json,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, decision.ID, decision.WorkflowRunID, decision.WorkflowStepID, decision.Kind, decision.Attempt, decision.Approved, decision.Note, string(decision.Context), formatTime(decision.CreatedAt)); err != nil {
				return fmt.Errorf("record upstream revision decision: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE workflow_ai_executions
			SET status='interrupted',output_json=CASE WHEN json_valid(output_json) AND json_type(output_json)='object' THEN output_json ELSE '{}' END,
				error_code='WORKFLOW_AI_RETRY_SUPERSEDED',error_message='该 AI 阶段尝试已由上游修订取代',completed_at=?,updated_at=?
			WHERE workflow_run_id=? AND workflow_step_id IN (
				SELECT id FROM workflow_steps WHERE workflow_run_id=? AND ordinal BETWEEN ? AND ?
			) AND status IN ('prepared','running')`, formatTime(at), formatTime(at), runID, runID, producerOrdinal, failedOrdinal); err != nil {
			return fmt.Errorf("finish superseded upstream AI execution: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE workflow_steps
			SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=?
			WHERE workflow_run_id=? AND ordinal BETWEEN ? AND ?`, formatTime(at), runID, producerOrdinal, failedOrdinal)
		if err != nil {
			return fmt.Errorf("reset upstream Workflow revision: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != int64(failedOrdinal-producerOrdinal+1) {
			return fmt.Errorf("upstream Workflow revision transition conflict")
		}
		result, err = tx.ExecContext(ctx, `
			UPDATE workflow_runs
			SET status='queued',outputs_json='{}',current_step_ordinal=?,error_code='',error_message='',completed_at=NULL,cancel_requested=0,resume_status='',updated_at=?
			WHERE id=? AND status IN ('failed','interrupted') AND current_step_ordinal=?`, producerOrdinal, formatTime(at), runID, failedOrdinal)
		return expectOne(result, err, "queue upstream Workflow revision")
	}, event)
}

// QueueAutomaticPythonRepair atomically records a generated-code failure as an
// audited event and rewinds the producer through the failed Python step. The
// failed tool call is already terminal in tool_calls; keeping the Workflow Run
// queued throughout this transaction prevents observers from mistaking the
// repair handoff for a user-visible terminal failure.
func (r *WorkflowRuntimeRepository) QueueAutomaticPythonRepair(ctx context.Context, runID, producerStepID, failedStepID string, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		var producerOrdinal, failedOrdinal int
		var producerStatus, failedStatus string
		if err := tx.QueryRowContext(ctx, `SELECT ordinal,status FROM workflow_steps WHERE id=? AND workflow_run_id=?`, producerStepID, runID).Scan(&producerOrdinal, &producerStatus); err != nil {
			return fmt.Errorf("inspect automatic repair producer: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT ordinal,status FROM workflow_steps WHERE id=? AND workflow_run_id=?`, failedStepID, runID).Scan(&failedOrdinal, &failedStatus); err != nil {
			return fmt.Errorf("inspect automatic repair consumer: %w", err)
		}
		if producerStatus != string(workflow.StepCompleted) || producerOrdinal >= failedOrdinal ||
			(failedStatus != string(workflow.StepRunning) && failedStatus != string(workflow.StepFailed)) {
			return fmt.Errorf("automatic Python repair state changed; refresh the task before continuing")
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE workflow_ai_executions
			SET status='interrupted',output_json=CASE WHEN json_valid(output_json) AND json_type(output_json)='object' THEN output_json ELSE '{}' END,
				error_code='WORKFLOW_AI_REPAIR_SUPERSEDED',error_message='AI 方法实现将依据 Python 运行错误自动修订',completed_at=?,updated_at=?
			WHERE workflow_run_id=? AND workflow_step_id IN (
				SELECT id FROM workflow_steps WHERE workflow_run_id=? AND ordinal BETWEEN ? AND ?
			) AND status IN ('prepared','running')`, formatTime(at), formatTime(at), runID, runID, producerOrdinal, failedOrdinal); err != nil {
			return fmt.Errorf("finish superseded automatic-repair AI execution: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE workflow_steps
			SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=?
			WHERE workflow_run_id=? AND ordinal BETWEEN ? AND ?`, formatTime(at), runID, producerOrdinal, failedOrdinal)
		if err != nil {
			return fmt.Errorf("queue automatic Python repair steps: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != int64(failedOrdinal-producerOrdinal+1) {
			return fmt.Errorf("automatic Python repair transition conflict")
		}
		result, err = tx.ExecContext(ctx, `
			UPDATE workflow_runs
			SET status='queued',outputs_json='{}',current_step_ordinal=?,error_code='',error_message='',completed_at=NULL,cancel_requested=0,resume_status='',updated_at=?
			WHERE id=? AND status IN ('running','failed','queued') AND current_step_ordinal=?`, producerOrdinal, formatTime(at), runID, failedOrdinal)
		return expectOne(result, err, "queue automatic Python repair")
	}, event)
}

// QueueAutomaticAIOutputRepair keeps a Workflow Run non-terminal while a
// bounded follow-up attempt corrects malformed or schema-invalid model output.
// The failed AI execution and its raw output remain immutable audit records;
// only the current Workflow step is reset for a new attempt.
func (r *WorkflowRuntimeRepository) QueueAutomaticAIOutputRepair(ctx context.Context, runID, stepID string, attempt int, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE workflow_steps
			SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',
				error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=?
			WHERE id=? AND workflow_run_id=? AND status='running' AND attempt=?`, formatTime(at), stepID, runID, attempt)
		if err := expectOne(result, err, "queue automatic AI output repair step"); err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `
			UPDATE workflow_runs
			SET status='queued',error_code='',error_message='',completed_at=NULL,cancel_requested=0,resume_status='',updated_at=?
			WHERE id=? AND status='running' AND current_step_ordinal=(SELECT ordinal FROM workflow_steps WHERE id=? AND workflow_run_id=?)`, formatTime(at), runID, stepID, runID)
		return expectOne(result, err, "queue automatic AI output repair run")
	}, event)
}

func (r *WorkflowRuntimeRepository) ResetStepsForReviewRevision(ctx context.Context, runID, producerStepID, gateStepID string, at time.Time, event workflow.RuntimeEvent) error {
	return r.transition(ctx, runID, func(tx *sql.Tx) error {
		var producerOrdinal, gateOrdinal int
		var producerStatus, gateStatus string
		if err := tx.QueryRowContext(ctx, `SELECT ordinal,status FROM workflow_steps WHERE id=? AND workflow_run_id=?`, producerStepID, runID).Scan(&producerOrdinal, &producerStatus); err != nil {
			return fmt.Errorf("inspect review producer step: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT ordinal,status FROM workflow_steps WHERE id=? AND workflow_run_id=?`, gateStepID, runID).Scan(&gateOrdinal, &gateStatus); err != nil {
			return fmt.Errorf("inspect review gate step: %w", err)
		}
		if producerStatus != string(workflow.StepCompleted) || gateStatus != string(workflow.StepFailed) || gateOrdinal != producerOrdinal+2 {
			return fmt.Errorf("independent-review revision state changed; refresh the task before retrying")
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE workflow_steps
			SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=?
			WHERE workflow_run_id=? AND ordinal BETWEEN ? AND ?
			  AND ((ordinal=? AND status='completed') OR (ordinal=? AND status='completed') OR (ordinal=? AND status='failed'))`,
			formatTime(at), runID, producerOrdinal, gateOrdinal, producerOrdinal, producerOrdinal+1, gateOrdinal)
		if err != nil {
			return fmt.Errorf("reset independent-review revision steps: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 3 {
			return fmt.Errorf("independent-review revision transition conflict")
		}
		result, err = tx.ExecContext(ctx, `
			UPDATE workflow_runs
			SET status='queued',outputs_json='{}',current_step_ordinal=?,error_code='',error_message='',completed_at=NULL,cancel_requested=0,resume_status='',updated_at=?
			WHERE id=? AND status='failed' AND current_step_ordinal=?`, producerOrdinal, formatTime(at), runID, gateOrdinal)
		return expectOne(result, err, "queue independent-review revision")
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

func (r *WorkflowRuntimeRepository) DeleteRun(ctx context.Context, projectID, runID string) error {
	projectID, runID = strings.TrimSpace(projectID), strings.TrimSpace(runID)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Workflow Run deletion: %w", err)
	}
	defer tx.Rollback()
	var selected workflow.Run
	if err := tx.QueryRowContext(ctx, `
		SELECT id,creation_key,COALESCE(research_task_id,''),workflow_purpose,status
		FROM workflow_runs
		WHERE project_id=? AND id=?`, projectID, runID).Scan(&selected.ID, &selected.CreationKey, &selected.ResearchTaskID, &selected.WorkflowPurpose, &selected.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("Workflow Run not found")
		}
		return fmt.Errorf("inspect Workflow Run deletion: %w", err)
	}
	taskID := selected.ResearchTaskID
	if taskID == "" {
		taskID = selected.ID
	}

	type taskRun struct {
		id             string
		conversationID string
		status         workflow.RunStatus
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT wr.id,wr.creation_key,COALESCE(wr.research_task_id,''),wr.workflow_purpose,wr.status,COALESCE(wc.conversation_id,'')
		FROM workflow_runs wr
		LEFT JOIN workflow_conversations wc ON wc.workflow_run_id=wr.id
		WHERE wr.project_id=?`, projectID)
	if err != nil {
		return fmt.Errorf("list related Workflow Runs: %w", err)
	}
	related := []taskRun{}
	for rows.Next() {
		var candidate workflow.Run
		var conversationID string
		if err := rows.Scan(&candidate.ID, &candidate.CreationKey, &candidate.ResearchTaskID, &candidate.WorkflowPurpose, &candidate.Status, &conversationID); err != nil {
			rows.Close()
			return fmt.Errorf("scan related Workflow Run: %w", err)
		}
		candidateTaskID := candidate.ResearchTaskID
		if candidateTaskID == "" {
			candidateTaskID = candidate.ID
		}
		if candidateTaskID == taskID {
			related = append(related, taskRun{id: candidate.ID, conversationID: conversationID, status: candidate.Status})
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close related Workflow Runs: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list related Workflow Runs: %w", err)
	}
	if len(related) == 0 {
		return fmt.Errorf("Workflow Run not found")
	}
	for _, item := range related {
		if !item.status.Terminal() {
			return fmt.Errorf("科研任务仍在进行；请先取消任务再删除")
		}
	}
	for _, item := range related {
		if item.conversationID == "" {
			continue
		}
		var activeChats int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE conversation_id=? AND status IN ('queued','running','waiting_approval')`, item.conversationID).Scan(&activeChats); err != nil {
			return fmt.Errorf("inspect research conversation runs: %w", err)
		}
		if activeChats > 0 {
			return fmt.Errorf("科研任务仍有正在运行的 AI 协作；请先停止生成再删除")
		}
	}
	for _, item := range related {
		result, err := tx.ExecContext(ctx, `DELETE FROM workflow_runs WHERE project_id=? AND id=? AND status IN ('completed','failed','cancelled','interrupted')`, projectID, item.id)
		if err != nil {
			return fmt.Errorf("delete Workflow Run: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return fmt.Errorf("Workflow Run changed before deletion")
		}
	}
	for _, item := range related {
		if item.conversationID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id=?`, item.conversationID); err != nil {
			return fmt.Errorf("delete research conversation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Workflow Run deletion: %w", err)
	}
	return nil
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

func (r *WorkflowRuntimeRepository) ResearchTaskIDForWorkflowRun(ctx context.Context, runID string) (string, error) {
	var taskID string
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(research_task_id,'') FROM workflow_runs WHERE id=?`, strings.TrimSpace(runID)).Scan(&taskID); err != nil {
		return "", fmt.Errorf("Workflow Run not found")
	}
	return strings.TrimSpace(taskID), nil
}

// Both stage execution and user discussion inherit their persisted Workflow
// binding. Only conversations without either binding use the project root.
func (r *WorkflowRuntimeRepository) ResearchTaskIDForSubject(ctx context.Context, subjectKind tool.SubjectKind, subjectID string) (string, error) {
	subjectKind = tool.NormalizeSubjectKind(subjectKind)
	if subjectKind == tool.SubjectWorkflowRun {
		return r.ResearchTaskIDForWorkflowRun(ctx, subjectID)
	}
	if subjectKind != tool.SubjectChatRun {
		return "", nil
	}
	_, taskID, err := r.chatResearchScope(ctx, subjectID)
	return taskID, err
}

func (r *WorkflowRuntimeRepository) chatResearchScope(ctx context.Context, chatRunID string) (string, string, error) {
	var workspacePath, taskID string
	var valid bool
	err := r.db.QueryRowContext(ctx, `WITH binding AS (
		SELECT c.project_id, COALESCE(
			(SELECT e.workflow_run_id FROM workflow_ai_chat_runs b JOIN workflow_ai_executions e ON e.id=b.execution_id WHERE b.chat_run_id=cr.id),
			(SELECT wc.workflow_run_id FROM workflow_conversations wc WHERE wc.conversation_id=cr.conversation_id)
		) AS workflow_run_id
		FROM runs cr JOIN conversations c ON c.id=cr.conversation_id WHERE cr.id=?
	)
	SELECT COALESCE(p.workspace_path,''), COALESCE(wr.research_task_id,''),
		COALESCE(wr.project_id=b.project_id AND p.workspace_path<>'' AND
		(COALESCE(wr.research_task_id,'')='' OR EXISTS(SELECT 1 FROM research_tasks t WHERE t.id=wr.research_task_id AND t.project_id=wr.project_id)),0)
	FROM binding b LEFT JOIN workflow_runs wr ON wr.id=b.workflow_run_id
	LEFT JOIN projects p ON p.id=wr.project_id WHERE b.workflow_run_id IS NOT NULL`, strings.TrimSpace(chatRunID)).Scan(&workspacePath, &taskID, &valid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("resolve research chat scope: %w", err)
	}
	if !valid {
		return "", "", fmt.Errorf("research chat task binding is invalid")
	}
	return workspacePath, strings.TrimSpace(taskID), nil
}

func (r *WorkflowRuntimeRepository) WorkspaceRootForSubject(ctx context.Context, subjectKind tool.SubjectKind, subjectID string) (string, error) {
	subjectKind = tool.NormalizeSubjectKind(subjectKind)
	if subjectKind != tool.SubjectWorkflowRun && subjectKind != tool.SubjectChatRun {
		return "", nil
	}
	var workspacePath, taskID string
	if subjectKind == tool.SubjectChatRun {
		var err error
		workspacePath, taskID, err = r.chatResearchScope(ctx, subjectID)
		if err != nil || workspacePath == "" {
			return "", err
		}
	} else {
		query := `SELECT p.workspace_path,COALESCE(wr.research_task_id,'') FROM workflow_runs wr JOIN projects p ON p.id=wr.project_id WHERE wr.id=?`
		if err := r.db.QueryRowContext(ctx, query, strings.TrimSpace(subjectID)).Scan(&workspacePath, &taskID); err != nil {
			return "", fmt.Errorf("Workflow Run not found")
		}
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return filepath.Clean(workspacePath), nil
	}
	selected := project.Project{ID: "workflow", WorkspacePath: workspacePath}
	return project.ResearchTaskWorkspacePath(selected, taskID)
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

func (r *WorkflowRuntimeRepository) listAIExecutions(ctx context.Context, runID string) ([]workflow.AIExecution, error) {
	rows, err := r.db.QueryContext(ctx, workflowAIExecutionSelect+` WHERE execution.workflow_run_id=? ORDER BY execution.created_at,execution.id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list Workflow AI executions: %w", err)
	}
	defer rows.Close()
	result := []workflow.AIExecution{}
	for rows.Next() {
		value, scanErr := scanWorkflowAIExecution(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

const workflowAIExecutionSelect = `SELECT execution.id,execution.workflow_run_id,execution.workflow_step_id,COALESCE(binding.chat_run_id,''),execution.attempt,execution.node_kind,execution.model_profile_id,execution.model_id,execution.reasoning_level,execution.prompt_version,execution.prompt_sha256,execution.input_sha256,execution.allowed_tools_json,execution.output_schema_json,execution.output_schema_sha256,execution.status,execution.output_text,execution.output_json,execution.output_sha256,execution.input_tokens,execution.output_tokens,execution.reasoning_tokens,execution.model_turns,execution.error_code,execution.error_message,execution.created_at,execution.started_at,execution.completed_at,execution.updated_at FROM workflow_ai_executions execution LEFT JOIN workflow_ai_chat_runs binding ON binding.execution_id=execution.id`

func scanWorkflowAIExecution(scanner workflowScanner) (workflow.AIExecution, error) {
	var value workflow.AIExecution
	var allowedJSON, schemaJSON, outputJSON, createdAt, updatedAt string
	var startedAt, completedAt sql.NullString
	if err := scanner.Scan(&value.ID, &value.WorkflowRunID, &value.WorkflowStepID, &value.ChatRunID, &value.Attempt, &value.NodeKind, &value.ModelProfileID, &value.ModelID, &value.ReasoningLevel, &value.PromptVersion, &value.PromptSHA256, &value.InputSHA256, &allowedJSON, &schemaJSON, &value.OutputSchemaSHA256, &value.Status, &value.OutputText, &outputJSON, &value.OutputSHA256, &value.InputTokens, &value.OutputTokens, &value.ReasoningTokens, &value.ModelTurns, &value.ErrorCode, &value.ErrorMessage, &createdAt, &startedAt, &completedAt, &updatedAt); err != nil {
		return workflow.AIExecution{}, err
	}
	if err := json.Unmarshal([]byte(allowedJSON), &value.AllowedTools); err != nil {
		return workflow.AIExecution{}, fmt.Errorf("decode Workflow AI allowed tools: %w", err)
	}
	value.OutputSchema, value.Output = json.RawMessage(schemaJSON), json.RawMessage(outputJSON)
	var err error
	if value.CreatedAt, err = parseTime(createdAt); err != nil {
		return workflow.AIExecution{}, err
	}
	if value.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return workflow.AIExecution{}, err
	}
	if startedAt.Valid {
		parsed, parseErr := parseTime(startedAt.String)
		if parseErr != nil {
			return workflow.AIExecution{}, parseErr
		}
		value.StartedAt = &parsed
	}
	if completedAt.Valid {
		parsed, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return workflow.AIExecution{}, parseErr
		}
		value.CompletedAt = &parsed
	}
	return value, nil
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
	if err := scanner.Scan(&value.ID, &value.CreationKey, &value.ResearchTaskID, &value.ConversationID, &value.ProjectID, &value.WorkflowID, &value.WorkflowVersionID, &value.WorkflowName, &value.WorkflowPurpose, &value.Status, &value.PermissionMode, &inputs, &value.InputsSHA256, &compilation, &value.CompilationSHA256, &outputs, &value.CurrentStep, &value.ErrorCode, &value.ErrorMessage, &value.CancelRequested, &value.ResumeStatus, &createdAt, &startedAt, &completedAt, &updatedAt); err != nil {
		return value, err
	}
	value.Inputs, value.Outputs = json.RawMessage(inputs), json.RawMessage(outputs)
	// Keep the persisted task identity authoritative.  ApplyResearchTaskLink
	// still derives ResearchStarterID from the immutable creation key for
	// adopted-route UI metadata, but must never overwrite a durable task ID.
	persistedTaskID := value.ResearchTaskID
	workflow.ApplyResearchTaskLink(&value)
	if strings.TrimSpace(persistedTaskID) != "" {
		value.ResearchTaskID = persistedTaskID
	}
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

const workflowRunSelect = `SELECT wr.id,wr.creation_key,COALESCE(wr.research_task_id,''),COALESCE(wc.conversation_id,''),wr.project_id,wr.workflow_id,wr.workflow_version_id,wr.workflow_name,wr.workflow_purpose,wr.status,wr.permission_mode,wr.inputs_json,wr.inputs_sha256,wr.compilation_json,wr.compilation_sha256,wr.outputs_json,wr.current_step_ordinal,wr.error_code,wr.error_message,wr.cancel_requested,wr.resume_status,wr.created_at,wr.started_at,wr.completed_at,wr.updated_at FROM workflow_runs wr LEFT JOIN workflow_conversations wc ON wc.workflow_run_id=wr.id`
const workflowStepSelect = `SELECT ws.id,ws.workflow_run_id,ws.node_id,ws.ordinal,ws.node_kind,ws.status,ws.attempt,ws.input_json,ws.input_sha256,ws.output_json,COALESCE(ws.tool_call_id,''),ws.idempotency_key,ws.error_code,ws.error_message,ws.started_at,ws.completed_at,ws.updated_at FROM workflow_steps ws`

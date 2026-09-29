package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

func (r *WorkflowRuntimeRepository) ResolveResearchDiscussion(ctx context.Context, chatRunID string) (workflow.ResearchDiscussionBinding, error) {
	var binding workflow.ResearchDiscussionBinding
	err := r.db.QueryRowContext(ctx, `SELECT wr.project_id,wr.id,cr.user_message_id
		FROM runs cr JOIN workflow_conversations wc ON wc.conversation_id=cr.conversation_id
		JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
		WHERE cr.id=? AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs WHERE chat_run_id=cr.id)`, chatRunID).
		Scan(&binding.ProjectID, &binding.WorkflowRunID, &binding.UserMessageID)
	if err != nil {
		return binding, fmt.Errorf("该工具仅用于当前科研任务的用户对话")
	}
	return binding, nil
}

// Only attachments actually sent by the user in this discussion are eligible;
// uploads sitting in a picker and attachments from internal AI runs are not.
func (r *WorkflowRuntimeRepository) DiscussionAttachmentIDs(ctx context.Context, runID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT json_extract(mp.payload_json,'$.attachmentId')
		FROM workflow_conversations wc JOIN messages m ON m.conversation_id=wc.conversation_id
		JOIN runs cr ON cr.id=m.run_id JOIN message_parts mp ON mp.message_id=m.id
		WHERE wc.workflow_run_id=? AND m.role='user' AND mp.part_type='media'
		AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs b WHERE b.chat_run_id=cr.id)
		AND json_extract(mp.payload_json,'$.attachmentId') IS NOT NULL ORDER BY m.rowid,mp.ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// Ordinary chat identity, latest human turn, delivery status, registration and
// latest task ownership are checked again inside the write transaction.
func validateDiscussionProposalState(ctx context.Context, tx *sql.Tx, proposal workflow.ResearchRevisionProposal, requireCompletedChat bool) error {
	var valid int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM runs cr JOIN workflow_conversations wc ON wc.conversation_id=cr.conversation_id
		JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
		WHERE cr.id=? AND wr.id=? AND cr.user_message_id=? AND wr.status='completed' AND wr.workflow_purpose='user_plan'
		AND cr.status IN ('running','completed') AND (?=0 OR cr.status='completed')
		AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs WHERE chat_run_id=cr.id)
		AND EXISTS(SELECT 1 FROM research_tasks task WHERE task.id=wr.research_task_id AND task.project_id=wr.project_id AND task.status<>'archived')
		AND cr.user_message_id=(SELECT id FROM messages WHERE conversation_id=cr.conversation_id AND role='user' ORDER BY rowid DESC LIMIT 1)
		AND NOT EXISTS(SELECT 1 FROM runs active WHERE active.conversation_id=cr.conversation_id AND active.id<>cr.id AND active.status IN ('queued','running','waiting_approval'))
		AND NOT EXISTS(SELECT 1 FROM artifact_versions v JOIN artifact_lineage l ON l.artifact_version_id=v.id
			WHERE l.source_workflow_run_id=wr.id AND json_extract(v.provenance_json,'$.extra.workflowDeliverable') IN ('research_design','report_draft'))
		AND NOT EXISTS(SELECT 1 FROM workflow_runs newer WHERE newer.research_task_id=wr.research_task_id AND newer.project_id=wr.project_id
			AND (newer.created_at>wr.created_at OR (newer.created_at=wr.created_at AND newer.id>wr.id)))
	)`, proposal.ChatRunID, proposal.RunID, proposal.UserMessageID, requireCompletedChat).Scan(&valid)
	if err != nil {
		return err
	}
	if valid != 1 {
		return fmt.Errorf("任务或对话已变化，或报告已登记；请等待回复结束并确认最新方案")
	}
	return nil
}

func (r *WorkflowRuntimeRepository) SaveResearchRevisionProposal(ctx context.Context, proposal workflow.ResearchRevisionProposal) (workflow.ResearchRevisionProposal, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return proposal, err
	}
	defer tx.Rollback()
	if err := validateDiscussionProposalState(ctx, tx, proposal, false); err != nil {
		return proposal, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT proposal_json FROM research_revision_proposals WHERE source_call_id=?`, proposal.SourceCallID).Scan(&existing)
	if err == nil {
		err = json.Unmarshal([]byte(existing), &proposal)
		return proposal, err
	}
	if err != sql.ErrNoRows {
		return proposal, err
	}
	var validCall int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tool_calls WHERE id=? AND run_id=? AND tool_name=? AND status='running')`, proposal.SourceCallID, proposal.ChatRunID, workflow.ResearchRevisionProposeTool).Scan(&validCall); err != nil {
		return proposal, err
	}
	if validCall != 1 {
		return proposal, fmt.Errorf("返修建议缺少有效的当前工具调用")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE research_revision_proposals SET status='superseded' WHERE workflow_run_id=? AND status='pending'`, proposal.RunID); err != nil {
		return proposal, err
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return proposal, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO research_revision_proposals(id,workflow_run_id,chat_run_id,user_message_id,source_call_id,status,proposal_json,created_at) VALUES (?,?,?,?,?,'pending',?,?)`, proposal.ID, proposal.RunID, proposal.ChatRunID, proposal.UserMessageID, proposal.SourceCallID, string(encoded), formatTime(proposal.CreatedAt))
	if err != nil {
		return proposal, err
	}
	return proposal, tx.Commit()
}

func (r *WorkflowRuntimeRepository) ListResearchRevisionProposals(ctx context.Context, runID string) ([]workflow.ResearchRevisionProposal, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT p.proposal_json,p.status,cr.status,
		p.status='pending' AND cr.status='completed' AND wr.status='completed'
		AND EXISTS(SELECT 1 FROM tool_calls tc WHERE tc.id=p.source_call_id AND tc.status='completed')
		AND cr.user_message_id=(SELECT id FROM messages WHERE conversation_id=cr.conversation_id AND role='user' ORDER BY rowid DESC LIMIT 1)
		AND NOT EXISTS(SELECT 1 FROM runs active WHERE active.conversation_id=cr.conversation_id AND active.status IN ('queued','running','waiting_approval'))
		AND NOT EXISTS(SELECT 1 FROM artifact_versions v JOIN artifact_lineage l ON l.artifact_version_id=v.id WHERE l.source_workflow_run_id=wr.id AND json_extract(v.provenance_json,'$.extra.workflowDeliverable') IN ('research_design','report_draft'))
		AND EXISTS(SELECT 1 FROM research_tasks task WHERE task.id=wr.research_task_id AND task.project_id=wr.project_id AND task.status<>'archived')
		AND NOT EXISTS(SELECT 1 FROM workflow_runs newer WHERE newer.research_task_id=wr.research_task_id AND newer.project_id=wr.project_id AND (newer.created_at>wr.created_at OR (newer.created_at=wr.created_at AND newer.id>wr.id)))
		FROM research_revision_proposals p JOIN runs cr ON cr.id=p.chat_run_id JOIN workflow_runs wr ON wr.id=p.workflow_run_id
		WHERE p.workflow_run_id=? ORDER BY p.rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []workflow.ResearchRevisionProposal{}
	for rows.Next() {
		var encoded, status, chatStatus string
		var ready bool
		if err := rows.Scan(&encoded, &status, &chatStatus, &ready); err != nil {
			return nil, err
		}
		var proposal workflow.ResearchRevisionProposal
		if err := json.Unmarshal([]byte(encoded), &proposal); err != nil {
			return nil, err
		}
		proposal.Status, proposal.CanConfirm = status, ready
		if status == "pending" && !ready && (chatStatus == "completed" || chatStatus == "failed" || chatStatus == "cancelled" || chatStatus == "interrupted") {
			proposal.Status = "superseded"
		}
		result = append(result, proposal)
	}
	return result, rows.Err()
}

func (r *WorkflowRuntimeRepository) ConfirmResearchRevision(ctx context.Context, proposal workflow.ResearchRevisionProposal, expected workflow.RunDetail, event workflow.RuntimeEvent) error {
	return r.transition(ctx, proposal.RunID, func(tx *sql.Tx) error {
		if err := validateDiscussionProposalState(ctx, tx, proposal, true); err != nil {
			return err
		}
		var callCompleted int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tool_calls tc JOIN research_revision_proposals p ON p.source_call_id=tc.id WHERE p.id=? AND tc.status='completed')`, proposal.ID).Scan(&callCompleted); err != nil {
			return err
		}
		if callCompleted != 1 {
			return fmt.Errorf("返修提案调用尚未成功完成")
		}
		var encoded, status string
		if err := tx.QueryRowContext(ctx, `SELECT proposal_json,status FROM research_revision_proposals WHERE id=? AND workflow_run_id=?`, proposal.ID, proposal.RunID).Scan(&encoded, &status); err != nil {
			return err
		}
		var stored workflow.ResearchRevisionProposal
		if json.Unmarshal([]byte(encoded), &stored) != nil || status != "pending" || stored.SnapshotSHA256 != proposal.SnapshotSHA256 || stored.NodeID != proposal.NodeID {
			return fmt.Errorf("返修方案已被替代或执行")
		}
		run, err := scanWorkflowRun(tx.QueryRowContext(ctx, workflowRunSelect+` WHERE wr.id=?`, proposal.RunID))
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, workflowStepSelect+` WHERE ws.workflow_run_id=? ORDER BY ws.ordinal`, proposal.RunID)
		if err != nil {
			return err
		}
		steps := []workflow.Step{}
		for rows.Next() {
			step, scanErr := scanWorkflowStep(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			steps = append(steps, step)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if workflow.ResearchRevisionSnapshot(workflow.RunDetail{Run: run, Steps: steps}) != proposal.SnapshotSHA256 || workflow.ResearchRevisionSnapshot(expected) != proposal.SnapshotSHA256 {
			return fmt.Errorf("任务快照已变化，请重新生成返修方案")
		}
		ordinal := -1
		for _, step := range steps {
			if step.NodeID == stored.NodeID && step.Status == workflow.StepCompleted {
				ordinal = step.Ordinal
			}
		}
		if ordinal < 0 {
			return fmt.Errorf("返修起点已失效")
		}
		snapshot, err := json.Marshal(map[string]any{"run": run, "steps": steps})
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE research_revision_proposals SET status='confirmed',previous_snapshot_json=?,confirmed_at=? WHERE id=? AND status='pending'`, string(snapshot), formatTime(event.CreatedAt), proposal.ID)
		if err := expectOne(result, err, "confirm research revision"); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE workflow_steps SET status='queued',input_json='{}',input_sha256='',output_json='{}',tool_call_id=NULL,idempotency_key='',error_code='',error_message='',started_at=NULL,completed_at=NULL,updated_at=? WHERE workflow_run_id=? AND ordinal>=?`, formatTime(event.CreatedAt), proposal.RunID, ordinal)
		if err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `UPDATE workflow_runs SET status='queued',outputs_json='{}',current_step_ordinal=?,error_code='',error_message='',completed_at=NULL,cancel_requested=0,resume_status='',updated_at=? WHERE id=? AND status='completed'`, ordinal, formatTime(event.CreatedAt), proposal.RunID)
		return expectOne(result, err, "queue user research revision")
	}, event)
}

func (r *WorkflowRuntimeRepository) ReadResearchDiscussionRecords(ctx context.Context, runID, section, nodeID string) (json.RawMessage, error) {
	// Queries derive ownership from the bound task, not a model-provided ID.
	queries := map[string]string{
		"messages": `SELECT m.id,m.role,m.status,m.created_at,ws.node_id,mp.ordinal,mp.part_type,mp.text_content,mp.payload_json
			FROM workflow_runs current JOIN workflow_runs related ON related.project_id=current.project_id AND related.research_task_id=current.research_task_id
			JOIN workflow_conversations wc ON wc.workflow_run_id=related.id JOIN messages m ON m.conversation_id=wc.conversation_id
			JOIN message_parts mp ON mp.message_id=m.id LEFT JOIN workflow_ai_chat_runs b ON b.chat_run_id=m.run_id
			LEFT JOIN workflow_ai_executions e ON e.id=b.execution_id LEFT JOIN workflow_steps ws ON ws.id=e.workflow_step_id
			WHERE current.id=? AND (?='' OR ws.node_id=?) ORDER BY related.created_at,m.rowid,mp.ordinal`,
		"attempts": `SELECT ws.node_id,e.attempt,e.prompt_text,e.output_text,e.output_json,e.status,e.error_code,e.error_message,e.input_sha256,e.output_sha256
			FROM workflow_ai_executions e JOIN workflow_steps ws ON ws.id=e.workflow_step_id WHERE e.workflow_run_id=? AND (?='' OR ws.node_id=?) ORDER BY ws.ordinal,e.attempt`,
		"tool_calls": `SELECT tc.id,tc.tool_name,tc.arguments_json,tc.status,tc.error_message,tr.text_content,tr.structured_json,tr.artifacts_json,tr.citations_json
			FROM tool_calls tc LEFT JOIN tool_results tr ON tr.tool_call_id=tc.id
			LEFT JOIN workflow_ai_chat_runs b ON b.chat_run_id=tc.run_id LEFT JOIN workflow_ai_executions e ON e.id=b.execution_id
			LEFT JOIN workflow_steps ws ON ws.id=e.workflow_step_id OR ws.tool_call_id=tc.id
			WHERE (tc.workflow_run_id=? OR e.workflow_run_id=?) AND (?='' OR ws.node_id=?) ORDER BY tc.created_at,tc.id`,
		"revisions": `SELECT proposal_json,status,previous_snapshot_json FROM research_revision_proposals WHERE workflow_run_id=? ORDER BY rowid`,
	}
	query := queries[section]
	if query == "" {
		return nil, fmt.Errorf("unknown task record section")
	}
	args := []any{runID, nodeID, nodeID}
	if section == "tool_calls" {
		args = []any{runID, runID, nodeID, nodeID}
	}
	if section == "revisions" {
		args = []any{runID}
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		item := map[string]any{}
		for i, column := range columns {
			item[column] = values[i]
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

const workingStateSelect = `SELECT s.id,s.project_id,s.research_task_id,s.version,s.current_focus,s.rationale,s.next_step,
 s.unknowns_json,s.source_chat_run_id,s.source_user_message_id,s.source_call_id,s.created_at
 FROM research_working_states s`

const effectiveWorkingState = ` JOIN tool_calls tc ON tc.id=s.source_call_id AND tc.status='completed'
 JOIN tool_results tr ON tr.tool_call_id=tc.id AND tr.status='success'`

func (r *WorkflowRuntimeRepository) ValidateResearchWorkingStateRead(ctx context.Context, projectID, taskID, chatRunID, callID string) error {
	return r.ValidateResearchDiscussionTool(ctx, projectID, taskID, chatRunID, callID, workflow.ResearchStateReadTool)
}

func (r *WorkflowRuntimeRepository) ValidateResearchDiscussionTool(ctx context.Context, projectID, taskID, chatRunID, callID, name string) error {
	if name != workflow.ResearchStateReadTool && name != workflow.ResearchDiscoverySearchTool && name != workflow.ResearchDiscoveryReadTool {
		return fmt.Errorf("未知科研讨论工具")
	}
	var valid bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM research_exploration_chats d
 JOIN tool_calls tc ON tc.run_id=d.chat_run_id AND tc.workflow_run_id IS NULL
 WHERE d.chat_run_id=? AND d.chat_status='running' AND tc.id=? AND tc.status='running' AND tc.tool_name=?
 AND d.project_id=? AND d.research_task_id=?)`, chatRunID, callID, name, projectID, taskID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("工具调用不属于当前未采用路线的科研讨论")
	}
	return nil
}

func scanResearchWorkingState(row workflowScanner) (workflow.ResearchWorkingState, error) {
	var value workflow.ResearchWorkingState
	var unknowns, created string
	if err := row.Scan(&value.ID, &value.ProjectID, &value.ResearchTaskID, &value.Version, &value.CurrentFocus, &value.Rationale,
		&value.NextStep, &unknowns, &value.SourceChatRunID, &value.SourceUserMessageID, &value.SourceCallID, &created); err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(unknowns), &value.Unknowns); err != nil {
		return value, fmt.Errorf("decode research working state: %w", err)
	}
	if value.Unknowns == nil {
		value.Unknowns = []string{}
	}
	var err error
	value.CreatedAt, err = parseTime(created)
	value.Status = "working_note"
	return value, err
}

func (r *WorkflowRuntimeRepository) GetResearchWorkingState(ctx context.Context, projectID, taskID string) (*workflow.ResearchWorkingState, error) {
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" {
		return nil, fmt.Errorf("project and research task are required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	value, err := readResearchWorkingState(ctx, tx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return value, nil
}

func readResearchWorkingState(ctx context.Context, tx *sql.Tx, projectID, taskID string) (*workflow.ResearchWorkingState, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM research_tasks WHERE project_id=? AND id=? AND status<>'archived')`, projectID, taskID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("当前项目不存在该科研任务，或任务已归档")
	}
	value, err := scanResearchWorkingState(tx.QueryRowContext(ctx, workingStateSelect+effectiveWorkingState+`
 WHERE s.project_id=? AND s.research_task_id=? ORDER BY s.version DESC LIMIT 1`, projectID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// SaveResearchWorkingState validates every identity again in the write
// transaction. The row is provisional until the exact ToolCall succeeds.
func (r *WorkflowRuntimeRepository) SaveResearchWorkingState(ctx context.Context, projectID, taskID, chatRunID, callID, stateID string, at time.Time, command workflow.ResearchWorkingStateUpdateCommand) (workflow.ResearchWorkingState, error) {
	var empty workflow.ResearchWorkingState
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	chatRunID, callID, stateID = strings.TrimSpace(chatRunID), strings.TrimSpace(callID), strings.TrimSpace(stateID)
	if projectID == "" || taskID == "" || chatRunID == "" || callID == "" || stateID == "" || at.IsZero() {
		return empty, fmt.Errorf("研究工作记录缺少宿主身份")
	}
	command, err := workflow.NormalizeResearchWorkingStateUpdate(command)
	if err != nil {
		return empty, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var workflowRunID, humanID, arguments, proposedHumanID string
	err = tx.QueryRowContext(ctx, `SELECT d.source_workflow_run_id,human.id,tc.arguments_json,proposed.source_user_message_id
 FROM research_exploration_chats d
 JOIN tool_calls tc ON tc.run_id=d.chat_run_id AND tc.workflow_run_id IS NULL
 JOIN research_working_state_proposals proposed ON proposed.source_call_id=tc.id
 JOIN research_discussion_latest_human latest ON latest.project_id=d.project_id AND latest.research_task_id=d.research_task_id
 JOIN messages human ON human.id=latest.user_message_id AND human.conversation_id=d.conversation_id
   AND human.run_id=d.chat_run_id AND human.role='user' AND human.status='complete'
 WHERE d.chat_run_id=? AND d.chat_status='running' AND tc.id=? AND tc.status='running' AND tc.tool_name=?
 AND d.project_id=? AND d.research_task_id=?`,
		chatRunID, callID, workflow.ResearchStateUpdateTool, projectID, taskID).Scan(&workflowRunID, &humanID, &arguments, &proposedHumanID)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, fmt.Errorf("研究讨论、工具调用或提议来源已变化，请根据当前用户消息重新提出工作记录")
	}
	if err != nil {
		return empty, err
	}
	if proposedHumanID != humanID {
		return empty, fmt.Errorf("这条研究工作记录在最新用户回答之前提出；请根据当前回答重新调用并提交")
	}
	persistedCommand, err := workflow.ParseResearchWorkingStateUpdate(json.RawMessage(arguments))
	if err != nil || !reflect.DeepEqual(command, persistedCommand) {
		return empty, fmt.Errorf("工具调用与研究工作记录参数不一致")
	}
	for _, ids := range command.Basis {
		for _, id := range ids {
			if _, _, err := readWorkingBasis(ctx, tx, projectID, taskID, callID, id); err != nil {
				return empty, err
			}
		}
	}
	// Replaying the same active ToolCall never creates a second version. The
	// persisted ToolCall arguments also prevent aliasing it to other content.
	existing, err := scanResearchWorkingState(tx.QueryRowContext(ctx, workingStateSelect+` WHERE s.source_call_id=?`, callID))
	if err == nil {
		if existing.ProjectID != projectID || existing.ResearchTaskID != taskID || existing.SourceChatRunID != chatRunID || existing.SourceUserMessageID != humanID || existing.Version != command.ExpectedVersion+1 || command.HasField("currentFocus") && existing.CurrentFocus != command.CurrentFocus || command.HasField("rationale") && existing.Rationale != command.Rationale || command.HasField("nextStep") && existing.NextStep != command.NextStep || command.HasField("unknowns") && !reflect.DeepEqual(existing.Unknowns, command.Unknowns) {
			return empty, fmt.Errorf("同一工具调用的研究工作记录内容冲突")
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	var current int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(s.version),0) FROM research_working_states s`+effectiveWorkingState+` WHERE s.project_id=? AND s.research_task_id=?`, projectID, taskID).Scan(&current); err != nil {
		return empty, err
	}
	if current != command.ExpectedVersion {
		return empty, fmt.Errorf("研究工作记录版本已变化，请读取当前状态后再更新")
	}
	var base *workflow.ResearchWorkingState
	if current > 0 {
		previous, err := scanResearchWorkingState(tx.QueryRowContext(ctx, workingStateSelect+effectiveWorkingState+` WHERE s.project_id=? AND s.research_task_id=? AND s.version=? LIMIT 1`, projectID, taskID, current))
		if err != nil {
			return empty, err
		}
		base = &previous
	}
	merged := workflow.ApplyResearchWorkingStateUpdate(base, command)
	// A second running pending write cannot reuse the same effective version.
	// Once the originating chat or call is terminal it cannot block a retry;
	// the finish trigger also prevents a stale late call from succeeding.
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM research_working_states s
 JOIN tool_calls pc ON pc.id=s.source_call_id AND pc.status='running'
 JOIN research_exploration_chats d ON d.chat_run_id=s.source_chat_run_id AND d.chat_status='running'
   AND d.project_id=s.project_id AND d.research_task_id=s.research_task_id
 JOIN research_discussion_latest_human latest ON latest.project_id=s.project_id AND latest.research_task_id=s.research_task_id
 WHERE s.project_id=? AND s.research_task_id=? AND s.source_call_id<>?
 AND s.source_user_message_id=latest.user_message_id)`, projectID, taskID, callID).Scan(&pending); err != nil {
		return empty, err
	}
	if pending {
		return empty, fmt.Errorf("另一条研究工作记录仍在提交，请等待结果后读取最新版本")
	}
	unknowns, err := json.Marshal(merged.Unknowns)
	if err != nil {
		return empty, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO research_working_states(id,project_id,research_task_id,workflow_run_id,source_chat_run_id,source_user_message_id,source_call_id,version,current_focus,rationale,next_step,unknowns_json,created_at)
	 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, stateID, projectID, taskID, workflowRunID, chatRunID, humanID, callID, current+1, merged.CurrentFocus, merged.Rationale, merged.NextStep, string(unknowns), formatTime(at))
	if err != nil {
		return empty, fmt.Errorf("保存研究工作记录: %w", err)
	}
	value := workflow.ResearchWorkingState{ID: stateID, ProjectID: projectID, ResearchTaskID: taskID, Version: current + 1,
		CurrentFocus: merged.CurrentFocus, Rationale: merged.Rationale, NextStep: merged.NextStep, Unknowns: merged.Unknowns,
		SourceChatRunID: chatRunID, SourceUserMessageID: humanID, SourceCallID: callID, CreatedAt: at.UTC(), Status: "working_note"}
	return value, tx.Commit()
}

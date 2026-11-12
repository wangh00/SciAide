package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

// Waiting is atomic with the persisted call, and keeps the existing active
// conversation reservation. The API projects it as waiting_user_input rather
// than falsely presenting the user question as a permission approval.
// Workflow stages use this boundary only with explicit frozen tool grants.
const activeWorkflowQuestionScope = `EXISTS (
 SELECT 1 FROM workflow_ai_chat_runs b
 JOIN workflow_ai_executions e ON e.id=b.execution_id
 JOIN workflow_steps s ON s.id=e.workflow_step_id AND s.workflow_run_id=e.workflow_run_id
 JOIN workflow_runs w ON w.id=e.workflow_run_id
 WHERE b.chat_run_id=r.id AND e.status='running' AND w.status='running' AND w.cancel_requested=0
 AND s.status='running' AND s.attempt=e.attempt AND w.current_step_ordinal=s.ordinal
 AND EXISTS(SELECT 1 FROM json_each(e.allowed_tools_json) t WHERE t.value='builtin.user_input.request')
 AND EXISTS(SELECT 1 FROM json_each(w.compilation_json,'$.nodes') n,
 json_each(n.value,'$.allowedTools') t WHERE json_extract(n.value,'$.id')=s.node_id
 AND json_extract(t.value,'$.qualifiedName')='builtin.user_input.request')
)`

func (r *RunRepository) WaitForUserInput(ctx context.Context, runID, callID string, at time.Time) (chat.Run, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return chat.Run{}, err
	}
	defer tx.Rollback()
	var raw string
	var pending string
	err = tx.QueryRowContext(ctx, `SELECT tc.arguments_json,r.pending_user_input_call_id FROM tool_calls tc JOIN runs r ON r.id=tc.run_id WHERE r.id=? AND tc.id=? AND tc.tool_name=? AND r.status IN ('running','waiting_approval') AND (NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs WHERE chat_run_id=r.id) OR `+activeWorkflowQuestionScope+`)`, runID, callID, userinput.ToolName).Scan(&raw, &pending)
	if err != nil {
		return chat.Run{}, fmt.Errorf("问题不属于当前对话或已授权的科研阶段：%w", err)
	}
	if _, err = userinput.Decode(json.RawMessage(raw)); err != nil {
		return chat.Run{}, err
	}
	if pending != "" && pending != callID {
		return chat.Run{}, fmt.Errorf("当前对话已有待回答问题")
	}
	if pending == "" {
		result, err := tx.ExecContext(ctx, `UPDATE runs SET status='waiting_approval',pending_user_input_call_id=?,updated_at=? WHERE id=? AND status='running' AND pending_user_input_call_id=''`, callID, formatTime(at), runID)
		if err != nil {
			return chat.Run{}, err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return chat.Run{}, fmt.Errorf("对话已停止或已发生变化")
		}
		result, err = tx.ExecContext(ctx, `UPDATE tool_calls SET status='running',started_at=COALESCE(started_at,?),updated_at=? WHERE id=? AND status IN ('pending','running')`, formatTime(at), formatTime(at), callID)
		if err != nil {
			return chat.Run{}, err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return chat.Run{}, fmt.Errorf("问题调用已变化")
		}
	}
	if err = tx.Commit(); err != nil {
		return chat.Run{}, err
	}
	return r.Get(ctx, runID)
}

func (r *RunRepository) RespondToUserInput(ctx context.Context, response userinput.Response, at time.Time) (chat.Run, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return chat.Run{}, err
	}
	defer tx.Rollback()
	var raw, status, conversationID, pending string
	err = tx.QueryRowContext(ctx, `SELECT tc.arguments_json,tc.status,r.conversation_id,r.pending_user_input_call_id FROM tool_calls tc JOIN runs r ON r.id=tc.run_id WHERE r.id=? AND tc.id=? AND tc.tool_name=?`, response.RunID, response.CallID, userinput.ToolName).Scan(&raw, &status, &conversationID, &pending)
	if err != nil || conversationID != response.ConversationID {
		return chat.Run{}, fmt.Errorf("问题不属于当前对话")
	}
	questions, err := userinput.Decode(json.RawMessage(raw))
	if err != nil {
		return chat.Run{}, err
	}
	answers := userinput.Answers{}
	if !response.Dismiss {
		answers, err = userinput.NormalizeAnswers(questions.Questions, response.Answers)
		if err != nil {
			return chat.Run{}, err
		}
	}
	payload, _ := json.Marshal(map[string]any{"answers": answers, "dismissed": response.Dismiss})
	// Exact duplicate response is safe (including a retry after an IPC failure).
	if status == "completed" {
		var saved string
		if err = tx.QueryRowContext(ctx, `SELECT structured_json FROM tool_results WHERE tool_call_id=?`, response.CallID).Scan(&saved); err != nil || saved != string(payload) {
			return chat.Run{}, fmt.Errorf("此问题已经回答，不能覆盖旧答案")
		}
		tx.Rollback()
		return r.Get(ctx, response.RunID)
	}
	if status != "running" || pending != response.CallID {
		return chat.Run{}, &apperr.Error{Code: "USER_INPUT_EXPIRED", UserMessage: "此问题已经结束或已变化，答案未提交；请刷新对话。"}
	}
	var authorized bool
	if err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs WHERE chat_run_id=r.id) OR `+activeWorkflowQuestionScope+` FROM runs r WHERE r.id=?`, response.RunID).Scan(&authorized); err != nil || !authorized {
		return chat.Run{}, fmt.Errorf("科研阶段已结束、变化或未授权此问题，答案未提交")
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status='running',pending_user_input_call_id='',updated_at=? WHERE id=? AND status='waiting_approval' AND pending_user_input_call_id=?`, formatTime(at), response.RunID, response.CallID)
	if err != nil {
		return chat.Run{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return chat.Run{}, &apperr.Error{Code: "USER_INPUT_EXPIRED", UserMessage: "对话已经停止，答案未提交；请刷新对话。"}
	}
	text := userinput.AnswerText(questions.Questions, answers)
	if response.Dismiss {
		text = "用户暂不回答这些问题；不要自动选择答案或将建议当作确认。"
	}
	value := tool.Result{Status: tool.ResultSuccess, Text: text, Structured: payload, CreatedAt: at}
	eventID, err := id.New()
	if err != nil {
		return chat.Run{}, err
	}
	eventData, _ := json.Marshal(map[string]any{"callId": response.CallID, "runId": response.RunID, "status": "completed"})
	event := events.New(eventID, response.RunID, "run", "tool.completed", 0, eventData)
	event.Timestamp = at
	if err = finishToolCall(ctx, tx, response.CallID, tool.CallRunning, tool.CallCompleted, value, "", "", at); err != nil {
		return chat.Run{}, err
	}
	if err = appendToolEvent(ctx, tx, event); err != nil {
		return chat.Run{}, err
	}
	// Human answers also remain in future turns and the research timeline, not
	// only in the provider's transient tool-result context.
	messageID, err := id.New()
	if err != nil {
		return chat.Run{}, err
	}
	partID, err := id.New()
	if err != nil {
		return chat.Run{}, err
	}
	message := conversation.Message{ID: messageID, ConversationID: conversationID, RunID: response.RunID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: at, UpdatedAt: at, Parts: []conversation.MessagePart{{ID: partID, Ordinal: 0, Type: "text", Text: text, CreatedAt: at}}}
	if err = insertMessage(ctx, tx, message); err != nil {
		return chat.Run{}, err
	}
	// The normal timeline trigger reserves two messages at Run creation.
	// Later human tool replies need their own append-only entry.
	if _, err = tx.ExecContext(ctx, `INSERT INTO research_timeline(research_task_id,workflow_run_id,kind,source_id,created_at)
 SELECT wr.research_task_id,wr.id,'message',?,? FROM workflow_conversations wc JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
 WHERE wc.conversation_id=? AND wr.research_task_id<>''`, messageID, formatTime(at), conversationID); err != nil {
		return chat.Run{}, err
	}
	// Put the empty final-answer placeholder after the latest human answer.
	if _, err = tx.ExecContext(ctx, `UPDATE messages SET created_at=?,updated_at=? WHERE id=(SELECT assistant_message_id FROM runs WHERE id=?) AND status='streaming'`, formatTime(at.Add(time.Nanosecond)), formatTime(at), response.RunID); err != nil {
		return chat.Run{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE conversations SET updated_at=? WHERE id=?`, formatTime(at), conversationID); err != nil {
		return chat.Run{}, err
	}
	if err = tx.Commit(); err != nil {
		return chat.Run{}, err
	}
	return r.Get(ctx, response.RunID)
}

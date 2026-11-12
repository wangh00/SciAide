package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Recheck discussion ownership at the actual execution write, not just in the
// UI/service projection. A concurrent chat must finish before route adoption.
func validateResearchDiscussionAdoption(ctx context.Context, tx *sql.Tx, starterID string) error {
	var applicable, valid bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(wr.inputs_json,'$.starter_context.discussionUserMessageId'),'')<>'',
		NOT EXISTS(SELECT 1 FROM workflow_runs newer WHERE newer.project_id=wr.project_id AND newer.research_task_id=wr.research_task_id
			AND (newer.created_at>wr.created_at OR (newer.created_at=wr.created_at AND newer.id>wr.id)))
		AND NOT EXISTS(SELECT 1 FROM workflow_runs same JOIN workflow_conversations wc ON wc.workflow_run_id=same.id JOIN runs cr ON cr.conversation_id=wc.conversation_id
			WHERE same.project_id=wr.project_id AND same.research_task_id=wr.research_task_id AND cr.status IN ('queued','running','waiting_approval')
			AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs b WHERE b.chat_run_id=cr.id))
		FROM workflow_runs wr WHERE wr.id=?`, starterID).Scan(&applicable, &valid)
	if err != nil {
		return err
	}
	if applicable && !valid {
		return fmt.Errorf("需求讨论尚未结束或已准备新路线，请待回复完成后确认当前路线")
	}
	return nil
}

// Require the current real invocation and latest human message, not model IDs.
func (r *WorkflowRuntimeRepository) ResearchPlanningChat(ctx context.Context, chatRunID, callID string) (conversation.Conversation, string, error) {
	var conversationID, humanID string
	err := r.db.QueryRowContext(ctx, `SELECT cr.conversation_id,(SELECT id FROM messages WHERE conversation_id=cr.conversation_id AND role='user' ORDER BY rowid DESC LIMIT 1)
		FROM runs cr JOIN tool_calls tc ON tc.run_id=cr.id
		WHERE cr.id=? AND cr.status='running' AND tc.id=? AND tc.tool_name=? AND tc.status='running'
		AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs WHERE chat_run_id=cr.id)
		AND cr.id=(SELECT run_id FROM messages WHERE conversation_id=cr.conversation_id AND role='user' ORDER BY rowid DESC LIMIT 1)`, chatRunID, callID, workflow.ResearchPlanPrepareTool).Scan(&conversationID, &humanID)
	if err != nil {
		return conversation.Conversation{}, "", fmt.Errorf("需求或对话已变化，请在当前讨论中重新准备路线")
	}
	value, err := NewConversationRepository(r.db).GetConversation(ctx, conversationID)
	if err != nil {
		return value, "", err
	}
	run, err := NewRunRepository(r.db).Get(ctx, chatRunID)
	value.ModelProfileID, value.ModelID, value.ReasoningLevel = run.ModelProfileID, run.ModelID, run.RequestedReasoningLevel
	value.PermissionMode = run.PermissionMode
	return value, humanID, err
}

func (r *WorkflowRuntimeRepository) ResearchHumanMessages(ctx context.Context, conversationID string) ([]conversation.Message, error) {
	return NewConversationRepository(r.db).ListMessages(ctx, conversationID, -1)
}

// Read the same task across immutable runs without copying human messages.
func (r *WorkflowRuntimeRepository) ResearchTaskDiscussionMessages(ctx context.Context, conversationID string) ([]conversation.Message, error) {
	var projectID, taskID string
	err := r.db.QueryRowContext(ctx, `SELECT wr.project_id,COALESCE(wr.research_task_id,'') FROM workflow_conversations wc JOIN workflow_runs wr ON wr.id=wc.workflow_run_id WHERE wc.conversation_id=?`, conversationID).Scan(&projectID, &taskID)
	if err != nil {
		return nil, err
	}
	// Legacy unbound runs are not a shared task. Never merge all empty IDs.
	if taskID == "" {
		return r.ResearchHumanMessages(ctx, conversationID)
	}
	return NewConversationRepository(r.db).listMessagesWhere(ctx, `m.conversation_id IN (
		SELECT wc.conversation_id FROM workflow_conversations wc JOIN workflow_runs wr ON wr.id=wc.workflow_run_id WHERE wr.project_id=? AND wr.research_task_id=?)`, []any{projectID, taskID}, -1)
}

func (r *WorkflowRuntimeRepository) ResearchStarterPlanCanAdopt(ctx context.Context, runID, conversationID, humanID string) (bool, error) {
	if humanID == "" {
		return true, nil
	}
	var valid bool
	// An explanatory question or greeting does not change the frozen plan.
	// Preparing a changed plan creates a new current run (checked by adoption).
	// While a discussion is active, wait for its semantic tool decisions first.
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages m WHERE m.id=? AND m.conversation_id=? AND m.role='user'
		AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs b WHERE b.chat_run_id=m.run_id)
		AND EXISTS(SELECT 1 FROM workflow_conversations source JOIN workflow_runs old ON old.id=source.workflow_run_id
			JOIN workflow_runs current ON current.id=? WHERE source.conversation_id=m.conversation_id AND current.project_id=old.project_id AND current.research_task_id=old.research_task_id)
		AND NOT EXISTS(SELECT 1 FROM runs cr WHERE cr.status IN ('queued','running','waiting_approval') AND cr.conversation_id IN (
			SELECT wc.conversation_id FROM workflow_conversations wc JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
			WHERE wr.id=? OR wc.conversation_id=?)))`, humanID, conversationID, runID, runID, conversationID).Scan(&valid)
	return valid, err
}

// Only expose files actually sent in human messages of this exact task.
// Keep their original conversation owner; this is reading, never adoption.
func (r *WorkflowRuntimeRepository) TaskDiscussionAttachmentOwner(ctx context.Context, runID, attachmentID string) (string, error) {
	var owner string
	err := r.db.QueryRowContext(ctx, `SELECT m.conversation_id FROM workflow_runs current
		JOIN workflow_runs prior ON prior.project_id=current.project_id AND (prior.id=current.id OR (COALESCE(current.research_task_id,'')<>'' AND prior.research_task_id=current.research_task_id))
		JOIN workflow_conversations wc ON wc.workflow_run_id=prior.id JOIN messages m ON m.conversation_id=wc.conversation_id
		JOIN message_parts mp ON mp.message_id=m.id WHERE current.id=? AND m.role='user' AND mp.part_type='media'
		AND json_extract(mp.payload_json,'$.attachmentId')=? AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs b WHERE b.chat_run_id=m.run_id)
		ORDER BY m.rowid DESC LIMIT 1`, runID, attachmentID).Scan(&owner)
	return owner, err
}

func (r *WorkflowRuntimeRepository) TaskDiscussionAttachmentIDs(ctx context.Context, runID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT json_extract(mp.payload_json,'$.attachmentId') FROM workflow_runs current
		JOIN workflow_runs prior ON prior.project_id=current.project_id AND (prior.id=current.id OR (COALESCE(current.research_task_id,'')<>'' AND prior.research_task_id=current.research_task_id))
		JOIN workflow_conversations wc ON wc.workflow_run_id=prior.id JOIN messages m ON m.conversation_id=wc.conversation_id
		JOIN message_parts mp ON mp.message_id=m.id WHERE current.id=? AND m.role='user' AND mp.part_type='media'
		AND json_extract(mp.payload_json,'$.attachmentId') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs b WHERE b.chat_run_id=m.run_id)
		ORDER BY m.rowid,mp.ordinal`, runID)
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

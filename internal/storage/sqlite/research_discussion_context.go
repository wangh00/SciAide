package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

const researchDiscussionContextSelect = `SELECT d.project_id,d.research_task_id,d.conversation_id,
 COALESCE((SELECT primary_discussion.conversation_id FROM research_discussion_conversations primary_discussion
   WHERE primary_discussion.conversation_id=t.discussion_conversation_id
     AND primary_discussion.project_id=d.project_id AND primary_discussion.research_task_id=d.research_task_id),
   (SELECT first.conversation_id FROM research_discussion_conversations first
    WHERE first.project_id=d.project_id AND first.research_task_id=d.research_task_id ORDER BY first.created_at,first.rowid LIMIT 1)),
 d.source_workflow_run_id,t.research_question,
 EXISTS(SELECT 1 FROM research_exploration_context active WHERE active.conversation_id=d.conversation_id)
 FROM research_discussion_conversations d
 JOIN research_tasks t ON t.id=d.research_task_id AND t.project_id=d.project_id
 JOIN conversations c ON c.id=d.conversation_id AND c.project_id=d.project_id`

func scanResearchDiscussion(row workflowScanner) (workflow.ResearchDiscussionContext, bool, error) {
	var value workflow.ResearchDiscussionContext
	err := row.Scan(&value.ProjectID, &value.ResearchTaskID, &value.ConversationID, &value.PrimaryConversationID, &value.SourceWorkflowRunID, &value.ResearchQuestion, &value.CanExplore)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func (r *WorkflowRuntimeRepository) ResearchDiscussionForConversation(ctx context.Context, conversationID string) (workflow.ResearchDiscussionContext, bool, error) {
	return scanResearchDiscussion(r.db.QueryRowContext(ctx, researchDiscussionContextSelect+` WHERE d.conversation_id=?`, strings.TrimSpace(conversationID)))
}

func (r *WorkflowRuntimeRepository) ResearchDiscussionForChat(ctx context.Context, chatRunID string) (workflow.ResearchDiscussionContext, bool, error) {
	return scanResearchDiscussion(r.db.QueryRowContext(ctx, researchDiscussionContextSelect+`
 JOIN runs cr ON cr.conversation_id=d.conversation_id WHERE cr.id=?
 AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs b WHERE b.chat_run_id=cr.id)`, strings.TrimSpace(chatRunID)))
}

type researchPreparationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Reused before preparing a snapshot and inside CreateResearchRun's write
// transaction. Reading/discussing stays available while planning; creating a
// second planner does not. A late proposal may not borrow another human turn.
func validateResearchPlanPreparation(ctx context.Context, db researchPreparationQuery, projectID, taskID, chatRunID, callID, humanID, priorID string) error {
	var valid bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM research_exploration_chats d
 JOIN tool_calls tc ON tc.run_id=d.chat_run_id AND tc.workflow_run_id IS NULL
 JOIN research_discussion_latest_human latest ON latest.project_id=d.project_id AND latest.research_task_id=d.research_task_id
 JOIN messages human ON human.id=latest.user_message_id AND human.conversation_id=d.conversation_id
   AND human.run_id=d.chat_run_id AND human.role='user' AND human.status='complete'
 WHERE d.project_id=? AND d.research_task_id=? AND d.chat_run_id=? AND d.chat_status='running'
   AND tc.id=? AND tc.tool_name=? AND tc.status='running' AND human.id=?
   AND (
     ?=(SELECT wr.id FROM workflow_runs wr WHERE wr.project_id=d.project_id AND wr.research_task_id=d.research_task_id
       ORDER BY wr.created_at DESC,wr.id DESC LIMIT 1)
     OR (?='' AND d.source_workflow_run_id='' AND NOT EXISTS(SELECT 1 FROM workflow_runs existing
       WHERE existing.project_id=d.project_id AND existing.research_task_id=d.research_task_id)))
   AND NOT EXISTS(SELECT 1 FROM workflow_runs busy WHERE busy.project_id=d.project_id AND busy.research_task_id=d.research_task_id
     AND busy.status NOT IN ('completed','failed','cancelled','interrupted')))`, projectID, taskID, chatRunID, callID, workflow.ResearchPlanPrepareTool, humanID, priorID, priorID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("已有规划正在进行，或研究讨论/用户需求已变化；可继续讨论，待当前规划结束后按最新需求准备候选路线")
	}
	return nil
}

func (r *WorkflowRuntimeRepository) ValidateResearchPlanPreparation(ctx context.Context, projectID, taskID, chatRunID, callID, humanID, priorID string) error {
	return validateResearchPlanPreparation(ctx, r.db, projectID, taskID, chatRunID, callID, humanID, priorID)
}

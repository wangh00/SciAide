package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

// CreateResearchDiscussion atomically creates the durable task, its primary
// conversation and the exploration binding. An empty source Run is accurate:
// this research task has never had a planner or compatibility Workflow.
func (r *WorkflowRuntimeRepository) CreateResearchDiscussion(ctx context.Context, entry workflow.ResearchDiscussionEntry) error {
	entry.ProjectID = strings.TrimSpace(entry.ProjectID)
	entry.ResearchTaskID = strings.TrimSpace(entry.ResearchTaskID)
	entry.ConversationID = strings.TrimSpace(entry.ConversationID)
	entry.Title = strings.TrimSpace(entry.Title)
	entry.ResearchQuestion = strings.TrimSpace(entry.ResearchQuestion)
	if entry.ProjectID == "" || entry.ResearchTaskID == "" || entry.ConversationID == "" || entry.Title == "" || entry.ResearchQuestion == "" || entry.ModelProfileID == "" || entry.ModelID == "" || entry.CreatedAt.IsZero() {
		return fmt.Errorf("invalid direct research discussion entry")
	}
	if !entry.PermissionMode.Valid() {
		return fmt.Errorf("invalid research conversation permission mode")
	}
	referenceIDs := entry.ReferenceAttachmentIDs
	if referenceIDs == nil {
		referenceIDs = []string{}
	}
	encodedReferences, err := json.Marshal(referenceIDs)
	if err != nil {
		return fmt.Errorf("encode direct research references: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// origin_kind is a historical display label, never an execution or access
	// capability. Exploration authority comes from the explicit binding below.
	_, err = tx.ExecContext(ctx, `INSERT INTO research_tasks(
		id,project_id,title,research_question,origin_kind,status,created_at,updated_at,discussion_phase,discussion_conversation_id,exploration_reference_ids_json
	) VALUES (?,?,?,?,?,'active',?,?,'exploring',?,?)`, entry.ResearchTaskID, entry.ProjectID, entry.Title, entry.ResearchQuestion,
		researchtask.OriginAI, formatTime(entry.CreatedAt), formatTime(entry.CreatedAt), entry.ConversationID, string(encodedReferences))
	if err != nil {
		return fmt.Errorf("insert direct research task: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO conversations(
		id,project_id,title,model_profile_id,model_id,permission_mode,reasoning_level,created_at,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?)`, entry.ConversationID, entry.ProjectID, "Research: "+entry.Title, entry.ModelProfileID,
		entry.ModelID, entry.PermissionMode, entry.ReasoningLevel, formatTime(entry.CreatedAt), formatTime(entry.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert direct research conversation: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO research_discussion_conversations(
		conversation_id,project_id,research_task_id,source_workflow_run_id,created_at
	) VALUES (?,?,?,'',?)`, entry.ConversationID, entry.ProjectID, entry.ResearchTaskID, formatTime(entry.CreatedAt))
	if err != nil {
		return fmt.Errorf("bind direct research discussion: %w", err)
	}
	return tx.Commit()
}

const researchDiscussionEntrySelect = `SELECT t.created_at,c.permission_mode,c.reasoning_level,
 t.project_id,t.id,c.id,t.title,t.research_question,c.model_profile_id,c.model_id,t.exploration_reference_ids_json,
 (t.status='active' AND t.archived_at IS NULL AND EXISTS(SELECT 1 FROM research_exploration_context active WHERE active.conversation_id=c.id)),d.source_workflow_run_id
 FROM research_tasks t
 JOIN research_discussion_conversations d ON d.project_id=t.project_id AND d.research_task_id=t.id
 JOIN conversations c ON c.id=d.conversation_id AND c.project_id=t.project_id`

func scanResearchDiscussionEntry(row workflowScanner) (workflow.ResearchDiscussionEntry, bool, error) {
	var entry workflow.ResearchDiscussionEntry
	var created, encodedReferences string
	err := row.Scan(&created, &entry.PermissionMode, &entry.ReasoningLevel, &entry.ProjectID, &entry.ResearchTaskID,
		&entry.ConversationID, &entry.Title, &entry.ResearchQuestion, &entry.ModelProfileID, &entry.ModelID, &encodedReferences, &entry.CanExplore, &entry.SourceWorkflowRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return entry, false, nil
	}
	if err != nil {
		return entry, false, err
	}
	if err := json.Unmarshal([]byte(encodedReferences), &entry.ReferenceAttachmentIDs); err != nil {
		return workflow.ResearchDiscussionEntry{}, false, fmt.Errorf("decode direct research references: %w", err)
	}
	if entry.ReferenceAttachmentIDs == nil {
		entry.ReferenceAttachmentIDs = []string{}
	}
	entry.CreatedAt, err = parseTime(created)
	return entry, err == nil, err
}

func (r *WorkflowRuntimeRepository) ResearchDiscussionEntryForTask(ctx context.Context, projectID, taskID string) (workflow.ResearchDiscussionEntry, bool, error) {
	return scanResearchDiscussionEntry(r.db.QueryRowContext(ctx, researchDiscussionEntrySelect+` WHERE t.project_id=? AND t.id=? AND t.discussion_conversation_id=d.conversation_id`, strings.TrimSpace(projectID), strings.TrimSpace(taskID)))
}

func (r *WorkflowRuntimeRepository) ResearchDiscussionEntryForConversation(ctx context.Context, conversationID string) (workflow.ResearchDiscussionEntry, bool, error) {
	return scanResearchDiscussionEntry(r.db.QueryRowContext(ctx, researchDiscussionEntrySelect+` WHERE d.conversation_id=?`, strings.TrimSpace(conversationID)))
}

package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/modelcap"
)

// StartResearchDiscussionCommand creates the research aggregate and its primary
// human conversation directly. No Workflow or planner Run is involved.
type StartResearchDiscussionCommand struct {
	ProjectID              string                      `json:"projectId"`
	ResearchIdea           string                      `json:"researchIdea"`
	ModelProfileID         string                      `json:"modelProfileId"`
	ModelID                string                      `json:"modelId"`
	ReferenceAttachmentIDs []string                    `json:"referenceAttachmentIds,omitempty"`
	PermissionMode         conversation.PermissionMode `json:"permissionMode,omitempty"`
	ReasoningLevel         modelcap.ReasoningLevel     `json:"reasoningLevel,omitempty"`
}

// ResearchDiscussionEntry is a task/conversation result, not a synthetic Run.
// Workflow execution remains optional and receives a separate immutable record.
type ResearchDiscussionEntry struct {
	CreatedAt              time.Time                   `json:"createdAt"`
	PermissionMode         conversation.PermissionMode `json:"permissionMode"`
	ReasoningLevel         modelcap.ReasoningLevel     `json:"reasoningLevel"`
	ProjectID              string                      `json:"projectId"`
	ResearchTaskID         string                      `json:"researchTaskId"`
	ConversationID         string                      `json:"conversationId"`
	Title                  string                      `json:"title"`
	ResearchQuestion       string                      `json:"researchQuestion"`
	ModelProfileID         string                      `json:"modelProfileId"`
	ModelID                string                      `json:"modelId"`
	ReferenceAttachmentIDs []string                    `json:"referenceAttachmentIds"`
	CanExplore             bool                        `json:"canExplore"`
	SourceWorkflowRunID    string                      `json:"sourceWorkflowRunId,omitempty"`
}

type ResearchTaskSession struct {
	Entry                 ResearchDiscussionEntry `json:"entry"`
	CurrentRun            *RunDetail              `json:"currentRun,omitempty"`
	CurrentRunUnavailable bool                    `json:"currentRunUnavailable,omitempty"`
}

type researchDiscussionCreator interface {
	CreateResearchDiscussion(context.Context, ResearchDiscussionEntry) error
}

func researchDiscussionTitle(question string) string {
	title := strings.TrimSpace(strings.SplitN(question, "\n", 2)[0])
	runes := []rune(title)
	if len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return title
}

func (s *StarterService) StartDiscussion(ctx context.Context, command StartResearchDiscussionCommand) (ResearchDiscussionEntry, error) {
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.ResearchIdea = strings.TrimSpace(command.ResearchIdea)
	command.ModelProfileID = strings.TrimSpace(command.ModelProfileID)
	command.ModelID = strings.TrimSpace(command.ModelID)
	if command.ProjectID == "" || command.ResearchIdea == "" {
		return ResearchDiscussionEntry{}, fmt.Errorf("项目和研究想法不能为空")
	}
	if len([]rune(command.ResearchIdea)) > maxResearchIdeaRunes {
		return ResearchDiscussionEntry{}, fmt.Errorf("研究想法不能超过 %d 个字符", maxResearchIdeaRunes)
	}
	if command.ModelProfileID == "" || command.ModelID == "" {
		return ResearchDiscussionEntry{}, fmt.Errorf("请先选择用于科研协作的 AI 模型")
	}
	if command.PermissionMode == "" {
		command.PermissionMode = conversation.PermissionFullAccess
	}
	if !command.PermissionMode.Valid() {
		return ResearchDiscussionEntry{}, fmt.Errorf("invalid research permission mode")
	}
	if !command.ReasoningLevel.Valid() {
		command.ReasoningLevel = modelcap.DefaultReasoningLevel
	}
	if _, err := s.projects.Get(ctx, command.ProjectID); err != nil {
		return ResearchDiscussionEntry{}, err
	}
	referenceIDs := make([]string, 0, len(command.ReferenceAttachmentIDs))
	if len(command.ReferenceAttachmentIDs) > 0 {
		loader, ok := s.attachments.(interface {
			SelectReferenceMaterials(context.Context, string, string, []string) ([]attachment.Attachment, error)
		})
		if !ok {
			return ResearchDiscussionEntry{}, fmt.Errorf("参考资料服务未配置")
		}
		materials, err := loader.SelectReferenceMaterials(ctx, command.ProjectID, "", command.ReferenceAttachmentIDs)
		if err != nil {
			return ResearchDiscussionEntry{}, err
		}
		for _, material := range materials {
			referenceIDs = append(referenceIDs, material.ID)
		}
	}
	creator, ok := s.runtime.repository.(researchDiscussionCreator)
	if !ok {
		return ResearchDiscussionEntry{}, fmt.Errorf("direct research discussion storage is not configured")
	}
	taskID, err := s.runtime.newID()
	if err != nil {
		return ResearchDiscussionEntry{}, err
	}
	conversationID, err := s.runtime.newID()
	if err != nil {
		return ResearchDiscussionEntry{}, err
	}
	now := s.runtime.now().UTC()
	entry := ResearchDiscussionEntry{
		CreatedAt: now, PermissionMode: command.PermissionMode, ReasoningLevel: command.ReasoningLevel, CanExplore: true,
		ProjectID: command.ProjectID, ResearchTaskID: taskID, ConversationID: conversationID,
		Title: researchDiscussionTitle(command.ResearchIdea), ResearchQuestion: command.ResearchIdea,
		ModelProfileID: command.ModelProfileID, ModelID: command.ModelID, ReferenceAttachmentIDs: referenceIDs,
	}
	if err := creator.CreateResearchDiscussion(ctx, entry); err != nil {
		return ResearchDiscussionEntry{}, err
	}
	return entry, nil
}

// Once the discussion binding is known, a failed run read must not erase it
// or look like proof that the task has never prepared a candidate.
func (s *RuntimeService) researchTaskSessionForEntry(ctx context.Context, entry ResearchDiscussionEntry) ResearchTaskSession {
	session := ResearchTaskSession{Entry: entry}
	reader, ok := s.repository.(interface {
		FindLatestTaskRun(context.Context, string, string) (Run, bool, error)
	})
	if !ok {
		session.CurrentRunUnavailable = true
		return session
	}
	latest, found, err := reader.FindLatestTaskRun(ctx, entry.ProjectID, entry.ResearchTaskID)
	if err != nil {
		session.CurrentRunUnavailable = true
		return session
	}
	if found {
		detail, err := s.repository.GetRun(ctx, entry.ProjectID, latest.ID)
		if err != nil || detail.Run.ID != latest.ID || detail.Run.ProjectID != entry.ProjectID || detail.Run.ResearchTaskID != entry.ResearchTaskID {
			session.CurrentRunUnavailable = true
			return session
		}
		session.CurrentRun = &detail
	}
	return session
}

func (s *RuntimeService) ResearchTaskSession(ctx context.Context, projectID, taskID string) (ResearchTaskSession, error) {
	reader, ok := s.repository.(interface {
		ResearchDiscussionEntryForTask(context.Context, string, string) (ResearchDiscussionEntry, bool, error)
	})
	if !ok {
		return ResearchTaskSession{}, fmt.Errorf("research task session storage is not configured")
	}
	entry, found, err := reader.ResearchDiscussionEntryForTask(ctx, strings.TrimSpace(projectID), strings.TrimSpace(taskID))
	if err != nil {
		return ResearchTaskSession{}, err
	}
	if !found {
		return ResearchTaskSession{}, fmt.Errorf("research task session not found")
	}
	return s.researchTaskSessionForEntry(ctx, entry), nil
}

// A nil session with no error is a confirmed absence, never a failed read.
func (s *RuntimeService) ResearchTaskSessionByConversation(ctx context.Context, conversationID string) (*ResearchTaskSession, error) {
	reader, ok := s.repository.(interface {
		ResearchDiscussionEntryForConversation(context.Context, string) (ResearchDiscussionEntry, bool, error)
	})
	if !ok {
		return nil, fmt.Errorf("research task session storage is not configured")
	}
	entry, found, err := reader.ResearchDiscussionEntryForConversation(ctx, strings.TrimSpace(conversationID))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	session := s.researchTaskSessionForEntry(ctx, entry)
	return &session, nil
}

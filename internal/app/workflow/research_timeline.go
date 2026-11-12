package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/wangh00/SciAide/internal/app/conversation"
)

type ResearchTimelineQuery struct {
	ProjectID string `json:"projectId"`
	TaskID    string `json:"taskId"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type ResearchTimelineEntry struct {
	ID             string                    `json:"id"`
	Sequence       string                    `json:"sequence"`
	RunID          string                    `json:"runId"`
	ConversationID string                    `json:"conversationId"`
	Kind           string                    `json:"kind"`
	EventType      string                    `json:"eventType,omitempty"`
	CreatedAt      string                    `json:"createdAt"`
	Snapshot       json.RawMessage           `json:"snapshot"`
	Message        *conversation.Message     `json:"message,omitempty"`
	Tool           *ToolActivity             `json:"tool,omitempty"`
	Proposal       *ResearchRevisionProposal `json:"proposal,omitempty"`
	Step           *Step                     `json:"step,omitempty"`
	Active         bool                      `json:"active"`
}

type ResearchTimelinePage struct {
	TaskID         string                  `json:"taskId"`
	LatestRunID    string                  `json:"latestRunId"`
	ConversationID string                  `json:"conversationId"`
	Entries        []ResearchTimelineEntry `json:"entries"`
	NextBefore     string                  `json:"nextBefore,omitempty"`
	HasMore        bool                    `json:"hasMore"`
}

type ResearchTimelineRepository interface {
	ReadResearchTimeline(context.Context, ResearchTimelineQuery) (ResearchTimelinePage, error)
}

// CancelledAIRecoveryStep identifies the same explicit recovery supported by
// Retry. Future steps also become cancelled, but only the current AI stage is
// recoverable; a cancelled tool must never acquire a replay action.
func CancelledAIRecoveryStep(detail RunDetail) *Step {
	if detail.Run.Status != RunCancelled {
		return nil
	}
	step := currentStep(detail)
	if step == nil || step.Status != StepCancelled || (step.NodeKind != NodeAgentStage && step.NodeKind != NodeAIAnalysis) {
		return nil
	}
	node := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
	if node.Kind != step.NodeKind {
		return nil
	}
	return step
}

func (s *RuntimeService) ResearchTimeline(ctx context.Context, query ResearchTimelineQuery) (ResearchTimelinePage, error) {
	query.ProjectID, query.TaskID = strings.TrimSpace(query.ProjectID), strings.TrimSpace(query.TaskID)
	if query.ProjectID == "" || query.TaskID == "" {
		return ResearchTimelinePage{}, fmt.Errorf("project and research task are required")
	}
	reader, ok := s.repository.(ResearchTimelineRepository)
	if !ok {
		return ResearchTimelinePage{}, fmt.Errorf("research timeline storage is not configured")
	}
	return reader.ReadResearchTimeline(ctx, query)
}

func (s *RuntimeService) ResearchTimelineRevision(ctx context.Context, query ResearchTimelineQuery) (string, error) {
	query.ProjectID, query.TaskID = strings.TrimSpace(query.ProjectID), strings.TrimSpace(query.TaskID)
	if query.ProjectID == "" || query.TaskID == "" {
		return "", fmt.Errorf("project and research task are required")
	}
	for _, cursor := range []string{query.Before, query.After} {
		if cursor != "" {
			value, err := strconv.ParseInt(cursor, 10, 64)
			if err != nil || value <= 0 || query.Before != "" && query.After != "" {
				return "", fmt.Errorf("invalid timeline cursor")
			}
		}
	}
	if reader, ok := s.repository.(interface {
		ResearchTimelineRevision(context.Context, string, string, string, string, int) (string, error)
	}); ok {
		return reader.ResearchTimelineRevision(ctx, query.ProjectID, query.TaskID, query.Before, query.After, query.Limit)
	}
	return "", nil
}

package workflow

import (
	"context"
	"encoding/json"
	"fmt"
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

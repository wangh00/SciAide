package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Submission provenance is mechanical history, not evidence support, intent
// inference or a claim that the user confirmed any model-authored field.
type ResearchWorkingStateFieldSource struct {
	Field               string                       `json:"field"`
	Status              string                       `json:"status"`
	StateID             string                       `json:"stateId,omitempty"`
	Version             int                          `json:"version,omitempty"`
	SourceChatRunID     string                       `json:"sourceChatRunId,omitempty"`
	SourceUserMessageID string                       `json:"sourceUserMessageId,omitempty"`
	SourceCallID        string                       `json:"sourceCallId,omitempty"`
	CreatedAt           *time.Time                   `json:"createdAt,omitempty"`
	Basis               []ResearchWorkingBasisSource `json:"basis,omitempty"`
}

type ResearchWorkingStateProvenance struct {
	ProjectID      string                            `json:"projectId"`
	ResearchTaskID string                            `json:"researchTaskId"`
	StateID        string                            `json:"stateId"`
	Version        int                               `json:"version"`
	Status         string                            `json:"status"`
	Fields         []ResearchWorkingStateFieldSource `json:"fields"`
}

type researchWorkingStateProvenanceReader interface {
	GetResearchWorkingStateProvenance(context.Context, string, string, int) (ResearchWorkingStateProvenance, error)
}

func (s *RuntimeService) GetResearchWorkingStateProvenance(ctx context.Context, projectID, taskID string, expectedVersion int) (ResearchWorkingStateProvenance, error) {
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" || expectedVersion < 1 {
		return ResearchWorkingStateProvenance{}, fmt.Errorf("需要当前项目、科研任务和工作记录版本")
	}
	reader, ok := s.repository.(researchWorkingStateProvenanceReader)
	if !ok {
		return ResearchWorkingStateProvenance{}, fmt.Errorf("研究工作记录字段来源读取未配置")
	}
	return reader.GetResearchWorkingStateProvenance(ctx, projectID, taskID, expectedVersion)
}

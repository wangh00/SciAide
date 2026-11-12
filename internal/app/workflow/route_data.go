package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/document"
)

// A draft choice, not an adopted input or permission to execute. Only tables
// actually sent by a human in this task are offered; the original owner remains.
type ResearchRouteDataCandidate struct {
	AttachmentID string   `json:"attachmentId"`
	Name         string   `json:"name"`
	SizeBytes    int64    `json:"sizeBytes"`
	SHA256       string   `json:"sha256"`
	PortNames    []string `json:"portNames"`
}

type StageResearchRouteDataCommand struct {
	ProjectID    string `json:"projectId"`
	StarterRunID string `json:"starterRunId"`
	RouteID      string `json:"routeId"`
	PortName     string `json:"portName"`
	AttachmentID string `json:"attachmentId"`
}

type routeDataAttachment struct {
	ResearchRouteDataCandidate
	conversationID string
}

func routeDataPorts(version Version) []Port {
	if len(version.RuntimeInputs) > 0 {
		return version.RuntimeInputs
	}
	return version.Definition.Inputs
}

func routeDataKindMatches(kind string, value attachment.Attachment) bool {
	ext := strings.ToLower(filepath.Ext(value.OriginalName))
	delimited := (ext == ".csv" || ext == ".tsv") && value.Format == document.FormatCSV
	xlsx := ext == ".xlsx" && value.Format == document.FormatXLSX
	return kind == "tabular" && (delimited || xlsx) || kind == "delimited" && delimited || kind == "xlsx" && xlsx
}

func (s *StarterService) routeDataCandidates(ctx context.Context, pending AdoptResearchRouteResult) ([]routeDataAttachment, error) {
	result := []routeDataAttachment{}
	reader, ok := s.runtime.repository.(interface {
		ResearchTaskDiscussionMessages(context.Context, string) ([]conversation.Message, error)
	})
	loader, canLoad := s.attachments.(interface {
		Get(context.Context, string) (attachment.Attachment, error)
	})
	if !ok || !canLoad {
		return result, nil // Older/custom hosts keep the existing file picker.
	}
	detail, err := s.runtime.repository.GetRun(ctx, pending.Workflow.Workflow.ProjectID, pending.StarterRunID)
	if err != nil {
		return nil, err
	}
	messages, err := reader.ResearchTaskDiscussionMessages(ctx, detail.Run.ConversationID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, message := range messages {
		if message.Internal || message.Role != conversation.RoleUser || message.Status != conversation.MessageComplete {
			continue
		}
		for _, part := range message.Parts {
			var ref attachment.MessageReference
			if part.Type != "media" || json.Unmarshal(part.Payload, &ref) != nil || ref.AttachmentID == "" || seen[ref.AttachmentID] {
				continue
			}
			seen[ref.AttachmentID] = true
			value, err := loader.Get(ctx, ref.AttachmentID)
			if errors.Is(err, sql.ErrNoRows) {
				continue // A removed historical attachment must not hide valid files.
			}
			if err != nil {
				return nil, err
			}
			if value.ProjectID != detail.Run.ProjectID || value.ScopeKind != attachment.ScopeConversation || value.ResearchTaskID != "conversation:"+message.ConversationID || value.Status != attachment.StatusReady {
				continue
			}
			ports := []string{}
			for _, port := range routeDataPorts(pending.Workflow.Version) {
				if (port.Type == TypeArray || port.Type == TypeString) && routeDataKindMatches(port.FileKind, value) {
					ports = append(ports, port.Name)
				}
			}
			if len(ports) > 0 {
				result = append(result, routeDataAttachment{ResearchRouteDataCandidate: ResearchRouteDataCandidate{AttachmentID: value.ID, Name: value.OriginalName, SizeBytes: value.SizeBytes, SHA256: value.SHA256, PortNames: ports}, conversationID: message.ConversationID})
			}
		}
	}
	return result, nil
}

func (s *StarterService) pendingRouteData(ctx context.Context, projectID, starterRunID, routeID string) (AdoptResearchRouteResult, error) {
	pending, err := s.PendingAdoption(ctx, strings.TrimSpace(projectID), strings.TrimSpace(starterRunID))
	if err != nil {
		return AdoptResearchRouteResult{}, err
	}
	if pending == nil || pending.RouteID != strings.TrimSpace(routeID) {
		return AdoptResearchRouteResult{}, fmt.Errorf("当前路线未采用、已执行或已变化，请返回当前任务核对")
	}
	return *pending, nil
}

// Read-only discovery. No file copy, promotion, model call or formal run.
func (s *StarterService) ResearchRouteDataCandidates(ctx context.Context, projectID, starterRunID, routeID string) ([]ResearchRouteDataCandidate, error) {
	pending, err := s.pendingRouteData(ctx, projectID, starterRunID, routeID)
	if err != nil {
		return nil, err
	}
	values, err := s.routeDataCandidates(ctx, pending)
	if err != nil {
		return nil, err
	}
	result := make([]ResearchRouteDataCandidate, 0, len(values))
	for _, value := range values {
		result = append(result, value.ResearchRouteDataCandidate)
	}
	return result, nil
}

// Uses the same owned-handle stager as confirmed data revisions. This prepares
// only the form draft; Runtime.Start still verifies/finalizes its exact inputs.
func (s *StarterService) StageResearchRouteData(ctx context.Context, command StageResearchRouteDataCommand) (RevisionDataFile, error) {
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()
	pending, err := s.pendingRouteData(ctx, command.ProjectID, command.StarterRunID, command.RouteID)
	if err != nil {
		return RevisionDataFile{}, err
	}
	values, err := s.routeDataCandidates(ctx, pending)
	if err != nil {
		return RevisionDataFile{}, err
	}
	for _, value := range values {
		if value.AttachmentID != strings.TrimSpace(command.AttachmentID) {
			continue
		}
		for _, port := range routeDataPorts(pending.Workflow.Version) {
			if port.Name != strings.TrimSpace(command.PortName) {
				continue
			}
			compatible := false
			for _, name := range value.PortNames {
				compatible = compatible || name == port.Name
			}
			if !compatible {
				break
			}
			loader, ok := s.attachments.(revisionDataService)
			if !ok {
				return RevisionDataFile{}, fmt.Errorf("已上传数据衔接服务未配置，可使用原数据文件选择入口")
			}
			original, file, err := loader.OpenRevisionData(ctx, pending.Workflow.Workflow.ProjectID, value.conversationID, value.AttachmentID)
			if err != nil {
				return RevisionDataFile{}, err
			}
			defer file.Close()
			if original.ID != value.AttachmentID || original.SHA256 != value.SHA256 || original.SizeBytes != value.SizeBytes || !routeDataKindMatches(port.FileKind, original) {
				return RevisionDataFile{}, fmt.Errorf("已上传数据记录已变化，请重新核对当前文件")
			}
			project, err := s.projects.Get(ctx, pending.Workflow.Workflow.ProjectID)
			if err != nil {
				return RevisionDataFile{}, err
			}
			staged, err := s.workflows.stageOpenedInputFile(project, file, "", original.OriginalName, port.FileKind, pending.ResearchTaskID, original.SHA256)
			if err != nil {
				return RevisionDataFile{}, err
			}
			if staged.SizeBytes != original.SizeBytes {
				return RevisionDataFile{}, fmt.Errorf("已上传数据大小与保存记录不一致")
			}
			return RevisionDataFile{InputFile: staged, AttachmentID: original.ID}, nil
		}
	}
	return RevisionDataFile{}, fmt.Errorf("该文件不是本任务已发送且适用于所选输入的数据，请在当前卡片选择")
}

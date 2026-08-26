// Package researchworkflow coordinates SciAide's fixed P7 research workflow
// operations. It does not execute a Workflow itself: every operation remains a
// registered Tool and therefore still crosses policy, audit, and artifact
// boundaries in the normal runtime.
package researchworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const ReportToolName = "builtin.research.workflow.report"

type Discovery interface {
	Search(ctx context.Context, command research.DiscoverySearchCommand) (research.DiscoverySearchResult, error)
	GetCandidate(ctx context.Context, projectID, candidateID string) (research.Candidate, error)
	UpdateReview(ctx context.Context, command research.ReviewCommand) (research.Candidate, error)
	ImportCandidate(ctx context.Context, command research.ImportCandidateCommand) (research.ImportCandidateResult, error)
}

type Knowledge interface {
	SynchronizeAttachments(ctx context.Context, projectID string, attachmentIDs []string) ([]knowledge.Document, error)
	ReadEvidenceChunk(ctx context.Context, projectID, indexVersionID, documentID, attachmentID, chunkID string) (knowledge.EvidenceChunk, error)
}

type Bibliography interface {
	CitationSnapshotForAttachment(ctx context.Context, projectID, attachmentID string) (research.CitationSnapshot, error)
}

type Environments interface {
	Get(ctx context.Context, projectID string) (pythonenv.Environment, error)
	Create(ctx context.Context, projectID, preferredPath string, rebuild bool) (pythonenv.Environment, error)
	Verify(ctx context.Context, projectID string) (pythonenv.Environment, error)
}

type ToolCalls interface {
	ListBySubject(ctx context.Context, subjectKind tool.SubjectKind, subjectID string) ([]tool.Call, error)
}

type Artifacts interface {
	PublishWorkflowReport(ctx context.Context, command artifact.WorkflowReportCommand) (artifact.WorkflowReportResult, error)
}

type Service struct {
	discovery    Discovery
	knowledge    Knowledge
	bibliography Bibliography
	environments Environments
	toolCalls    ToolCalls
	artifacts    Artifacts
}

func New(discovery Discovery, knowledgeService Knowledge, bibliography Bibliography, environments Environments, toolCalls ToolCalls, artifacts Artifacts) (*Service, error) {
	if discovery == nil || knowledgeService == nil || bibliography == nil || environments == nil || toolCalls == nil || artifacts == nil {
		return nil, fmt.Errorf("research Workflow dependencies are required")
	}
	return &Service{discovery: discovery, knowledge: knowledgeService, bibliography: bibliography, environments: environments, toolCalls: toolCalls, artifacts: artifacts}, nil
}

func (s *Service) Search(ctx context.Context, projectID, query string, sourceIDs []string, limit int) (research.DiscoverySearchResult, error) {
	return s.discovery.Search(ctx, research.DiscoverySearchCommand{ProjectID: strings.TrimSpace(projectID), Query: query, SourceIDs: sourceIDs, Limit: limit})
}

type ImportedMaterial struct {
	CandidateID  string              `json:"candidateId"`
	Title        string              `json:"title"`
	ImportKind   research.ImportKind `json:"importKind"`
	AttachmentID string              `json:"attachmentId"`
}

func (s *Service) ImportSelected(ctx context.Context, projectID string, candidateIDs []string, mode research.MaterializeMode) ([]ImportedMaterial, error) {
	projectID = strings.TrimSpace(projectID)
	ids, err := uniqueIDs(candidateIDs, 20, "candidate")
	if err != nil {
		return nil, err
	}
	result := make([]ImportedMaterial, 0, len(ids))
	for _, candidateID := range ids {
		candidate, err := s.discovery.GetCandidate(ctx, projectID, candidateID)
		if err != nil {
			return nil, err
		}
		if candidate.ReviewStatus != research.ReviewIncluded {
			candidate, err = s.discovery.UpdateReview(ctx, research.ReviewCommand{ProjectID: projectID, CandidateID: candidateID, Status: research.ReviewIncluded, Note: candidate.Note})
			if err != nil {
				return nil, err
			}
		}
		imported, err := s.discovery.ImportCandidate(ctx, research.ImportCandidateCommand{ProjectID: projectID, CandidateID: candidateID, Mode: mode})
		if err != nil {
			return nil, fmt.Errorf("import candidate %s: %w", candidateID, err)
		}
		candidate = imported.Candidate
		if candidate.ID == "" {
			candidate, err = s.discovery.GetCandidate(ctx, projectID, candidateID)
			if err != nil {
				return nil, err
			}
		}
		attachmentID := candidate.AttachmentID
		if attachmentID == "" {
			attachmentID = imported.Attachment.ID
		}
		if candidate.ImportStatus != research.ImportImported || attachmentID == "" {
			return nil, fmt.Errorf("candidate %s did not reach the imported state", candidateID)
		}
		result = append(result, ImportedMaterial{CandidateID: candidate.ID, Title: candidate.Preferred.Title, ImportKind: candidate.ImportKind, AttachmentID: attachmentID})
	}
	return result, nil
}

func (s *Service) Synchronize(ctx context.Context, projectID string, attachmentIDs []string) ([]knowledge.Document, error) {
	ids, err := uniqueIDs(attachmentIDs, 20, "attachment")
	if err != nil {
		return nil, err
	}
	return s.knowledge.SynchronizeAttachments(ctx, strings.TrimSpace(projectID), ids)
}

func (s *Service) EnsureEnvironment(ctx context.Context, projectID string) (pythonenv.Environment, bool, error) {
	current, err := s.environments.Get(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return pythonenv.Environment{}, false, err
	}
	switch current.State {
	case pythonenv.StateReady:
		verified, err := s.environments.Verify(ctx, projectID)
		return verified, false, err
	case pythonenv.StateAbsent:
		created, err := s.environments.Create(ctx, projectID, "", false)
		return created, true, err
	case pythonenv.StateBroken:
		if current.Kind == pythonenv.KindExternal {
			verified, err := s.environments.Verify(ctx, projectID)
			return verified, false, err
		}
		created, err := s.environments.Create(ctx, projectID, "", true)
		return created, true, err
	default:
		return pythonenv.Environment{}, false, fmt.Errorf("project Python environment is busy in state %s", current.State)
	}
}

type ReportRequest struct {
	ProjectID       string
	WorkflowRunID   string
	ToolCallID      string
	IdempotencyKey  string
	Name            string
	Markdown        string
	Citations       []tool.CitationRef
	SourceArtifacts []tool.ArtifactRef
}

var citationMarker = regexp.MustCompile(`\[K-[0-9A-F]{12}\]`)

func (s *Service) PublishReport(ctx context.Context, request ReportRequest) (artifact.WorkflowReportResult, error) {
	request.ProjectID, request.WorkflowRunID, request.ToolCallID = strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.WorkflowRunID), strings.TrimSpace(request.ToolCallID)
	request.Name, request.Markdown = strings.TrimSpace(request.Name), strings.TrimSpace(request.Markdown)
	if request.ProjectID == "" || request.WorkflowRunID == "" || request.ToolCallID == "" || request.Name == "" || request.Markdown == "" {
		return artifact.WorkflowReportResult{}, fmt.Errorf("research Workflow report identity, name, and Markdown are required")
	}
	if len(request.Citations) == 0 || len(request.Citations) > 256 {
		return artifact.WorkflowReportResult{}, fmt.Errorf("research Workflow report requires 1-256 selected citations")
	}
	calls, err := s.toolCalls.ListBySubject(ctx, tool.SubjectWorkflowRun, request.WorkflowRunID)
	if err != nil {
		return artifact.WorkflowReportResult{}, err
	}
	completed := make([]tool.Call, 0, len(calls))
	offered := map[string][]string{}
	availableArtifacts := map[string]struct{}{}
	for _, call := range calls {
		if call.ID == request.ToolCallID || call.RunID != request.WorkflowRunID || tool.NormalizeSubjectKind(call.SubjectKind) != tool.SubjectWorkflowRun || call.Status != tool.CallCompleted || call.Result == nil || call.Result.Status != tool.ResultSuccess {
			continue
		}
		completed = append(completed, call)
		if call.ToolName == citation.KnowledgeToolName {
			for _, value := range call.Result.Citations {
				key, marshalErr := snapshotKey(value)
				if marshalErr != nil {
					return artifact.WorkflowReportResult{}, marshalErr
				}
				offered[key] = append(offered[key], call.ID)
			}
		}
		for _, value := range call.Result.Artifacts {
			if strings.TrimSpace(value.WorkspacePath) == "" {
				continue
			}
			key, marshalErr := snapshotKey(value)
			if marshalErr != nil {
				return artifact.WorkflowReportResult{}, marshalErr
			}
			availableArtifacts[key] = struct{}{}
		}
	}
	if len(completed) == 0 {
		return artifact.WorkflowReportResult{}, fmt.Errorf("research Workflow report has no completed upstream ToolCalls")
	}

	verifiedCitations := make([]artifact.Citation, 0, len(request.Citations))
	selectedMarkers := map[string]struct{}{}
	for _, selected := range request.Citations {
		key, err := snapshotKey(selected)
		if err != nil || len(offered[key]) == 0 {
			return artifact.WorkflowReportResult{}, fmt.Errorf("report citation was not returned by a completed knowledge search in this Workflow Run")
		}
		value, err := s.verifyCitation(ctx, request.ProjectID, request.WorkflowRunID, offered[key][0], selected)
		if err != nil {
			return artifact.WorkflowReportResult{}, err
		}
		if _, duplicate := selectedMarkers[value.Reference]; duplicate {
			return artifact.WorkflowReportResult{}, fmt.Errorf("report citations must be unique")
		}
		selectedMarkers[value.Reference] = struct{}{}
		verifiedCitations = append(verifiedCitations, value)
	}
	for marker := range selectedMarkers {
		if !strings.Contains(request.Markdown, marker) {
			return artifact.WorkflowReportResult{}, fmt.Errorf("report Markdown does not cite selected marker %s", marker)
		}
	}
	for _, marker := range citationMarker.FindAllString(request.Markdown, -1) {
		if _, selected := selectedMarkers[marker]; !selected {
			return artifact.WorkflowReportResult{}, fmt.Errorf("report Markdown contains unverified marker %s", marker)
		}
	}

	paths := make([]string, 0, len(request.SourceArtifacts))
	seenPaths := map[string]struct{}{}
	for _, value := range request.SourceArtifacts {
		key, err := snapshotKey(value)
		if err != nil {
			return artifact.WorkflowReportResult{}, err
		}
		if _, ok := availableArtifacts[key]; !ok {
			return artifact.WorkflowReportResult{}, fmt.Errorf("report source Artifact was not returned by a completed tool in this Workflow Run")
		}
		path := strings.TrimSpace(value.WorkspacePath)
		if path == "" {
			return artifact.WorkflowReportResult{}, fmt.Errorf("report source Artifact has no Workspace path")
		}
		if _, duplicate := seenPaths[path]; !duplicate {
			seenPaths[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	callIDs := make([]string, 0, len(completed))
	for _, call := range completed {
		callIDs = append(callIDs, call.ID)
	}
	return s.artifacts.PublishWorkflowReport(ctx, artifact.WorkflowReportCommand{
		ProjectID: request.ProjectID, WorkflowRunID: request.WorkflowRunID, ToolCallID: request.ToolCallID,
		ToolName: ReportToolName, ToolVersion: "1", OperationKey: request.IdempotencyKey,
		Name: request.Name, Markdown: request.Markdown, Citations: verifiedCitations,
		SourceToolCallIDs: callIDs, SourceWorkspaceFiles: paths,
	})
}

func (s *Service) verifyCitation(ctx context.Context, projectID, runID, sourceCallID string, selected tool.CitationRef) (artifact.Citation, error) {
	if selected.Kind != citation.KindKnowledgeChunk || selected.ProjectID != projectID || selected.IndexVersionID == "" || selected.DocumentID == "" || selected.AttachmentID == "" || selected.ChunkID == "" {
		return artifact.Citation{}, fmt.Errorf("report citation identity is invalid")
	}
	if citation.QuoteSHA256(selected.Quote) != selected.QuoteSHA256 || selected.Reference != citation.KnowledgeReference(runID, selected.IndexVersionID, selected.ChunkID, selected.QuoteSHA256) {
		return artifact.Citation{}, fmt.Errorf("report citation marker or quote hash was altered")
	}
	chunk, err := s.knowledge.ReadEvidenceChunk(ctx, projectID, selected.IndexVersionID, selected.DocumentID, selected.AttachmentID, selected.ChunkID)
	if err != nil {
		return artifact.Citation{}, err
	}
	if chunk.IndexVersionID != selected.IndexVersionID || chunk.DocumentID != selected.DocumentID || chunk.AttachmentID != selected.AttachmentID || chunk.ChunkID != selected.ChunkID ||
		chunk.SourceName != selected.SourceName || chunk.MIMEType != selected.MIMEType || chunk.Locator != selected.Locator || chunk.Title != selected.Title ||
		chunk.SourceStart != selected.SourceStart || chunk.SourceEnd != selected.SourceEnd || !quoteBelongsToChunk(selected.Quote, chunk.Content) {
		return artifact.Citation{}, fmt.Errorf("report citation no longer matches the active verified knowledge chunk")
	}
	bibliography, err := s.bibliography.CitationSnapshotForAttachment(ctx, projectID, selected.AttachmentID)
	if err != nil {
		return artifact.Citation{}, err
	}
	if bibliography.EvidenceLevel != research.EvidenceFullText && bibliography.EvidenceLevel != research.EvidenceMetadataAbstract {
		return artifact.Citation{}, fmt.Errorf("report citation has no declared evidence level")
	}
	return artifact.Citation{
		Reference:           citation.KnowledgeReference(runID, selected.IndexVersionID, selected.ChunkID, selected.QuoteSHA256),
		SourceRunIDSnapshot: runID, SourceToolCallIDSnapshot: sourceCallID,
		IndexVersionID: selected.IndexVersionID, DocumentID: selected.DocumentID, AttachmentID: selected.AttachmentID, ChunkID: selected.ChunkID,
		SourceName: selected.SourceName, MIMEType: selected.MIMEType, Locator: selected.Locator, Title: selected.Title,
		Quote: selected.Quote, QuoteSHA256: selected.QuoteSHA256, SourceStart: selected.SourceStart, SourceEnd: selected.SourceEnd,
		BibliographyIDSnapshot: bibliography.BibliographyID, BibliographySnapshot: append(json.RawMessage(nil), bibliography.Bibliography...), EvidenceLevel: string(bibliography.EvidenceLevel),
	}, nil
}

func quoteBelongsToChunk(quote, content string) bool {
	quote, content = strings.TrimSpace(quote), strings.TrimSpace(content)
	if quote == "" || content == "" {
		return false
	}
	if strings.HasPrefix(quote, "...") {
		quote = strings.TrimPrefix(quote, "...")
	}
	if strings.HasSuffix(quote, "...") {
		quote = strings.TrimSuffix(quote, "...")
	}
	quote = strings.TrimSpace(quote)
	return quote != "" && strings.Contains(content, quote)
}

func uniqueIDs(values []string, maximum int, label string) ([]string, error) {
	if len(values) == 0 || len(values) > maximum {
		return nil, fmt.Errorf("provide 1-%d %s ids", maximum, label)
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 {
			return nil, fmt.Errorf("%s id is invalid", label)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("%s ids must be unique", label)
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func snapshotKey(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode frozen Workflow snapshot: %w", err)
	}
	return string(encoded), nil
}

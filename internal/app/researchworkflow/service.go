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
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const (
	ReportToolName        = "builtin.research.workflow.report"
	ReportToolVersion     = "3"
	ReviewGateToolName    = "builtin.research.workflow.review.gate"
	ReviewGateToolVersion = "1"
)

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

type TaskEvidenceReader interface {
	ReadEvidenceChunkForTask(ctx context.Context, projectID, taskID, indexVersionID, documentID, attachmentID, chunkID string) (knowledge.EvidenceChunk, error)
}

type Bibliography interface {
	CitationSnapshotForAttachment(ctx context.Context, projectID, attachmentID string) (research.CitationSnapshot, error)
}

type Environments interface {
	Get(ctx context.Context, projectID string) (pythonenv.Environment, error)
	Create(ctx context.Context, projectID, preferredPath string, rebuild bool) (pythonenv.Environment, error)
	Verify(ctx context.Context, projectID string) (pythonenv.Environment, error)
}

type dependencyInstaller interface {
	Install(ctx context.Context, projectID string, packages []string) (pythonenv.Environment, error)
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
	tasks        researchtask.Validator
}

func (s *Service) SetTaskValidator(validator researchtask.Validator) {
	if s != nil {
		s.tasks = validator
	}
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

func (s *Service) SearchPage(ctx context.Context, projectID, query string, sourceIDs []string, limit, offset int, snapshotKey string, taskIDs ...string) (research.DiscoverySearchResult, error) {
	taskID := ""
	if len(taskIDs) > 0 {
		taskID = taskIDs[0]
	}
	return s.discovery.Search(ctx, research.DiscoverySearchCommand{ProjectID: strings.TrimSpace(projectID), Query: query, SourceIDs: sourceIDs, Limit: limit, Offset: offset, SnapshotKey: snapshotKey, ResearchTaskID: taskID, EnrichMetadata: true})
}

func (s *Service) SearchScopedPage(ctx context.Context, c research.DiscoverySearchCommand) (research.DiscoverySearchResult, error) {
	c.EnrichMetadata = true
	return s.discovery.Search(ctx, c)
}

// LiteratureCandidates drains persisted query pages, not the discovery UI's
// first page. Only query IDs issued by the workflow are supplied by the host.
func (s *Service) LiteratureCandidates(ctx context.Context, projectID string, queryIDs []string) ([]research.Candidate, error) {
	reader, ok := s.discovery.(interface {
		ListCandidates(context.Context, research.CandidateListCommand) (research.CandidatePage, error)
	})
	if !ok {
		return nil, fmt.Errorf("literature candidate paging is not configured")
	}
	result := []research.Candidate{}
	seen := map[string]int{}
	for _, queryID := range queryIDs {
		for offset := 0; ; {
			page, err := reader.ListCandidates(ctx, research.CandidateListCommand{ProjectID: projectID, QueryID: queryID, Sort: "relevance", Offset: offset, Limit: 100})
			if err != nil {
				return nil, err
			}
			for _, candidate := range page.Items {
				if index, exists := seen[candidate.ID]; exists {
					// Merge only the issued queries, not the project-wide latest record.
					candidate.Records = append(result[index].Records, candidate.Records...)
					candidate.Preferred = research.PreferredWork(candidate.Records)
					result[index] = candidate
				} else {
					seen[candidate.ID] = len(result)
					result = append(result, candidate)
				}
			}
			offset += len(page.Items)
			if offset >= page.Total {
				break
			}
			if len(page.Items) == 0 {
				return nil, fmt.Errorf("literature candidate page made no progress")
			}
		}
	}
	return result, nil
}

type ImportedMaterial struct {
	FullTextAvailability string              `json:"fullTextAvailability,omitempty"`
	Warning              string              `json:"warning,omitempty"`
	CandidateID          string              `json:"candidateId"`
	Title                string              `json:"title"`
	ImportKind           research.ImportKind `json:"importKind"`
	AttachmentID         string              `json:"attachmentId"`
}

func (s *Service) ImportSelected(ctx context.Context, projectID string, candidateIDs []string, mode research.MaterializeMode) ([]ImportedMaterial, error) {
	return s.ImportSelectedForTask(ctx, projectID, candidateIDs, mode, "")
}

func (s *Service) ImportSelectedForTask(ctx context.Context, projectID string, candidateIDs []string, mode research.MaterializeMode, researchTaskID string) ([]ImportedMaterial, error) {
	projectID = strings.TrimSpace(projectID)
	researchTaskID = strings.TrimSpace(researchTaskID)
	if researchTaskID != "" {
		if err := researchtask.Validate(ctx, s.tasks, projectID, researchTaskID); err != nil {
			return nil, err
		}
	}
	ids, err := uniqueIDs(candidateIDs, 100, "candidate")
	if err != nil {
		return nil, err
	}
	result := make([]ImportedMaterial, 0, len(ids))
	failures := []string{}
	for _, candidateID := range ids {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		candidate, err := s.discovery.GetCandidate(ctx, projectID, candidateID)
		if researchTaskID != "" {
			if scoped, ok := s.discovery.(interface {
				GetCandidateForTask(context.Context, string, string, string) (research.Candidate, error)
			}); ok {
				candidate, err = scoped.GetCandidateForTask(ctx, projectID, candidateID, researchTaskID)
			}
		}
		if err != nil {
			return nil, err
		}
		if candidate.ReviewStatus != research.ReviewIncluded {
			candidate, err = s.discovery.UpdateReview(ctx, research.ReviewCommand{ProjectID: projectID, CandidateID: candidateID, Status: research.ReviewIncluded, Note: candidate.Note, ResearchTaskID: researchTaskID})
			if err != nil {
				return nil, err
			}
		}
		imported, err := s.discovery.ImportCandidate(ctx, research.ImportCandidateCommand{ProjectID: projectID, CandidateID: candidateID, Mode: mode, ResearchTaskID: researchTaskID})
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			failures = append(failures, fmt.Sprintf("%s (%s): %s", candidate.Preferred.Title, candidateID, importFailureReason(err)))
			continue
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
		availability := "unknown"
		if reader, ok := s.discovery.(interface {
			FullTextAvailability(research.Candidate) string
		}); ok {
			availability = reader.FullTextAvailability(candidate)
		}
		result = append(result, ImportedMaterial{FullTextAvailability: availability, CandidateID: candidate.ID, Title: candidate.Preferred.Title, ImportKind: candidate.ImportKind, AttachmentID: attachmentID, Warning: imported.Warning})
	}
	if len(failures) > 0 {
		return result, &ImportBatchError{Succeeded: len(result), Failed: len(failures), Details: failures}
	}
	return result, nil
}

type ImportBatchError struct {
	Succeeded, Failed int
	Details           []string
}

func (e *ImportBatchError) Error() string { return e.UserFacingMessage() }
func (e *ImportBatchError) UserFacingMessage() string {
	details := e.Details
	if len(details) > 3 {
		details = details[:3]
	}
	return fmt.Sprintf("研究材料导入：成功%d篇，失败%d篇。成功记录已保存；未完成材料不会被静默忽略。%s", e.Succeeded, e.Failed, strings.Join(details, "；"))
}
func importFailureReason(err error) string {
	text := err.Error()
	if strings.Contains(text, "redirect") {
		return "全文下载跳转不符合允许的来源或认证路径，已安全停止下载"
	}
	if strings.Contains(text, "size limit") {
		return "全文超过下载大小限制或未能完整读取"
	}
	for _, code := range []string{"429", "403", "404", "408", "500", "502", "503", "504"} {
		if strings.Contains(text, "HTTP "+code) {
			return "全文来源返回 HTTP " + code
		}
	}
	if strings.Contains(text, "timed out") || strings.Contains(text, "deadline exceeded") {
		return "全文来源请求超时"
	}
	if strings.Contains(text, "not a PDF") || strings.Contains(text, "MIME") {
		return "下载内容不是有效PDF"
	}
	if strings.Contains(text, "no open full text") {
		return "没有可访问的开放全文"
	}
	return "材料获取、校验或保存失败，具体原因保留在该文献导入记录"
}

func (s *Service) Synchronize(ctx context.Context, projectID string, attachmentIDs []string) ([]knowledge.Document, error) {
	ids, err := uniqueIDs(attachmentIDs, 100, "attachment")
	if err != nil {
		return nil, err
	}
	result := []knowledge.Document{}
	for offset := 0; offset < len(ids); offset += 20 {
		batch, err := s.knowledge.SynchronizeAttachments(ctx, strings.TrimSpace(projectID), ids[offset:min(offset+20, len(ids))])
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
	}
	return result, nil
}

func (s *Service) SynchronizeForTask(ctx context.Context, projectID, taskID string, attachmentIDs []string) ([]knowledge.Document, error) {
	projectID = strings.TrimSpace(projectID)
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("research task id is required")
	}
	if err := researchtask.Validate(ctx, s.tasks, projectID, taskID); err != nil {
		return nil, err
	}
	ids, err := uniqueIDs(attachmentIDs, 100, "attachment")
	if err != nil {
		return nil, err
	}
	if scoped, ok := s.knowledge.(interface {
		SynchronizeAttachmentsForTask(context.Context, string, string, []string) ([]knowledge.Document, error)
	}); ok {
		result := []knowledge.Document{}
		for offset := 0; offset < len(ids); offset += 20 {
			batch, err := scoped.SynchronizeAttachmentsForTask(ctx, projectID, taskID, ids[offset:min(offset+20, len(ids))])
			if err != nil {
				return nil, err
			}
			result = append(result, batch...)
		}
		return result, nil
	}
	return nil, fmt.Errorf("task-isolated knowledge synchronization is not supported")
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

// PrepareEnvironment makes a method's explicit dependency list part of the
// project environment transaction. An empty list only verifies/creates the
// environment; package URLs, local paths and arbitrary pip flags remain
// rejected by pythonenv.Service.Install.
func (s *Service) PrepareEnvironment(ctx context.Context, projectID string, packages []string) (pythonenv.Environment, bool, error) {
	current, created, err := s.EnsureEnvironment(ctx, projectID)
	if err != nil || len(packages) == 0 {
		return current, created, err
	}
	installer, ok := s.environments.(dependencyInstaller)
	if !ok {
		return current, created, fmt.Errorf("project Python dependency installation is not configured")
	}
	updated, err := installer.Install(ctx, projectID, packages)
	return updated, err == nil && updated.EnvironmentFingerprint != current.EnvironmentFingerprint, err
}

type ReportRequest struct {
	ProjectID       string
	WorkflowRunID   string
	ResearchTaskID  string
	ToolCallID      string
	IdempotencyKey  string
	Name            string
	Markdown        string
	Citations       []tool.CitationRef
	Analysis        json.RawMessage
	SourceArtifacts []tool.ArtifactRef
	ReviewGate      json.RawMessage
}

var citationMarker = regexp.MustCompile(`\[K-[0-9A-F]{12}\]`)

func (s *Service) PublishReport(ctx context.Context, request ReportRequest) (artifact.WorkflowReportResult, error) {
	request.ProjectID, request.WorkflowRunID, request.ResearchTaskID, request.ToolCallID = strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.WorkflowRunID), strings.TrimSpace(request.ResearchTaskID), strings.TrimSpace(request.ToolCallID)
	if request.ResearchTaskID == "" {
		request.ResearchTaskID = request.WorkflowRunID
	}
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
	searchCalls := map[string]tool.Call{}
	availableArtifacts := map[string]struct{}{}
	verifiedReviewGates := map[string]string{}
	for _, call := range calls {
		if call.ID == request.ToolCallID || call.RunID != request.WorkflowRunID || tool.NormalizeSubjectKind(call.SubjectKind) != tool.SubjectWorkflowRun || call.Status != tool.CallCompleted || call.Result == nil || call.Result.Status != tool.ResultSuccess {
			continue
		}
		completed = append(completed, call)
		if call.ToolName == citation.KnowledgeToolName {
			searchCalls[call.ID] = call
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
		if call.ToolName == ReviewGateToolName && len(call.Result.Structured) > 0 {
			key, marshalErr := snapshotKey(json.RawMessage(call.Result.Structured))
			if marshalErr != nil {
				return artifact.WorkflowReportResult{}, marshalErr
			}
			var gateInput struct {
				Subject json.RawMessage `json:"subject"`
			}
			if json.Unmarshal(call.Arguments, &gateInput) != nil || len(gateInput.Subject) == 0 || !json.Valid(gateInput.Subject) {
				return artifact.WorkflowReportResult{}, fmt.Errorf("completed independent review gate has an invalid subject snapshot")
			}
			// New runtime bindings store the exact independent-review stage input
			// in subject. Its context member is the report/result projection used
			// by publication; legacy calls stored that projection directly.
			subjectForPublication := gateInput.Subject
			var subjectObject map[string]json.RawMessage
			if json.Unmarshal(gateInput.Subject, &subjectObject) == nil {
				if contextSnapshot, ok := subjectObject["context"]; ok && len(contextSnapshot) > 0 && json.Valid(contextSnapshot) {
					subjectForPublication = contextSnapshot
				}
			}
			subjectKey, marshalErr := semanticSnapshotKey(subjectForPublication)
			if marshalErr != nil {
				return artifact.WorkflowReportResult{}, marshalErr
			}
			verifiedReviewGates[key] = subjectKey
		}
	}
	if len(completed) == 0 {
		return artifact.WorkflowReportResult{}, fmt.Errorf("research Workflow report has no completed upstream ToolCalls")
	}
	if len(request.ReviewGate) == 0 || !json.Valid(request.ReviewGate) {
		return artifact.WorkflowReportResult{}, fmt.Errorf("research Workflow report requires a valid independent review gate")
	}
	reviewKey, err := snapshotKey(request.ReviewGate)
	if err != nil {
		return artifact.WorkflowReportResult{}, err
	}
	reviewedSubject, verified := verifiedReviewGates[reviewKey]
	if !verified {
		return artifact.WorkflowReportResult{}, fmt.Errorf("report independent review gate was not returned by a completed review in this Workflow Run")
	}
	analysisKey, analysisErr := semanticSnapshotKey(request.Analysis)
	if analysisErr != nil || reviewedSubject != analysisKey {
		return artifact.WorkflowReportResult{}, fmt.Errorf("report independent review gate does not cover the current frozen analysis")
	}

	verifiedCitations := make([]artifact.Citation, 0, len(request.Citations))
	selectedMarkers := map[string]struct{}{}
	for _, selected := range request.Citations {
		key, err := snapshotKey(selected)
		if err != nil || len(offered[key]) == 0 {
			return artifact.WorkflowReportResult{}, fmt.Errorf("report citation was not returned by a completed knowledge search in this Workflow Run")
		}
		value, err := s.verifyCitation(ctx, request.ProjectID, request.WorkflowRunID, request.ResearchTaskID, searchCalls[offered[key][0]], selected)
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
		ProjectID: request.ProjectID, WorkflowRunID: request.WorkflowRunID, ResearchTaskID: request.ResearchTaskID, ToolCallID: request.ToolCallID,
		ToolName: ReportToolName, ToolVersion: ReportToolVersion, OperationKey: request.IdempotencyKey,
		Name: request.Name, Markdown: request.Markdown, Citations: verifiedCitations,
		SourceToolCallIDs: callIDs, SourceWorkspaceFiles: paths,
	})
}

func (s *Service) verifyCitation(ctx context.Context, projectID, runID, researchTaskID string, sourceCall tool.Call, selected tool.CitationRef) (artifact.Citation, error) {
	if selected.Kind != citation.KindKnowledgeChunk || selected.ProjectID != projectID || selected.IndexVersionID == "" || selected.DocumentID == "" || selected.AttachmentID == "" || selected.ChunkID == "" {
		return artifact.Citation{}, fmt.Errorf("report citation identity is invalid")
	}
	if citation.QuoteSHA256(selected.Quote) != selected.QuoteSHA256 || selected.Reference != citation.KnowledgeReference(runID, selected.IndexVersionID, selected.ChunkID, selected.QuoteSHA256) {
		return artifact.Citation{}, fmt.Errorf("report citation marker or quote hash was altered")
	}
	var chunk knowledge.EvidenceChunk
	var err error
	if scoped, ok := s.knowledge.(TaskEvidenceReader); ok && researchTaskID != "" {
		chunk, err = scoped.ReadEvidenceChunkForTask(ctx, projectID, researchTaskID, selected.IndexVersionID, selected.DocumentID, selected.AttachmentID, selected.ChunkID)
	} else {
		chunk, err = s.knowledge.ReadEvidenceChunk(ctx, projectID, selected.IndexVersionID, selected.DocumentID, selected.AttachmentID, selected.ChunkID)
	}
	if err != nil {
		return artifact.Citation{}, err
	}
	if chunk.IndexVersionID != selected.IndexVersionID || chunk.DocumentID != selected.DocumentID || chunk.AttachmentID != selected.AttachmentID || chunk.ChunkID != selected.ChunkID ||
		chunk.SourceName != selected.SourceName || chunk.MIMEType != selected.MIMEType || chunk.Locator != selected.Locator ||
		(chunk.Title != selected.Title && !legacyAbstractTitle(sourceCall, selected, chunk)) ||
		chunk.SourceStart != selected.SourceStart || chunk.SourceEnd != selected.SourceEnd || !quoteBelongsToChunk(selected.Quote, chunk.Content) {
		return artifact.Citation{}, fmt.Errorf("report citation no longer matches the active verified knowledge chunk")
	}
	var bibliography research.CitationSnapshot
	if scoped, ok := s.bibliography.(interface {
		CitationSnapshotForAttachmentForTask(context.Context, string, string, string) (research.CitationSnapshot, error)
	}); ok && researchTaskID != "" {
		bibliography, err = scoped.CitationSnapshotForAttachmentForTask(ctx, projectID, researchTaskID, selected.AttachmentID)
	} else {
		bibliography, err = s.bibliography.CitationSnapshotForAttachment(ctx, projectID, selected.AttachmentID)
	}
	if err != nil {
		return artifact.Citation{}, err
	}
	if bibliography.EvidenceLevel != research.EvidenceFullText && bibliography.EvidenceLevel != research.EvidenceMetadataAbstract {
		return artifact.Citation{}, fmt.Errorf("report citation has no declared evidence level")
	}
	return artifact.Citation{
		Reference:           citation.KnowledgeReference(runID, selected.IndexVersionID, selected.ChunkID, selected.QuoteSHA256),
		SourceRunIDSnapshot: runID, SourceToolCallIDSnapshot: sourceCall.ID,
		IndexVersionID: selected.IndexVersionID, DocumentID: selected.DocumentID, AttachmentID: selected.AttachmentID, ChunkID: selected.ChunkID,
		SourceName: selected.SourceName, MIMEType: selected.MIMEType, Locator: selected.Locator, Title: selected.Title,
		Quote: selected.Quote, QuoteSHA256: selected.QuoteSHA256, SourceStart: selected.SourceStart, SourceEnd: selected.SourceEnd,
		BibliographyIDSnapshot: bibliography.BibliographyID, BibliographySnapshot: append(json.RawMessage(nil), bibliography.Bibliography...), EvidenceLevel: string(bibliography.EvidenceLevel),
	}, nil
}

// Version 6 per-document retrieval issued an Abstract display label as Title.
// Accept only that persisted projection; all identity, quote and source checks
// above still apply, and the original issued snapshot remains in the audit.
func legacyAbstractTitle(call tool.Call, selected tool.CitationRef, chunk knowledge.EvidenceChunk) bool {
	if call.ToolName != citation.KnowledgeToolName || call.ToolVersion != "6" || selected.Title != "Abstract" || chunk.Title != "" ||
		!strings.Contains(strings.ToLower(chunk.SourceName), "-metadata.") || chunk.MIMEType != "text/markdown" {
		return false
	}
	var args struct {
		PerDocument bool     `json:"perDocument"`
		DocumentIDs []string `json:"documentIds"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil || !args.PerDocument {
		return false
	}
	for _, id := range args.DocumentIDs {
		if id == selected.DocumentID {
			return true
		}
	}
	return false
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

func semanticSnapshotKey(value json.RawMessage) (string, error) {
	var decoded any
	if len(value) == 0 || json.Unmarshal(value, &decoded) != nil {
		return "", fmt.Errorf("frozen Workflow snapshot is invalid")
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", fmt.Errorf("encode frozen Workflow snapshot: %w", err)
	}
	return string(encoded), nil
}

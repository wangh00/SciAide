package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/id"
)

type ReviewStatus string

const (
	ReviewPending  ReviewStatus = "pending"
	ReviewIncluded ReviewStatus = "included"
	ReviewExcluded ReviewStatus = "excluded"
)

type ImportStatus string

const (
	ImportNotImported ImportStatus = "not_imported"
	ImportImporting   ImportStatus = "importing"
	ImportImported    ImportStatus = "imported"
	ImportFailed      ImportStatus = "failed"
)

type ImportKind string

const (
	ImportFullText         ImportKind = "full_text"
	ImportMetadataAbstract ImportKind = "metadata_abstract"
)

type Query struct {
	TaskDeleted       bool           `json:"taskDeleted,omitempty"`
	LegacySnapshot    bool           `json:"legacySnapshot,omitempty"`
	ResearchTaskID    string         `json:"researchTaskId,omitempty"`
	ResearchTaskTitle string         `json:"researchTaskTitle,omitempty"`
	ID                string         `json:"id"`
	ProjectID         string         `json:"projectId"`
	Text              string         `json:"text"`
	SourceIDs         []string       `json:"sourceIds"`
	LimitPerSource    int            `json:"limitPerSource"`
	Sources           []SourceSearch `json:"sources"`
	Partial           bool           `json:"partial"`
	ResultCount       int            `json:"resultCount"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
}

type SourceRecord struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	Work        Work      `json:"work"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type CandidateAlias struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Candidate struct {
	ID              string           `json:"id"`
	ProjectID       string           `json:"projectId"`
	CandidateKey    string           `json:"candidateKey"`
	ReviewStatus    ReviewStatus     `json:"reviewStatus"`
	ExclusionReason string           `json:"exclusionReason,omitempty"`
	Note            string           `json:"note,omitempty"`
	ImportStatus    ImportStatus     `json:"importStatus"`
	ImportKind      ImportKind       `json:"importKind,omitempty"`
	AttachmentID    string           `json:"attachmentId,omitempty"`
	ImportError     string           `json:"importError,omitempty"`
	Preferred       Work             `json:"preferred"`
	Records         []SourceRecord   `json:"records"`
	Aliases         []CandidateAlias `json:"aliases"`
	CreatedAt       time.Time        `json:"createdAt"`
	UpdatedAt       time.Time        `json:"updatedAt"`
}

type DiscoverySearchCommand struct {
	ProviderQueries map[string]string `json:"providerQueries,omitempty"`
	Years           PublicationYears  `json:"publicationYears"`
	EnrichMetadata  bool              `json:"-"`
	SnapshotKey     string            `json:"-"`
	Offset          int               `json:"offset,omitempty"`
	ProjectID       string            `json:"projectId"`
	Query           string            `json:"query"`
	SourceIDs       []string          `json:"sourceIds,omitempty"`
	Limit           int               `json:"limit,omitempty"`
	ResearchTaskID  string            `json:"researchTaskId,omitempty"`
}

type CandidateListCommand struct {
	ProjectID      string       `json:"projectId"`
	QueryID        string       `json:"queryId,omitempty"`
	Status         ReviewStatus `json:"status,omitempty"`
	Search         string       `json:"search,omitempty"`
	Sort           string       `json:"sort,omitempty"`
	Offset         int          `json:"offset,omitempty"`
	Limit          int          `json:"limit,omitempty"`
	ResearchTaskID string       `json:"researchTaskId,omitempty"`
}

type CandidatePage struct {
	Items  []Candidate `json:"items"`
	Total  int         `json:"total"`
	Offset int         `json:"offset"`
	Limit  int         `json:"limit"`
}

type DiscoverySearchResult struct {
	Query Query         `json:"query"`
	Page  CandidatePage `json:"page"`
}

type ReviewCommand struct {
	ResearchTaskID  string       `json:"researchTaskId,omitempty"`
	ProjectID       string       `json:"projectId"`
	CandidateID     string       `json:"candidateId"`
	Status          ReviewStatus `json:"status"`
	ExclusionReason string       `json:"exclusionReason,omitempty"`
	Note            string       `json:"note,omitempty"`
}

type ImportStateCommand struct {
	ProjectID      string
	CandidateID    string
	ResearchTaskID string
	Status         ImportStatus
	Kind           ImportKind
	AttachmentID   string
	ErrorMessage   string
	At             time.Time
}

// CandidateTaskImport is the task-owned import state for a discovered work.
// The candidate itself is project-wide discovery metadata; this record keeps
// materialization and attachment ownership isolated for each research task.
type CandidateTaskImport struct {
	ID             string       `json:"id"`
	ProjectID      string       `json:"projectId"`
	CandidateID    string       `json:"candidateId"`
	ResearchTaskID string       `json:"researchTaskId"`
	Status         ImportStatus `json:"status"`
	Kind           ImportKind   `json:"kind,omitempty"`
	AttachmentID   string       `json:"attachmentId,omitempty"`
	ErrorMessage   string       `json:"errorMessage,omitempty"`
	CreatedAt      time.Time    `json:"createdAt"`
	UpdatedAt      time.Time    `json:"updatedAt"`
}

type DiscoveryRepository interface {
	SaveSearch(ctx context.Context, projectID, queryKey string, command SearchCommand, result SearchResult, at time.Time) (Query, error)
	ListQueries(ctx context.Context, projectID string, limit int) ([]Query, error)
	ListCandidates(ctx context.Context, projectID, queryID string) ([]Candidate, error)
	GetCandidate(ctx context.Context, projectID, candidateID string) (Candidate, error)
	UpdateReview(ctx context.Context, command ReviewCommand, at time.Time) (Candidate, error)
	UpdateImportState(ctx context.Context, command ImportStateCommand) (Candidate, error)
	RecoverImports(ctx context.Context, at time.Time) (int64, error)
}

// TaskImportRepository is implemented by persistent repositories that support
// task-isolated candidate imports. It is optional so historical project-level
// discovery remains readable, but task imports must not silently fall back to
// the legacy global import columns.
type TaskImportRepository interface {
	GetCandidateTaskImport(ctx context.Context, projectID, candidateID, researchTaskID string) (CandidateTaskImport, bool, error)
	UpdateCandidateTaskImport(ctx context.Context, command ImportStateCommand) (CandidateTaskImport, error)
}

type DiscoveryProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

type MaterializeMode string

const (
	MaterializeAuto     MaterializeMode = "auto"
	MaterializeFullText MaterializeMode = "full_text"
	MaterializeMetadata MaterializeMode = "metadata_abstract"
)

type MaterializedCandidate struct {
	Warning string
	Path    string
	Name    string
	SHA256  string
	Kind    ImportKind
}

type CandidateMaterializer interface {
	Materialize(ctx context.Context, selected project.Project, candidate Candidate, mode MaterializeMode) (MaterializedCandidate, error)
	Cleanup(value MaterializedCandidate)
}

type ResearchAttachmentImporter interface {
	ImportResearchStaged(ctx context.Context, projectID, path, name string) (attachment.Attachment, error)
}

type ResearchTaskAttachmentImporter interface {
	ImportResearchStagedForTask(ctx context.Context, projectID, path, name, researchTaskID string) (attachment.Attachment, error)
}

type ResearchKnowledgeImporter interface {
	Enqueue(ctx context.Context, value attachment.Attachment) error
}

type ImportCandidateCommand struct {
	ProjectID      string          `json:"projectId"`
	CandidateID    string          `json:"candidateId"`
	Mode           MaterializeMode `json:"mode,omitempty"`
	ResearchTaskID string          `json:"researchTaskId,omitempty"`
}

type ImportCandidateResult struct {
	Warning    string                `json:"warning,omitempty"`
	Candidate  Candidate             `json:"candidate"`
	Attachment attachment.Attachment `json:"attachment"`
}

type DiscoveryService struct {
	importMu     sync.Mutex
	research     *Service
	repository   DiscoveryRepository
	projects     DiscoveryProjectLoader
	materializer CandidateMaterializer
	attachments  ResearchAttachmentImporter
	knowledge    ResearchKnowledgeImporter
	tasks        researchtask.Validator
	now          func() time.Time
}

func (s *DiscoveryService) SetTaskValidator(validator researchtask.Validator) {
	if s != nil {
		s.tasks = validator
	}
}

func (s *DiscoveryService) validateTask(ctx context.Context, projectID, taskID string) error {
	return researchtask.Validate(ctx, s.tasks, projectID, taskID)
}

func (s *DiscoveryService) SetImportPipeline(materializer CandidateMaterializer, attachments ResearchAttachmentImporter, knowledge ResearchKnowledgeImporter) error {
	if materializer == nil || attachments == nil || knowledge == nil {
		return fmt.Errorf("research import pipeline dependencies are required")
	}
	s.materializer, s.attachments, s.knowledge = materializer, attachments, knowledge
	return nil
}

func (s *DiscoveryService) Recover(ctx context.Context) (int64, error) {
	return s.repository.RecoverImports(ctx, s.now())
}

func NewDiscoveryService(research *Service, repository DiscoveryRepository, projects DiscoveryProjectLoader) (*DiscoveryService, error) {
	if research == nil || repository == nil || projects == nil {
		return nil, fmt.Errorf("research discovery dependencies are required")
	}
	return &DiscoveryService{research: research, repository: repository, projects: projects, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *DiscoveryService) Catalog() []Source { return s.research.Catalog() }

func (s *DiscoveryService) Search(ctx context.Context, command DiscoverySearchCommand) (DiscoverySearchResult, error) {
	projectID := strings.TrimSpace(command.ProjectID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return DiscoverySearchResult{}, err
	}
	if taskID := strings.TrimSpace(command.ResearchTaskID); taskID != "" {
		if err := s.validateTask(ctx, projectID, taskID); err != nil {
			return DiscoverySearchResult{}, err
		}
		command.ResearchTaskID = taskID
	}
	limit := command.Limit
	if limit == 0 {
		limit = 20
	}
	result, err := s.research.Search(ctx, SearchCommand{Query: command.Query, ProviderQueries: command.ProviderQueries, SourceIDs: command.SourceIDs, Limit: limit, Offset: command.Offset, EnrichMetadata: command.EnrichMetadata, Years: command.Years})
	if err != nil {
		return DiscoverySearchResult{}, err
	}
	effectiveSourceIDs := append([]string(nil), command.SourceIDs...)
	if len(effectiveSourceIDs) == 0 {
		for _, status := range result.Sources {
			effectiveSourceIDs = append(effectiveSourceIDs, status.SourceID)
		}
	}
	key := SearchQueryKey(result.Query, effectiveSourceIDs, limit)
	if len(command.ProviderQueries) > 0 {
		projection, _ := json.Marshal(command.ProviderQueries)
		key = hashText(key + ":providers:" + string(projection))
	}
	if command.Years.Active() {
		key = hashText(fmt.Sprintf("%s:years:%d:%d", key, command.Years.From, command.Years.To))
	}
	if command.SnapshotKey == "" {
		command.SnapshotKey, err = id.New()
		if err != nil {
			return DiscoverySearchResult{}, err
		}
	}
	if command.SnapshotKey != "" {
		key = hashText(key + ":" + command.SnapshotKey)
	}
	if command.Offset > 0 {
		key = hashText(fmt.Sprintf("%s:offset:%d", key, command.Offset))
	}
	query, err := s.repository.SaveSearch(ctx, projectID, key, SearchCommand{Query: result.Query, SourceIDs: effectiveSourceIDs, Limit: limit, ResearchTaskID: command.ResearchTaskID}, result, s.now())
	if err != nil {
		return DiscoverySearchResult{}, err
	}
	page, err := s.ListCandidates(ctx, CandidateListCommand{ProjectID: projectID, QueryID: query.ID, Sort: "relevance", Limit: 20, ResearchTaskID: command.ResearchTaskID})
	if err != nil {
		return DiscoverySearchResult{}, err
	}
	return DiscoverySearchResult{Query: query, Page: page}, nil
}

func (s *DiscoveryService) ListQueries(ctx context.Context, projectID string) ([]Query, error) {
	projectID = strings.TrimSpace(projectID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return nil, err
	}
	return s.repository.ListQueries(ctx, projectID, 50)
}

func (s *DiscoveryService) ListCandidates(ctx context.Context, command CandidateListCommand) (CandidatePage, error) {
	projectID := strings.TrimSpace(command.ProjectID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return CandidatePage{}, err
	}
	command.ProjectID = projectID
	command.QueryID = strings.TrimSpace(command.QueryID)
	command.ResearchTaskID = strings.TrimSpace(command.ResearchTaskID)
	if command.Offset < 0 {
		return CandidatePage{}, fmt.Errorf("candidate offset cannot be negative")
	}
	if command.Limit == 0 {
		command.Limit = 20
	}
	if command.Limit < 1 || command.Limit > 100 {
		return CandidatePage{}, fmt.Errorf("candidate page limit must be between 1 and 100")
	}
	if command.QueryID != "" {
		if repository, ok := s.repository.(interface {
			CandidatePage(context.Context, CandidateListCommand) (CandidatePage, error)
		}); ok {
			// The repository resolves the persisted query owner. Historical reads
			// do not grant mutation rights to a deleted task.
			return repository.CandidatePage(ctx, command)
		}
	}
	if taskID := strings.TrimSpace(command.ResearchTaskID); taskID != "" {
		if err := s.validateTask(ctx, projectID, taskID); err != nil {
			return CandidatePage{}, err
		}
		command.ResearchTaskID = taskID
	}
	values, err := s.repository.ListCandidates(ctx, projectID, strings.TrimSpace(command.QueryID))
	if err != nil {
		return CandidatePage{}, err
	}
	if taskID := strings.TrimSpace(command.ResearchTaskID); taskID != "" {
		if err := s.applyTaskImportState(ctx, projectID, taskID, values); err != nil {
			return CandidatePage{}, err
		}
	}
	search := normalizeTitle(command.Search)
	filtered := make([]Candidate, 0, len(values))
	for _, value := range values {
		if command.Status != "" && value.ReviewStatus != command.Status {
			continue
		}
		if search != "" && !strings.Contains(normalizeTitle(value.Preferred.Title+" "+authorNames(value.Preferred.Authors)+" "+value.Preferred.Venue), search) {
			continue
		}
		filtered = append(filtered, value)
	}
	sortCandidates(filtered, command.Sort)
	offset, limit := command.Offset, command.Limit
	if offset < 0 {
		return CandidatePage{}, fmt.Errorf("candidate offset cannot be negative")
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return CandidatePage{}, fmt.Errorf("candidate page limit must be between 1 and 100")
	}
	page := CandidatePage{Items: []Candidate{}, Total: len(filtered), Offset: offset, Limit: limit}
	if offset >= len(filtered) {
		return page, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page.Items = filtered[offset:end]
	return page, nil
}

// applyTaskImportState replaces the legacy project-wide import columns with
// the state owned by the requested research task. A candidate imported by a
// different task must appear as not imported here, while a project-shared
// materialization remains reusable and may retain its imported state.
func (s *DiscoveryService) applyTaskImportState(ctx context.Context, projectID, taskID string, values []Candidate) error {
	taskRepository, ok := s.repository.(TaskImportRepository)
	if !ok {
		return fmt.Errorf("task-isolated research imports are not supported by this repository")
	}
	attachmentGetter, _ := s.attachments.(interface {
		Get(context.Context, string) (attachment.Attachment, error)
	})
	for index := range values {
		state, found, err := taskRepository.GetCandidateTaskImport(ctx, projectID, values[index].ID, taskID)
		if err != nil {
			return err
		}
		if found {
			values[index].ImportStatus = state.Status
			values[index].ImportKind = state.Kind
			values[index].AttachmentID = state.AttachmentID
			values[index].ImportError = state.ErrorMessage
			continue
		}
		// Only a project-shared attachment is visible across tasks. A task-owned
		// attachment recorded on the candidate belongs to another task and must
		// not leak its status into this task's view.
		if attachmentGetter != nil && values[index].AttachmentID != "" {
			value, getErr := attachmentGetter.Get(ctx, values[index].AttachmentID)
			if getErr == nil && value.ProjectID == projectID && value.ScopeKind == attachment.ScopeProjectShared {
				continue
			}
		}
		values[index].ImportStatus = ImportNotImported
		values[index].ImportKind = ""
		values[index].AttachmentID = ""
		values[index].ImportError = ""
	}
	return nil
}

func (s *DiscoveryService) GetCandidate(ctx context.Context, projectID, candidateID string) (Candidate, error) {
	projectID, candidateID = strings.TrimSpace(projectID), strings.TrimSpace(candidateID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return Candidate{}, err
	}
	if candidateID == "" {
		return Candidate{}, fmt.Errorf("research candidate id is required")
	}
	return s.repository.GetCandidate(ctx, projectID, candidateID)
}

func (s *DiscoveryService) GetCandidateForTask(ctx context.Context, projectID, candidateID, researchTaskID string) (Candidate, error) {
	value, err := s.GetCandidate(ctx, projectID, candidateID)
	if err != nil {
		return Candidate{}, err
	}
	taskID := strings.TrimSpace(researchTaskID)
	if taskID == "" {
		return value, nil
	}
	if err := s.validateTask(ctx, projectID, taskID); err != nil {
		return Candidate{}, err
	}
	if scoped, ok := s.repository.(interface {
		CandidateForTask(context.Context, string, string, string) (Candidate, error)
	}); ok {
		value, err = scoped.CandidateForTask(ctx, projectID, candidateID, taskID)
		if err != nil {
			return Candidate{}, err
		}
	}
	values := []Candidate{value}
	if err := s.applyTaskImportState(ctx, projectID, taskID, values); err != nil {
		return Candidate{}, err
	}
	value = values[0]
	return value, nil
}

func (s *DiscoveryService) UpdateReview(ctx context.Context, command ReviewCommand) (Candidate, error) {
	command.ResearchTaskID = strings.TrimSpace(command.ResearchTaskID)
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.CandidateID = strings.TrimSpace(command.CandidateID)
	command.ExclusionReason = strings.TrimSpace(command.ExclusionReason)
	command.Note = strings.TrimSpace(command.Note)
	if _, err := s.projects.Get(ctx, command.ProjectID); err != nil {
		return Candidate{}, err
	}
	if command.ResearchTaskID != "" {
		if err := s.validateTask(ctx, command.ProjectID, command.ResearchTaskID); err != nil {
			return Candidate{}, err
		}
	}
	if command.Status != ReviewPending && command.Status != ReviewIncluded && command.Status != ReviewExcluded {
		return Candidate{}, fmt.Errorf("candidate review status is invalid")
	}
	if command.Status == ReviewExcluded && command.ExclusionReason == "" {
		return Candidate{}, fmt.Errorf("excluded candidates require a reason")
	}
	if command.Status != ReviewExcluded {
		command.ExclusionReason = ""
	}
	if utf8.RuneCountInString(command.ExclusionReason) > 2000 || utf8.RuneCountInString(command.Note) > 20000 {
		return Candidate{}, fmt.Errorf("candidate review text is too long")
	}
	return s.repository.UpdateReview(ctx, command, s.now())
}

func (s *DiscoveryService) ImportCandidate(ctx context.Context, command ImportCandidateCommand) (result ImportCandidateResult, returnErr error) {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	projectID, candidateID := strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.CandidateID)
	researchTaskID := strings.TrimSpace(command.ResearchTaskID)
	selected, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return result, err
	}
	if s.materializer == nil || s.attachments == nil || s.knowledge == nil {
		return result, fmt.Errorf("research import pipeline is not configured")
	}
	if researchTaskID != "" {
		if err := s.validateTask(ctx, projectID, researchTaskID); err != nil {
			return result, err
		}
	}
	mode := command.Mode
	if mode == "" {
		mode = MaterializeAuto
	}
	if mode != MaterializeAuto && mode != MaterializeFullText && mode != MaterializeMetadata {
		return result, fmt.Errorf("research import mode is invalid")
	}
	candidate, err := s.GetCandidateForTask(ctx, projectID, candidateID, researchTaskID)
	if err != nil {
		return result, err
	}
	if candidate.ReviewStatus != ReviewIncluded {
		return result, fmt.Errorf("include the candidate before importing it into the knowledge base")
	}
	var taskRepository TaskImportRepository
	if researchTaskID != "" {
		var supported bool
		taskRepository, supported = s.repository.(TaskImportRepository)
		if !supported {
			return result, fmt.Errorf("task-isolated research imports are not supported by this repository")
		}
		if recovery, ok := s.repository.(interface {
			RecoverMaterialIntent(context.Context, string, string, string) (bool, error)
		}); ok {
			// No import row on the first request is normal; lookup before recovery.
			if _, found, err := taskRepository.GetCandidateTaskImport(ctx, projectID, candidateID, researchTaskID); err != nil {
				return result, err
			} else if found {
				if _, err := recovery.RecoverMaterialIntent(ctx, projectID, candidateID, researchTaskID); err != nil {
					return result, err
				}
			}
		}
		if state, found, stateErr := taskRepository.GetCandidateTaskImport(ctx, projectID, candidateID, researchTaskID); stateErr != nil {
			return result, stateErr
		} else if found {
			candidate.ImportStatus, candidate.ImportKind, candidate.AttachmentID, candidate.ImportError = state.Status, state.Kind, state.AttachmentID, state.ErrorMessage
		}
	}
	// A project-level import can be replayed directly. Task-scoped imports
	// must still pass through the scoped attachment importer: the candidate
	// row stores only the latest attachment ID and must not silently point a
	// different research task at another task's attachment.
	repairMetadata := false
	if researchTaskID != "" && mode != MaterializeFullText && candidate.ImportKind != ImportFullText {
		// Only an explicit task import (including a confirmed evidence rewind)
		// may create corrected bytes. Reads and application startup never do.
		if recovered, changed := RecoverCandidateAbstracts(candidate); changed {
			candidate, repairMetadata = recovered, true
		}
	}
	if candidate.ImportStatus == ImportImported && candidate.AttachmentID != "" && researchTaskID == "" && (mode != MaterializeFullText || candidate.ImportKind == ImportFullText) {
		return ImportCandidateResult{Candidate: candidate, Warning: candidate.ImportError}, nil
	}
	if candidate.ImportStatus == ImportImported && candidate.AttachmentID != "" && researchTaskID != "" && (mode != MaterializeFullText || candidate.ImportKind == ImportFullText) {
		if scoped, ok := s.attachments.(interface {
			Get(context.Context, string) (attachment.Attachment, error)
		}); ok {
			if existing, getErr := scoped.Get(ctx, candidate.AttachmentID); getErr == nil && existing.ProjectID == projectID && (existing.ScopeKind == attachment.ScopeProjectShared || existing.ScopeKind == attachment.ScopeTask && existing.ResearchTaskID == strings.TrimSpace(command.ResearchTaskID)) {
				if repairMetadata {
					fingerprint, ok := s.materializer.(interface{ MetadataSHA256(Candidate) string })
					repairMetadata = !ok || fingerprint.MetadataSHA256(candidate) != existing.SHA256
				}
				if !repairMetadata {
					if err := s.knowledge.Enqueue(ctx, existing); err != nil {
						return result, err
					}
					return ImportCandidateResult{Candidate: candidate, Attachment: existing, Warning: candidate.ImportError}, nil
				}
			}
		}
	}
	now := s.now()
	stateCommand := ImportStateCommand{ProjectID: projectID, CandidateID: candidateID, ResearchTaskID: researchTaskID, Status: ImportImporting, Kind: candidate.ImportKind, AttachmentID: candidate.AttachmentID, At: now}
	if taskRepository != nil {
		if _, err := taskRepository.UpdateCandidateTaskImport(ctx, stateCommand); err != nil {
			return result, err
		}
	} else if _, err := s.repository.UpdateImportState(ctx, stateCommand); err != nil {
		return result, err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		message := boundedText(returnErr.Error(), 4000)
		failed := ImportStateCommand{ProjectID: projectID, CandidateID: candidateID, ResearchTaskID: researchTaskID, Status: ImportFailed, ErrorMessage: message, At: s.now()}
		if candidate.ImportStatus == ImportImported && candidate.AttachmentID != "" {
			failed.Status, failed.Kind, failed.AttachmentID = candidate.ImportStatus, candidate.ImportKind, candidate.AttachmentID
		}
		if taskRepository != nil {
			_, _ = taskRepository.UpdateCandidateTaskImport(context.WithoutCancel(ctx), failed)
		} else {
			_, _ = s.repository.UpdateImportState(context.WithoutCancel(ctx), failed)
		}
	}()
	materialized, err := s.materializer.Materialize(ctx, selected, candidate, mode)
	if err != nil {
		return result, err
	}
	defer s.materializer.Cleanup(materialized)
	if researchTaskID != "" && (materialized.Kind == ImportFullText || repairMetadata) {
		if intents, ok := s.repository.(interface {
			RecordMaterialIntent(context.Context, string, string, string, string, ImportKind) error
		}); ok {
			if err := intents.RecordMaterialIntent(ctx, projectID, candidateID, researchTaskID, materialized.SHA256, materialized.Kind); err != nil {
				return result, err
			}
		}
	}
	var imported attachment.Attachment
	if scoped, ok := s.attachments.(ResearchTaskAttachmentImporter); ok {
		imported, err = scoped.ImportResearchStagedForTask(ctx, projectID, materialized.Path, materialized.Name, command.ResearchTaskID)
	} else {
		imported, err = s.attachments.ImportResearchStaged(ctx, projectID, materialized.Path, materialized.Name)
	}
	if err != nil {
		return result, err
	}
	if materialized.SHA256 != "" && !strings.EqualFold(materialized.SHA256, imported.SHA256) {
		return result, fmt.Errorf("research attachment SHA256 changed between download and import")
	}
	// Once bytes exist, finish the tiny durable binding even if cancellation arrived.
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelPersist()
	stateCommand = ImportStateCommand{ProjectID: projectID, CandidateID: candidateID, ResearchTaskID: researchTaskID, Status: ImportImported, Kind: materialized.Kind, AttachmentID: imported.ID, At: s.now()}
	stateCommand.ErrorMessage = materialized.Warning
	if taskRepository != nil {
		var state CandidateTaskImport
		state, err = taskRepository.UpdateCandidateTaskImport(persistCtx, stateCommand)
		if err == nil {
			candidate.ImportStatus, candidate.ImportKind, candidate.AttachmentID, candidate.ImportError = state.Status, state.Kind, state.AttachmentID, state.ErrorMessage
		}
	} else {
		candidate, err = s.repository.UpdateImportState(persistCtx, stateCommand)
	}
	if err != nil {
		return result, err
	}
	// candidate now contains the bound attachment; failure cleanup preserves it.
	// The attachment is bound before indexing; explicit sync repairs a cancelled queue.
	if err := s.knowledge.Enqueue(persistCtx, imported); err != nil {
		return result, err
	}
	return ImportCandidateResult{Candidate: candidate, Attachment: imported, Warning: materialized.Warning}, nil
}

func SearchQueryKey(query string, sourceIDs []string, limit int) string {
	ids := append([]string(nil), sourceIDs...)
	for index := range ids {
		ids[index] = strings.ToLower(strings.TrimSpace(ids[index]))
	}
	sort.Strings(ids)
	payload := strings.ToLower(strings.Join(strings.Fields(query), " ")) + "\n" + strings.Join(ids, ",") + fmt.Sprintf("\n%d", limit)
	return hashText(payload)
}

func CandidateAliases(value Work) []CandidateAlias {
	aliases := make([]CandidateAlias, 0, 5)
	appendAlias := func(kind, raw string) {
		raw = strings.TrimSpace(raw)
		if raw != "" {
			aliases = append(aliases, CandidateAlias{Kind: kind, Value: raw})
		}
	}
	appendAlias("doi", NormalizeDOI(value.Identifiers.DOI))
	appendAlias("pmid", digitsOnly(value.Identifiers.PMID))
	appendAlias("arxiv", strings.ToLower(NormalizeArXiv(value.Identifiers.ArXiv)))
	appendAlias("openalex", strings.ToUpper(normalizePrefixedID(value.Identifiers.OpenAlex, "https://openalex.org/")))
	title := normalizeTitle(value.Title)
	if value.Year >= 1000 && utf8.RuneCountInString(title) >= 16 {
		appendAlias("title_year", fmt.Sprintf("%d\t%s", value.Year, title))
	}
	return aliases
}

func CandidateKey(value Work) string {
	aliases := CandidateAliases(value)
	if len(aliases) > 0 {
		return hashText(aliases[0].Kind + ":" + aliases[0].Value)
	}
	return hashText("source:" + strings.ToLower(strings.TrimSpace(value.SourceID)) + ":" + strings.ToLower(strings.TrimSpace(value.SourceRecordID)))
}

func PreferredWork(records []SourceRecord) Work {
	if len(records) == 0 {
		return Work{Authors: []Author{}}
	}
	best := records[0].Work
	bestScore := workCompleteness(best)
	for _, record := range records[1:] {
		score := workCompleteness(record.Work)
		if score > bestScore || (score == bestScore && record.Work.SourceID < best.SourceID) {
			best, bestScore = record.Work, score
		}
	}
	best.RawSnapshot = nil
	if best.Authors == nil {
		best.Authors = []Author{}
	}
	return best
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func normalizeTitle(value string) string {
	var builder strings.Builder
	space := false
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
			space = false
		} else if !space && builder.Len() > 0 {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func authorNames(values []Author) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.Name)
	}
	return strings.Join(names, " ")
}

func workCompleteness(value Work) int {
	score := 0
	if value.Abstract != "" {
		score += 8
	}
	if len(value.Authors) > 0 {
		score += 5
	}
	if value.Year > 0 {
		score += 2
	}
	if value.Venue != "" {
		score += 3
	}
	if value.Volume != "" || value.Issue != "" || value.Pages != "" {
		score += 2
	}
	if value.Identifiers.DOI != "" {
		score += 4
	}
	if value.Identifiers.PMID != "" || value.Identifiers.ArXiv != "" {
		score += 3
	}
	if value.PDFURL != "" {
		score += 2
	}
	return score
}

func sortCandidates(values []Candidate, mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	sort.SliceStable(values, func(i, j int) bool {
		a, b := values[i].Preferred, values[j].Preferred
		switch mode {
		case "year_desc":
			if a.Year != b.Year {
				return a.Year > b.Year
			}
		case "cited_desc":
			if a.CitedByCount != b.CitedByCount {
				return a.CitedByCount > b.CitedByCount
			}
		case "title":
			if normalizeTitle(a.Title) != normalizeTitle(b.Title) {
				return normalizeTitle(a.Title) < normalizeTitle(b.Title)
			}
		case "updated":
			if !values[i].UpdatedAt.Equal(values[j].UpdatedAt) {
				return values[i].UpdatedAt.After(values[j].UpdatedAt)
			}
		default:
			if a.Score != b.Score {
				return a.Score > b.Score
			}
		}
		return values[i].ID < values[j].ID
	})
}

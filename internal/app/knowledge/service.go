package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/embedding"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
)

const (
	maxSearchLimit        = 20
	maxSearchResultRunes  = 8_000
	maxSearchSnippetRunes = 900
)

type Service struct {
	repository  Repository
	projects    ProjectLoader
	attachments AttachmentLoader
	embeddings  EmbeddingProvider
	now         func() time.Time
	wake        chan struct{}

	stateMu   sync.Mutex
	started   bool
	closed    bool
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	processMu sync.Mutex
	jobMu     sync.Mutex
	running   map[string]*runningKnowledgeJob
}

type runningKnowledgeJob struct {
	projectID     string
	documentID    string
	cancel        context.CancelFunc
	userCancelled bool
	committing    bool
}

func (s *Service) SetEmbeddingProvider(provider EmbeddingProvider) error {
	if provider == nil {
		return fmt.Errorf("Embedding provider is required")
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.started {
		return fmt.Errorf("Embedding provider must be configured before knowledge service starts")
	}
	s.embeddings = provider
	return nil
}

func NewService(repository Repository, projects ProjectLoader, attachments AttachmentLoader) *Service {
	return &Service{
		repository: repository, projects: projects, attachments: attachments,
		now: func() time.Time { return time.Now().UTC() }, wake: make(chan struct{}, 1), running: map[string]*runningKnowledgeJob{},
	}
}

func (s *Service) Start() (int64, error) {
	if s == nil || s.repository == nil || s.projects == nil || s.attachments == nil {
		return 0, fmt.Errorf("knowledge service is not configured")
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.closed {
		return 0, fmt.Errorf("knowledge service is closed")
	}
	if s.started {
		return 0, nil
	}
	recovered, err := s.repository.Recover(context.Background(), s.now())
	if err != nil {
		return 0, err
	}
	workerContext, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.started = true
	s.wg.Add(1)
	go s.worker(workerContext)
	s.signal()
	return recovered, nil
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return
	}
	s.closed = true
	cancel := s.cancel
	s.stateMu.Unlock()

	// Cancel active parsing/embedding work first, then wait until processNext has
	// left its SQLite critical section before cancelling the worker loop. On
	// Windows, interrupting modernc SQLite while a claim statement is being
	// finalized can leave the database file handle alive after sql.DB.Close.
	s.jobMu.Lock()
	runningCancels := make([]context.CancelFunc, 0, len(s.running))
	for _, value := range s.running {
		runningCancels = append(runningCancels, value.cancel)
	}
	s.jobMu.Unlock()
	for _, runningCancel := range runningCancels {
		runningCancel()
	}
	s.signal()
	s.processMu.Lock()
	if cancel != nil {
		cancel()
	}
	s.processMu.Unlock()
	s.wg.Wait()
}

// Enqueue explicitly adds a ready attachment to the project knowledge base.
// Ordinary chat attachment imports never call this method.
func (s *Service) Enqueue(ctx context.Context, value attachment.Attachment) error {
	if value.Status != attachment.StatusReady {
		return fmt.Errorf("attachment is not ready for knowledge indexing")
	}
	selectedProject, version, err := s.ensureProjectVersion(ctx, value.ProjectID)
	if err != nil {
		return err
	}
	index, err := openProjectIndex(ctx, selectedProject, version)
	if err != nil {
		return err
	}
	hasAttachment, err := index.HasAttachment(ctx, value.ID, value.SHA256)
	closeErr := index.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	_, queued, err := s.repository.Enqueue(ctx, value, version, !hasAttachment, s.now())
	if err != nil {
		return err
	}
	if version.Status == IndexBuilding {
		if err := s.queueMissingProjectDocuments(ctx, selectedProject, version); err != nil {
			return err
		}
	}
	if queued {
		s.signal()
	}
	return nil
}

func (s *Service) ListDocuments(ctx context.Context, projectID string) ([]Document, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return nil, err
	}
	documents, err := s.repository.ListDocuments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	jobs, err := s.repository.ListLatestJobs(ctx, projectID)
	if err != nil {
		return nil, err
	}
	attachments, err := s.attachments.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	jobsByDocument := make(map[string]ImportJob, len(jobs))
	for _, value := range jobs {
		jobsByDocument[value.DocumentID] = value
	}
	attachmentsByID := make(map[string]attachment.Attachment, len(attachments))
	for _, value := range attachments {
		attachmentsByID[value.ID] = value
	}
	for index := range documents {
		if value, found := jobsByDocument[documents[index].ID]; found {
			job := value
			documents[index].Job = &job
		}
		documents[index].Progress = jobProgress(documents[index].Status, documents[index].Job)
		if value, found := attachmentsByID[documents[index].AttachmentID]; found {
			documents[index].Diagnostic = buildParseDiagnostic(value)
		} else {
			documents[index].Diagnostic = ParseDiagnostic{Quality: ParseQualityUnavailable, Summary: "附件原件不可用", Warnings: []string{"知识库记录对应的附件已不可用。"}}
		}
	}
	return documents, nil
}

func (s *Service) RefreshProject(ctx context.Context, projectID string) error {
	s.processMu.Lock()
	defer s.processMu.Unlock()
	selectedProject, version, err := s.ensureProjectVersion(ctx, projectID)
	if err != nil {
		return err
	}
	if err := s.queueMissingProjectDocuments(ctx, selectedProject, version); err != nil {
		return err
	}
	s.signal()
	return nil
}

func (s *Service) CancelDocument(ctx context.Context, projectID, documentID string) (ImportJob, error) {
	projectID, documentID = strings.TrimSpace(projectID), strings.TrimSpace(documentID)
	if projectID == "" || documentID == "" {
		return ImportJob{}, fmt.Errorf("project and knowledge document id are required")
	}
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return ImportJob{}, err
	}
	if job, found, cancelErr := s.requestRunningCancellation(projectID, documentID); found {
		return job, cancelErr
	}
	job, cancelled, err := s.repository.CancelQueued(ctx, projectID, documentID, s.now())
	if err != nil {
		return ImportJob{}, err
	}
	if cancelled {
		return job, nil
	}
	if job, found, cancelErr := s.requestRunningCancellation(projectID, documentID); found {
		return job, cancelErr
	}
	return ImportJob{}, fmt.Errorf("knowledge task is no longer cancellable")
}

func (s *Service) RetryDocument(ctx context.Context, projectID, documentID string) (ImportJob, error) {
	documents, err := s.ListDocuments(ctx, projectID)
	if err != nil {
		return ImportJob{}, err
	}
	for _, value := range documents {
		if value.ID != strings.TrimSpace(documentID) {
			continue
		}
		if value.Status != DocumentFailed && (value.Job == nil || (value.Job.Status != JobFailed && value.Job.Status != JobCancelled)) {
			return ImportJob{}, fmt.Errorf("only a failed or cancelled knowledge task can be retried")
		}
		return s.enqueueDocument(ctx, value, true)
	}
	return ImportJob{}, fmt.Errorf("knowledge document was not found")
}

func (s *Service) RebuildDocument(ctx context.Context, projectID, documentID string) (ImportJob, error) {
	value, found, err := s.repository.GetDocument(ctx, strings.TrimSpace(projectID), strings.TrimSpace(documentID))
	if err != nil {
		return ImportJob{}, err
	}
	if !found {
		return ImportJob{}, fmt.Errorf("knowledge document was not found")
	}
	return s.enqueueDocument(ctx, value, true)
}

func (s *Service) enqueueDocument(ctx context.Context, value Document, force bool) (ImportJob, error) {
	selectedProject, version, err := s.ensureProjectVersion(ctx, value.ProjectID)
	if err != nil {
		return ImportJob{}, err
	}
	attachments, err := s.attachments.List(ctx, value.ProjectID)
	if err != nil {
		return ImportJob{}, err
	}
	for _, item := range attachments {
		if item.ID != value.AttachmentID {
			continue
		}
		if item.Status != attachment.StatusReady {
			return ImportJob{}, fmt.Errorf("attachment %q is not ready: %s", item.OriginalName, item.ErrorMessage)
		}
		job, queued, err := s.repository.Enqueue(ctx, item, version, force, s.now())
		if err != nil {
			return ImportJob{}, err
		}
		if queued {
			s.signal()
		}
		if version.Status == IndexBuilding {
			if err := s.queueMissingProjectDocuments(ctx, selectedProject, version); err != nil {
				return ImportJob{}, err
			}
		}
		if job.ID == "" {
			return ImportJob{}, fmt.Errorf("knowledge document is already current")
		}
		return job, nil
	}
	return ImportJob{}, fmt.Errorf("knowledge attachment is unavailable")
}

func (s *Service) RemoveDocument(ctx context.Context, projectID, documentID string) (Document, error) {
	projectID, documentID = strings.TrimSpace(projectID), strings.TrimSpace(documentID)
	if projectID == "" || documentID == "" {
		return Document{}, fmt.Errorf("project and knowledge document id are required")
	}
	selectedProject, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return Document{}, err
	}
	if err := project.VerifyPrivateDataLayout(selectedProject); err != nil {
		return Document{}, fmt.Errorf("project knowledge storage is unavailable: %w", err)
	}
	s.processMu.Lock()
	defer s.processMu.Unlock()
	value, found, err := s.repository.GetDocument(ctx, projectID, documentID)
	if err != nil {
		return Document{}, err
	}
	if !found {
		return Document{}, fmt.Errorf("knowledge document was not found")
	}
	versions, err := s.repository.ActiveVersions(ctx, projectID)
	if err != nil {
		return Document{}, err
	}
	for _, version := range versions {
		index, err := openProjectIndex(ctx, selectedProject, version)
		if err != nil {
			return Document{}, err
		}
		removeErr := index.RemoveDocument(ctx, value.ID, value.AttachmentID)
		closeErr := index.Close()
		if removeErr != nil {
			return Document{}, removeErr
		}
		if closeErr != nil {
			return Document{}, closeErr
		}
	}
	removed, err := s.repository.RemoveDocument(ctx, projectID, documentID)
	if err != nil {
		return Document{}, err
	}
	if !removed {
		return Document{}, fmt.Errorf("knowledge document removal conflict")
	}
	for _, version := range versions {
		if version.Status == IndexBuilding {
			if _, err := s.tryActivate(ctx, selectedProject, version); err != nil {
				return Document{}, err
			}
		}
	}
	return value, nil
}

func (s *Service) Search(ctx context.Context, projectID, query string, limit int) (SearchResult, error) {
	return s.SearchWithOptions(ctx, projectID, SearchOptions{Query: query, Limit: limit})
}

// SynchronizeAttachments drains only the selected project and proves that the
// requested attachments are readable from the active index before returning.
// It is used by deterministic Workflows that cannot continue on a stale ready
// snapshot after importing new research material.
func (s *Service) SynchronizeAttachments(ctx context.Context, projectID string, attachmentIDs []string) ([]Document, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || len(attachmentIDs) == 0 || len(attachmentIDs) > 20 {
		return nil, fmt.Errorf("project and 1-20 knowledge attachments are required")
	}
	wanted := make(map[string]struct{}, len(attachmentIDs))
	for _, value := range attachmentIDs {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("knowledge attachment id is required")
		}
		if _, exists := wanted[value]; exists {
			return nil, fmt.Errorf("knowledge attachment ids must be unique")
		}
		wanted[value] = struct{}{}
	}

	s.processMu.Lock()
	defer s.processMu.Unlock()
	selectedProject, version, err := s.ensureProjectVersion(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.queueMissingProjectDocuments(ctx, selectedProject, version); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		processed, err := s.processNext(ctx, projectID)
		if err != nil {
			return nil, err
		}
		if !processed {
			break
		}
	}
	if _, err := s.tryActivate(ctx, selectedProject, version); err != nil {
		return nil, err
	}
	active, found, err := s.repository.ReadyVersion(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("project knowledge index is still building")
	}
	documents, err := s.repository.ListDocuments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	byAttachment := make(map[string]Document, len(documents))
	for _, value := range documents {
		if _, selected := wanted[value.AttachmentID]; selected {
			byAttachment[value.AttachmentID] = value
		}
	}
	result := make([]Document, 0, len(attachmentIDs))
	for _, attachmentID := range attachmentIDs {
		value, exists := byAttachment[strings.TrimSpace(attachmentID)]
		if !exists {
			return nil, fmt.Errorf("imported research attachment has no knowledge document")
		}
		if value.Status != DocumentReady || value.IndexVersionID != active.ID {
			message := value.ErrorMessage
			if message == "" {
				message = "knowledge document is not ready in the active index"
			}
			return nil, fmt.Errorf("%s: %s", value.Title, message)
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Service) ReadEvidenceChunk(ctx context.Context, projectID, indexVersionID, documentID, attachmentID, chunkID string) (EvidenceChunk, error) {
	projectID, indexVersionID = strings.TrimSpace(projectID), strings.TrimSpace(indexVersionID)
	selectedProject, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return EvidenceChunk{}, err
	}
	version, found, err := s.repository.ReadyVersion(ctx, projectID)
	if err != nil {
		return EvidenceChunk{}, err
	}
	if !found || version.ID != indexVersionID {
		return EvidenceChunk{}, fmt.Errorf("knowledge evidence index is no longer the active verified version")
	}
	documentValue, found, err := s.repository.GetDocument(ctx, projectID, strings.TrimSpace(documentID))
	if err != nil {
		return EvidenceChunk{}, err
	}
	if !found || documentValue.Status != DocumentReady || documentValue.IndexVersionID != version.ID || documentValue.AttachmentID != strings.TrimSpace(attachmentID) {
		return EvidenceChunk{}, fmt.Errorf("knowledge evidence document is not ready in the selected index")
	}
	index, err := openProjectIndex(ctx, selectedProject, version)
	if err != nil {
		return EvidenceChunk{}, err
	}
	value, readErr := index.EvidenceChunk(ctx, documentID, attachmentID, chunkID)
	closeErr := index.Close()
	if readErr != nil {
		return EvidenceChunk{}, readErr
	}
	if closeErr != nil {
		return EvidenceChunk{}, closeErr
	}
	return value, nil
}

func (s *Service) SearchWithOptions(ctx context.Context, projectID string, options SearchOptions) (SearchResult, error) {
	projectID, options.Query = strings.TrimSpace(projectID), strings.TrimSpace(options.Query)
	query := options.Query
	if projectID == "" || query == "" {
		return SearchResult{}, fmt.Errorf("project and knowledge search query are required")
	}
	if len([]rune(query)) > 200 {
		return SearchResult{}, fmt.Errorf("knowledge search query is too long")
	}
	if options.Limit == 0 {
		options.Limit = 8
	}
	if options.Limit < 1 || options.Limit > maxSearchLimit {
		return SearchResult{}, fmt.Errorf("knowledge search result limit is invalid")
	}
	if len(options.DocumentIDs) > 20 || len(options.Formats) > 6 {
		return SearchResult{}, fmt.Errorf("knowledge search filter is too large")
	}
	for index, value := range options.DocumentIDs {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 {
			return SearchResult{}, fmt.Errorf("knowledge document filter is invalid")
		}
		options.DocumentIDs[index] = value
	}
	for _, value := range options.Formats {
		switch value {
		case document.FormatText, document.FormatMarkdown, document.FormatCSV, document.FormatPDF, document.FormatDOCX, document.FormatXLSX:
		default:
			return SearchResult{}, fmt.Errorf("unsupported knowledge document format %q", value)
		}
	}
	selectedProject, version, err := s.ensureProjectVersion(ctx, projectID)
	if err != nil {
		return SearchResult{}, err
	}
	if err := s.queueMissingProjectDocuments(ctx, selectedProject, version); err != nil {
		return SearchResult{}, err
	}
	searchVersion, found, err := s.repository.ReadyVersion(ctx, projectID)
	if err != nil {
		return SearchResult{}, err
	}
	if found {
		found, err = s.readyVersionSearchable(ctx, selectedProject, searchVersion)
		if err != nil {
			return SearchResult{}, err
		}
	}
	// Keep the last ready index searchable while a replacement version or an
	// explicit document rebuild is running. Only the first project index must
	// block the caller until enough work has completed to activate it.
	if !found {
		if err := s.drainProject(ctx, projectID); err != nil {
			return SearchResult{}, err
		}
		if _, err := s.tryActivate(ctx, selectedProject, version); err != nil {
			return SearchResult{}, err
		}
		searchVersion, found, err = s.repository.ReadyVersion(ctx, projectID)
		if err != nil {
			return SearchResult{}, err
		}
	}
	if !found {
		return SearchResult{}, fmt.Errorf("project knowledge index is still building")
	}
	index, err := openProjectIndex(ctx, selectedProject, searchVersion)
	if err != nil {
		return SearchResult{}, err
	}
	var queryVector []float32
	mode := HybridBM25Only
	warning := ""
	if searchVersion.HybridStrategy == HybridRRF && s.embeddings != nil {
		cached, found, _ := index.CachedQueryVector(ctx, query, s.now())
		if found {
			queryVector = cached
			mode = HybridRRF
		} else {
			identity := embedding.Identity{ModelID: searchVersion.EmbeddingModel, Dimensions: searchVersion.EmbeddingDimensions, Fingerprint: searchVersion.EmbeddingFingerprint}
			vectors, embedErr := s.embeddings.Embed(ctx, identity, []string{query})
			if embedErr != nil {
				warning = "语义检索暂时不可用，已回退到 FTS5/BM25：" + embedErr.Error()
			} else if len(vectors) != 1 {
				warning = "语义检索返回数量异常，已回退到 FTS5/BM25。"
			} else {
				queryVector = vectors[0]
				mode = HybridRRF
				_ = index.StoreQueryVector(ctx, query, queryVector, s.now())
			}
		}
	}
	matches, total, searchErr := index.SearchWithOptions(ctx, options, queryVector)
	closeErr := index.Close()
	if searchErr != nil {
		return SearchResult{}, searchErr
	}
	if closeErr != nil {
		return SearchResult{}, closeErr
	}
	matches = fitSearchResultBudget(matches, maxSearchResultRunes)
	for index := range matches {
		matches[index].IndexVersionID = searchVersion.ID
	}
	status, err := s.repository.ProjectStatus(ctx, projectID)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Query: query, Matches: matches, TotalMatches: total, Status: status, RetrievalMode: mode, EmbeddingWarning: warning}, nil
}

func fitSearchResultBudget(values []Match, maximum int) []Match {
	if maximum <= 0 {
		return []Match{}
	}
	result := make([]Match, 0, len(values))
	used := 0
	for _, value := range values {
		snippet := []rune(strings.TrimSpace(value.Snippet))
		if len(snippet) > maxSearchSnippetRunes {
			snippet = snippet[:maxSearchSnippetRunes]
			value.Snippet = strings.TrimSpace(string(snippet)) + "..."
		}
		cost := len([]rune(value.Name)) + len([]rune(value.Locator)) + len([]rune(value.Title)) + len([]rune(value.Snippet)) + 64
		if len(result) > 0 && used+cost > maximum {
			break
		}
		if cost > maximum {
			allowed := max(1, maximum-64-len([]rune(value.Name))-len([]rune(value.Locator))-len([]rune(value.Title)))
			content := []rune(value.Snippet)
			if len(content) > allowed {
				value.Snippet = string(content[:allowed]) + "..."
				cost = maximum
			}
		}
		value.Rank = len(result) + 1
		result = append(result, value)
		used += cost
	}
	return result
}

func (s *Service) queueMissingProjectDocuments(ctx context.Context, selectedProject project.Project, version IndexVersion) error {
	documents, err := s.repository.ListDocuments(ctx, selectedProject.ID)
	if err != nil {
		return fmt.Errorf("list selected knowledge documents: %w", err)
	}
	attachments, err := s.attachments.List(ctx, selectedProject.ID)
	if err != nil {
		return fmt.Errorf("list project attachments for indexing: %w", err)
	}
	jobs, err := s.repository.ListLatestJobs(ctx, selectedProject.ID)
	if err != nil {
		return fmt.Errorf("list project knowledge jobs: %w", err)
	}
	blocked := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		if job.IndexVersionID == version.ID && (job.Status == JobFailed || job.Status == JobCancelled) {
			blocked[job.DocumentID] = struct{}{}
		}
	}
	byID := make(map[string]attachment.Attachment, len(attachments))
	for _, value := range attachments {
		byID[value.ID] = value
	}
	index, err := openProjectIndex(ctx, selectedProject, version)
	if err != nil {
		return err
	}
	defer index.Close()
	queued := false
	for _, documentValue := range documents {
		if err := ctx.Err(); err != nil {
			return err
		}
		value, found := byID[documentValue.AttachmentID]
		if _, skip := blocked[documentValue.ID]; skip {
			continue
		}
		if !found {
			return fmt.Errorf("knowledge attachment %q is unavailable", documentValue.Title)
		}
		if value.ProjectID != selectedProject.ID || value.Status != attachment.StatusReady {
			continue
		}
		hasAttachment, err := index.HasAttachment(ctx, value.ID, value.SHA256)
		if err != nil {
			return err
		}
		_, created, err := s.repository.Enqueue(ctx, value, version, !hasAttachment, s.now())
		if err != nil {
			return err
		}
		queued = queued || created
	}
	if queued {
		s.signal()
	}
	return nil
}

func (s *Service) ensureProjectVersion(ctx context.Context, projectID string) (project.Project, IndexVersion, error) {
	selectedProject, err := s.projects.Get(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return project.Project{}, IndexVersion{}, err
	}
	if err := project.VerifyPrivateDataLayout(selectedProject); err != nil {
		return project.Project{}, IndexVersion{}, fmt.Errorf("project knowledge storage is unavailable: %w", err)
	}
	spec := DefaultIndexSpec()
	if s.embeddings != nil {
		identity, enabled, embeddingErr := s.embeddings.Current(ctx)
		if embeddingErr != nil {
			return project.Project{}, IndexVersion{}, embeddingErr
		}
		if enabled {
			spec = IndexSpecForEmbedding(identity)
		}
	}
	version, err := s.repository.EnsureVersion(ctx, selectedProject.ID, spec, s.now())
	if err != nil {
		return project.Project{}, IndexVersion{}, err
	}
	index, err := openProjectIndex(ctx, selectedProject, version)
	if err != nil {
		return project.Project{}, IndexVersion{}, err
	}
	if err := index.Close(); err != nil {
		return project.Project{}, IndexVersion{}, err
	}
	return selectedProject, version, nil
}

func (s *Service) readyVersionSearchable(ctx context.Context, selectedProject project.Project, version IndexVersion) (bool, error) {
	documents, err := s.repository.ListDocuments(ctx, selectedProject.ID)
	if err != nil {
		return false, fmt.Errorf("list ready knowledge documents: %w", err)
	}
	index, err := openProjectIndex(ctx, selectedProject, version)
	if err != nil {
		return false, err
	}
	defer index.Close()
	for _, value := range documents {
		// Documents assigned to a newer building version do not invalidate the
		// previous ready snapshot. Documents assigned to this version must still
		// exist, otherwise its derived cache was deleted or is incomplete.
		if value.IndexVersionID != version.ID {
			continue
		}
		present, err := index.HasAttachment(ctx, value.AttachmentID, value.AttachmentSHA256)
		if err != nil {
			return false, err
		}
		if !present {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) tryActivate(ctx context.Context, selectedProject project.Project, version IndexVersion) (bool, error) {
	if version.Status == IndexReady {
		return true, nil
	}
	documents, err := s.repository.ListDocuments(ctx, selectedProject.ID)
	if err != nil {
		return false, fmt.Errorf("list selected documents for index activation: %w", err)
	}
	canActivate, err := s.repository.CanActivate(ctx, selectedProject.ID, version.ID, len(documents))
	if err != nil || !canActivate {
		return false, err
	}
	index, err := openProjectIndex(ctx, selectedProject, version)
	if err != nil {
		return false, err
	}
	for _, value := range documents {
		if value.IndexVersionID != version.ID {
			index.Close()
			return false, nil
		}
		present, err := index.HasAttachment(ctx, value.AttachmentID, value.AttachmentSHA256)
		if err != nil {
			index.Close()
			return false, err
		}
		if !present {
			index.Close()
			return false, nil
		}
	}
	if err := index.Optimize(ctx); err != nil {
		index.Close()
		return false, err
	}
	if err := index.Close(); err != nil {
		return false, err
	}
	if err := s.repository.MarkVersionReady(ctx, version.ID, selectedProject.ID, s.now()); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) drainProject(ctx context.Context, projectID string) error {
	s.processMu.Lock()
	defer s.processMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		processed, err := s.processNext(ctx, projectID)
		if err != nil {
			return err
		}
		if !processed {
			return nil
		}
	}
}

func (s *Service) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		s.processMu.Lock()
		processed, err := s.processNext(ctx, "")
		s.processMu.Unlock()
		if processed {
			continue
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
	}
}

func (s *Service) processNext(ctx context.Context, projectID string) (bool, error) {
	work, found, err := s.repository.ClaimNext(ctx, projectID, s.now())
	if err != nil || !found {
		return false, err
	}
	jobCtx, cancel := context.WithCancel(ctx)
	s.registerRunning(work, cancel)
	defer func() {
		cancel()
		s.unregisterRunning(work.Job.ID)
	}()
	value, parsed, processErr := s.attachments.Parsed(jobCtx, work.ProjectID(), work.Job.AttachmentID)
	if processErr == nil {
		if value.ID != work.Job.AttachmentID || value.ProjectID != work.Job.ProjectID || value.SHA256 != work.Document.AttachmentSHA256 || value.Status != attachment.StatusReady {
			processErr = fmt.Errorf("attachment changed before knowledge indexing")
		}
	}
	var chunks []Chunk
	var vectors [][]float32
	if processErr == nil {
		processErr = s.repository.UpdateStage(jobCtx, work, "chunking", s.now())
	}
	if processErr == nil {
		chunks, processErr = buildChunks(work.Document, parsed)
	}
	if processErr == nil && work.Version.HybridStrategy == HybridRRF {
		if s.embeddings == nil {
			processErr = fmt.Errorf("Embedding provider is unavailable")
		} else {
			inputs := make([]string, len(chunks))
			for index, chunk := range chunks {
				inputs[index] = strings.TrimSpace(chunk.Title + "\n" + chunk.Content)
			}
			identity := embedding.Identity{ModelID: work.Version.EmbeddingModel, Dimensions: work.Version.EmbeddingDimensions, Fingerprint: work.Version.EmbeddingFingerprint}
			vectors, processErr = s.embeddings.Embed(jobCtx, identity, inputs)
		}
	}
	if processErr == nil {
		processErr = s.repository.UpdateStage(jobCtx, work, "indexing", s.now())
	}
	if processErr == nil {
		selectedProject, loadErr := s.projects.Get(ctx, work.Job.ProjectID)
		if loadErr != nil {
			processErr = loadErr
		} else {
			index, openErr := openProjectIndex(ctx, selectedProject, work.Version)
			if openErr != nil {
				processErr = openErr
			} else {
				processErr = index.ReplaceDocument(jobCtx, value, work.Document, chunks, vectors, s.now().Format(time.RFC3339Nano), func() error {
					if err := jobCtx.Err(); err != nil {
						return err
					}
					if !s.beginRunningCompletion(work.Job.ID) {
						return context.Canceled
					}
					return nil
				})
				if closeErr := index.Close(); processErr == nil {
					processErr = closeErr
				}
			}
		}
	}
	if processErr == nil {
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer finishCancel()
		if err := s.repository.Complete(finishCtx, work, len(chunks), s.now()); err != nil {
			return true, err
		}
		selectedProject, err := s.projects.Get(finishCtx, work.Job.ProjectID)
		if err != nil {
			return true, err
		}
		if _, err := s.tryActivate(finishCtx, selectedProject, work.Version); err != nil {
			return true, err
		}
		return true, nil
	}
	if errors.Is(processErr, context.Canceled) || errors.Is(processErr, context.DeadlineExceeded) || jobCtx.Err() != nil {
		requeueContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if s.wasUserCancelled(work.Job.ID) {
			if err := s.repository.CancelRunning(requeueContext, work, s.now()); err != nil {
				return true, err
			}
			return true, nil
		}
		if err := s.repository.Requeue(requeueContext, work, s.now()); err != nil {
			return true, err
		}
		return true, processErr
	}
	if err := s.repository.Fail(ctx, work, processErr.Error(), s.now()); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Service) registerRunning(work Work, cancel context.CancelFunc) {
	s.jobMu.Lock()
	s.running[work.Job.ID] = &runningKnowledgeJob{projectID: work.Job.ProjectID, documentID: work.Document.ID, cancel: cancel}
	s.jobMu.Unlock()
	s.stateMu.Lock()
	closed := s.closed
	s.stateMu.Unlock()
	if closed {
		cancel()
	}
}

func (s *Service) unregisterRunning(jobID string) {
	s.jobMu.Lock()
	delete(s.running, jobID)
	s.jobMu.Unlock()
}

func (s *Service) requestRunningCancellation(projectID, documentID string) (ImportJob, bool, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	for jobID, value := range s.running {
		if value.projectID != projectID || value.documentID != documentID {
			continue
		}
		job := ImportJob{ID: jobID, ProjectID: projectID, DocumentID: documentID, Status: JobRunning}
		if value.committing {
			return job, true, fmt.Errorf("knowledge task is already committing and is no longer cancellable")
		}
		value.userCancelled = true
		value.cancel()
		return job, true, nil
	}
	return ImportJob{}, false, nil
}

func (s *Service) beginRunningCompletion(jobID string) bool {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	value := s.running[jobID]
	if value == nil || value.userCancelled {
		return false
	}
	value.committing = true
	return true
}

func (s *Service) wasUserCancelled(jobID string) bool {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	value := s.running[jobID]
	return value != nil && value.userCancelled
}

func (w Work) ProjectID() string { return w.Job.ProjectID }

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

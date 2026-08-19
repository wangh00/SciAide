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
		now: func() time.Time { return time.Now().UTC() }, wake: make(chan struct{}, 1),
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
	if cancel != nil {
		cancel()
	}
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
	return s.repository.ListDocuments(ctx, projectID)
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
	if err := s.drainProject(ctx, projectID); err != nil {
		return SearchResult{}, err
	}
	if _, err := s.tryActivate(ctx, selectedProject, version); err != nil {
		return SearchResult{}, err
	}
	searchVersion, found, err := s.repository.ReadyVersion(ctx, projectID)
	if err != nil {
		return SearchResult{}, err
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
	value, parsed, processErr := s.attachments.Parsed(ctx, work.ProjectID(), work.Job.AttachmentID)
	if processErr == nil {
		if value.ID != work.Job.AttachmentID || value.ProjectID != work.Job.ProjectID || value.SHA256 != work.Document.AttachmentSHA256 || value.Status != attachment.StatusReady {
			processErr = fmt.Errorf("attachment changed before knowledge indexing")
		}
	}
	var chunks []Chunk
	var vectors [][]float32
	if processErr == nil {
		processErr = s.repository.UpdateStage(ctx, work, "chunking", s.now())
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
			vectors, processErr = s.embeddings.Embed(ctx, identity, inputs)
		}
	}
	if processErr == nil {
		processErr = s.repository.UpdateStage(ctx, work, "indexing", s.now())
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
				processErr = index.ReplaceDocument(ctx, value, work.Document, chunks, vectors, s.now().Format(time.RFC3339Nano))
				if closeErr := index.Close(); processErr == nil {
					processErr = closeErr
				}
			}
		}
	}
	if processErr == nil {
		if err := s.repository.Complete(ctx, work, len(chunks), s.now()); err != nil {
			return true, err
		}
		selectedProject, err := s.projects.Get(ctx, work.Job.ProjectID)
		if err != nil {
			return true, err
		}
		if _, err := s.tryActivate(ctx, selectedProject, work.Version); err != nil {
			return true, err
		}
		return true, nil
	}
	if errors.Is(processErr, context.Canceled) || errors.Is(processErr, context.DeadlineExceeded) || ctx.Err() != nil {
		requeueContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
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

func (w Work) ProjectID() string { return w.Job.ProjectID }

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

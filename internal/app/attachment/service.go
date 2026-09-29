package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/model"
)

const (
	maxImportFiles      = 20
	maxImportFileBytes  = 250 << 20
	maxImportBatchBytes = 1 << 30
	maxImageFileBytes   = 20 << 20
	maxImageDimension   = 8192
	maxImagePixels      = 40_000_000
)

type ProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

// ConversationValidator resolves the durable conversation record used as an
// attachment owner. A caller-provided conversation ID is never trusted by
// itself because conversation IDs are unique across projects.
type ConversationValidator interface {
	GetConversation(ctx context.Context, conversationID string) (conversation.Conversation, error)
}

type Service struct {
	repository    Repository
	projects      ProjectLoader
	tasks         researchtask.Validator
	conversations ConversationValidator
	now           func() time.Time
	mu            sync.Mutex
}

func NewService(repository Repository, projects ProjectLoader) *Service {
	return &Service{repository: repository, projects: projects, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetTaskValidator(validator researchtask.Validator) {
	if s != nil {
		s.tasks = validator
	}
}

func (s *Service) SetConversationValidator(validator ConversationValidator) {
	if s != nil {
		s.conversations = validator
	}
}

func (s *Service) validateTask(ctx context.Context, projectID, taskID string) error {
	return researchtask.Validate(ctx, s.tasks, projectID, taskID)
}

func (s *Service) validateConversation(ctx context.Context, projectID, conversationID string) error {
	projectID, conversationID = strings.TrimSpace(projectID), strings.TrimSpace(conversationID)
	if projectID == "" || conversationID == "" {
		return fmt.Errorf("project and conversation id are required")
	}
	if s.conversations == nil {
		return fmt.Errorf("conversation ownership validator is not configured")
	}
	value, err := s.conversations.GetConversation(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("validate conversation ownership: %w", err)
	}
	if value.ProjectID != projectID {
		return fmt.Errorf("conversation does not belong to the current project")
	}
	return nil
}

func (s *Service) ImportPaths(ctx context.Context, projectID string, paths []string) (ImportBatch, error) {
	return s.importPathsWithScope(ctx, projectID, paths, "")
}

// ImportPathsForTask creates task-owned attachment rows. The content object
// may be deduplicated by bytes, but the ownership record remains isolated.
func (s *Service) ImportPathsForTask(ctx context.Context, projectID string, paths []string, researchTaskID string) (ImportBatch, error) {
	researchTaskID = strings.TrimSpace(researchTaskID)
	if researchTaskID == "" {
		return ImportBatch{}, fmt.Errorf("research task id is required")
	}
	if err := s.validateTask(ctx, projectID, researchTaskID); err != nil {
		return ImportBatch{}, err
	}
	return s.importPathsWithScope(ctx, projectID, paths, researchTaskID)
}

// ImportPathsForConversation keeps ordinary chat uploads out of the project
// and research-task scopes. The conversation ID is stored as ownership
// metadata, not as a user-visible filesystem path.
func (s *Service) ImportPathsForConversation(ctx context.Context, projectID string, paths []string, conversationID string) (ImportBatch, error) {
	conversationID = strings.TrimSpace(conversationID)
	if err := s.validateConversation(ctx, projectID, conversationID); err != nil {
		return ImportBatch{}, err
	}
	return s.importPathsWithScope(ctx, projectID, paths, "conversation:"+conversationID)
}

func (s *Service) importPathsWithScope(ctx context.Context, projectID string, paths []string, researchTaskID string) (ImportBatch, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ImportBatch{}, fmt.Errorf("project id is required")
	}
	if len(paths) == 0 || len(paths) > maxImportFiles {
		return ImportBatch{}, fmt.Errorf("select between 1 and %d documents", maxImportFiles)
	}
	selectedProject, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return ImportBatch{}, err
	}
	if err := project.VerifyPrivateDataLayout(selectedProject); err != nil {
		return ImportBatch{}, fmt.Errorf("project attachment storage is unavailable: %w", err)
	}
	result := ImportBatch{Attachments: []Attachment{}, Errors: []ImportError{}}
	var total int64
	for _, sourcePath := range paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		info, statErr := os.Stat(strings.TrimSpace(sourcePath))
		if statErr == nil {
			total += info.Size()
		}
		if total > maxImportBatchBytes {
			result.Errors = append(result.Errors, ImportError{Path: sourcePath, Message: "selected documents exceed the 1 GiB batch limit"})
			continue
		}
		value, importErr := s.importOneWithScope(ctx, selectedProject, sourcePath, "", false, researchTaskID)
		if value.ID != "" {
			result.Attachments = append(result.Attachments, value)
		}
		if importErr != nil {
			result.Errors = append(result.Errors, ImportError{Path: sourcePath, Message: importErr.Error()})
		}
	}
	return result, nil
}

// ListForTask excludes legacy and unrelated task material. Project-shared
// attachments remain visible because they are explicitly reusable resources.
func (s *Service) ListForTask(ctx context.Context, projectID, taskID string) ([]Attachment, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("research task id is required")
	}
	if err := s.validateTask(ctx, projectID, taskID); err != nil {
		return nil, err
	}
	all, err := s.ListAll(ctx, projectID)
	if err != nil {
		return nil, err
	}
	filtered := make([]Attachment, 0, len(all))
	for _, value := range all {
		if value.ScopeKind == ScopeProjectShared || value.ScopeKind == ScopeTask && value.ResearchTaskID == taskID {
			filtered = append(filtered, value)
		}
	}
	return filtered, nil
}

func (s *Service) ListForConversation(ctx context.Context, projectID, conversationID string) ([]Attachment, error) {
	conversationID = strings.TrimSpace(conversationID)
	if err := s.validateConversation(ctx, projectID, conversationID); err != nil {
		return nil, err
	}
	owner := "conversation:" + conversationID
	all, err := s.ListAll(ctx, projectID)
	if err != nil {
		return nil, err
	}
	filtered := make([]Attachment, 0, len(all))
	for _, value := range all {
		if value.ScopeKind == ScopeProjectShared || value.ScopeKind == ScopeConversation && value.ResearchTaskID == owner {
			filtered = append(filtered, value)
		}
	}
	return filtered, nil
}

func (s *Service) ResolveForConversation(ctx context.Context, projectID, conversationID string, ids []string) ([]MessageReference, error) {
	projectID, conversationID = strings.TrimSpace(projectID), strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, fmt.Errorf("conversation id is required")
	}
	allowed, err := s.ListForConversation(ctx, projectID, conversationID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Attachment, len(allowed))
	for _, value := range allowed {
		byID[value.ID] = value
	}
	if len(ids) > maxImportFiles {
		return nil, fmt.Errorf("too many message attachments")
	}
	result := make([]MessageReference, 0, len(ids))
	seen := map[string]struct{}{}
	for _, rawID := range ids {
		attachmentID := strings.TrimSpace(rawID)
		if attachmentID == "" {
			return nil, fmt.Errorf("attachment id is required")
		}
		if _, ok := seen[attachmentID]; ok {
			continue
		}
		seen[attachmentID] = struct{}{}
		value, ok := byID[attachmentID]
		if !ok || value.Status != StatusReady {
			return nil, fmt.Errorf("attachment does not belong to the current conversation")
		}
		result = append(result, MessageReference{AttachmentID: value.ID, OriginalName: value.OriginalName, MIMEType: value.MIMEType, Format: value.Format, SizeBytes: value.SizeBytes, UnitCount: value.UnitCount, Truncated: value.Truncated})
	}
	return result, nil
}

// ImportResearchStaged imports a file produced by the research downloader. It
// accepts only the dedicated project-private staging prefix; ordinary callers
// must continue to use ImportPaths and cannot import SciAide's private data.
func (s *Service) ImportResearchStaged(ctx context.Context, projectID, sourcePath, originalName string) (Attachment, error) {
	return s.ImportResearchStagedForTask(ctx, projectID, sourcePath, originalName, "")
}

func (s *Service) ImportResearchStagedForTask(ctx context.Context, projectID, sourcePath, originalName, researchTaskID string) (Attachment, error) {
	researchTaskID = strings.TrimSpace(researchTaskID)
	if researchTaskID != "" {
		if err := s.validateTask(ctx, projectID, researchTaskID); err != nil {
			return Attachment{}, err
		}
	}
	selectedProject, err := s.projects.Get(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return Attachment{}, err
	}
	if err := project.VerifyPrivateDataLayout(selectedProject); err != nil {
		return Attachment{}, fmt.Errorf("project attachment storage is unavailable: %w", err)
	}
	absSource, err := filepath.Abs(strings.TrimSpace(sourcePath))
	if err != nil {
		return Attachment{}, err
	}
	privateRoot, err := filepath.Abs(project.PrivateDataPath(selectedProject))
	if err != nil {
		return Attachment{}, err
	}
	relative, err := filepath.Rel(privateRoot, absSource)
	if err != nil || filepath.Dir(relative) != "tmp" || !strings.HasPrefix(filepath.Base(relative), "research-import-") {
		return Attachment{}, fmt.Errorf("research attachment staging path is invalid")
	}
	info, err := os.Lstat(absSource)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Attachment{}, fmt.Errorf("research attachment staging file is unavailable")
	}
	return s.importOneWithScope(ctx, selectedProject, absSource, originalName, true, researchTaskID)
}

func (s *Service) importOne(ctx context.Context, selectedProject project.Project, sourcePath, originalName string, allowPrivate bool) (Attachment, error) {
	return s.importOneWithScope(ctx, selectedProject, sourcePath, originalName, allowPrivate, "")
}

func (s *Service) importOneWithScope(ctx context.Context, selectedProject project.Project, sourcePath, originalName string, allowPrivate bool, researchTaskID string) (Attachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sourcePath = strings.TrimSpace(sourcePath)
	formatName := sourcePath
	if strings.TrimSpace(originalName) != "" {
		formatName = originalName
	}
	format, supported := document.FormatForName(formatName)
	if sourcePath == "" || !supported {
		return Attachment{}, fmt.Errorf("supported formats are PDF, DOCX, XLSX, TXT, Markdown, CSV, TSV, JPEG, PNG and WebP")
	}
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return Attachment{}, fmt.Errorf("resolve attachment path: %w", err)
	}
	privateRoot := project.PrivateDataPath(selectedProject)
	if insidePath(privateRoot, absSource) && !allowPrivate {
		return Attachment{}, fmt.Errorf("cannot import SciAide's own project data directory")
	}
	input, err := os.Open(absSource)
	if err != nil {
		return Attachment{}, fmt.Errorf("open attachment: %w", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return Attachment{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxBytesForFormat(format) {
		if format == document.FormatImage {
			return Attachment{}, fmt.Errorf("image attachment must be a regular file no larger than 20 MiB")
		}
		return Attachment{}, fmt.Errorf("attachment must be a regular file no larger than 250 MiB")
	}
	root, err := os.OpenRoot(privateRoot)
	if err != nil {
		return Attachment{}, fmt.Errorf("open project data root: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll("tmp", 0o700); err != nil {
		return Attachment{}, fmt.Errorf("create attachment staging directory: %w", err)
	}
	tempID, err := id.New()
	if err != nil {
		return Attachment{}, err
	}
	tempRelative := filepath.Join("tmp", "import-"+tempID)
	temporary, err := root.OpenFile(tempRelative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Attachment{}, fmt.Errorf("create attachment staging file: %w", err)
	}
	hash := sha256.New()
	maxBytes := maxBytesForFormat(format)
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(input, maxBytes+1))
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written != info.Size() || written > maxBytes {
		_ = root.Remove(tempRelative)
		if copyErr != nil {
			return Attachment{}, fmt.Errorf("copy attachment: %w", copyErr)
		}
		return Attachment{}, fmt.Errorf("attachment changed while it was being imported")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	mimeType := document.MIMEType(format)
	parseMetadata := map[string]string{}
	if format == document.FormatImage {
		detectedMIMEType, width, height, inspectErr := inspectImage(filepath.Join(privateRoot, filepath.FromSlash(tempRelative)))
		if inspectErr != nil {
			_ = root.Remove(tempRelative)
			return Attachment{}, inspectErr
		}
		mimeType = detectedMIMEType
		parseMetadata = map[string]string{"width": fmt.Sprint(width), "height": fmt.Sprint(height)}
	}
	scopeKind := ScopeProjectShared
	scopeOwnerID := ""
	sourceKind := SourceUserImport
	if strings.HasPrefix(strings.TrimSpace(researchTaskID), "conversation:") {
		scopeKind = ScopeConversation
		scopeOwnerID = strings.TrimSpace(researchTaskID)
		sourceKind = SourceConversation
	} else if strings.TrimSpace(researchTaskID) != "" {
		scopeKind = ScopeTask
		scopeOwnerID = strings.TrimSpace(researchTaskID)
	}
	if allowPrivate {
		sourceKind = SourceResearchImport
	}
	var existing Attachment
	var found bool
	var findErr error
	if scoped, ok := s.repository.(ScopedHashRepository); ok {
		existing, found, findErr = scoped.FindByHashInScope(ctx, selectedProject.ID, digest, scopeKind, scopeOwnerID)
	} else if scopeKind == ScopeProjectShared {
		existing, found, findErr = s.repository.FindByHash(ctx, selectedProject.ID, digest)
	}
	if findErr != nil {
		_ = root.Remove(tempRelative)
		return Attachment{}, findErr
	} else if found {
		_ = root.Remove(tempRelative)
		if existing.Format == document.FormatImage {
			existing.Status = StatusReady
			existing.UnitCount = 0
			existing.ExtractedRunes = 0
			existing.Truncated = false
			existing.ParseMetadata = parseMetadata
			existing.ErrorMessage = ""
			existing.UpdatedAt = s.now()
			if updateErr := s.repository.UpdateParse(ctx, existing); updateErr != nil {
				return existing, updateErr
			}
			return existing, nil
		}
		parsed, parseErr := s.ensureParsedLocked(ctx, selectedProject, existing)
		if parseErr == nil {
			existing.UnitCount = len(parsed.Units)
			existing.ExtractedRunes = parsed.ExtractedRunes
			existing.Truncated = parsed.Truncated
			existing.ParseMetadata = parsed.Metadata
			existing.Status = StatusReady
			existing.ErrorMessage = ""
			existing.UpdatedAt = s.now()
			if updateErr := s.repository.UpdateParse(ctx, existing); updateErr != nil {
				return existing, updateErr
			}
		} else {
			existing.Status = StatusFailed
			existing.ErrorMessage = boundedError(parseErr)
			existing.UpdatedAt = s.now()
			_ = s.repository.UpdateParse(ctx, existing)
		}
		return existing, parseErr
	}
	attachmentID, err := id.New()
	if err != nil {
		_ = root.Remove(tempRelative)
		return Attachment{}, err
	}
	name := safeFileName(filepath.Base(absSource))
	if strings.TrimSpace(originalName) != "" {
		name = safeFileName(filepath.Base(originalName))
	}
	objectDirectory := filepath.Join("attachments", "objects", digest)
	if err := root.MkdirAll(objectDirectory, 0o700); err != nil {
		_ = root.Remove(tempRelative)
		return Attachment{}, fmt.Errorf("create attachment object directory: %w", err)
	}
	storageRelative := filepath.Join(objectDirectory, name)
	objectCreated := false
	if _, err := root.Stat(storageRelative); os.IsNotExist(err) {
		if err := root.Rename(tempRelative, storageRelative); err != nil {
			_ = root.Remove(tempRelative)
			return Attachment{}, fmt.Errorf("commit attachment object: %w", err)
		}
		objectCreated = true
	} else if err != nil {
		_ = root.Remove(tempRelative)
		return Attachment{}, err
	} else {
		_ = root.Remove(tempRelative)
	}
	now := s.now()
	value := Attachment{
		ID: attachmentID, ProjectID: selectedProject.ID, OriginalName: name,
		SourceKind: sourceKind,
		MIMEType:   mimeType, Format: format, SizeBytes: written, SHA256: digest,
		StorageRelativePath: filepath.ToSlash(storageRelative),
		CacheRelativePath:   filepath.ToSlash(filepath.Join("cache", "documents", attachmentID+".json")),
		Status:              StatusParsing, ParseMetadata: parseMetadata, CreatedAt: now, UpdatedAt: now,
	}
	value.ScopeKind = scopeKind
	value.ResearchTaskID = scopeOwnerID
	if err := s.repository.Create(ctx, value); err != nil {
		if objectCreated {
			_ = root.Remove(storageRelative)
		}
		return Attachment{}, err
	}
	if format == document.FormatImage {
		value.Status = StatusReady
		value.UpdatedAt = s.now()
		if err := s.repository.UpdateParse(ctx, value); err != nil {
			return value, err
		}
		return value, nil
	}
	parsed, parseErr := s.parseAndPersistLocked(ctx, selectedProject, value)
	if parseErr != nil {
		value.Status = StatusFailed
		value.ErrorMessage = boundedError(parseErr)
		value.UpdatedAt = s.now()
		_ = s.repository.UpdateParse(ctx, value)
		return value, parseErr
	}
	value.Status = StatusReady
	value.UnitCount = len(parsed.Units)
	value.ExtractedRunes = parsed.ExtractedRunes
	value.Truncated = parsed.Truncated
	value.ParseMetadata = parsed.Metadata
	value.UpdatedAt = s.now()
	if err := s.repository.UpdateParse(ctx, value); err != nil {
		return value, err
	}
	return value, nil
}

func (s *Service) List(ctx context.Context, projectID string) ([]Attachment, error) {
	values, err := s.ListAll(ctx, projectID)
	if err != nil {
		return nil, err
	}
	filtered := make([]Attachment, 0, len(values))
	for _, value := range values {
		if value.ScopeKind == ScopeProjectShared {
			filtered = append(filtered, value)
		}
	}
	return filtered, nil
}

// ListForProject is the resource-manager view. It includes shared material,
// task-owned material, and historical rows so users can explicitly identify
// and re-home them, while conversation-local uploads remain private to chat.
func (s *Service) ListForProject(ctx context.Context, projectID string) ([]Attachment, error) {
	values, err := s.ListAll(ctx, projectID)
	if err != nil {
		return nil, err
	}
	filtered := make([]Attachment, 0, len(values))
	for _, value := range values {
		if value.ScopeKind != ScopeConversation {
			filtered = append(filtered, value)
		}
	}
	return filtered, nil
}

// ListAll is reserved for internal maintenance such as rebuilding the
// project index. User-facing and model-facing callers must use List,
// ListForTask, or ListForConversation so unrelated task data is not exposed.
func (s *Service) ListAll(ctx context.Context, projectID string) ([]Attachment, error) {
	return s.repository.ListByProject(ctx, strings.TrimSpace(projectID))
}

// Get exposes the immutable ownership metadata needed by higher-level import
// flows to decide whether an existing content record is reusable in the
// requested resource scope.
func (s *Service) Get(ctx context.Context, attachmentID string) (Attachment, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return Attachment{}, fmt.Errorf("attachment id is required")
	}
	return s.repository.Get(ctx, attachmentID)
}

func (s *Service) Resolve(ctx context.Context, projectID string, ids []string) ([]MessageReference, error) {
	projectID = strings.TrimSpace(projectID)
	if len(ids) > maxImportFiles {
		return nil, fmt.Errorf("too many message attachments")
	}
	selectedProject, err := s.projects.Get(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	if err := project.VerifyPrivateDataLayout(selectedProject); err != nil {
		return nil, fmt.Errorf("project attachment storage is unavailable: %w", err)
	}
	seen := map[string]struct{}{}
	result := make([]MessageReference, 0, len(ids))
	for _, attachmentID := range ids {
		attachmentID = strings.TrimSpace(attachmentID)
		if attachmentID == "" {
			return nil, fmt.Errorf("attachment id is required")
		}
		if _, duplicate := seen[attachmentID]; duplicate {
			continue
		}
		seen[attachmentID] = struct{}{}
		value, err := s.repository.Get(ctx, attachmentID)
		if err != nil {
			return nil, fmt.Errorf("load attachment: %w", err)
		}
		if value.ProjectID != projectID {
			return nil, fmt.Errorf("attachment does not belong to the current project")
		}
		if value.Status != StatusReady {
			return nil, fmt.Errorf("attachment %q is not ready: %s", value.OriginalName, value.ErrorMessage)
		}
		result = append(result, MessageReference{AttachmentID: value.ID, OriginalName: value.OriginalName, MIMEType: value.MIMEType, Format: value.Format, SizeBytes: value.SizeBytes, UnitCount: value.UnitCount, Truncated: value.Truncated})
	}
	return result, nil
}

func (s *Service) ResolveImage(ctx context.Context, projectID, attachmentID string) (model.ContentPart, error) {
	projectID, attachmentID = strings.TrimSpace(projectID), strings.TrimSpace(attachmentID)
	value, err := s.repository.Get(ctx, attachmentID)
	if err != nil {
		return model.ContentPart{}, err
	}
	if value.ProjectID != projectID || value.Format != document.FormatImage || value.Status != StatusReady {
		return model.ContentPart{}, fmt.Errorf("image attachment is unavailable for the current project")
	}
	selectedProject, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return model.ContentPart{}, err
	}
	if err := project.VerifyPrivateDataLayout(selectedProject); err != nil {
		return model.ContentPart{}, fmt.Errorf("project attachment storage is unavailable: %w", err)
	}
	root, err := os.OpenRoot(project.PrivateDataPath(selectedProject))
	if err != nil {
		return model.ContentPart{}, fmt.Errorf("open project data root: %w", err)
	}
	defer root.Close()
	contents, err := root.ReadFile(filepath.FromSlash(value.StorageRelativePath))
	if err != nil {
		return model.ContentPart{}, fmt.Errorf("read stored image: %w", err)
	}
	if len(contents) == 0 || len(contents) > maxImageFileBytes || int64(len(contents)) != value.SizeBytes {
		return model.ContentPart{}, fmt.Errorf("stored image size no longer matches its import record")
	}
	digest := sha256.Sum256(contents)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), value.SHA256) {
		return model.ContentPart{}, fmt.Errorf("stored image SHA256 no longer matches its import record")
	}
	return model.ContentPart{
		Type: "input_image", MediaType: value.MIMEType, Data: base64.StdEncoding.EncodeToString(contents),
		Name: value.OriginalName, AttachmentID: value.ID,
	}, nil
}

func (s *Service) ResolveImageForConversation(ctx context.Context, projectID, conversationID, attachmentID string) (model.ContentPart, error) {
	allowed, err := s.ResolveForConversation(ctx, projectID, conversationID, []string{attachmentID})
	if err != nil || len(allowed) != 1 {
		if err != nil {
			return model.ContentPart{}, err
		}
		return model.ContentPart{}, fmt.Errorf("image attachment does not belong to the current conversation")
	}
	return s.ResolveImage(ctx, projectID, attachmentID)
}

func (s *Service) Parsed(ctx context.Context, projectID, attachmentID string) (Attachment, document.Parsed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.repository.Get(ctx, strings.TrimSpace(attachmentID))
	if err != nil {
		return Attachment{}, document.Parsed{}, err
	}
	if value.ProjectID != strings.TrimSpace(projectID) {
		return Attachment{}, document.Parsed{}, fmt.Errorf("attachment does not belong to the current project")
	}
	if value.Format == document.FormatImage {
		return value, document.Parsed{}, fmt.Errorf("image attachments are sent directly to vision models and cannot be inspected as parsed documents")
	}
	selectedProject, err := s.projects.Get(ctx, value.ProjectID)
	if err != nil {
		return Attachment{}, document.Parsed{}, err
	}
	if err := project.VerifyPrivateDataLayout(selectedProject); err != nil {
		return value, document.Parsed{}, fmt.Errorf("project attachment storage is unavailable: %w", err)
	}
	parsed, err := s.ensureParsedLocked(ctx, selectedProject, value)
	if err != nil {
		value.Status = StatusFailed
		value.ErrorMessage = boundedError(err)
		value.UpdatedAt = s.now()
		_ = s.repository.UpdateParse(ctx, value)
		return value, document.Parsed{}, err
	}
	value.Status = StatusReady
	value.UnitCount = len(parsed.Units)
	value.ExtractedRunes = parsed.ExtractedRunes
	value.Truncated = parsed.Truncated
	value.ParseMetadata = parsed.Metadata
	value.ErrorMessage = ""
	value.UpdatedAt = s.now()
	if err := s.repository.UpdateParse(ctx, value); err != nil {
		return value, document.Parsed{}, err
	}
	return value, parsed, nil
}

func (s *Service) ParsedForTask(ctx context.Context, projectID, taskID, attachmentID string) (Attachment, document.Parsed, error) {
	if strings.TrimSpace(taskID) == "" {
		return Attachment{}, document.Parsed{}, fmt.Errorf("research task id is required")
	}
	if err := s.validateTask(ctx, projectID, taskID); err != nil {
		return Attachment{}, document.Parsed{}, err
	}
	value, err := s.Get(ctx, attachmentID)
	if err != nil {
		return Attachment{}, document.Parsed{}, err
	}
	if value.ProjectID != strings.TrimSpace(projectID) || value.ScopeKind != ScopeProjectShared && (value.ScopeKind != ScopeTask || value.ResearchTaskID != strings.TrimSpace(taskID)) {
		return Attachment{}, document.Parsed{}, fmt.Errorf("attachment does not belong to the current research task")
	}
	return s.Parsed(ctx, projectID, attachmentID)
}

func (s *Service) ensureParsedLocked(ctx context.Context, selectedProject project.Project, value Attachment) (document.Parsed, error) {
	value = versionedPDFCache(value)
	root, err := os.OpenRoot(project.PrivateDataPath(selectedProject))
	if err != nil {
		return document.Parsed{}, err
	}
	contents, readErr := root.ReadFile(filepath.FromSlash(value.CacheRelativePath))
	_ = root.Close()
	if readErr == nil {
		var parsed document.Parsed
		if json.Unmarshal(contents, &parsed) == nil && validParsedCache(parsed, value) {
			return parsed, nil
		}
	}
	return s.parseAndPersistLocked(ctx, selectedProject, value)
}

func validParsedCache(parsed document.Parsed, value Attachment) bool {
	if value.Format == document.FormatPDF && parsed.Metadata["structureParser"] != document.PDFParserVersion {
		return false
	}
	return validParsedCacheStructure(parsed, value)
}

func validParsedCacheStructure(parsed document.Parsed, value Attachment) bool {
	if parsed.SchemaVersion != document.SchemaVersion || parsed.Format != value.Format || parsed.ExtractedRunes < 0 || parsed.ExtractedRunes > document.MaxExtractedRunes {
		return false
	}
	if value.Status == StatusReady && value.UnitCount != len(parsed.Units) {
		return false
	}
	used := 0
	for index, unit := range parsed.Units {
		if unit.Index != index+1 || strings.TrimSpace(unit.Locator) == "" || strings.TrimSpace(unit.Content) == "" {
			return false
		}
		used += len([]rune(unit.Content))
		if used > document.MaxExtractedRunes {
			return false
		}
	}
	return used == parsed.ExtractedRunes
}

// Old queued index jobs must not replace their frozen parser generation with
// newly decoded text. Their original cache remains available for that purpose.
func (s *Service) ParsedForIndexVersion(ctx context.Context, projectID, attachmentID, chunkingVersion string) (Attachment, document.Parsed, error) {
	if chunkingVersion != "bounded-unit-v2" && chunkingVersion != "bounded-unit-v3" {
		return s.Parsed(ctx, projectID, attachmentID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.repository.Get(ctx, attachmentID)
	if err != nil || value.ProjectID != projectID {
		return Attachment{}, document.Parsed{}, fmt.Errorf("index attachment is unavailable")
	}
	if value.Format != document.FormatPDF {
		p, err := s.projects.Get(ctx, projectID)
		if err != nil {
			return value, document.Parsed{}, err
		}
		parsed, err := s.ensureParsedLocked(ctx, p, value)
		return value, parsed, err
	}
	p, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return value, document.Parsed{}, err
	}
	if err = project.VerifyPrivateDataLayout(p); err != nil {
		return value, document.Parsed{}, err
	}
	root, err := os.OpenRoot(project.PrivateDataPath(p))
	if err != nil {
		return value, document.Parsed{}, err
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.FromSlash(value.CacheRelativePath))
	var parsed document.Parsed
	// UnitCount on the attachment may already reflect a newer derived cache.
	legacy := value
	legacy.Status = StatusParsing
	if err != nil || json.Unmarshal(data, &parsed) != nil || !validParsedCacheStructure(parsed, legacy) || parsed.Metadata["structureParser"] == document.PDFParserVersion {
		return value, document.Parsed{}, fmt.Errorf("旧版 PDF 索引缓存不可用；请重新导入材料以建立新版索引，不能覆盖旧版引用依据")
	}
	return value, parsed, nil
}

func (s *Service) parseAndPersistLocked(ctx context.Context, selectedProject project.Project, value Attachment) (document.Parsed, error) {
	value = versionedPDFCache(value)
	rootPath := project.PrivateDataPath(selectedProject)
	original := filepath.Join(rootPath, filepath.FromSlash(value.StorageRelativePath))
	if !insidePath(rootPath, original) {
		return document.Parsed{}, fmt.Errorf("attachment storage path is invalid")
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return document.Parsed{}, fmt.Errorf("project attachment root is unavailable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(original)
	if err != nil || !insidePath(resolvedRoot, resolved) {
		return document.Parsed{}, fmt.Errorf("attachment storage path is unavailable or escapes the project data root")
	}
	if err := verifyStoredObject(resolved, value.SizeBytes, value.SHA256); err != nil {
		return document.Parsed{}, err
	}
	parsed, err := document.Parse(ctx, resolved, value.Format)
	if err != nil {
		return document.Parsed{}, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return document.Parsed{}, err
	}
	defer root.Close()
	cacheRelative := filepath.FromSlash(value.CacheRelativePath)
	if err := root.MkdirAll(filepath.Dir(cacheRelative), 0o700); err != nil {
		return document.Parsed{}, err
	}
	if err := root.MkdirAll("tmp", 0o700); err != nil {
		return document.Parsed{}, err
	}
	tempID, err := id.New()
	if err != nil {
		return document.Parsed{}, err
	}
	tempRelative := filepath.Join("tmp", "parsed-"+tempID+".json")
	output, err := root.OpenFile(tempRelative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return document.Parsed{}, err
	}
	encoder := json.NewEncoder(output)
	encodeErr := encoder.Encode(parsed)
	syncErr := output.Sync()
	closeErr := output.Close()
	if encodeErr != nil || syncErr != nil || closeErr != nil {
		_ = root.Remove(tempRelative)
		if encodeErr != nil {
			return document.Parsed{}, encodeErr
		}
		return document.Parsed{}, fmt.Errorf("flush parsed document cache")
	}
	_ = root.Remove(cacheRelative)
	if err := root.Rename(tempRelative, cacheRelative); err != nil {
		_ = root.Remove(tempRelative)
		return document.Parsed{}, err
	}
	return parsed, nil
}

// Keep earlier derived text intact for audit; original PDF bytes and persisted
// attachment identity are unchanged. Each parser generation has its own cache.
func versionedPDFCache(value Attachment) Attachment {
	if value.Format == document.FormatPDF {
		value.CacheRelativePath = filepath.ToSlash(filepath.Join("cache", "documents", document.PDFParserVersion, value.ID+".json"))
	}
	return value
}

func verifyStoredObject(path string, expectedSize int64, expectedHash string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open stored attachment: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expectedSize {
		return fmt.Errorf("stored attachment size no longer matches its import record")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxImportFileBytes+1)); err != nil {
		return fmt.Errorf("verify stored attachment: %w", err)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedHash) {
		return fmt.Errorf("stored attachment SHA256 no longer matches its import record")
	}
	return nil
}

func maxBytesForFormat(format document.Format) int64 {
	if format == document.FormatImage {
		return maxImageFileBytes
	}
	return maxImportFileBytes
}

func inspectImage(path string) (string, int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, 0, fmt.Errorf("open image attachment: %w", err)
	}
	defer file.Close()
	header := make([]byte, 30)
	count, readErr := io.ReadFull(file, header)
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return "", 0, 0, fmt.Errorf("read image attachment: %w", readErr)
	}
	var mimeType string
	var width, height int
	if count >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP" {
		if count < len(header) {
			return "", 0, 0, fmt.Errorf("image content is not a valid WebP file")
		}
		mimeType = "image/webp"
		width, height = webpDimensions(header)
	} else {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", 0, 0, fmt.Errorf("rewind image attachment: %w", err)
		}
		config, format, decodeErr := image.DecodeConfig(file)
		if decodeErr != nil {
			return "", 0, 0, fmt.Errorf("image content is not a supported JPEG, PNG or WebP file")
		}
		switch format {
		case "jpeg":
			mimeType = "image/jpeg"
		case "png":
			mimeType = "image/png"
		default:
			return "", 0, 0, fmt.Errorf("image content is not a supported JPEG, PNG or WebP file")
		}
		width, height = config.Width, config.Height
	}
	if width <= 0 || height <= 0 || width > maxImageDimension || height > maxImageDimension || int64(width)*int64(height) > maxImagePixels {
		return "", 0, 0, fmt.Errorf("image dimensions exceed the supported 8192 px / 40 MP limit")
	}
	return mimeType, width, height, nil
}

func webpDimensions(header []byte) (int, int) {
	if len(header) < 30 {
		return 0, 0
	}
	switch string(header[12:16]) {
	case "VP8X":
		return 1 + int(header[24]) + int(header[25])<<8 + int(header[26])<<16,
			1 + int(header[27]) + int(header[28])<<8 + int(header[29])<<16
	case "VP8L":
		if header[20] != 0x2f {
			return 0, 0
		}
		return 1 + int(header[21]) + (int(header[22])&0x3f)<<8,
			1 + (int(header[22]) >> 6) + int(header[23])<<2 + (int(header[24])&0x0f)<<10
	case "VP8 ":
		if header[23] != 0x9d || header[24] != 0x01 || header[25] != 0x2a {
			return 0, 0
		}
		return int(header[26]) | int(header[27]&0x3f)<<8, int(header[28]) | int(header[29]&0x3f)<<8
	default:
		return 0, 0
	}
}

func safeFileName(value string) string {
	value = strings.TrimSpace(filepath.Base(value))
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return '_'
		}
		return r
	}, value)
	if value == "" || value == "." {
		value = "attachment"
	}
	runes := []rune(value)
	if len(runes) > 180 {
		extension := filepath.Ext(value)
		base := []rune(strings.TrimSuffix(value, extension))
		limit := max(1, 180-len([]rune(extension)))
		if len(base) > limit {
			base = base[:limit]
		}
		value = string(base) + extension
	}
	return value
}

func insidePath(root, target string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, targetAbs)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func boundedError(err error) string {
	value := strings.TrimSpace(err.Error())
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000])
	}
	return value
}

package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/id"
)

const (
	maxImportFiles      = 20
	maxImportFileBytes  = 250 << 20
	maxImportBatchBytes = 1 << 30
)

type ProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

type Service struct {
	repository Repository
	projects   ProjectLoader
	now        func() time.Time
	mu         sync.Mutex
}

func NewService(repository Repository, projects ProjectLoader) *Service {
	return &Service{repository: repository, projects: projects, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) ImportPaths(ctx context.Context, projectID string, paths []string) (ImportBatch, error) {
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
		value, importErr := s.importOne(ctx, selectedProject, sourcePath)
		if value.ID != "" {
			result.Attachments = append(result.Attachments, value)
		}
		if importErr != nil {
			result.Errors = append(result.Errors, ImportError{Path: sourcePath, Message: importErr.Error()})
		}
	}
	return result, nil
}

func (s *Service) importOne(ctx context.Context, selectedProject project.Project, sourcePath string) (Attachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sourcePath = strings.TrimSpace(sourcePath)
	format, supported := document.FormatForName(sourcePath)
	if sourcePath == "" || !supported {
		return Attachment{}, fmt.Errorf("supported formats are PDF, DOCX, XLSX, TXT, Markdown, CSV and TSV")
	}
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return Attachment{}, fmt.Errorf("resolve attachment path: %w", err)
	}
	privateRoot := project.PrivateDataPath(selectedProject)
	if insidePath(privateRoot, absSource) {
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
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxImportFileBytes {
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
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(input, maxImportFileBytes+1))
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written != info.Size() || written > maxImportFileBytes {
		_ = root.Remove(tempRelative)
		if copyErr != nil {
			return Attachment{}, fmt.Errorf("copy attachment: %w", copyErr)
		}
		return Attachment{}, fmt.Errorf("attachment changed while it was being imported")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if existing, found, findErr := s.repository.FindByHash(ctx, selectedProject.ID, digest); findErr != nil {
		_ = root.Remove(tempRelative)
		return Attachment{}, findErr
	} else if found {
		_ = root.Remove(tempRelative)
		parsed, parseErr := s.ensureParsedLocked(ctx, selectedProject, existing)
		if parseErr == nil {
			existing.UnitCount = len(parsed.Units)
			existing.ExtractedRunes = parsed.ExtractedRunes
			existing.Truncated = parsed.Truncated
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
		MIMEType: document.MIMEType(format), Format: format, SizeBytes: written, SHA256: digest,
		StorageRelativePath: filepath.ToSlash(storageRelative),
		CacheRelativePath:   filepath.ToSlash(filepath.Join("cache", "documents", attachmentID+".json")),
		Status:              StatusParsing, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.Create(ctx, value); err != nil {
		if objectCreated {
			_ = root.Remove(storageRelative)
		}
		return Attachment{}, err
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
	value.UpdatedAt = s.now()
	if err := s.repository.UpdateParse(ctx, value); err != nil {
		return value, err
	}
	return value, nil
}

func (s *Service) List(ctx context.Context, projectID string) ([]Attachment, error) {
	return s.repository.ListByProject(ctx, strings.TrimSpace(projectID))
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
	value.ErrorMessage = ""
	value.UpdatedAt = s.now()
	if err := s.repository.UpdateParse(ctx, value); err != nil {
		return value, document.Parsed{}, err
	}
	return value, parsed, nil
}

func (s *Service) ensureParsedLocked(ctx context.Context, selectedProject project.Project, value Attachment) (document.Parsed, error) {
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

func (s *Service) parseAndPersistLocked(ctx context.Context, selectedProject project.Project, value Attachment) (document.Parsed, error) {
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

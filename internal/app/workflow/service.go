package workflow

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

const maxWorkflowInputFileBytes int64 = 256 << 20
const maxDelimitedInputFileBytes int64 = 64 << 20

type InputFile struct {
	RelativePath string `json:"relativePath"`
	Name         string `json:"name"`
	SizeBytes    int64  `json:"sizeBytes"`
	SHA256       string `json:"sha256"`
	Staged       bool   `json:"staged"`
}

type ProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

type Service struct {
	repository Repository
	projects   ProjectLoader
	compiler   *Compiler
	saveMu     sync.Mutex
	now        func() time.Time
	newID      func() (string, error)
}

func NewService(repository Repository, projects ProjectLoader, compiler *Compiler) (*Service, error) {
	if repository == nil || projects == nil || compiler == nil {
		return nil, fmt.Errorf("Workflow service is not configured")
	}
	return &Service{repository: repository, projects: projects, compiler: compiler, now: func() time.Time { return time.Now().UTC() }, newID: id.New}, nil
}

func (s *Service) Validate(ctx context.Context, projectID string, definition Definition) Preview {
	if _, err := s.projects.Get(ctx, strings.TrimSpace(projectID)); err != nil {
		return Preview{Valid: false, Diagnostics: []Diagnostic{diagnostic("error", "project", "projectId", err.Error())}, Nodes: []PreviewNode{}}
	}
	return s.compiler.Preview(ctx, definition)
}

func (s *Service) Save(ctx context.Context, command SaveCommand) (SaveResult, error) {
	return s.save(ctx, command, PurposeUserPlan)
}

func (s *Service) save(ctx context.Context, command SaveCommand, purpose Purpose) (SaveResult, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.WorkflowID = strings.TrimSpace(command.WorkflowID)
	command.ExpectedCurrentVersionID = strings.TrimSpace(command.ExpectedCurrentVersionID)
	if command.ProjectID == "" {
		return SaveResult{}, fmt.Errorf("project is required")
	}
	if !purpose.Valid() {
		return SaveResult{}, fmt.Errorf("invalid Workflow purpose")
	}
	if command.WorkflowID == "" && command.ExpectedCurrentVersionID != "" {
		return SaveResult{}, fmt.Errorf("a new Workflow cannot have an expected current version")
	}
	if command.WorkflowID != "" && command.ExpectedCurrentVersionID == "" {
		return SaveResult{}, fmt.Errorf("expected current version is required when editing a Workflow")
	}
	if _, err := s.projects.Get(ctx, command.ProjectID); err != nil {
		return SaveResult{}, err
	}
	compiled, err := s.compiler.Compile(ctx, command.Definition)
	if err != nil {
		return SaveResult{}, fmt.Errorf("Workflow static validation failed: %w", err)
	}
	workflowID := command.WorkflowID
	created := workflowID == ""
	if created {
		workflowID, err = s.newID()
		if err != nil {
			return SaveResult{}, err
		}
	}
	versionID, err := s.newID()
	if err != nil {
		return SaveResult{}, err
	}
	now := s.now()
	value := Workflow{ID: workflowID, ProjectID: command.ProjectID, Purpose: purpose, Name: strings.TrimSpace(command.Definition.Name), Description: strings.TrimSpace(command.Definition.Description), CurrentVersionID: versionID, CreatedAt: now, UpdatedAt: now}
	version := Version{
		ID:                versionID,
		WorkflowID:        workflowID,
		Definition:        command.Definition,
		DefinitionSHA256:  compiled.DefinitionSHA256,
		Compilation:       compiled,
		CompilationSHA256: compiled.CompilationSHA256,
		CreatedAt:         now,
	}
	return s.repository.SaveVersion(ctx, SaveRecord{
		Workflow:                 value,
		Version:                  version,
		ExpectedCurrentVersionID: command.ExpectedCurrentVersionID,
	})
}

func (s *Service) List(ctx context.Context, projectID string) ([]Workflow, error) {
	return s.repository.List(ctx, strings.TrimSpace(projectID))
}
func (s *Service) Get(ctx context.Context, projectID, workflowID string) (Detail, error) {
	detail, err := s.repository.Get(ctx, strings.TrimSpace(projectID), strings.TrimSpace(workflowID))
	if err != nil {
		return detail, err
	}
	for index := range detail.Versions {
		detail.Versions[index].RuntimeInputs = append([]Port(nil), detail.Versions[index].Compilation.Inputs...)
	}
	return detail, nil
}

// Delete removes a saved research plan together with its version and Run
// history. The repository rejects deletion while any Run can still resume or
// mutate state.
func (s *Service) Delete(ctx context.Context, projectID, workflowID string) error {
	projectID, workflowID = strings.TrimSpace(projectID), strings.TrimSpace(workflowID)
	if projectID == "" || workflowID == "" {
		return fmt.Errorf("project and Workflow are required")
	}
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return err
	}
	detail, err := s.repository.Get(ctx, projectID, workflowID)
	if err != nil {
		return err
	}
	if detail.Workflow.Purpose != PurposeUserPlan {
		return fmt.Errorf("system Workflow cannot be deleted as a saved plan")
	}
	return s.repository.Delete(ctx, projectID, workflowID)
}

// deleteUnstarted removes only a just-created Workflow that has never had a
// Run. It is intentionally narrower than Delete, which also removes
// equivalent saved plans and terminal history. Starter adoption uses this
// rollback path when creating the formal Run fails after the plan was saved.
func (s *Service) deleteUnstarted(ctx context.Context, projectID, workflowID, versionID string) error {
	if deleter, ok := s.repository.(interface {
		DeleteUnstarted(context.Context, string, string, string) error
	}); ok {
		return deleter.DeleteUnstarted(ctx, strings.TrimSpace(projectID), strings.TrimSpace(workflowID), strings.TrimSpace(versionID))
	}
	return fmt.Errorf("Workflow repository does not support unstarted rollback")
}

// StageInputFile turns an explicitly selected tabular file into a stable
// Workspace input. Both external and existing Workspace files are copied into
// a content-addressed snapshot without modifying their source.
func (s *Service) StageInputFile(ctx context.Context, projectID, sourcePath, kind string) (InputFile, error) {
	return s.stageInputFile(ctx, projectID, sourcePath, kind, "")
}

// StageInputFileForTask creates the immutable input snapshot inside the
// private workspace of one research task. The selected source may still be a
// normal project file, but the resulting snapshot is never shared implicitly.
func (s *Service) StageInputFileForTask(ctx context.Context, projectID, sourcePath, kind, taskID string) (InputFile, error) {
	return s.stageInputFile(ctx, projectID, sourcePath, kind, taskID)
}

func (s *Service) stageInputFile(ctx context.Context, projectID, sourcePath, kind, taskID string) (InputFile, error) {
	selected, err := s.projects.Get(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return InputFile{}, err
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return InputFile{}, fmt.Errorf("input file is required")
	}
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return InputFile{}, fmt.Errorf("resolve input file: %w", err)
	}
	sourceRoot, err := pathguard.Open(filepath.Dir(absSource))
	if err != nil {
		return InputFile{}, fmt.Errorf("validate input file path: %w", err)
	}
	defer sourceRoot.Close()
	source, _, err := sourceRoot.OpenFile(filepath.Base(absSource))
	if err != nil {
		return InputFile{}, fmt.Errorf("open input file: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxWorkflowInputFileBytes {
		return InputFile{}, fmt.Errorf("input must be a non-empty regular CSV, TSV, or XLSX file no larger than 256 MiB")
	}
	extension := strings.ToLower(filepath.Ext(info.Name()))
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "delimited" && kind != "xlsx" && kind != "tabular" {
		return InputFile{}, fmt.Errorf("unsupported Workflow input file kind")
	}
	allowed := kind == "tabular" && (extension == ".csv" || extension == ".tsv" || extension == ".xlsx") || kind == "delimited" && (extension == ".csv" || extension == ".tsv") || kind == "xlsx" && extension == ".xlsx"
	if !allowed {
		return InputFile{}, fmt.Errorf("selected file does not match the required %s data format", kind)
	}
	if extension != ".xlsx" && info.Size() > maxDelimitedInputFileBytes {
		return InputFile{}, fmt.Errorf("CSV/TSV input cannot exceed 64 MiB")
	}
	header := make([]byte, 4096)
	read, readErr := source.Read(header)
	if readErr != nil && readErr != io.EOF {
		return InputFile{}, fmt.Errorf("inspect input file: %w", readErr)
	}
	header = header[:read]
	if extension == ".xlsx" {
		if len(header) < 4 || string(header[:2]) != "PK" {
			return InputFile{}, fmt.Errorf("selected .xlsx file is not an Office ZIP workbook")
		}
		if err := validateXLSXArchive(source, info.Size()); err != nil {
			return InputFile{}, err
		}
	} else if strings.IndexByte(string(header), 0) >= 0 {
		return InputFile{}, fmt.Errorf("selected CSV/TSV file contains binary NUL bytes")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return InputFile{}, err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, source); err != nil {
		return InputFile{}, fmt.Errorf("hash input file: %w", err)
	}
	sha := hex.EncodeToString(digest.Sum(nil))
	projectGuard, err := pathguard.Open(selected.WorkspacePath)
	if err != nil {
		return InputFile{}, err
	}
	defer projectGuard.Close()
	if relative, relErr := filepath.Rel(selected.WorkspacePath, absSource); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		clean, cleanErr := projectGuard.Relative(relative)
		if cleanErr != nil || clean == "." || workflowPrivatePath(clean) {
			return InputFile{}, fmt.Errorf("Workspace input must be a regular project file outside .sciaide")
		}
		if clean != "." {
			guarded, _, openErr := projectGuard.OpenFile(clean)
			if openErr != nil {
				return InputFile{}, fmt.Errorf("validate Workspace input: %w", openErr)
			}
			guardedInfo, statErr := guarded.Stat()
			guardedDigest := sha256.New()
			_, hashErr := io.Copy(guardedDigest, guarded)
			closeErr := guarded.Close()
			if statErr != nil || hashErr != nil || closeErr != nil || !guardedInfo.Mode().IsRegular() || guardedInfo.Size() != info.Size() || hex.EncodeToString(guardedDigest.Sum(nil)) != sha {
				return InputFile{}, fmt.Errorf("Workspace input changed while it was selected")
			}
		}
	}
	destinationRoot := selected.WorkspacePath
	if strings.TrimSpace(taskID) != "" {
		destinationRoot, err = project.ResearchTaskWorkspacePath(selected, taskID)
		if err != nil {
			return InputFile{}, err
		}
		if err := os.MkdirAll(destinationRoot, 0o700); err != nil {
			return InputFile{}, err
		}
	}
	guard, err := pathguard.Open(destinationRoot)
	if err != nil {
		return InputFile{}, err
	}
	defer guard.Close()
	if _, err := guard.MkdirAll("research-inputs", 0o700); err != nil {
		return InputFile{}, err
	}
	base := workflowSnapshotBase(info.Name(), extension)
	target := filepath.Join("research-inputs", base+"-"+sha+extension)
	if existing, _, openErr := guard.OpenFile(target); openErr == nil {
		existingInfo, statErr := existing.Stat()
		existingDigest := sha256.New()
		_, hashErr := io.Copy(existingDigest, existing)
		closeErr := existing.Close()
		if statErr == nil && hashErr == nil && closeErr == nil && existingInfo.Mode().IsRegular() && existingInfo.Size() == info.Size() && hex.EncodeToString(existingDigest.Sum(nil)) == sha {
			if err := validateStagedWorkflowInput(guard, target, kind); err != nil {
				return InputFile{}, err
			}
			return InputFile{RelativePath: filepath.ToSlash(target), Name: info.Name(), SizeBytes: info.Size(), SHA256: sha, Staged: true}, nil
		}
		return InputFile{}, fmt.Errorf("staged input path already exists with different content")
	}
	destination, cleanTarget, err := guard.CreateFile(target, 0o600)
	if err != nil {
		return InputFile{}, fmt.Errorf("create staged input: %w", err)
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		destination.Close()
		_ = guard.Remove(cleanTarget)
		return InputFile{}, err
	}
	copyDigest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, copyDigest), source)
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || written != info.Size() || hex.EncodeToString(copyDigest.Sum(nil)) != sha {
		_ = guard.Remove(cleanTarget)
		if copyErr != nil {
			return InputFile{}, fmt.Errorf("stage input file: %w", copyErr)
		}
		if closeErr != nil {
			return InputFile{}, fmt.Errorf("close staged input: %w", closeErr)
		}
		return InputFile{}, fmt.Errorf("source input changed while it was being copied")
	}
	if err := validateStagedWorkflowInput(guard, cleanTarget, kind); err != nil {
		_ = guard.Remove(cleanTarget)
		return InputFile{}, err
	}
	return InputFile{RelativePath: filepath.ToSlash(target), Name: info.Name(), SizeBytes: info.Size(), SHA256: sha, Staged: true}, nil
}

func validateStagedWorkflowInput(guard *pathguard.Guard, relative, kind string) error {
	file, _, err := guard.OpenFile(relative)
	if err != nil {
		return fmt.Errorf("open staged input for validation: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("staged input is not a regular file")
	}
	if kind == "xlsx" || kind == "tabular" && strings.EqualFold(filepath.Ext(relative), ".xlsx") {
		return validateXLSXArchive(file, info.Size())
	}
	buffer := make([]byte, 64*1024)
	for {
		read, readErr := file.Read(buffer)
		if bytes.IndexByte(buffer[:read], 0) >= 0 {
			return fmt.Errorf("selected CSV/TSV file contains binary NUL bytes")
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("validate staged input: %w", readErr)
		}
	}
}

func workflowSnapshotBase(name, extension string) string {
	base := strings.TrimSuffix(name, extension)
	if separator := strings.LastIndexByte(base, '-'); separator >= 0 && len(base)-separator-1 == sha256.Size*2 {
		if _, err := hex.DecodeString(base[separator+1:]); err == nil {
			base = base[:separator]
		}
	}
	return workflowSafeFileName(base)
}

func validateXLSXArchive(source io.ReaderAt, size int64) error {
	archive, err := zip.NewReader(source, size)
	if err != nil {
		return fmt.Errorf("open XLSX archive: %w", err)
	}
	if len(archive.File) == 0 || len(archive.File) > 2_000 {
		return fmt.Errorf("XLSX archive must contain 1-2000 entries")
	}
	var total uint64
	workbook := false
	for _, value := range archive.File {
		name := filepath.ToSlash(value.Name)
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
		if name == "" || strings.HasPrefix(name, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("XLSX archive contains an unsafe entry path")
		}
		if value.UncompressedSize64 > 64<<20 {
			return fmt.Errorf("XLSX archive contains an entry larger than 64 MiB")
		}
		total += value.UncompressedSize64
		if total > 256<<20 {
			return fmt.Errorf("XLSX archive expands beyond 256 MiB")
		}
		workbook = workbook || strings.EqualFold(name, "xl/workbook.xml")
	}
	if !workbook {
		return fmt.Errorf("XLSX archive is missing xl/workbook.xml")
	}
	return nil
}

func workflowSafeFileName(value string) string {
	value = strings.TrimSpace(value)
	var result strings.Builder
	for _, character := range value {
		if character < 32 || strings.ContainsRune(`<>:"/\\|?*`, character) {
			result.WriteByte('_')
		} else {
			result.WriteRune(character)
		}
		if result.Len() >= 80 {
			break
		}
	}
	clean := strings.Trim(result.String(), " ._")
	if clean == "" {
		return "research-data"
	}
	return clean
}

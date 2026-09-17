package pythonenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/platform/filepublish"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

type KernelResult struct {
	KernelID               string            `json:"kernelId"`
	ExecutionID            string            `json:"executionId"`
	Sequence               int               `json:"sequence"`
	Status                 string            `json:"status"`
	Stdout                 string            `json:"stdout"`
	Stderr                 string            `json:"stderr"`
	StdoutTruncated        bool              `json:"stdoutTruncated"`
	StderrTruncated        bool              `json:"stderrTruncated"`
	Value                  any               `json:"value,omitempty"`
	ValueType              string            `json:"valueType,omitempty"`
	Table                  *KernelTable      `json:"table,omitempty"`
	Exception              *KernelError      `json:"exception,omitempty"`
	Images                 []string          `json:"images"`
	CodeSHA256             string            `json:"codeSha256"`
	InputSHA256            map[string]string `json:"inputSha256"`
	OutputSHA256           map[string]string `json:"outputSha256"`
	EnvironmentFingerprint string            `json:"environmentFingerprint"`
	ReproductionSHA256     string            `json:"reproductionSha256"`
	StartedAt              time.Time         `json:"startedAt"`
	FinishedAt             time.Time         `json:"finishedAt"`
}

type KernelTable struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Total   int      `json:"total"`
}

type KernelError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	Traceback string `json:"traceback"`
}

type KernelExecuteRequest struct {
	ProjectID     string
	WorkspacePath string
	ToolCallID    string
	Code          string
	InputPaths    []string
	InputData     json.RawMessage
	OutputPaths   []string
	FigurePath    string
	Timeout       time.Duration
	Environment   Environment
}

type KernelRuntime interface {
	Execute(ctx context.Context, request KernelExecuteRequest, workspacePath string) (KernelResult, error)
	Stop(projectID string) error
	Restart(projectID string) error
	Close() error
}

// ScopedKernelRuntime is implemented by runtimes that keep an isolated
// interpreter per project workspace. The optional interface lets older test
// doubles and integrations retain the original project-wide Stop/Restart API.
type ScopedKernelRuntime interface {
	StopScoped(projectID, workspacePath string) error
	RestartScoped(projectID, workspacePath string) error
}

type KernelExecutionAudit struct {
	ID                     string
	ProjectID              string
	ToolCallID             string
	RunID                  string
	EnvironmentID          string
	EnvironmentFingerprint string
	KernelID               string
	ExecutionID            string
	Sequence               int
	Status                 string
	CodeSHA256             string
	InputSHA256            map[string]string
	OutputSHA256           map[string]string
	ReproductionSHA256     string
	StdoutTruncated        bool
	StderrTruncated        bool
	ExceptionType          string
	ErrorMessage           string
	StartedAt              time.Time
	CompletedAt            time.Time
}

type KernelAuditRepository interface {
	SaveKernelExecution(ctx context.Context, value KernelExecutionAudit) error
}

type KernelService struct {
	projects     projectService
	environments *Service
	runtime      KernelRuntime
	audits       KernelAuditRepository
	mu           sync.Mutex
}

func (s *KernelService) Project(ctx context.Context, projectID string) (project.Project, error) {
	return s.projects.Get(ctx, strings.TrimSpace(projectID))
}

type KernelRecoveryResult struct {
	TemporaryPathsRemoved int `json:"temporaryPathsRemoved"`
}

type projectLister interface {
	List(ctx context.Context) ([]project.Project, error)
}

func (s *KernelService) SetAuditRepository(repository KernelAuditRepository) {
	s.mu.Lock()
	s.audits = repository
	s.mu.Unlock()
}

func NewKernelService(projects projectService, environments *Service, runtime KernelRuntime) (*KernelService, error) {
	if projects == nil || environments == nil || runtime == nil {
		return nil, fmt.Errorf("Python Kernel service dependencies are required")
	}
	return &KernelService{projects: projects, environments: environments, runtime: runtime}, nil
}

func (s *KernelService) Environment(ctx context.Context, projectID string) (Environment, error) {
	return s.environments.Get(ctx, projectID)
}

func (s *KernelService) Execute(ctx context.Context, request KernelExecuteRequest) (KernelResult, error) {
	return s.ExecuteTool(ctx, request, "", "")
}

func (s *KernelService) ExecuteTool(ctx context.Context, request KernelExecuteRequest, toolCallID, runID string) (KernelResult, error) {
	request.ProjectID, request.Code = strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.Code)
	if request.ProjectID == "" || request.Code == "" {
		return KernelResult{}, fmt.Errorf("project and Python code are required")
	}
	if request.Timeout <= 0 || request.Timeout > 5*time.Minute {
		return KernelResult{}, fmt.Errorf("Kernel timeout must be between 1 second and 5 minutes")
	}
	unlock := s.environments.lockProject(request.ProjectID)
	defer unlock()
	selected, err := s.projects.Get(ctx, request.ProjectID)
	if err != nil {
		return KernelResult{}, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return KernelResult{}, err
	}
	environment, err := s.environments.Get(ctx, request.ProjectID)
	if err != nil {
		return KernelResult{}, err
	}
	if environment.State != StateReady {
		return KernelResult{}, fmt.Errorf("当前项目尚未创建可用的 Python 环境；请先在项目 Python 环境中创建或重建")
	}
	if len(request.InputData) == 0 {
		request.InputData = json.RawMessage(`null`)
	}
	if len(request.InputData) > 256*1024 || !json.Valid(request.InputData) {
		return KernelResult{}, fmt.Errorf("Kernel structured input is invalid or exceeds 256 KiB")
	}
	workspaceRoot := selected.WorkspacePath
	if strings.TrimSpace(request.WorkspacePath) != "" {
		workspaceRoot = request.WorkspacePath
	}
	guard, err := pathguard.Open(workspaceRoot)
	if err != nil {
		return KernelResult{}, err
	}
	defer guard.Close()
	inputs, inputHashes, err := validateKernelInputs(guard, request.InputPaths)
	if err != nil {
		return KernelResult{}, err
	}
	outputs, _, err := validateKernelOutputs(guard, request.OutputPaths)
	if err != nil {
		return KernelResult{}, err
	}
	stagingID, err := id.New()
	if err != nil {
		return KernelResult{}, err
	}
	stagedOutputs, stagingDirectory, err := prepareKernelOutputStaging(guard, outputs, stagingID)
	if err != nil {
		return KernelResult{}, err
	}
	defer cleanupKernelOutputStaging(guard, stagingDirectory)
	request.InputPaths, request.OutputPaths, request.FigurePath, request.Environment = inputs, stagedOutputs, filepath.ToSlash(filepath.Join(stagingDirectory, "figures")), environment
	restoreInputs, err := makeKernelInputsReadOnly(guard, inputs)
	if err != nil {
		return KernelResult{}, err
	}
	defer restoreInputs()
	result, runErr := s.runtime.Execute(ctx, request, workspaceRoot)
	result.CodeSHA256 = hashKernelText(request.Code)
	result.InputSHA256 = cloneKernelHashes(inputHashes)
	if string(request.InputData) != "null" {
		result.InputSHA256["$data"] = hashKernelJSON(request.InputData)
	}
	result.EnvironmentFingerprint = environment.EnvironmentFingerprint
	if changed, verifyErr := verifyKernelInputs(guard, inputHashes); verifyErr != nil {
		_ = s.stopRuntime(request)
		return KernelResult{}, verifyErr
	} else if changed != "" {
		_ = s.stopRuntime(request)
		return KernelResult{}, fmt.Errorf("Kernel modified declared read-only input %q; the Kernel was stopped", changed)
	}
	outputHashes := map[string]string{}
	if runErr == nil && result.Status == "success" {
		stagedHashes, verifyErr := collectKernelOutputs(guard, stagedOutputs, map[string]string{})
		if verifyErr != nil {
			_ = s.stopRuntime(request)
			return KernelResult{}, verifyErr
		}
		stagedImages, stagedImageHashes, imageErr := validateKernelImages(guard, request.FigurePath, result.Images)
		if imageErr != nil {
			_ = s.stopRuntime(request)
			return KernelResult{}, imageErr
		}
		images, imageErr := kernelImageTargets(guard, result.ExecutionID, stagedImages)
		if imageErr != nil {
			_ = s.stopRuntime(request)
			return KernelResult{}, imageErr
		}
		for path, hash := range stagedImageHashes {
			stagedHashes[path] = hash
		}
		allStaged := append(append([]string(nil), stagedOutputs...), stagedImages...)
		allOutputs := append(append([]string(nil), outputs...), images...)
		outputHashes, verifyErr = publishKernelOutputs(guard, allStaged, allOutputs, stagedHashes)
		if verifyErr != nil {
			_ = s.stopRuntime(request)
			return KernelResult{}, verifyErr
		}
		result.Images = images
	} else {
		result.Images = []string{}
	}
	result.OutputSHA256 = outputHashes
	result.ReproductionSHA256 = kernelReproductionHash(result, inputs, outputs)
	if result.Images == nil {
		result.Images = []string{}
	}
	if strings.TrimSpace(toolCallID) != "" {
		auditID, auditErr := id.New()
		if auditErr != nil {
			return KernelResult{}, auditErr
		}
		status, message, exceptionType := result.Status, "", ""
		if runErr != nil {
			status, message = "failed", runErr.Error()
			if errors.Is(runErr, context.Canceled) {
				status = "cancelled"
			}
			if errors.Is(runErr, context.DeadlineExceeded) {
				status = "timed_out"
			}
		}
		if result.Exception != nil {
			exceptionType, message = result.Exception.Type, result.Exception.Message
		}
		s.mu.Lock()
		audits := s.audits
		s.mu.Unlock()
		if audits == nil {
			return KernelResult{}, fmt.Errorf("Python Kernel execution audit is not configured")
		}
		if auditErr := audits.SaveKernelExecution(context.Background(), KernelExecutionAudit{
			ID: auditID, ProjectID: request.ProjectID, ToolCallID: toolCallID, RunID: runID, EnvironmentID: environment.ID,
			EnvironmentFingerprint: environment.EnvironmentFingerprint, KernelID: result.KernelID, ExecutionID: result.ExecutionID,
			Sequence: result.Sequence, Status: status, CodeSHA256: result.CodeSHA256, InputSHA256: result.InputSHA256,
			OutputSHA256: result.OutputSHA256, ReproductionSHA256: result.ReproductionSHA256,
			StdoutTruncated: result.StdoutTruncated, StderrTruncated: result.StderrTruncated,
			ExceptionType: exceptionType, ErrorMessage: message, StartedAt: result.StartedAt, CompletedAt: result.FinishedAt,
		}); auditErr != nil {
			return KernelResult{}, fmt.Errorf("save Python Kernel execution audit: %w", auditErr)
		}
	}
	return result, runErr
}

func cloneKernelHashes(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values)+1)
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func makeKernelInputsReadOnly(guard *pathguard.Guard, paths []string) (func(), error) {
	type permission struct {
		path string
		mode os.FileMode
	}
	values := make([]permission, 0, len(paths))
	restore := func() {
		for index := len(values) - 1; index >= 0; index-- {
			if absolute, err := guard.Absolute(values[index].path); err == nil {
				_ = os.Chmod(absolute, values[index].mode)
			}
		}
	}
	for _, path := range paths {
		absolute, err := guard.Absolute(path)
		if err != nil {
			restore()
			return nil, err
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			restore()
			return nil, fmt.Errorf("Kernel input is not a regular file")
		}
		values = append(values, permission{path: path, mode: info.Mode()})
		if err := os.Chmod(absolute, info.Mode().Perm()&^0o222); err != nil {
			restore()
			return nil, fmt.Errorf("make Kernel input read-only: %w", err)
		}
	}
	return restore, nil
}

func validateKernelImages(guard *pathguard.Guard, stagingDirectory string, paths []string) ([]string, map[string]string, error) {
	if len(paths) > 16 {
		return nil, nil, fmt.Errorf("Kernel produced too many figures")
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(stagingDirectory), "/") + "/"
	clean := make([]string, 0, len(paths))
	hashes := map[string]string{}
	for _, path := range paths {
		relative, err := guard.Relative(strings.TrimSpace(path))
		if err != nil || relative == "." {
			return nil, nil, fmt.Errorf("Kernel figure path is invalid")
		}
		relative = filepath.ToSlash(relative)
		if !strings.HasPrefix(relative, prefix) || strings.ToLower(filepath.Ext(relative)) != ".png" {
			return nil, nil, fmt.Errorf("Kernel figure path is outside its execution output directory")
		}
		hash, err := hashGuardFile(guard, relative)
		if err != nil {
			return nil, nil, err
		}
		clean, hashes[relative] = append(clean, relative), hash
	}
	return clean, hashes, nil
}

func kernelImageTargets(guard *pathguard.Guard, executionID string, staged []string) ([]string, error) {
	executionID = strings.TrimSpace(executionID)
	if executionID == "" || filepath.Base(executionID) != executionID {
		return nil, fmt.Errorf("Kernel figure execution identity is invalid")
	}
	if len(staged) == 0 {
		return []string{}, nil
	}
	root := filepath.ToSlash(filepath.Join("analysis-output", "figures", executionID))
	if _, err := guard.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create Kernel figure output directory: %w", err)
	}
	outputs := make([]string, len(staged))
	for index, path := range staged {
		outputs[index] = filepath.ToSlash(filepath.Join(root, filepath.Base(path)))
	}
	return outputs, nil
}

func (s *KernelService) Stop(projectID string) error {
	return s.runtime.Stop(strings.TrimSpace(projectID))
}
func (s *KernelService) Restart(projectID string) error {
	return s.runtime.Restart(strings.TrimSpace(projectID))
}

func (s *KernelService) stopRuntime(request KernelExecuteRequest) error {
	if scoped, ok := s.runtime.(ScopedKernelRuntime); ok && strings.TrimSpace(request.WorkspacePath) != "" {
		return scoped.StopScoped(strings.TrimSpace(request.ProjectID), request.WorkspacePath)
	}
	return s.runtime.Stop(strings.TrimSpace(request.ProjectID))
}
func (s *KernelService) Close() error { return s.runtime.Close() }

// Recover removes only abandoned Kernel staging directories from SciAide's
// private project storage. Active Kernels do not survive an application
// restart, so every matching directory is stale at this point.
func (s *KernelService) Recover(ctx context.Context) (KernelRecoveryResult, error) {
	var result KernelRecoveryResult
	lister, ok := s.projects.(projectLister)
	if !ok {
		return result, nil
	}
	projects, err := lister.List(ctx)
	if err != nil {
		return result, err
	}
	for _, selected := range projects {
		if err := project.VerifyPrivateDataLayout(selected); err != nil {
			if selected.WorkspaceKind == project.WorkspaceExternal {
				continue
			}
			return result, fmt.Errorf("recover Kernel staging for project %q: %w", selected.ID, err)
		}
		guard, err := pathguard.Open(selected.WorkspacePath)
		if err != nil {
			return result, fmt.Errorf("recover Kernel staging for project %q: %w", selected.ID, err)
		}
		removed, cleanupErr := cleanupAbandonedKernelStaging(guard)
		closeErr := guard.Close()
		result.TemporaryPathsRemoved += removed
		if cleanupErr != nil || closeErr != nil {
			return result, fmt.Errorf("recover Kernel staging for project %q: %w", selected.ID, errors.Join(cleanupErr, closeErr))
		}
	}
	return result, nil
}

func validateKernelInputs(guard *pathguard.Guard, values []string) ([]string, map[string]string, error) {
	if len(values) > 64 {
		return nil, nil, fmt.Errorf("too many Kernel input files")
	}
	clean := make([]string, 0, len(values))
	hashes := make(map[string]string, len(values))
	for _, value := range values {
		relative, err := kernelWorkspaceFile(guard, value, true)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := hashes[relative]; exists {
			return nil, nil, fmt.Errorf("Kernel input paths must be unique")
		}
		hash, err := hashGuardFile(guard, relative)
		if err != nil {
			return nil, nil, err
		}
		clean, hashes[relative] = append(clean, relative), hash
	}
	return clean, hashes, nil
}

func validateKernelOutputs(guard *pathguard.Guard, values []string) ([]string, map[string]string, error) {
	if len(values) > 32 {
		return nil, nil, fmt.Errorf("too many Kernel output files")
	}
	clean := make([]string, 0, len(values))
	before := make(map[string]string, len(values))
	for _, value := range values {
		relative, err := kernelWorkspaceFile(guard, value, false)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := before[relative]; exists {
			return nil, nil, fmt.Errorf("Kernel output paths must be unique")
		}
		parent := filepath.Dir(relative)
		if _, parentErr := guard.MkdirAll(parent, 0o700); parentErr != nil {
			return nil, nil, fmt.Errorf("create declared Kernel output parent: %w", parentErr)
		}
		directory, _, parentErr := guard.OpenFile(parent)
		if parentErr != nil {
			return nil, nil, fmt.Errorf("declared Kernel output parent is unavailable: %w", parentErr)
		}
		info, parentErr := directory.Stat()
		directory.Close()
		if parentErr != nil || !info.IsDir() {
			return nil, nil, fmt.Errorf("declared Kernel output parent is not a Workspace directory")
		}
		absolute, err := guard.Absolute(relative)
		if err != nil {
			return nil, nil, err
		}
		_, err = os.Lstat(absolute)
		if err == nil {
			return nil, nil, fmt.Errorf("declared Kernel output %q already exists; choose a new output path so failed execution can be rolled back", relative)
		}
		if !os.IsNotExist(err) {
			return nil, nil, err
		}
		clean, before[relative] = append(clean, relative), ""
	}
	return clean, before, nil
}

var kernelStagingNamePattern = regexp.MustCompile(`^kernel-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

const kernelStagingRecoveryAge = 10 * time.Minute

func prepareKernelOutputStaging(guard *pathguard.Guard, outputs []string, executionID string) ([]string, string, error) {
	directory := filepath.ToSlash(filepath.Join(project.PrivateDirectoryName, "tmp", "kernel-"+executionID))
	if _, err := guard.MkdirAll(directory, 0o700); err != nil {
		return nil, "", fmt.Errorf("create Kernel output staging directory: %w", err)
	}
	staged := make([]string, len(outputs))
	for index, output := range outputs {
		extension := filepath.Ext(output)
		staged[index] = filepath.ToSlash(filepath.Join(directory, fmt.Sprintf("output-%02d%s", index+1, extension)))
	}
	return staged, directory, nil
}

func cleanupKernelOutputStaging(guard *pathguard.Guard, directory string) {
	_ = removeKernelStagingDirectory(guard, directory)
}

func cleanupAbandonedKernelStaging(guard *pathguard.Guard) (int, error) {
	root := filepath.ToSlash(filepath.Join(project.PrivateDirectoryName, "tmp"))
	if _, err := guard.MkdirAll(root, 0o700); err != nil {
		return 0, fmt.Errorf("prepare Kernel private temporary directory: %w", err)
	}
	directory, _, err := guard.OpenFile(root)
	if err != nil {
		return 0, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	err = errors.Join(readErr, closeErr)
	if err != nil {
		return 0, fmt.Errorf("list Kernel private temporary directory: %w", err)
	}
	removed := 0
	cutoff := time.Now().UTC().Add(-kernelStagingRecoveryAge)
	for _, entry := range entries {
		if !kernelStagingNamePattern.MatchString(entry.Name()) {
			continue
		}
		relative := filepath.ToSlash(filepath.Join(root, entry.Name()))
		stale, err := kernelStagingTreeOlderThan(guard, relative, cutoff)
		if err != nil {
			return removed, err
		}
		if !stale {
			continue
		}
		if err := removeKernelStagingDirectory(guard, relative); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func kernelStagingTreeOlderThan(guard *pathguard.Guard, relative string, cutoff time.Time) (bool, error) {
	stale := true
	err := guard.WalkDir(relative, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().UTC().Before(cutoff) {
			stale = false
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("inspect Kernel staging age: %w", err)
	}
	return stale, nil
}

func removeKernelStagingDirectory(guard *pathguard.Guard, directory string) error {
	clean, err := guard.Relative(directory)
	if err != nil {
		return err
	}
	wantParent := filepath.Clean(filepath.Join(project.PrivateDirectoryName, "tmp"))
	if filepath.Clean(filepath.Dir(clean)) != wantParent || !kernelStagingNamePattern.MatchString(filepath.Base(clean)) {
		return fmt.Errorf("refuse to remove a path outside the Kernel staging namespace")
	}
	if err := guard.RemoveAll(clean); err != nil {
		return fmt.Errorf("remove Kernel staging directory: %w", err)
	}
	return nil
}

func publishKernelOutputs(guard *pathguard.Guard, staged, outputs []string, hashes map[string]string) (map[string]string, error) {
	if len(staged) != len(outputs) {
		return nil, fmt.Errorf("Kernel output staging does not match declared outputs")
	}
	type publishedOutput struct {
		path string
		info os.FileInfo
		file *os.File
	}
	published := make([]publishedOutput, 0, len(outputs))
	closePublished := func() {
		for _, value := range published {
			_ = value.file.Close()
		}
	}
	defer closePublished()
	rollback := func() {
		for index := len(published) - 1; index >= 0; index-- {
			absolute, err := guard.Absolute(published[index].path)
			if err != nil {
				continue
			}
			info, err := os.Stat(absolute)
			if err == nil && os.SameFile(published[index].info, info) {
				_ = guard.Remove(published[index].path)
			}
		}
	}
	result := make(map[string]string, len(outputs))
	for index, output := range outputs {
		source, err := guard.Absolute(staged[index])
		if err != nil {
			rollback()
			return nil, err
		}
		destination, err := guard.Absolute(output)
		if err != nil {
			rollback()
			return nil, err
		}
		sourceInfo, err := os.Stat(source)
		if err != nil || !sourceInfo.Mode().IsRegular() {
			rollback()
			return nil, fmt.Errorf("staged Kernel output %q is not a regular file", staged[index])
		}
		handle, err := filepublish.NoReplaceOpen(source, destination)
		if err != nil {
			rollback()
			return nil, fmt.Errorf("publish declared Kernel output %q without replacing an existing file: %w", output, err)
		}
		info, err := handle.Stat()
		if err != nil || !info.Mode().IsRegular() {
			handle.Close()
			rollback()
			return nil, fmt.Errorf("inspect published Kernel output %q: %w", output, err)
		}
		published = append(published, publishedOutput{path: output, info: info, file: handle})
		actual, err := hashGuardFile(guard, output)
		if err != nil || actual != hashes[staged[index]] {
			rollback()
			return nil, fmt.Errorf("published Kernel output %q changed before commit", output)
		}
		result[output] = hashes[staged[index]]
	}
	return result, nil
}

func kernelWorkspaceFile(guard *pathguard.Guard, value string, mustExist bool) (string, error) {
	relative, err := guard.Relative(strings.TrimSpace(value))
	if err != nil || relative == "." {
		if err == nil {
			err = fmt.Errorf("Kernel file path must name a file")
		}
		return "", err
	}
	private := strings.ToLower(filepath.ToSlash(relative))
	if private == ".sciaide" || strings.HasPrefix(private, ".sciaide/") {
		return "", fmt.Errorf("Kernel files cannot use SciAide private storage")
	}
	if !mustExist && private != "analysis-output" && !strings.HasPrefix(private, "analysis-output/") {
		return "", fmt.Errorf("Kernel outputs must be written below analysis-output/")
	}
	if mustExist {
		file, _, err := guard.OpenFile(relative)
		if err != nil {
			return "", err
		}
		info, statErr := file.Stat()
		file.Close()
		if statErr != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("Kernel input is not a regular Workspace file")
		}
	}
	return filepath.ToSlash(relative), nil
}

func verifyKernelInputs(guard *pathguard.Guard, before map[string]string) (string, error) {
	for path, expected := range before {
		actual, err := hashGuardFile(guard, path)
		if err != nil {
			return path, err
		}
		if actual != expected {
			return path, nil
		}
	}
	return "", nil
}

func collectKernelOutputs(guard *pathguard.Guard, paths []string, before map[string]string) (map[string]string, error) {
	result := map[string]string{}
	for _, path := range paths {
		hash, err := hashGuardFile(guard, path)
		if err != nil {
			return nil, fmt.Errorf("declared Kernel output %q is missing or invalid: %w", path, err)
		}
		if hash == before[path] {
			return nil, fmt.Errorf("declared Kernel output %q was not produced or changed", path)
		}
		result[path] = hash
	}
	return result, nil
}

func hashGuardFile(guard *pathguard.Guard, relative string) (string, error) {
	file, _, err := guard.OpenFile(relative)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("Workspace path is not a regular file")
	}
	hash := sha256.New()
	if _, err := file.WriteTo(hash); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashKernelText(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func hashKernelJSON(value json.RawMessage) string {
	var normalized any
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	if decoder.Decode(&normalized) != nil {
		return hashKernelText(string(value))
	}
	encoded, _ := json.Marshal(normalized)
	return hashKernelText(string(encoded))
}

func kernelReproductionHash(result KernelResult, inputPaths, outputPaths []string) string {
	payload := struct {
		Status                 string       `json:"status"`
		Stdout                 string       `json:"stdout"`
		Stderr                 string       `json:"stderr"`
		Value                  any          `json:"value,omitempty"`
		ValueType              string       `json:"valueType,omitempty"`
		Table                  *KernelTable `json:"table,omitempty"`
		Exception              *KernelError `json:"exception,omitempty"`
		CodeSHA256             string       `json:"codeSha256"`
		InputSHA256            []string     `json:"inputSha256"`
		InputDataSHA256        string       `json:"inputDataSha256,omitempty"`
		OutputSHA256           []string     `json:"outputSha256"`
		FigureSHA256           []string     `json:"figureSha256"`
		EnvironmentFingerprint string       `json:"environmentFingerprint"`
	}{
		Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Value: result.Value, ValueType: result.ValueType,
		Table: result.Table, Exception: result.Exception, CodeSHA256: result.CodeSHA256,
		InputSHA256: orderedKernelHashes(inputPaths, result.InputSHA256), InputDataSHA256: result.InputSHA256["$data"],
		OutputSHA256: orderedKernelHashes(outputPaths, result.OutputSHA256), FigureSHA256: remainingKernelHashes(outputPaths, result.OutputSHA256),
		EnvironmentFingerprint: result.EnvironmentFingerprint,
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func orderedKernelHashes(paths []string, values map[string]string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if value := values[filepath.ToSlash(path)]; value != "" {
			result = append(result, value)
		}
	}
	return result
}

func remainingKernelHashes(paths []string, values map[string]string) []string {
	declared := make(map[string]struct{}, len(paths)+1)
	declared["$data"] = struct{}{}
	for _, path := range paths {
		declared[filepath.ToSlash(path)] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for path, value := range values {
		if _, exists := declared[path]; !exists && value != "" {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

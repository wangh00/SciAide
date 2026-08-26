package pythonenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/id"
)

type projectService interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

type Service struct {
	repository Repository
	projects   projectService
	runtime    Runtime
	root       string
	now        func() time.Time

	mu         sync.Mutex
	project    map[string]*sync.Mutex
	stopKernel func(string) error
}

type RecoveryResult struct {
	EnvironmentsRecovered int `json:"environmentsRecovered"`
	OperationsInterrupted int `json:"operationsInterrupted"`
	TemporaryPathsRemoved int `json:"temporaryPathsRemoved"`
}

var packageSpecPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:\[[A-Za-z0-9._,-]+\])?(?:(?:==|!=|~=|>=|<=|>|<)[A-Za-z0-9][A-Za-z0-9._+!-]*)?$`)
var lockedPackagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*==[A-Za-z0-9][A-Za-z0-9._+!-]*$`)

func NewService(repository Repository, projects projectService, runtimeAdapter Runtime, managedRoot string) (*Service, error) {
	if repository == nil || projects == nil || runtimeAdapter == nil || strings.TrimSpace(managedRoot) == "" {
		return nil, fmt.Errorf("Python environment service dependencies are required")
	}
	root, err := filepath.Abs(managedRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve Python environment root: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create Python environment root: %w", err)
	}
	return &Service{repository: repository, projects: projects, runtime: runtimeAdapter, root: filepath.Clean(root), now: func() time.Time { return time.Now().UTC() }, project: map[string]*sync.Mutex{}}, nil
}

func (s *Service) DetectInterpreters(ctx context.Context, preferredPath string) (Discovery, error) {
	return s.runtime.Discover(ctx, strings.TrimSpace(preferredPath))
}

func (s *Service) SetKernelStopper(stop func(string) error) {
	s.mu.Lock()
	s.stopKernel = stop
	s.mu.Unlock()
}

// Recover reconciles database declarations with managed, derived venv bytes
// after an unclean shutdown. It never promotes an environment unless its
// recorded fingerprint, dependency lock and on-disk interpreter all agree.
func (s *Service) Recover(ctx context.Context) (RecoveryResult, error) {
	result := RecoveryResult{}
	environments, err := s.repository.List(ctx)
	if err != nil {
		return result, err
	}
	operations, err := s.repository.ListRunningOperations(ctx)
	if err != nil {
		return result, err
	}
	byProject := make(map[string][]Operation)
	for _, operation := range operations {
		byProject[operation.ProjectID] = append(byProject[operation.ProjectID], operation)
	}
	known := make(map[string]bool, len(environments))
	for _, value := range environments {
		known[value.ProjectID] = true
		unlock := s.lockProject(value.ProjectID)
		recovered, recoverErr := s.recoverEnvironment(ctx, value, byProject[value.ProjectID], &result)
		unlock()
		if recoverErr != nil {
			return result, fmt.Errorf("recover Python environment for project %s: %w", value.ProjectID, recoverErr)
		}
		if recovered {
			result.EnvironmentsRecovered++
		}
		delete(byProject, value.ProjectID)
	}
	for _, orphaned := range byProject {
		for _, operation := range orphaned {
			if err := s.repository.FinishOperation(ctx, operation.ID, "failed", "", "Python environment operation was interrupted and its environment declaration is unavailable", s.now()); err != nil {
				return result, err
			}
			result.OperationsInterrupted++
		}
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.IsDir() && !known[entry.Name()] {
			if err := s.removeManagedEnvironment(filepath.Join(s.root, entry.Name())); err != nil {
				return result, err
			}
			result.TemporaryPathsRemoved++
		}
	}
	return result, nil
}

func (s *Service) recoverEnvironment(ctx context.Context, value Environment, operations []Operation, result *RecoveryResult) (bool, error) {
	originalState := value.State
	temporaryBefore := result.TemporaryPathsRemoved
	if value.Kind == "" {
		value.Kind = KindLegacyManaged
	}
	if value.Kind == KindExternal {
		return s.recoverExternalEnvironment(ctx, value, operations, result)
	}
	projectRoot, target, err := s.environmentPaths(ctx, value.ProjectID, value.Kind)
	if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(projectRoot)
	if os.IsNotExist(err) {
		entries, err = []os.DirEntry{}, nil
	}
	if err != nil {
		return false, err
	}
	var staging, previous, deleting []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(projectRoot, entry.Name())
		switch {
		case strings.HasPrefix(entry.Name(), ".staging-"):
			staging = append(staging, path)
		case strings.HasPrefix(entry.Name(), ".previous-"):
			previous = append(previous, path)
		case strings.HasPrefix(entry.Name(), ".deleting-"):
			deleting = append(deleting, path)
		}
	}
	for _, path := range staging {
		if err := s.removeEnvironmentWithin(projectRoot, path); err != nil {
			return false, err
		}
		result.TemporaryPathsRemoved++
	}
	if value.State == StateAbsent && len(operations) == 0 {
		for _, path := range append(append(previous, deleting...), target) {
			if _, statErr := os.Stat(path); statErr == nil {
				if err := s.removeEnvironmentWithin(projectRoot, path); err != nil {
					return false, err
				}
				result.TemporaryPathsRemoved++
			} else if !os.IsNotExist(statErr) {
				return false, statErr
			}
		}
		return result.TemporaryPathsRemoved != temporaryBefore, nil
	}
	interruptedDelete := false
	for _, operation := range operations {
		if operation.Kind == "delete" {
			interruptedDelete = true
			break
		}
	}
	if interruptedDelete {
		for _, path := range append(append(previous, deleting...), target) {
			if _, statErr := os.Stat(path); statErr == nil {
				if err := s.removeEnvironmentWithin(projectRoot, path); err != nil {
					return false, err
				}
				result.TemporaryPathsRemoved++
			} else if !os.IsNotExist(statErr) {
				return false, statErr
			}
		}
		now := s.now()
		value.State, value.EnvironmentPythonPath, value.EnvironmentFingerprint = StateAbsent, "", ""
		value.FreezeSHA256, value.LastVerifiedAt, value.ErrorMessage, value.UpdatedAt = "", nil, "", now
		completedDelete := false
		for _, operation := range operations {
			state, message := "failed", "Python environment operation was interrupted by application shutdown"
			if operation.Kind == "delete" && !completedDelete {
				if err := s.repository.CompleteOperation(ctx, value, operation.ID, "completed", "", "", now); err != nil {
					return false, err
				}
				state, completedDelete = "", true
			}
			if state != "" {
				if err := s.repository.FinishOperation(ctx, operation.ID, state, "", message, now); err != nil {
					return false, err
				}
			}
			result.OperationsInterrupted++
		}
		return true, nil
	}

	// A publish may have stopped between moving the old environment aside and
	// committing its new declaration. Prefer the previous bytes, then demand a
	// complete fingerprint match before restoring ready state.
	if len(operations) > 0 && len(previous) > 0 {
		sort.Strings(previous)
		if _, statErr := os.Stat(target); statErr == nil {
			if err := s.removeEnvironmentWithin(projectRoot, target); err != nil {
				return false, err
			}
		} else if !os.IsNotExist(statErr) {
			return false, statErr
		}
		candidate := previous[len(previous)-1]
		if err := os.Rename(candidate, target); err != nil {
			return false, err
		}
		previous = previous[:len(previous)-1]
	}
	if len(operations) == 0 {
		if _, statErr := os.Stat(target); os.IsNotExist(statErr) && len(deleting) > 0 {
			sort.Strings(deleting)
			candidate := deleting[len(deleting)-1]
			if err := os.Rename(candidate, target); err != nil {
				return false, err
			}
			deleting = deleting[:len(deleting)-1]
		}
	}
	for _, path := range append(previous, deleting...) {
		if err := s.removeEnvironmentWithin(projectRoot, path); err != nil {
			return false, err
		}
		result.TemporaryPathsRemoved++
	}

	verified, verifyErr := s.verifyRecordedEnvironment(ctx, value, target)
	now := s.now()
	if verifyErr == nil {
		value = verified
		value.State, value.ErrorMessage, value.UpdatedAt = StateReady, "", now
		value.LastVerifiedAt = &now
	} else {
		value.State, value.ErrorMessage, value.UpdatedAt = StateBroken, "Python environment recovery requires rebuild: "+verifyErr.Error(), now
		value.LastVerifiedAt = nil
	}
	if len(operations) == 0 {
		if err := s.repository.Save(ctx, value); err != nil {
			return false, err
		}
		return originalState != value.State || result.TemporaryPathsRemoved != temporaryBefore, nil
	}
	for index, operation := range operations {
		message := "Python environment operation was interrupted by application shutdown"
		if verifyErr != nil {
			message += "; " + verifyErr.Error()
		}
		if index == 0 {
			if err := s.repository.CompleteOperation(ctx, value, operation.ID, "failed", value.EnvironmentFingerprint, message, now); err != nil {
				return false, err
			}
		} else if err := s.repository.FinishOperation(ctx, operation.ID, "failed", value.EnvironmentFingerprint, message, now); err != nil {
			return false, err
		}
		result.OperationsInterrupted++
	}
	return true, nil
}

func (s *Service) verifyRecordedEnvironment(ctx context.Context, value Environment, target string) (Environment, error) {
	if value.EnvironmentFingerprint == "" || value.FreezeSHA256 == "" {
		return value, fmt.Errorf("no committed environment fingerprint exists")
	}
	expectedPython := environmentPython(target)
	if value.Kind == KindExternal {
		expectedPython = filepath.Clean(target)
	}
	info, err := os.Stat(expectedPython)
	if err != nil {
		return value, fmt.Errorf("managed interpreter is unavailable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return value, fmt.Errorf("managed interpreter is not a regular file")
	}
	created, err := s.runtime.Probe(ctx, expectedPython)
	if err != nil {
		return value, err
	}
	if created.BasePrefix == created.Prefix || !created.HasPip {
		return value, fmt.Errorf("managed interpreter is not an isolated venv with pip")
	}
	lock, err := s.runtime.Freeze(ctx, expectedPython)
	if err != nil {
		return value, err
	}
	base := Interpreter{ExecutablePath: value.BaseExecutablePath, Version: value.BaseExecutableVersion, Architecture: value.Architecture, Implementation: value.Implementation, ExecutableSHA256: value.BaseExecutableSHA256}
	fingerprint, freezeHash := environmentHashes(base, created, lock)
	if fingerprint != value.EnvironmentFingerprint || freezeHash != value.FreezeSHA256 {
		return value, fmt.Errorf("on-disk interpreter or dependency lock differs from the committed fingerprint")
	}
	value.EnvironmentPythonPath, value.Lock = expectedPython, lock
	return value, nil
}

func (s *Service) recoverExternalEnvironment(ctx context.Context, value Environment, operations []Operation, result *RecoveryResult) (bool, error) {
	verified, verifyErr := s.verifyRecordedEnvironment(ctx, value, value.EnvironmentPythonPath)
	now := s.now()
	if verifyErr == nil {
		value = verified
		value.State, value.ErrorMessage, value.UpdatedAt = StateReady, "", now
		value.LastVerifiedAt = &now
	} else {
		value.State, value.ErrorMessage, value.UpdatedAt = StateBroken, "External Python environment requires attention: "+verifyErr.Error(), now
		value.LastVerifiedAt = nil
	}
	if len(operations) == 0 {
		if err := s.repository.Save(ctx, value); err != nil {
			return false, err
		}
		return true, nil
	}
	for _, operation := range operations {
		message := "External Python environment operation was interrupted by application shutdown"
		if verifyErr != nil {
			message += "; " + verifyErr.Error()
		}
		if err := s.repository.FinishOperation(ctx, operation.ID, "failed", value.EnvironmentFingerprint, message, now); err != nil {
			return false, err
		}
		result.OperationsInterrupted++
	}
	if err := s.repository.Save(ctx, value); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) Get(ctx context.Context, projectID string) (Environment, error) {
	if _, err := s.requireProject(ctx, projectID); err != nil {
		return Environment{}, err
	}
	value, err := s.repository.Get(ctx, strings.TrimSpace(projectID))
	if errors.Is(err, ErrEnvironmentNotFound) {
		now := s.now()
		return Environment{ProjectID: strings.TrimSpace(projectID), State: StateAbsent, Kind: KindWorkspaceManaged, Lock: []string{}, CreatedAt: now, UpdatedAt: now}, nil
	}
	return value, err
}

func (s *Service) Create(ctx context.Context, projectID, preferredPath string, rebuild bool) (result Environment, err error) {
	projectID = strings.TrimSpace(projectID)
	if _, err := s.requireProject(ctx, projectID); err != nil {
		return Environment{}, err
	}
	unlock := s.lockProject(projectID)
	defer unlock()

	existing, getErr := s.repository.Get(ctx, projectID)
	if getErr != nil && !errors.Is(getErr, ErrEnvironmentNotFound) {
		return Environment{}, getErr
	}
	if getErr == nil && existing.State == StateReady && !rebuild {
		return existing, nil
	}
	if getErr == nil && existing.Kind == KindExternal {
		return Environment{}, fmt.Errorf("external Python environments cannot be rebuilt by SciAide; verify or remove the binding instead")
	}
	previous := existing
	hadRecordedEnvironment := getErr == nil
	discovery, err := s.runtime.Discover(ctx, strings.TrimSpace(preferredPath))
	if err != nil {
		return Environment{}, err
	}
	if len(discovery.Interpreters) == 0 {
		return Environment{}, fmt.Errorf("未检测到 Python 3；请安装 64 位 Python 3，或手动选择 python.exe 后重新检测")
	}
	base := discovery.Interpreters[0]
	if !base.HasVenv {
		return Environment{}, fmt.Errorf("所选 Python %s 不支持 venv", base.Version)
	}
	if filepath.Clean(base.Prefix) != filepath.Clean(base.BasePrefix) {
		return Environment{}, fmt.Errorf("所选解释器属于已有虚拟环境；请直接绑定该环境，或选择基础 Python 创建 Workspace 环境")
	}
	now := s.now()
	operationID, err := id.New()
	if err != nil {
		return Environment{}, err
	}
	if existing.ID == "" {
		existing.ID, err = id.New()
		if err != nil {
			return Environment{}, err
		}
		existing.ProjectID, existing.CreatedAt = projectID, now
	}
	if existing.Kind == "" || existing.Kind == KindExternal || existing.State == StateAbsent {
		existing.Kind = KindWorkspaceManaged
	}
	existing.State = StateCreating
	existing.BaseExecutablePath = base.ExecutablePath
	existing.BaseExecutableVersion = base.Version
	existing.BaseExecutableSHA256 = base.ExecutableSHA256
	existing.Architecture = base.Architecture
	existing.Implementation = base.Implementation
	existing.UpdatedAt = now
	existing.ErrorMessage = ""
	if existing.Lock == nil {
		existing.Lock = []string{}
	}
	if err := s.repository.Save(ctx, existing); err != nil {
		return Environment{}, err
	}
	kind := "create"
	if rebuild {
		kind = "rebuild"
	}
	request, _ := json.Marshal(map[string]any{"baseVersion": base.Version, "baseSha256": base.ExecutableSHA256, "rebuild": rebuild})
	operation := Operation{ID: operationID, ProjectID: projectID, EnvironmentID: existing.ID, Kind: kind, State: "running", RequestJSON: string(request), BeforeFingerprint: existing.EnvironmentFingerprint, StartedAt: now}
	if err := s.repository.CreateOperation(ctx, operation); err != nil {
		if hadRecordedEnvironment {
			_ = s.repository.Save(context.Background(), previous)
		} else {
			_ = s.repository.Delete(context.Background(), projectID)
		}
		return Environment{}, err
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		state := "failed"
		message := "Python environment operation did not complete"
		if err != nil {
			message = err.Error()
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			state, message = "cancelled", "Python environment creation was cancelled"
		}
		if rebuild && hadRecordedEnvironment && previous.State == StateReady {
			_ = s.repository.Save(context.Background(), previous)
		} else {
			existing.State, existing.ErrorMessage, existing.UpdatedAt = StateBroken, message, s.now()
			_ = s.repository.Save(context.Background(), existing)
		}
		_ = s.repository.FinishOperation(context.Background(), operationID, state, "", message, s.now())
	}()

	projectRoot, target, pathErr := s.environmentPaths(ctx, projectID, existing.Kind)
	if pathErr != nil {
		return Environment{}, pathErr
	}
	if err = os.MkdirAll(projectRoot, 0o700); err != nil {
		return Environment{}, fmt.Errorf("create managed Python project root: %w", err)
	}
	staging := filepath.Join(projectRoot, ".staging-"+operationID)
	_ = s.removeEnvironmentWithin(projectRoot, staging)
	defer s.removeEnvironmentWithin(projectRoot, staging)
	if !rebuild {
		if _, statErr := os.Stat(target); statErr == nil {
			return Environment{}, fmt.Errorf("managed Python environment already exists but is not recorded as ready; use rebuild")
		} else if !os.IsNotExist(statErr) {
			return Environment{}, statErr
		}
	}
	if err = s.runtime.CreateEnvironment(ctx, base.ExecutablePath, staging); err != nil {
		return Environment{}, err
	}
	pythonPath := environmentPython(staging)
	if len(previous.Lock) > 0 {
		locked, lockErr := normalizeLockedPackages(previous.Lock)
		if lockErr != nil {
			err = fmt.Errorf("saved Python dependency lock is not reproducible: %w", lockErr)
			return Environment{}, err
		}
		if err = s.runtime.InstallPackages(ctx, pythonPath, locked); err != nil {
			return Environment{}, fmt.Errorf("restore saved Python dependencies: %w", err)
		}
	}
	created, probeErr := s.runtime.Probe(ctx, pythonPath)
	if probeErr != nil {
		err = fmt.Errorf("verify created Python environment: %w", probeErr)
		return Environment{}, err
	}
	if created.BasePrefix == created.Prefix || !created.HasPip {
		err = fmt.Errorf("created Python environment failed isolation or pip verification")
		return Environment{}, err
	}
	lock, freezeErr := s.runtime.Freeze(ctx, pythonPath)
	if freezeErr != nil {
		err = fmt.Errorf("freeze created Python environment: %w", freezeErr)
		return Environment{}, err
	}
	backup := filepath.Join(projectRoot, ".previous-"+operationID)
	hadPrevious := false
	if err = s.stopProjectKernel(projectID); err != nil {
		return Environment{}, fmt.Errorf("stop project Python Kernel before environment publish: %w", err)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		if !rebuild {
			return Environment{}, fmt.Errorf("managed Python environment appeared during creation")
		}
		if err = os.Rename(target, backup); err != nil {
			return Environment{}, fmt.Errorf("stage previous Python environment: %w", err)
		}
		hadPrevious = true
	} else if !os.IsNotExist(statErr) {
		return Environment{}, statErr
	}
	if err = os.Rename(staging, target); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, target)
		}
		return Environment{}, fmt.Errorf("publish Python environment: %w", err)
	}
	createdPath := environmentPython(target)
	fingerprint, freezeHash := environmentHashes(base, created, lock)
	verified := s.now()
	existing.State = StateReady
	existing.EnvironmentPythonPath = createdPath
	existing.EnvironmentFingerprint = fingerprint
	existing.Lock = lock
	existing.FreezeSHA256 = freezeHash
	existing.LastVerifiedAt = &verified
	existing.UpdatedAt = verified
	existing.ErrorMessage = ""
	if err = s.repository.CompleteOperation(ctx, existing, operationID, "completed", fingerprint, "", verified); err != nil {
		_ = s.removeEnvironmentWithin(projectRoot, target)
		if hadPrevious {
			_ = os.Rename(backup, target)
		}
		return Environment{}, err
	}
	if hadPrevious {
		_ = s.removeEnvironmentWithin(projectRoot, backup)
	}
	finished = true
	return existing, nil
}

// BindExternal records an existing virtual environment without copying or
// modifying it. External bytes remain entirely user-owned.
func (s *Service) BindExternal(ctx context.Context, projectID, interpreterPath string) (Environment, error) {
	projectID = strings.TrimSpace(projectID)
	if _, err := s.requireProject(ctx, projectID); err != nil {
		return Environment{}, err
	}
	unlock := s.lockProject(projectID)
	defer unlock()
	existing, getErr := s.repository.Get(ctx, projectID)
	if getErr != nil && !errors.Is(getErr, ErrEnvironmentNotFound) {
		return Environment{}, getErr
	}
	if getErr == nil && existing.State != StateAbsent {
		return Environment{}, fmt.Errorf("remove the current project Python environment before binding an existing virtual environment")
	}
	interpreterPath = strings.TrimSpace(interpreterPath)
	if interpreterPath == "" {
		return Environment{}, fmt.Errorf("select an existing Python virtual environment interpreter")
	}
	selected, err := s.runtime.Probe(ctx, interpreterPath)
	if err != nil {
		return Environment{}, err
	}
	if selected.Prefix == selected.BasePrefix || !selected.HasPip {
		return Environment{}, fmt.Errorf("selected interpreter is not an existing virtual environment with pip")
	}
	lock, err := s.runtime.Freeze(ctx, selected.ExecutablePath)
	if err != nil {
		return Environment{}, fmt.Errorf("freeze existing Python environment: %w", err)
	}
	if _, err := normalizeLockedPackages(lock); err != nil {
		return Environment{}, fmt.Errorf("existing Python environment dependency lock is not reproducible: %w", err)
	}
	previous := existing
	hadRecordedEnvironment := getErr == nil
	now := s.now()
	if existing.ID == "" {
		existing.ID, err = id.New()
		if err != nil {
			return Environment{}, err
		}
		existing.ProjectID, existing.CreatedAt = projectID, now
	}
	fingerprint, freezeHash := environmentHashes(selected, selected, lock)
	existing.State, existing.Kind = StateReady, KindExternal
	existing.BaseExecutablePath = selected.ExecutablePath
	existing.BaseExecutableVersion = selected.Version
	existing.BaseExecutableSHA256 = selected.ExecutableSHA256
	existing.Architecture, existing.Implementation = selected.Architecture, selected.Implementation
	existing.EnvironmentPythonPath, existing.EnvironmentFingerprint = selected.ExecutablePath, fingerprint
	existing.Lock, existing.FreezeSHA256 = lock, freezeHash
	existing.LastVerifiedAt, existing.UpdatedAt, existing.ErrorMessage = &now, now, ""
	completed := existing
	existing.State = StateCreating
	operationID, err := id.New()
	if err != nil {
		return Environment{}, err
	}
	request, _ := json.Marshal(map[string]any{"environmentSha256": selected.ExecutableSHA256})
	if err := s.stopProjectKernel(projectID); err != nil {
		return Environment{}, err
	}
	if err := s.repository.Save(ctx, existing); err != nil {
		return Environment{}, err
	}
	rollback := func() {
		if hadRecordedEnvironment {
			_ = s.repository.Save(context.Background(), previous)
		} else {
			_ = s.repository.Delete(context.Background(), projectID)
		}
	}
	operation := Operation{ID: operationID, ProjectID: projectID, EnvironmentID: existing.ID, Kind: "bind", State: "running", RequestJSON: string(request), BeforeFingerprint: previous.EnvironmentFingerprint, StartedAt: now}
	if err := s.repository.CreateOperation(ctx, operation); err != nil {
		rollback()
		return Environment{}, err
	}
	if err := s.repository.CompleteOperation(ctx, completed, operationID, "completed", fingerprint, "", now); err != nil {
		rollback()
		_ = s.repository.FinishOperation(context.Background(), operationID, "failed", "", err.Error(), s.now())
		return Environment{}, err
	}
	return completed, nil
}

func (s *Service) Install(ctx context.Context, projectID string, packages []string) (result Environment, err error) {
	projectID = strings.TrimSpace(projectID)
	if _, err := s.requireProject(ctx, projectID); err != nil {
		return Environment{}, err
	}
	requested, err := normalizeRequestedPackages(packages)
	if err != nil {
		return Environment{}, err
	}
	unlock := s.lockProject(projectID)
	defer unlock()
	previous, err := s.repository.Get(ctx, projectID)
	if err != nil {
		return Environment{}, err
	}
	if previous.State != StateReady {
		return Environment{}, fmt.Errorf("project Python environment is not ready")
	}
	if previous.Kind == KindExternal {
		return Environment{}, fmt.Errorf("SciAide does not install packages into a user-owned external Python environment")
	}
	locked, err := normalizeLockedPackages(previous.Lock)
	if err != nil {
		return Environment{}, fmt.Errorf("saved Python dependency lock is not reproducible: %w", err)
	}
	operationID, err := id.New()
	if err != nil {
		return Environment{}, err
	}
	now := s.now()
	requestJSON, _ := json.Marshal(map[string]any{"packages": requested})
	operation := Operation{ID: operationID, ProjectID: projectID, EnvironmentID: previous.ID, Kind: "install", State: "running", RequestJSON: string(requestJSON), BeforeFingerprint: previous.EnvironmentFingerprint, StartedAt: now}
	if err := s.repository.CreateOperation(ctx, operation); err != nil {
		return Environment{}, err
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		state, message := "failed", "Python dependency installation did not complete"
		if err != nil {
			message = err.Error()
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			state, message = "cancelled", "Python dependency installation was cancelled"
		}
		_ = s.repository.FinishOperation(context.Background(), operationID, state, "", message, s.now())
	}()
	projectRoot, target, pathErr := s.environmentPaths(ctx, projectID, previous.Kind)
	if pathErr != nil {
		return Environment{}, pathErr
	}
	staging := filepath.Join(projectRoot, ".staging-"+operationID)
	backup := filepath.Join(projectRoot, ".previous-"+operationID)
	_ = s.removeEnvironmentWithin(projectRoot, staging)
	defer s.removeEnvironmentWithin(projectRoot, staging)
	if err = s.runtime.CreateEnvironment(ctx, previous.BaseExecutablePath, staging); err != nil {
		return Environment{}, err
	}
	stagingPython := environmentPython(staging)
	if len(locked) > 0 {
		if err = s.runtime.InstallPackages(ctx, stagingPython, locked); err != nil {
			return Environment{}, fmt.Errorf("restore locked Python dependencies: %w", err)
		}
	}
	if err = s.runtime.InstallPackages(ctx, stagingPython, requested); err != nil {
		return Environment{}, fmt.Errorf("install requested Python dependencies: %w", err)
	}
	created, err := s.runtime.Probe(ctx, stagingPython)
	if err != nil || created.BasePrefix == created.Prefix || !created.HasPip {
		if err == nil {
			err = fmt.Errorf("updated Python environment failed isolation or pip verification")
		}
		return Environment{}, err
	}
	lock, err := s.runtime.Freeze(ctx, stagingPython)
	if err != nil {
		return Environment{}, err
	}
	if _, err = normalizeLockedPackages(lock); err != nil {
		return Environment{}, fmt.Errorf("updated dependency lock is not reproducible: %w", err)
	}
	base := Interpreter{ExecutablePath: previous.BaseExecutablePath, Version: previous.BaseExecutableVersion, Architecture: previous.Architecture, Implementation: previous.Implementation, ExecutableSHA256: previous.BaseExecutableSHA256}
	fingerprint, freezeHash := environmentHashes(base, created, lock)
	if err = s.stopProjectKernel(projectID); err != nil {
		return Environment{}, fmt.Errorf("stop project Python Kernel before dependency publish: %w", err)
	}
	if err = os.Rename(target, backup); err != nil {
		return Environment{}, fmt.Errorf("stage current Python environment: %w", err)
	}
	if err = os.Rename(staging, target); err != nil {
		_ = os.Rename(backup, target)
		return Environment{}, fmt.Errorf("publish updated Python environment: %w", err)
	}
	verified := s.now()
	updated := previous
	updated.State, updated.EnvironmentPythonPath = StateReady, environmentPython(target)
	updated.EnvironmentFingerprint, updated.Lock, updated.FreezeSHA256 = fingerprint, lock, freezeHash
	updated.LastVerifiedAt, updated.UpdatedAt, updated.ErrorMessage = &verified, verified, ""
	if err = s.repository.CompleteOperation(ctx, updated, operationID, "completed", fingerprint, "", verified); err != nil {
		_ = s.removeEnvironmentWithin(projectRoot, target)
		_ = os.Rename(backup, target)
		return Environment{}, err
	}
	_ = s.removeEnvironmentWithin(projectRoot, backup)
	finished = true
	return updated, nil
}

func (s *Service) Verify(ctx context.Context, projectID string) (Environment, error) {
	projectID = strings.TrimSpace(projectID)
	if _, err := s.requireProject(ctx, projectID); err != nil {
		return Environment{}, err
	}
	unlock := s.lockProject(projectID)
	defer unlock()
	value, err := s.repository.Get(ctx, projectID)
	if err != nil {
		return Environment{}, err
	}
	operationID, err := id.New()
	if err != nil {
		return Environment{}, err
	}
	now := s.now()
	op := Operation{ID: operationID, ProjectID: projectID, EnvironmentID: value.ID, Kind: "verify", State: "running", RequestJSON: "{}", BeforeFingerprint: value.EnvironmentFingerprint, StartedAt: now}
	if err := s.repository.CreateOperation(ctx, op); err != nil {
		return Environment{}, err
	}
	fail := func(cause error) (Environment, error) {
		value.State, value.ErrorMessage, value.UpdatedAt = StateBroken, cause.Error(), s.now()
		_ = s.repository.Save(context.Background(), value)
		_ = s.repository.FinishOperation(context.Background(), operationID, "failed", "", cause.Error(), s.now())
		return value, cause
	}
	if value.Kind != KindExternal {
		_, expected, pathErr := s.environmentPaths(ctx, projectID, value.Kind)
		if pathErr != nil || filepath.Clean(value.EnvironmentPythonPath) != filepath.Clean(environmentPython(expected)) {
			if pathErr == nil {
				pathErr = fmt.Errorf("recorded Python path is outside the managed environment")
			}
			return fail(pathErr)
		}
	}
	created, err := s.runtime.Probe(ctx, value.EnvironmentPythonPath)
	if err != nil {
		return fail(err)
	}
	lock, err := s.runtime.Freeze(ctx, value.EnvironmentPythonPath)
	if err != nil {
		return fail(err)
	}
	base := Interpreter{ExecutablePath: value.BaseExecutablePath, Version: value.BaseExecutableVersion, Architecture: value.Architecture, Implementation: value.Implementation, ExecutableSHA256: value.BaseExecutableSHA256}
	fingerprint, freezeHash := environmentHashes(base, created, lock)
	verified := s.now()
	value.State, value.Lock, value.FreezeSHA256 = StateReady, lock, freezeHash
	value.EnvironmentFingerprint, value.LastVerifiedAt, value.UpdatedAt, value.ErrorMessage = fingerprint, &verified, verified, ""
	if err := s.repository.CompleteOperation(ctx, value, operationID, "completed", fingerprint, "", verified); err != nil {
		return fail(err)
	}
	return value, nil
}

func (s *Service) Delete(ctx context.Context, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if _, err := s.requireProject(ctx, projectID); err != nil {
		return err
	}
	unlock := s.lockProject(projectID)
	defer unlock()
	value, err := s.repository.Get(ctx, projectID)
	if errors.Is(err, ErrEnvironmentNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	previous := value
	if err := s.stopProjectKernel(projectID); err != nil {
		return fmt.Errorf("stop project Python Kernel before environment removal: %w", err)
	}
	operationID, err := id.New()
	if err != nil {
		return err
	}
	value.State, value.UpdatedAt = StateDeleting, s.now()
	if err := s.repository.Save(ctx, value); err != nil {
		return err
	}
	op := Operation{ID: operationID, ProjectID: projectID, EnvironmentID: value.ID, Kind: "delete", State: "running", RequestJSON: "{}", BeforeFingerprint: value.EnvironmentFingerprint, StartedAt: s.now()}
	if err := s.repository.CreateOperation(ctx, op); err != nil {
		_ = s.repository.Save(context.Background(), previous)
		return err
	}
	var projectRoot, target string
	if value.Kind != KindExternal {
		projectRoot, target, err = s.environmentPaths(ctx, projectID, value.Kind)
	}
	staged := ""
	if projectRoot != "" {
		staged = filepath.Join(projectRoot, ".deleting-"+operationID)
	}
	hadEnvironment := false
	if value.Kind == KindExternal {
		err = nil
	} else if err == nil {
		if _, statErr := os.Stat(target); statErr == nil {
			err = os.Rename(target, staged)
			hadEnvironment = err == nil
		} else if !os.IsNotExist(statErr) {
			err = statErr
		}
	}
	if err != nil {
		_ = s.repository.Save(context.Background(), previous)
		_ = s.repository.FinishOperation(context.Background(), operationID, "failed", "", err.Error(), s.now())
		return err
	}
	completed := s.now()
	wasExternal := value.Kind == KindExternal
	value.State = StateAbsent
	value.Kind = KindWorkspaceManaged
	value.EnvironmentPythonPath = ""
	value.EnvironmentFingerprint = ""
	value.FreezeSHA256 = ""
	value.LastVerifiedAt = nil
	value.ErrorMessage = ""
	value.UpdatedAt = completed
	if wasExternal {
		value.BaseExecutablePath = ""
		value.BaseExecutableVersion = ""
		value.BaseExecutableSHA256 = ""
		value.Architecture = ""
		value.Implementation = ""
		value.Lock = []string{}
	}
	if err := s.repository.CompleteOperation(ctx, value, operationID, "completed", "", "", completed); err != nil {
		if hadEnvironment {
			_ = os.Rename(staged, target)
		}
		_ = s.repository.Save(context.Background(), previous)
		_ = s.repository.FinishOperation(context.Background(), operationID, "failed", "", err.Error(), s.now())
		return err
	}
	if hadEnvironment {
		_ = s.removeEnvironmentWithin(projectRoot, staged)
	}
	return nil
}

// CleanupRemovedProject removes only the derived managed bytes after the
// owning project row has been deleted. Database declarations are removed by
// the project foreign-key cascade.
func (s *Service) CleanupRemovedProject(projectID string) error {
	projectID = strings.TrimSpace(projectID)
	unlock := s.lockProject(projectID)
	defer unlock()
	projectRoot, _, err := s.legacyEnvironmentPaths(projectID)
	if err != nil {
		return err
	}
	return s.removeManagedEnvironment(projectRoot)
}

func (s *Service) requireProject(ctx context.Context, projectID string) (project.Project, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return project.Project{}, fmt.Errorf("project ID is required")
	}
	return s.projects.Get(ctx, projectID)
}

func (s *Service) lockProject(projectID string) func() {
	s.mu.Lock()
	lock := s.project[projectID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.project[projectID] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *Service) environmentPaths(ctx context.Context, projectID string, kind EnvironmentKind) (string, string, error) {
	if kind == "" || kind == KindLegacyManaged {
		return s.legacyEnvironmentPaths(projectID)
	}
	if kind != KindWorkspaceManaged {
		return "", "", fmt.Errorf("external Python environments do not have a managed path")
	}
	value, err := s.requireProject(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	if err := project.EnsurePrivateDataLayout(value); err != nil {
		return "", "", err
	}
	projectRoot := filepath.Join(project.PrivateDataPath(value), "python")
	privateResolved, err := filepath.EvalSymlinks(project.PrivateDataPath(value))
	if err != nil {
		return "", "", fmt.Errorf("resolve project private data root: %w", err)
	}
	rootInfo, err := os.Lstat(projectRoot)
	if err != nil {
		return "", "", fmt.Errorf("inspect project Python root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", "", fmt.Errorf("project Python root must be a real directory")
	}
	rootResolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve project Python root: %w", err)
	}
	if relative, relErr := filepath.Rel(privateResolved, rootResolved); relErr != nil || !strings.EqualFold(relative, "python") {
		return "", "", fmt.Errorf("project Python root escapes the project private data directory")
	}
	return projectRoot, filepath.Join(projectRoot, "venv"), nil
}

func (s *Service) legacyEnvironmentPaths(projectID string) (string, string, error) {
	if projectID == "" || filepath.Base(projectID) != projectID || projectID == "." || projectID == ".." {
		return "", "", fmt.Errorf("invalid project identity for managed Python environment")
	}
	projectRoot := filepath.Join(s.root, projectID)
	rel, err := filepath.Rel(s.root, projectRoot)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("managed Python environment escaped its root")
	}
	return projectRoot, filepath.Join(projectRoot, "venv"), nil
}

func (s *Service) removeManagedEnvironment(target string) error {
	rel, err := filepath.Rel(s.root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("refuse to remove Python environment outside managed root")
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("remove managed Python environment: %w", err)
	}
	return nil
}

func (s *Service) removeEnvironmentWithin(root, target string) error {
	root, rootErr := filepath.Abs(root)
	target, targetErr := filepath.Abs(target)
	if rootErr != nil || targetErr != nil {
		return fmt.Errorf("resolve managed Python environment removal path")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("refuse to remove Python environment outside its project root")
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("remove managed Python environment: %w", err)
	}
	return nil
}

func (s *Service) stopProjectKernel(projectID string) error {
	s.mu.Lock()
	stop := s.stopKernel
	s.mu.Unlock()
	if stop == nil {
		return nil
	}
	return stop(projectID)
}

func normalizeRequestedPackages(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > 32 {
		return nil, fmt.Errorf("provide between 1 and 32 Python package specifications")
	}
	return normalizePackages(values, packageSpecPattern, "requested")
}

func normalizeLockedPackages(values []string) ([]string, error) {
	if len(values) > 512 {
		return nil, fmt.Errorf("dependency lock contains too many packages")
	}
	return normalizePackages(values, lockedPackagePattern, "locked")
}

func normalizePackages(values []string, pattern *regexp.Regexp, kind string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) == 0 || len(value) > 256 || !pattern.MatchString(value) {
			return nil, fmt.Errorf("invalid %s Python package specification %q", kind, value)
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result, nil
}

func environmentPython(root string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "Scripts", "python.exe")
	}
	return filepath.Join(root, "bin", "python")
}

func environmentHashes(base, environment Interpreter, lock []string) (string, string) {
	normalized := append([]string(nil), lock...)
	sort.Strings(normalized)
	freezeBytes := []byte(strings.Join(normalized, "\n"))
	freezeSum := sha256.Sum256(freezeBytes)
	freezeHash := hex.EncodeToString(freezeSum[:])
	payload, _ := json.Marshal(map[string]any{
		"baseExecutableSha256": base.ExecutableSHA256,
		"baseVersion":          base.Version,
		"environmentVersion":   environment.Version,
		"architecture":         environment.Architecture,
		"implementation":       environment.Implementation,
		"freezeSha256":         freezeHash,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), freezeHash
}

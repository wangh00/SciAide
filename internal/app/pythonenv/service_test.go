package pythonenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
)

type environmentRepositoryFixture struct {
	values             map[string]Environment
	operations         map[string]Operation
	createOperationErr error
	completeErr        error
}

func (r *environmentRepositoryFixture) Get(_ context.Context, projectID string) (Environment, error) {
	value, ok := r.values[projectID]
	if !ok {
		return Environment{}, ErrEnvironmentNotFound
	}
	return value, nil
}
func (r *environmentRepositoryFixture) List(context.Context) ([]Environment, error) {
	result := make([]Environment, 0, len(r.values))
	for _, value := range r.values {
		result = append(result, value)
	}
	return result, nil
}
func (r *environmentRepositoryFixture) ListRunningOperations(context.Context) ([]Operation, error) {
	result := []Operation{}
	for _, value := range r.operations {
		if value.State == "running" {
			result = append(result, value)
		}
	}
	return result, nil
}
func (r *environmentRepositoryFixture) Save(_ context.Context, value Environment) error {
	r.values[value.ProjectID] = value
	return nil
}
func (r *environmentRepositoryFixture) Delete(_ context.Context, projectID string) error {
	delete(r.values, projectID)
	return nil
}
func (r *environmentRepositoryFixture) CreateOperation(_ context.Context, value Operation) error {
	if r.createOperationErr != nil {
		return r.createOperationErr
	}
	r.operations[value.ID] = value
	return nil
}
func (r *environmentRepositoryFixture) FinishOperation(_ context.Context, id, state, after, message string, completed time.Time) error {
	value := r.operations[id]
	value.State, value.AfterFingerprint, value.ErrorMessage, value.CompletedAt = state, after, message, &completed
	r.operations[id] = value
	return nil
}
func (r *environmentRepositoryFixture) CompleteOperation(ctx context.Context, value Environment, id, state, after, message string, completed time.Time) error {
	if r.completeErr != nil {
		return r.completeErr
	}
	if err := r.Save(ctx, value); err != nil {
		return err
	}
	return r.FinishOperation(ctx, id, state, after, message, completed)
}

type projectFixture struct{ value project.Project }

func (p projectFixture) Get(_ context.Context, id string) (project.Project, error) {
	if id != p.value.ID {
		return project.Project{}, errors.New("project not found")
	}
	return p.value, nil
}

type projectsFixture map[string]project.Project

func (p projectsFixture) Get(_ context.Context, id string) (project.Project, error) {
	value, ok := p[id]
	if !ok {
		return project.Project{}, errors.New("project not found")
	}
	return value, nil
}

func (p projectsFixture) List(context.Context) ([]project.Project, error) {
	values := make([]project.Project, 0, len(p))
	for _, value := range p {
		values = append(values, value)
	}
	return values, nil
}

type environmentRuntimeFixture struct {
	discovery          Interpreter
	probe              Interpreter
	lock               []string
	createErr          error
	installErr         error
	createdDestination *string
}

type blockingEnvironmentRuntimeFixture struct {
	environmentRuntimeFixture
	entered chan struct{}
	release chan struct{}
}

type reproducibleKernelFixture struct{}

type failingKernelFixture struct{}

type racingFailingKernelFixture struct{}

type racingSuccessfulKernelFixture struct{}

type serializedKernelFixture struct {
	entered chan string
	release chan struct{}
}

type figureKernelFixture struct {
	raceFinal bool
}

func (reproducibleKernelFixture) Execute(_ context.Context, request KernelExecuteRequest, workspace string) (KernelResult, error) {
	input, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(request.InputPaths[0])))
	if err != nil {
		return KernelResult{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(input)), "\n")
	total := 0
	for _, line := range lines[1:] {
		var value int
		if _, err := fmt.Sscanf(line, "%d", &value); err != nil {
			return KernelResult{}, err
		}
		total += value
	}
	output := filepath.Join(workspace, filepath.FromSlash(request.OutputPaths[0]))
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return KernelResult{}, err
	}
	if err := os.WriteFile(output, []byte(fmt.Sprintf("count,total\n%d,%d\n", len(lines)-1, total)), 0o600); err != nil {
		return KernelResult{}, err
	}
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	return KernelResult{KernelID: "kernel", ExecutionID: "execution", Sequence: 1, Status: "success", Stdout: "", Stderr: "", Value: total, ValueType: "int", Images: []string{}, StartedAt: now, FinishedAt: now}, nil
}
func (reproducibleKernelFixture) Stop(string) error    { return nil }
func (reproducibleKernelFixture) Restart(string) error { return nil }
func (reproducibleKernelFixture) Close() error         { return nil }

func (failingKernelFixture) Execute(_ context.Context, request KernelExecuteRequest, workspace string) (KernelResult, error) {
	output := filepath.Join(workspace, filepath.FromSlash(request.OutputPaths[0]))
	if err := os.WriteFile(output, []byte("partial"), 0o600); err != nil {
		return KernelResult{}, err
	}
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	return KernelResult{KernelID: "failed-kernel", ExecutionID: "failed-execution", Sequence: 1, Status: "error", Images: []string{}, Exception: &KernelError{Type: "ModuleNotFoundError", Message: "No module named fixture_missing_package", Traceback: "fixture traceback"}, StartedAt: now, FinishedAt: now}, nil
}
func (failingKernelFixture) Stop(string) error    { return nil }
func (failingKernelFixture) Restart(string) error { return nil }
func (failingKernelFixture) Close() error         { return nil }

func (racingFailingKernelFixture) Execute(_ context.Context, request KernelExecuteRequest, workspace string) (KernelResult, error) {
	staged := filepath.Join(workspace, filepath.FromSlash(request.OutputPaths[0]))
	if err := os.WriteFile(staged, []byte("partial"), 0o600); err != nil {
		return KernelResult{}, err
	}
	foreign := filepath.Join(workspace, "analysis-output", "race.csv")
	if err := os.WriteFile(foreign, []byte("external owner"), 0o600); err != nil {
		return KernelResult{}, err
	}
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	return KernelResult{KernelID: "race-kernel", ExecutionID: "race-execution", Sequence: 1, Status: "error", Images: []string{}, Exception: &KernelError{Type: "RuntimeError", Message: "fixture failure"}, StartedAt: now, FinishedAt: now}, nil
}
func (racingFailingKernelFixture) Stop(string) error    { return nil }
func (racingFailingKernelFixture) Restart(string) error { return nil }
func (racingFailingKernelFixture) Close() error         { return nil }

func (racingSuccessfulKernelFixture) Execute(_ context.Context, request KernelExecuteRequest, workspace string) (KernelResult, error) {
	staged := filepath.Join(workspace, filepath.FromSlash(request.OutputPaths[0]))
	if err := os.WriteFile(staged, []byte("kernel owner"), 0o600); err != nil {
		return KernelResult{}, err
	}
	foreign := filepath.Join(workspace, "analysis-output", "race-success.csv")
	if err := os.WriteFile(foreign, []byte("external owner"), 0o600); err != nil {
		return KernelResult{}, err
	}
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	return KernelResult{KernelID: "race-kernel", ExecutionID: "race-success-execution", Sequence: 1, Status: "success", Images: []string{}, StartedAt: now, FinishedAt: now}, nil
}
func (racingSuccessfulKernelFixture) Stop(string) error    { return nil }
func (racingSuccessfulKernelFixture) Restart(string) error { return nil }
func (racingSuccessfulKernelFixture) Close() error         { return nil }

func (r *serializedKernelFixture) Execute(_ context.Context, request KernelExecuteRequest, workspace string) (KernelResult, error) {
	output := filepath.Join(workspace, filepath.FromSlash(request.OutputPaths[0]))
	if err := os.WriteFile(output, []byte(request.Code), 0o600); err != nil {
		return KernelResult{}, err
	}
	r.entered <- request.Code
	if request.Code == "first" {
		<-r.release
	}
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	return KernelResult{KernelID: "serialized-kernel", ExecutionID: "serialized-" + request.Code, Sequence: 1, Status: "success", Images: []string{}, StartedAt: now, FinishedAt: now}, nil
}
func (r *serializedKernelFixture) Stop(string) error    { return nil }
func (r *serializedKernelFixture) Restart(string) error { return nil }
func (r *serializedKernelFixture) Close() error         { return nil }

func (r figureKernelFixture) Execute(_ context.Context, request KernelExecuteRequest, workspace string) (KernelResult, error) {
	output := filepath.Join(workspace, filepath.FromSlash(request.OutputPaths[0]))
	if err := os.WriteFile(output, []byte("data"), 0o600); err != nil {
		return KernelResult{}, err
	}
	figure := filepath.ToSlash(filepath.Join(request.FigurePath, "figure-01.png"))
	figureAbsolute := filepath.Join(workspace, filepath.FromSlash(figure))
	if err := os.MkdirAll(filepath.Dir(figureAbsolute), 0o700); err != nil {
		return KernelResult{}, err
	}
	if err := os.WriteFile(figureAbsolute, []byte("PNG fixture"), 0o600); err != nil {
		return KernelResult{}, err
	}
	executionID := "figure-execution"
	if r.raceFinal {
		final := filepath.Join(workspace, "analysis-output", "figures", executionID, "figure-01.png")
		if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
			return KernelResult{}, err
		}
		if err := os.WriteFile(final, []byte("external owner"), 0o600); err != nil {
			return KernelResult{}, err
		}
	}
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	return KernelResult{KernelID: "figure-kernel", ExecutionID: executionID, Sequence: 1, Status: "success", Images: []string{figure}, StartedAt: now, FinishedAt: now}, nil
}
func (figureKernelFixture) Stop(string) error    { return nil }
func (figureKernelFixture) Restart(string) error { return nil }
func (figureKernelFixture) Close() error         { return nil }

func (r environmentRuntimeFixture) Discover(context.Context, string) (Discovery, error) {
	value := r.discovery
	if value.ExecutablePath == "" {
		value = r.probe
	}
	return Discovery{Interpreters: []Interpreter{value}}, nil
}
func (r environmentRuntimeFixture) Probe(context.Context, string) (Interpreter, error) {
	return r.probe, nil
}
func (r environmentRuntimeFixture) CreateEnvironment(_ context.Context, _, destination string) error {
	if r.createdDestination != nil {
		*r.createdDestination = destination
	}
	if r.createErr != nil {
		return r.createErr
	}
	pythonPath := environmentPython(destination)
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(pythonPath, []byte("fixture Python"), 0o600)
}
func (r environmentRuntimeFixture) InstallPackages(context.Context, string, []string) error {
	return r.installErr
}
func (r environmentRuntimeFixture) Freeze(context.Context, string) ([]string, error) {
	return append([]string(nil), r.lock...), nil
}

func (r blockingEnvironmentRuntimeFixture) CreateEnvironment(ctx context.Context, _, destination string) error {
	close(r.entered)
	select {
	case <-r.release:
		return os.MkdirAll(filepath.Dir(environmentPython(destination)), 0o700)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestNormalizeRequestedPackagesRejectsPipInjection(t *testing.T) {
	for _, value := range []string{"--index-url=https://evil.test", "pkg;whoami", "https://evil.test/pkg.whl", "../fixture", "pkg @ file:///tmp/pkg"} {
		if _, err := normalizeRequestedPackages([]string{value}); err == nil {
			t.Fatalf("package specification %q was accepted", value)
		}
	}
}

func TestRecoverInterruptedRebuildRestoresCommittedEnvironment(t *testing.T) {
	service, repository, root, value := recoveryFixture(t, StateCreating)
	projectRoot := filepath.Join(root, value.ProjectID)
	previous := filepath.Join(projectRoot, ".previous-rebuild")
	if err := os.MkdirAll(filepath.Dir(environmentPython(previous)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentPython(previous), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := Operation{ID: "rebuild", ProjectID: value.ProjectID, EnvironmentID: value.ID, Kind: "rebuild", State: "running", StartedAt: time.Now().UTC()}
	repository.operations[operation.ID] = operation

	result, err := service.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := repository.values[value.ProjectID]
	if got.State != StateReady || got.EnvironmentFingerprint != value.EnvironmentFingerprint {
		t.Fatalf("recovered environment = %#v", got)
	}
	if repository.operations[operation.ID].State != "failed" || result.OperationsInterrupted != 1 {
		t.Fatalf("recovery result = %#v operation=%#v", result, repository.operations[operation.ID])
	}
	if _, err := os.Stat(environmentPython(filepath.Join(projectRoot, "venv"))); err != nil {
		t.Fatalf("committed environment was not restored: %v", err)
	}
}

func TestRecoverInterruptedDeleteFinishesAbsent(t *testing.T) {
	service, repository, root, value := recoveryFixture(t, StateDeleting)
	staged := filepath.Join(root, value.ProjectID, ".deleting-delete")
	if err := os.MkdirAll(staged, 0o700); err != nil {
		t.Fatal(err)
	}
	operation := Operation{ID: "delete", ProjectID: value.ProjectID, EnvironmentID: value.ID, Kind: "delete", State: "running", StartedAt: time.Now().UTC()}
	repository.operations[operation.ID] = operation
	if _, err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := repository.values[value.ProjectID]; got.State != StateAbsent || got.EnvironmentFingerprint != "" || len(got.Lock) == 0 {
		t.Fatalf("deleted environment declaration = %#v", got)
	}
	if repository.operations[operation.ID].State != "completed" {
		t.Fatalf("delete operation = %#v", repository.operations[operation.ID])
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged deleting path still exists: %v", err)
	}
}

func TestRecoverMissingReadyEnvironmentMarksBroken(t *testing.T) {
	service, repository, _, value := recoveryFixture(t, StateReady)
	result, err := service.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := repository.values[value.ProjectID]
	if got.State != StateBroken || !strings.Contains(got.ErrorMessage, "requires rebuild") || result.EnvironmentsRecovered != 1 {
		t.Fatalf("recovered missing environment = %#v, %#v", got, result)
	}
}

func TestCreateFailureDoesNotPublishStagingEnvironment(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "project"); err != nil {
		t.Fatal(err)
	}
	repository := &environmentRepositoryFixture{values: map[string]Environment{}, operations: map[string]Operation{}}
	base := Interpreter{ExecutablePath: filepath.Join(root, "python.exe"), Version: "3.12", Architecture: "64bit", Implementation: "CPython", ExecutableSHA256: strings.Repeat("a", 64), HasVenv: true, HasPip: true, Prefix: "base", BasePrefix: "base"}
	created := base
	created.Prefix = "env"
	service, err := NewService(repository, projectFixture{project.Project{ID: "project", WorkspacePath: workspace}}, environmentRuntimeFixture{discovery: base, probe: created, createErr: errors.New("fixture create failure")}, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), "project", base.ExecutablePath, false); err == nil {
		t.Fatal("Create() unexpectedly succeeded")
	}
	entries, err := os.ReadDir(filepath.Join(workspace, project.PrivateDirectoryName, "python"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".staging-") || entry.Name() == "venv" {
			t.Fatalf("partial environment remained: %s", entry.Name())
		}
	}
}

func TestCreatePublishesEnvironmentInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	projectID := "workspace-environment"
	if err := project.PrepareRestoredWorkspace(workspace, projectID); err != nil {
		t.Fatal(err)
	}
	repository := &environmentRepositoryFixture{values: map[string]Environment{}, operations: map[string]Operation{}}
	base := Interpreter{ExecutablePath: filepath.Join(t.TempDir(), "python.exe"), Version: "3.12.4", Architecture: "64bit", Implementation: "CPython", ExecutableSHA256: strings.Repeat("a", 64), HasVenv: true, HasPip: true, Prefix: "base", BasePrefix: "base"}
	created := base
	created.Prefix = filepath.Join(workspace, project.PrivateDirectoryName, "python", "venv")
	var destination string
	service, err := NewService(repository, projectFixture{project.Project{ID: projectID, WorkspacePath: workspace}}, environmentRuntimeFixture{discovery: base, probe: created, lock: []string{"pip==24.0"}, createdDestination: &destination}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.Create(context.Background(), projectID, base.ExecutablePath, false)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workspace, project.PrivateDirectoryName, "python")
	if value.Kind != KindWorkspaceManaged || filepath.Clean(value.EnvironmentPythonPath) != filepath.Clean(environmentPython(filepath.Join(root, "venv"))) {
		t.Fatalf("workspace environment = %#v", value)
	}
	if relative, err := filepath.Rel(root, destination); err != nil || !strings.HasPrefix(relative, ".staging-") {
		t.Fatalf("staging destination %q is outside Workspace Python root: %q, %v", destination, relative, err)
	}
	if _, err := os.Stat(value.EnvironmentPythonPath); err != nil {
		t.Fatalf("published Workspace interpreter is unavailable: %v", err)
	}
}

func TestBindExternalAndDeletePreservesUserEnvironment(t *testing.T) {
	workspace := t.TempDir()
	projectID := "external-environment"
	if err := project.PrepareRestoredWorkspace(workspace, projectID); err != nil {
		t.Fatal(err)
	}
	externalRoot := t.TempDir()
	externalPython := filepath.Join(externalRoot, "Scripts", "python.exe")
	if err := os.MkdirAll(filepath.Dir(externalPython), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(externalPython, []byte("user-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	selected := Interpreter{ExecutablePath: externalPython, Version: "3.12.4", Architecture: "64bit", Implementation: "CPython", ExecutableSHA256: strings.Repeat("b", 64), HasVenv: true, HasPip: true, Prefix: externalRoot, BasePrefix: filepath.Join(t.TempDir(), "base")}
	repository := &environmentRepositoryFixture{values: map[string]Environment{}, operations: map[string]Operation{}}
	service, err := NewService(repository, projectFixture{project.Project{ID: projectID, WorkspacePath: workspace}}, environmentRuntimeFixture{probe: selected, lock: []string{"pip==24.0"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	service.SetKernelStopper(func(id string) error {
		stopped = id == projectID
		return nil
	})
	value, err := service.BindExternal(context.Background(), projectID, externalPython)
	if err != nil || value.Kind != KindExternal || value.State != StateReady || filepath.Clean(value.EnvironmentPythonPath) != filepath.Clean(externalPython) {
		t.Fatalf("bound external environment = %#v, %v", value, err)
	}
	if _, err := service.Create(context.Background(), projectID, externalPython, true); err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatalf("external rebuild error = %v", err)
	}
	if _, err := service.Install(context.Background(), projectID, []string{"numpy"}); err == nil || !strings.Contains(err.Error(), "user-owned") {
		t.Fatalf("external install error = %v", err)
	}
	stopped = false
	if err := service.Delete(context.Background(), projectID); err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("external environment was unbound without stopping its project Kernel")
	}
	if contents, err := os.ReadFile(externalPython); err != nil || string(contents) != "user-owned" {
		t.Fatalf("external environment was modified: %q, %v", contents, err)
	}
	got := repository.values[projectID]
	if got.State != StateAbsent || got.Kind != KindWorkspaceManaged || got.EnvironmentPythonPath != "" || len(got.Lock) != 0 {
		t.Fatalf("environment was not cleanly unbound: %#v", got)
	}
}

func TestBindExternalRejectsBaseInterpreterAndRollsBackRegistration(t *testing.T) {
	workspace := t.TempDir()
	projectID := "external-rollback"
	if err := project.PrepareRestoredWorkspace(workspace, projectID); err != nil {
		t.Fatal(err)
	}
	base := Interpreter{ExecutablePath: filepath.Join(t.TempDir(), "python.exe"), Version: "3.12.4", Architecture: "64bit", Implementation: "CPython", ExecutableSHA256: strings.Repeat("c", 64), HasVenv: true, HasPip: true, Prefix: "base", BasePrefix: "base"}
	repository := &environmentRepositoryFixture{values: map[string]Environment{}, operations: map[string]Operation{}}
	service, err := NewService(repository, projectFixture{project.Project{ID: projectID, WorkspacePath: workspace}}, environmentRuntimeFixture{probe: base}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.BindExternal(context.Background(), projectID, base.ExecutablePath); err == nil || !strings.Contains(err.Error(), "not an existing virtual environment") {
		t.Fatalf("base interpreter binding error = %v", err)
	}
	venv := base
	venv.Prefix = "venv"
	service.runtime = environmentRuntimeFixture{probe: venv, lock: []string{"pip==24.0"}}
	repository.createOperationErr = errors.New("fixture operation failure")
	if _, err := service.BindExternal(context.Background(), projectID, venv.ExecutablePath); err == nil {
		t.Fatal("BindExternal() unexpectedly succeeded")
	}
	if _, exists := repository.values[projectID]; exists {
		t.Fatalf("failed binding left an environment record: %#v", repository.values[projectID])
	}
}

func TestFixedDatasetReproducesAcrossCleanProjectWorkspaces(t *testing.T) {
	projects := projectsFixture{}
	repository := &environmentRepositoryFixture{values: map[string]Environment{}, operations: map[string]Operation{}}
	now := time.Now().UTC()
	for _, id := range []string{"clean-a", "clean-b"} {
		workspace := filepath.Join(t.TempDir(), id)
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := project.PrepareRestoredWorkspace(workspace, id); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "fixed.csv"), []byte("value\n3\n5\n8\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		projects[id] = project.Project{ID: id, WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}
		repository.values[id] = Environment{ID: "environment-" + id, ProjectID: id, State: StateReady, EnvironmentFingerprint: strings.Repeat("f", 64), Lock: []string{"pip==24.0"}, CreatedAt: now, UpdatedAt: now}
	}
	environments, err := NewService(repository, projects, environmentRuntimeFixture{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kernels, err := NewKernelService(projects, environments, reproducibleKernelFixture{})
	if err != nil {
		t.Fatal(err)
	}
	hashes := []string{}
	for index, id := range []string{"clean-a", "clean-b"} {
		inputPath := fmt.Sprintf("fixed-%d.csv", index+1)
		outputPath := fmt.Sprintf("analysis-output/summary-%d.csv", index+1)
		if err := os.Rename(filepath.Join(projects[id].WorkspacePath, "fixed.csv"), filepath.Join(projects[id].WorkspacePath, inputPath)); err != nil {
			t.Fatal(err)
		}
		result, err := kernels.Execute(context.Background(), KernelExecuteRequest{ProjectID: id, Code: "sum fixed dataset values", InputPaths: []string{inputPath}, InputData: json.RawMessage(`{"selection":["fixed"]}`), OutputPaths: []string{outputPath}, Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.ReproductionSHA256) != 64 || result.Value != 16 {
			t.Fatalf("reproducible result = %#v", result)
		}
		if len(result.InputSHA256["$data"]) != 64 {
			t.Fatalf("structured input hash is missing: %#v", result.InputSHA256)
		}
		hashes = append(hashes, result.ReproductionSHA256)
	}
	if hashes[0] != hashes[1] {
		t.Fatalf("clean project reproduction hashes differ: %s != %s", hashes[0], hashes[1])
	}
}

func TestKernelPythonFailureRollsBackDeclaredOutputsAndHasReproductionHash(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "failure-project"); err != nil {
		t.Fatal(err)
	}
	projects := projectsFixture{"failure-project": {ID: "failure-project", WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}}
	now := time.Now().UTC()
	repository := &environmentRepositoryFixture{values: map[string]Environment{
		"failure-project": {ID: "failure-environment", ProjectID: "failure-project", State: StateReady, EnvironmentFingerprint: strings.Repeat("e", 64), CreatedAt: now, UpdatedAt: now},
	}, operations: map[string]Operation{}}
	environments, err := NewService(repository, projects, environmentRuntimeFixture{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kernels, err := NewKernelService(projects, environments, failingKernelFixture{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := kernels.Execute(context.Background(), KernelExecuteRequest{
		ProjectID: "failure-project", Code: "import fixture_missing_package", OutputPaths: []string{"analysis-output/partial.csv"}, Timeout: time.Second,
	})
	if err != nil || result.Status != "error" || result.Exception == nil || result.Exception.Type != "ModuleNotFoundError" {
		t.Fatalf("failed Kernel result = %#v, %v", result, err)
	}
	if len(result.ReproductionSHA256) != 64 || len(result.OutputSHA256) != 0 {
		t.Fatalf("failed Kernel provenance = %#v", result)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, "analysis-output", "partial.csv")); !os.IsNotExist(statErr) {
		t.Fatalf("failed Kernel left a declared partial output: %v", statErr)
	}
}

func TestKernelFailurePreservesOutputCreatedByAnotherOwnerDuringExecution(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "race-project"); err != nil {
		t.Fatal(err)
	}
	projects := projectsFixture{"race-project": {ID: "race-project", WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}}
	now := time.Now().UTC()
	repository := &environmentRepositoryFixture{values: map[string]Environment{
		"race-project": {ID: "race-environment", ProjectID: "race-project", State: StateReady, EnvironmentFingerprint: strings.Repeat("e", 64), CreatedAt: now, UpdatedAt: now},
	}, operations: map[string]Operation{}}
	environments, err := NewService(repository, projects, environmentRuntimeFixture{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kernels, err := NewKernelService(projects, environments, racingFailingKernelFixture{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := kernels.Execute(context.Background(), KernelExecuteRequest{ProjectID: "race-project", Code: "raise RuntimeError()", OutputPaths: []string{"analysis-output/race.csv"}, Timeout: time.Second})
	if err != nil || result.Status != "error" {
		t.Fatalf("failed Kernel result = %#v, %v", result, err)
	}
	contents, err := os.ReadFile(filepath.Join(workspace, "analysis-output", "race.csv"))
	if err != nil || string(contents) != "external owner" {
		t.Fatalf("external output was changed or removed: %q, %v", contents, err)
	}
	entries, err := filepath.Glob(filepath.Join(workspace, project.PrivateDirectoryName, "tmp", "kernel-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("Kernel staging directories = %#v, %v", entries, err)
	}
}

func TestKernelSuccessDoesNotReplaceOutputCreatedByAnotherOwnerDuringExecution(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "race-success-project"); err != nil {
		t.Fatal(err)
	}
	projects := projectsFixture{"race-success-project": {ID: "race-success-project", WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}}
	now := time.Now().UTC()
	repository := &environmentRepositoryFixture{values: map[string]Environment{
		"race-success-project": {ID: "race-success-environment", ProjectID: "race-success-project", State: StateReady, EnvironmentFingerprint: strings.Repeat("e", 64), CreatedAt: now, UpdatedAt: now},
	}, operations: map[string]Operation{}}
	environments, err := NewService(repository, projects, environmentRuntimeFixture{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kernels, err := NewKernelService(projects, environments, racingSuccessfulKernelFixture{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := kernels.Execute(context.Background(), KernelExecuteRequest{ProjectID: "race-success-project", Code: "write output", OutputPaths: []string{"analysis-output/race-success.csv"}, Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "without replacing an existing file") || result.Status != "" {
		t.Fatalf("racing successful Kernel = %#v, %v", result, err)
	}
	contents, readErr := os.ReadFile(filepath.Join(workspace, "analysis-output", "race-success.csv"))
	if readErr != nil || string(contents) != "external owner" {
		t.Fatalf("external output was replaced: %q, %v", contents, readErr)
	}
	entries, globErr := filepath.Glob(filepath.Join(workspace, project.PrivateDirectoryName, "tmp", "kernel-*"))
	if globErr != nil || len(entries) != 0 {
		t.Fatalf("Kernel staging directories = %#v, %v", entries, globErr)
	}
}

func TestKernelPublishesFiguresAndDeclaredOutputsAsOneBatch(t *testing.T) {
	for _, test := range []struct {
		name      string
		raceFinal bool
		wantError bool
	}{
		{name: "success"},
		{name: "figure target race", raceFinal: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			projectID := "figure-project"
			if err := project.PrepareRestoredWorkspace(workspace, projectID); err != nil {
				t.Fatal(err)
			}
			projects := projectsFixture{projectID: {ID: projectID, WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}}
			now := time.Now().UTC()
			repository := &environmentRepositoryFixture{values: map[string]Environment{
				projectID: {ID: "figure-environment", ProjectID: projectID, State: StateReady, EnvironmentFingerprint: strings.Repeat("e", 64), CreatedAt: now, UpdatedAt: now},
			}, operations: map[string]Operation{}}
			environments, err := NewService(repository, projects, environmentRuntimeFixture{}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			kernels, err := NewKernelService(projects, environments, figureKernelFixture{raceFinal: test.raceFinal})
			if err != nil {
				t.Fatal(err)
			}
			result, executeErr := kernels.Execute(context.Background(), KernelExecuteRequest{ProjectID: projectID, Code: "plot", OutputPaths: []string{"analysis-output/data.csv"}, Timeout: time.Second})
			dataPath := filepath.Join(workspace, "analysis-output", "data.csv")
			figurePath := filepath.Join(workspace, "analysis-output", "figures", "figure-execution", "figure-01.png")
			if test.wantError {
				if executeErr == nil || result.Status != "" {
					t.Fatalf("racing figure result = %#v, %v", result, executeErr)
				}
				if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
					t.Fatalf("declared output was not rolled back: %v", err)
				}
				contents, err := os.ReadFile(figurePath)
				if err != nil || string(contents) != "external owner" {
					t.Fatalf("external figure was changed: %q, %v", contents, err)
				}
				return
			}
			if executeErr != nil || len(result.Images) != 1 || result.Images[0] != filepath.ToSlash("analysis-output/figures/figure-execution/figure-01.png") || len(result.OutputSHA256) != 2 || len(result.ReproductionSHA256) != 64 {
				t.Fatalf("figure result = %#v, %v", result, executeErr)
			}
			if contents, err := os.ReadFile(dataPath); err != nil || string(contents) != "data" {
				t.Fatalf("declared output = %q, %v", contents, err)
			}
			if contents, err := os.ReadFile(figurePath); err != nil || string(contents) != "PNG fixture" {
				t.Fatalf("figure output = %q, %v", contents, err)
			}
		})
	}
}

func TestKernelSerializesExecutionsWithinOneProject(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "serialized-project"); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(workspace, "input.csv")
	if err := os.WriteFile(input, []byte("value\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projects := projectsFixture{"serialized-project": {ID: "serialized-project", WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}}
	now := time.Now().UTC()
	repository := &environmentRepositoryFixture{values: map[string]Environment{
		"serialized-project": {ID: "serialized-environment", ProjectID: "serialized-project", State: StateReady, EnvironmentFingerprint: strings.Repeat("e", 64), CreatedAt: now, UpdatedAt: now},
	}, operations: map[string]Operation{}}
	environments, err := NewService(repository, projects, environmentRuntimeFixture{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &serializedKernelFixture{entered: make(chan string, 2), release: make(chan struct{})}
	kernels, err := NewKernelService(projects, environments, runtime)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, err := kernels.Execute(context.Background(), KernelExecuteRequest{ProjectID: "serialized-project", Code: "first", InputPaths: []string{"input.csv"}, OutputPaths: []string{"analysis-output/first.csv"}, Timeout: time.Second})
		done <- err
	}()
	if entered := <-runtime.entered; entered != "first" {
		t.Fatalf("first entered execution = %q", entered)
	}
	go func() {
		_, err := kernels.Execute(context.Background(), KernelExecuteRequest{ProjectID: "serialized-project", Code: "second", InputPaths: []string{"input.csv"}, OutputPaths: []string{"analysis-output/second.csv"}, Timeout: time.Second})
		done <- err
	}()
	select {
	case entered := <-runtime.entered:
		t.Fatalf("second execution entered before the first completed: %q", entered)
	case <-time.After(100 * time.Millisecond):
	}
	close(runtime.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if entered := <-runtime.entered; entered != "second" {
		t.Fatalf("second entered execution = %q", entered)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(input)
	if err != nil || info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("input permissions were not restored: %v, %v", info.Mode(), err)
	}
}

func TestKernelExecutionAndEnvironmentMutationShareProjectLock(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "shared-lock-project"); err != nil {
		t.Fatal(err)
	}
	projects := projectsFixture{"shared-lock-project": {ID: "shared-lock-project", WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}}
	now := time.Now().UTC()
	repository := &environmentRepositoryFixture{values: map[string]Environment{
		"shared-lock-project": {ID: "shared-lock-environment", ProjectID: "shared-lock-project", State: StateReady, EnvironmentFingerprint: strings.Repeat("e", 64), CreatedAt: now, UpdatedAt: now},
	}, operations: map[string]Operation{}}
	createdInterpreter := Interpreter{ExecutablePath: "python", Version: "3.13.1", Implementation: "cpython", Architecture: "amd64", Prefix: "environment", BasePrefix: "base", ExecutableSHA256: strings.Repeat("a", 64), HasVenv: true, HasPip: true}
	baseInterpreter := createdInterpreter
	baseInterpreter.Prefix = baseInterpreter.BasePrefix
	environmentRuntime := blockingEnvironmentRuntimeFixture{environmentRuntimeFixture: environmentRuntimeFixture{discovery: baseInterpreter, probe: createdInterpreter}, entered: make(chan struct{}), release: make(chan struct{})}
	environments, err := NewService(repository, projects, environmentRuntime, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kernelRuntime := &serializedKernelFixture{entered: make(chan string, 1), release: make(chan struct{})}
	kernels, err := NewKernelService(projects, environments, kernelRuntime)
	if err != nil {
		t.Fatal(err)
	}
	kernelDone := make(chan error, 1)
	go func() {
		_, err := kernels.Execute(context.Background(), KernelExecuteRequest{ProjectID: "shared-lock-project", Code: "first", OutputPaths: []string{"analysis-output/shared-lock.csv"}, Timeout: time.Second})
		kernelDone <- err
	}()
	<-kernelRuntime.entered
	rebuildDone := make(chan error, 1)
	go func() {
		_, err := environments.Create(context.Background(), "shared-lock-project", "python", true)
		rebuildDone <- err
	}()
	select {
	case <-environmentRuntime.entered:
		t.Fatal("environment rebuild entered while the Kernel still owned the project lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(kernelRuntime.release)
	if err := <-kernelDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-environmentRuntime.entered:
	case <-time.After(time.Second):
		t.Fatal("environment rebuild did not enter after the Kernel released the project lock")
	}
	close(environmentRuntime.release)
	if err := <-rebuildDone; err != nil {
		t.Fatal(err)
	}
}

func TestKernelRecoveryRemovesOnlyOwnedUUIDStagingDirectories(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "recovery-project"); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(workspace, project.PrivateDirectoryName, "tmp")
	owned := "kernel-12345678-1234-4abc-8def-1234567890ab"
	lookalike := "kernel-user-notes"
	for _, name := range []string{owned, lookalike} {
		if err := os.MkdirAll(filepath.Join(temporary, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-kernelStagingRecoveryAge - time.Minute)
	if err := os.Chtimes(filepath.Join(temporary, owned), old, old); err != nil {
		t.Fatal(err)
	}
	projects := projectsFixture{"recovery-project": {ID: "recovery-project", WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}}
	environments, err := NewService(&environmentRepositoryFixture{values: map[string]Environment{}, operations: map[string]Operation{}}, projects, environmentRuntimeFixture{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kernels, err := NewKernelService(projects, environments, reproducibleKernelFixture{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := kernels.Recover(context.Background())
	if err != nil || result.TemporaryPathsRemoved != 1 {
		t.Fatalf("Recover() = %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(temporary, owned)); !os.IsNotExist(err) {
		t.Fatalf("owned staging directory remains: %v", err)
	}
	if info, err := os.Stat(filepath.Join(temporary, lookalike)); err != nil || !info.IsDir() {
		t.Fatalf("lookalike directory was removed: %v", err)
	}
}

func TestKernelRecoveryPreservesRecentOwnedStagingDirectory(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "recent-recovery-project"); err != nil {
		t.Fatal(err)
	}
	name := "kernel-12345678-1234-4abc-8def-1234567890ab"
	path := filepath.Join(workspace, project.PrivateDirectoryName, "tmp", name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	projects := projectsFixture{"recent-recovery-project": {ID: "recent-recovery-project", WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}}
	environments, err := NewService(&environmentRepositoryFixture{values: map[string]Environment{}, operations: map[string]Operation{}}, projects, environmentRuntimeFixture{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kernels, err := NewKernelService(projects, environments, reproducibleKernelFixture{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := kernels.Recover(context.Background())
	if err != nil || result.TemporaryPathsRemoved != 0 {
		t.Fatalf("Recover() = %#v, %v", result, err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("recent staging directory was removed: %v", err)
	}
}

func TestKernelRejectsEscapingOutputBeforeRuntimeExecution(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "path-project"); err != nil {
		t.Fatal(err)
	}
	projects := projectsFixture{"path-project": {ID: "path-project", WorkspacePath: workspace, WorkspaceKind: project.WorkspaceManaged}}
	now := time.Now().UTC()
	repository := &environmentRepositoryFixture{values: map[string]Environment{
		"path-project": {ID: "path-environment", ProjectID: "path-project", State: StateReady, EnvironmentFingerprint: strings.Repeat("e", 64), CreatedAt: now, UpdatedAt: now},
	}, operations: map[string]Operation{}}
	environments, err := NewService(repository, projects, environmentRuntimeFixture{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kernels, err := NewKernelService(projects, environments, reproducibleKernelFixture{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = kernels.Execute(context.Background(), KernelExecuteRequest{
		ProjectID: "path-project", Code: "print('must not run')", OutputPaths: []string{"../escape.csv"}, Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("Kernel accepted an escaping output path")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(workspace), "escape.csv")); !os.IsNotExist(statErr) {
		t.Fatalf("escaping Kernel output appeared: %v", statErr)
	}
}

func recoveryFixture(t *testing.T, state State) (*Service, *environmentRepositoryFixture, string, Environment) {
	t.Helper()
	root := t.TempDir()
	probe := Interpreter{Version: "3.12.4", Architecture: "64bit", Implementation: "CPython", Prefix: "env", BasePrefix: "base", HasPip: true}
	base := Interpreter{Version: probe.Version, Architecture: probe.Architecture, Implementation: probe.Implementation, ExecutableSHA256: strings.Repeat("a", 64)}
	lock := []string{"pip==24.0"}
	fingerprint, freeze := environmentHashes(base, probe, lock)
	now := time.Now().UTC()
	value := Environment{ID: "environment", ProjectID: "project", State: state, BaseExecutableVersion: base.Version, BaseExecutableSHA256: base.ExecutableSHA256, Architecture: base.Architecture, Implementation: base.Implementation, EnvironmentFingerprint: fingerprint, FreezeSHA256: freeze, Lock: lock, CreatedAt: now, UpdatedAt: now}
	repository := &environmentRepositoryFixture{values: map[string]Environment{value.ProjectID: value}, operations: map[string]Operation{}}
	service, err := NewService(repository, projectFixture{project.Project{ID: value.ProjectID}}, environmentRuntimeFixture{probe: probe, lock: lock}, root)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(environmentPython("x"), "python.exe") {
		t.Fatal("unexpected Windows environment path")
	}
	return service, repository, root, value
}

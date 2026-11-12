package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/platform/pythonruntime"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	"github.com/wangh00/SciAide/internal/tools/builtin"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// An installed, isolated fixture library exercises the environment inventory,
// dependency reuse and actual Kernel import without network package installs.
// It does not claim to test an arbitrary model's library/method selection.
func TestRecordedLibraryReusedThroughImplementationPreparationAndKernel(t *testing.T) {
	testRecordedLibraryImplementation(t, false)
}

func TestImplementationPartsAssembleAndExecuteWithRecordedLibrary(t *testing.T) {
	testRecordedLibraryImplementation(t, true)
}

func testRecordedLibraryImplementation(t *testing.T, split bool) {
	t.Helper()
	const requirement = "sciaide-fixture-math==1.0.0"
	var implementationRequests atomic.Int32
	var firstSource string
	server := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if _, implementation := output["analysisInput"]; !implementation {
			return
		}
		n := implementationRequests.Add(1)
		for _, want := range []string{"<recorded_python_environment>", requirement, `"sourceNodeId":"data_environment"`, "Prefer suitable, established library APIs", "not a live import test"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("implementation provider missing %s", want)
			}
		}
		output["dependencies"] = []string{requirement}
		output["researchChanges"] = []any{}
		output["_fixturePythonCode"] = `import csv
import json
from pathlib import Path
from importlib.metadata import version
from sciaide_fixture_math import sum_values
with Path(SCIAIDE_INPUTS[0]).open("r", encoding="utf-8-sig", newline="") as handle:
    rows = list(csv.DictReader(handle))
result = {"rowCount": len(rows), "x_sum": sum_values(float(row["x"]) for row in rows), "package_version": version("sciaide-fixture-math")}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\nThe installed fixture library sums x. No scientific claim.", encoding="utf-8")
result`
		if split {
			const boundary = "result = {\"rowCount\""
			code := output["_fixturePythonCode"].(string)
			at := strings.Index(code, boundary)
			if at < 1 {
				t.Fatal("source fixture boundary missing")
			}
			if n == 1 {
				firstSource = code[:at]
				output["_fixturePythonCode"] = firstSource
				output["_fixtureImplementationComplete"] = false
			} else {
				if n != 2 || !strings.Contains(prompt, "Completed source prefix") || !strings.Contains(prompt, "from sciaide_fixture_math import sum_values") || !strings.Contains(prompt, "NOT yet an accepted") {
					t.Error("second source request lost saved first part or made extra requests")
				}
				output["_fixturePythonCode"] = code[at:]
			}
		}
	}, mutateStarterToEmpiricalReportRoute)
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "recorded library reuse"})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := app.PythonFacade.DetectInterpreters()
	if err != nil || len(discovery.Interpreters) == 0 {
		t.Skip("Python unavailable")
	}
	env := filepath.Join(t.TempDir(), "python")
	if err := pythonruntime.New(app.pythonexec).CreateEnvironment(context.Background(), discovery.Interpreters[0].ExecutablePath, env); err != nil {
		t.Skipf("Python environment unavailable: %v", err)
	}
	site := filepath.Join(env, "Lib", "site-packages")
	if err := os.MkdirAll(filepath.Join(site, "sciaide_fixture_math"), 0700); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(site, "sciaide_fixture_math-1.0.0.dist-info")
	if err := os.MkdirAll(metadata, 0700); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(site, "sciaide_fixture_math", "__init__.py"): "def sum_values(values):\n    return sum(values)\n",
		filepath.Join(metadata, "METADATA"):                        "Metadata-Version: 2.1\nName: sciaide-fixture-math\nVersion: 1.0.0\n",
		filepath.Join(metadata, "INSTALLER"):                       "sciaide-test-fixture\n",
		filepath.Join(metadata, "RECORD"):                          "",
	} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.PythonFacade.BindProjectEnvironment(project.ID, filepath.Join(env, "Scripts", "python.exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project.WorkspacePath, "input.csv"), []byte("x,y\n1,2\n3,4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "Analyze the numerical columns using an already available library", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planned.Run.ID, RouteID: "empirical_report"})
	if err != nil {
		t.Fatal(err)
	}
	var inputs map[string]any
	if json.Unmarshal(adopted.InitialInputs, &inputs) != nil {
		t.Fatal("invalid route inputs")
	}
	inputs["input_paths"] = []string{"input.csv"}
	formal, err := app.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{ProjectID: project.ID, StarterRunID: planned.Run.ID, RouteID: adopted.RouteID, WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID, Inputs: mustBootstrapJSON(t, inputs), ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunCompleted, 40*time.Second)
	expectedRequests := int32(1)
	if split {
		expectedRequests = 2
	}
	if implementationRequests.Load() != expectedRequests || len(final.HumanDecisions) != 0 {
		t.Fatal("added implementation correction or confirmation", implementationRequests.Load(), final.HumanDecisions)
	}
	var fingerprints []string
	for _, step := range final.Steps {
		if step.NodeID == "data_environment" || step.NodeID == "dependency_preparation" {
			var output struct {
				Structured struct {
					EnvironmentFingerprint string   `json:"environmentFingerprint"`
					Lock                   []string `json:"lock"`
				} `json:"structured"`
			}
			if json.Unmarshal(step.Output, &output) != nil || !strings.Contains(strings.Join(output.Structured.Lock, ","), requirement) {
				t.Fatal("installed library lost from verified environment", step.NodeID)
			}
			fingerprints = append(fingerprints, output.Structured.EnvironmentFingerprint)
		}
		if step.NodeID == "python_analysis" {
			var output struct {
				Structured struct {
					Status string `json:"status"`
					Value  struct {
						XSum           int    `json:"x_sum"`
						PackageVersion string `json:"package_version"`
					} `json:"value"`
				} `json:"structured"`
			}
			if json.Unmarshal(step.Output, &output) != nil || output.Structured.Status != "success" || output.Structured.Value.XSum != 4 || output.Structured.Value.PackageVersion != "1.0.0" {
				t.Fatal("actual Kernel did not reuse the installed library", string(step.Output))
			}
		}
	}
	if len(fingerprints) != 2 || fingerprints[0] != fingerprints[1] || fingerprints[0] == "" {
		t.Fatal("reuse changed the dependency environment", fingerprints)
	}
	var installs int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM python_environment_operations WHERE project_id=? AND kind='install'`, project.ID).Scan(&installs); err != nil || installs != 0 {
		t.Fatal("satisfied dependency triggered an installation", installs, err)
	}
	for _, execution := range final.AIExecutions {
		if strings.Contains(execution.PromptVersion, "dynamic-implementation") && (!strings.Contains(execution.PromptVersion, "-implementation-efficiency-v1") || !strings.Contains(execution.PromptVersion, "-data-audit-v2")) {
			t.Fatal("effective implementation guidance not versioned", execution.PromptVersion)
		}
		if split && strings.Contains(execution.PromptVersion, "dynamic-implementation") {
			draft, found, err := sqlite.NewRunRepository(app.store.DB()).ImplementationDraft(context.Background(), execution.ChatRunID)
			if err != nil || !found || !draft.Complete || draft.PartCount != 2 || !strings.HasPrefix(draft.Code, firstSource) || execution.ModelTurns != 3 {
				t.Fatal("actual Kernel execution was not produced by audited two-part assembly", found, draft.PartCount, execution.ModelTurns, err)
			}
			var paid int
			if err := app.store.DB().QueryRow(`SELECT count(*) FROM model_request_usage WHERE run_id=? AND status_code=200`, execution.ChatRunID).Scan(&paid); err != nil || paid != 2 {
				t.Fatal("local assembly became a phantom paid request", paid, err)
			}
		}
	}
	// Exercise the direct builtin handler too: it need not have run workflow
	// ensure beforehand. Use only this temporary venv/database, never the
	// researcher's environment. A post-binding package removal must not report
	// success from the now-stale persisted lock or mutate its saved identity.
	repository := sqlite.NewPythonEnvironmentRepository(app.store.DB())
	service, err := pythonenv.NewService(repository, sqlite.NewProjectRepository(app.store.DB()), pythonruntime.New(app.pythonexec), filepath.Join(root, "direct-handler-managed"))
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	service.SetKernelStopper(func(string) error { stopped = true; return nil })
	direct := builtin.NewPythonEnvironmentInstall(service)
	invocation := tool.Invocation{ProjectID: project.ID, Arguments: mustBootstrapJSON(t, map[string]any{"packages": []string{requirement}})}
	saved, err := repository.Get(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := string(mustBootstrapJSON(t, saved))
	if result, err := direct.Invoke(context.Background(), invocation); err != nil || result.Status != tool.ResultSuccess {
		t.Fatal("direct external dependency reuse rejected a current environment", result, err)
	}
	var operations int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM python_environment_operations WHERE project_id=?`, project.ID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	for from, to := range map[string]string{
		metadata: filepath.Join(env, "fixture-uninstalled-metadata"),
		filepath.Join(site, "sciaide_fixture_math"): filepath.Join(env, "fixture-uninstalled-package"),
	} {
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := direct.Invoke(context.Background(), invocation); err == nil || result.Status == tool.ResultSuccess || !strings.Contains(err.Error(), "verify the project environment") {
		t.Fatal("direct tool falsely reused a removed actual dependency", result, err)
	}
	unchanged, err := repository.Get(context.Background(), project.ID)
	var afterOperations int
	if err != nil || string(mustBootstrapJSON(t, unchanged)) != before || stopped {
		t.Fatal("read-only direct reuse changed the saved environment or stopped Kernel", err)
	}
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM python_environment_operations WHERE project_id=?`, project.ID).Scan(&afterOperations); err != nil || operations != afterOperations {
		t.Fatal("read-only direct reuse started environment operations", operations, afterOperations, err)
	}
	t.Log("actual isolated library reused by implementation/prepare/Kernel; direct tool refused the subsequently removed dependency without changing the committed record")
}

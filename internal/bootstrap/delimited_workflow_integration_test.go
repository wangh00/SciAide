package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestDelimitedAnalysisWorkflowProducesReplayableResearchArtifacts(t *testing.T) {
	root, err := os.MkdirTemp("", "sciaide-p7-data-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	application, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	discovery, err := application.PythonFacade.DetectInterpreters()
	if err != nil || len(discovery.Interpreters) == 0 {
		t.Skipf("64-bit Python 3 with venv is not installed: %v (%s)", err, discovery.Message)
	}
	selected, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Delimited analysis"})
	if err != nil {
		t.Fatal(err)
	}
	inputName := "observations.csv"
	inputPath := filepath.Join(selected.WorkspacePath, inputName)
	contents := "group,value,score,note\nalpha,10,1.5,\"first line\nsecond line\"\nbeta,20,,ok\ngamma,30,4.5,=2+2\n"
	if err := os.WriteFile(inputPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	inputSHA := fileDigest(t, inputPath)
	environment, err := application.PythonFacade.CreateProjectEnvironment(selected.ID, discovery.Interpreters[0].ExecutablePath, false)
	if err != nil || environment.State != pythonenv.StateReady {
		t.Fatalf("create environment = %#v, %v", environment, err)
	}
	template := referenceWorkflowTemplate(t, application, "python-analysis")
	saved, err := application.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: selected.ID, Definition: template.Definition})
	if err != nil {
		t.Fatal(err)
	}
	inputs, _ := json.Marshal(map[string]any{
		"input_paths":      []string{inputName},
		"analysis_request": map[string]any{"goal": "检查缺失值并概览主要数值字段", "method": "data_quality"},
	})
	started, err := application.WorkflowFacade.Start(workflow.StartCommand{ProjectID: selected.ID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	completed := completeApprovedWorkflow(t, application, selected.ID, started.Run.ID, 3*time.Minute)
	if completed.Run.Status != workflow.RunCompleted || len(completed.Steps) != 2 {
		t.Fatalf("delimited Workflow = %#v", completed.Run)
	}
	var analysisStep workflow.Step
	for _, step := range completed.Steps {
		if step.NodeID == "analysis" {
			analysisStep = step
		}
	}
	var output struct {
		Structured pythonenv.KernelResult `json:"structured"`
		Artifacts  []tool.ArtifactRef     `json:"artifacts"`
	}
	var frozenInputs struct {
		InputPaths []string `json:"input_paths"`
	}
	if json.Unmarshal(completed.Run.Inputs, &frozenInputs) != nil || len(frozenInputs.InputPaths) != 1 {
		t.Fatalf("frozen Workflow inputs = %s", completed.Run.Inputs)
	}
	frozenInput := frozenInputs.InputPaths[0]
	if !strings.HasPrefix(frozenInput, "research-inputs/") || !strings.Contains(frozenInput, inputSHA) || fileDigest(t, inputPath) != inputSHA {
		t.Fatalf("input was not frozen by content without modifying its source: %q", frozenInput)
	}
	if analysisStep.ID == "" || json.Unmarshal(analysisStep.Output, &output) != nil {
		t.Fatalf("analysis output = %s", analysisStep.Output)
	}
	result := output.Structured
	if result.Status != "success" || result.InputSHA256[frozenInput] != inputSHA || result.EnvironmentFingerprint != environment.EnvironmentFingerprint || len(result.ReproductionSHA256) != 64 || len(result.OutputSHA256) != 5 || len(output.Artifacts) != 5 {
		t.Fatalf("Kernel evidence = %#v; artifacts=%#v", result, output.Artifacts)
	}
	value, ok := result.Value.(map[string]any)
	if !ok || value["rowCount"] != float64(3) || value["columnCount"] != float64(4) || value["numericColumnCount"] != float64(2) || value["missingCellCount"] != float64(1) || value["method"] != "data_quality" {
		t.Fatalf("analysis result = %#v", result.Value)
	}
	artifacts, err := application.ArtifactFacade.ListArtifacts(selected.ID, false)
	if err != nil || len(artifacts) != 5 {
		t.Fatalf("Artifacts = %#v, %v", artifacts, err)
	}
	roles := map[string]string{}
	for _, item := range artifacts {
		detail, getErr := application.ArtifactFacade.GetArtifact(selected.ID, item.ID)
		if getErr != nil || len(detail.Versions) != 1 {
			t.Fatalf("Artifact detail = %#v, %v", detail, getErr)
		}
		version := detail.Versions[0]
		if version.SourceKind != artifact.SourceTool || version.Provenance.WorkflowRunID != completed.Run.ID || version.Provenance.ToolCallID != analysisStep.ToolCallID {
			t.Fatalf("Artifact provenance = %#v", version.Provenance)
		}
		roles[delimitedArtifactRole(version.FileName)] = version.Provenance.WorkspaceRelativePath
	}
	for _, role := range []string{"cleaned", "summary", "chart", "methods", "script"} {
		if roles[role] == "" {
			t.Fatalf("missing %s Artifact: %#v", role, roles)
		}
	}
	cleaned, err := os.ReadFile(filepath.Join(selected.WorkspacePath, filepath.FromSlash(roles["cleaned"])))
	if err != nil || !strings.Contains(string(cleaned), "'=2+2") {
		t.Fatalf("cleaned CSV did not neutralize formula-like cells: %q, %v", cleaned, err)
	}
	methods, err := os.ReadFile(filepath.Join(selected.WorkspacePath, filepath.FromSlash(roles["methods"])))
	if err != nil || !strings.Contains(string(methods), "检查缺失值") || !strings.Contains(string(methods), "描述性探索") {
		t.Fatalf("methods evidence = %q, %v", methods, err)
	}
	replayDelimitedAnalysis(t, environment.EnvironmentPythonPath, selected.WorkspacePath, inputPath, roles, map[string]any{"goal": "检查缺失值并概览主要数值字段", "method": "data_quality"}, result.OutputSHA256)
}

func replayDelimitedAnalysis(t *testing.T, python, workspace, input string, roles map[string]string, request map[string]any, expected map[string]string) {
	t.Helper()
	directory := t.TempDir()
	paths := map[string]string{
		"cleaned": filepath.Join(directory, "cleaned.csv"),
		"summary": filepath.Join(directory, "summary.csv"),
		"chart":   filepath.Join(directory, "overview.svg"),
		"methods": filepath.Join(directory, "methods.md"),
	}
	requestJSON, _ := json.Marshal(request)
	command := exec.Command(python, "-I", filepath.Join(workspace, filepath.FromSlash(roles["script"])), input, paths["cleaned"], paths["summary"], paths["chart"], paths["methods"], string(requestJSON))
	command.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("replay delimited analysis: %v\n%s", err, output)
	}
	for role, path := range paths {
		originalPath := roles[role]
		if digest := fileDigest(t, path); digest != expected[originalPath] {
			t.Fatalf("replayed %s hash = %s, want %s", role, digest, expected[originalPath])
		}
	}
}

func delimitedArtifactRole(name string) string {
	switch {
	case strings.HasSuffix(name, "-cleaned.csv"):
		return "cleaned"
	case strings.HasSuffix(name, "-summary.csv"):
		return "summary"
	case strings.HasSuffix(name, "-overview.svg"):
		return "chart"
	case strings.HasSuffix(name, "-methods.md"):
		return "methods"
	case strings.HasSuffix(name, "-analysis.py"):
		return "script"
	default:
		return ""
	}
}

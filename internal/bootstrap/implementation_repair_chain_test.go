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

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/platform/pythonruntime"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real HTTP adapter + temporary SQLite + isolated Python. The remote model
// responses are controlled fixtures, not a commercial model quality claim.
func TestImplementationFormatCorrectionPythonKeyErrorAndSmallPatchReachDelivery(t *testing.T) {
	const source = `import csv
import json
from pathlib import Path
with Path(SCIAIDE_INPUTS[0]).open("r", encoding="utf-8-sig", newline="") as handle:
    rows = list(csv.DictReader(handle))
split_info = {"split_label_counts": {"train": len(rows)}}
result = {"rowCount": len(rows), "partitionCount": split_info["label_counts"]["train"]}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\nCount input rows, no scientific claim.", encoding="utf-8")
result`
	const old = `split_info["label_counts"]["train"]`
	const newText = `split_info["split_label_counts"]["train"]`
	boundary := strings.Index(source, "split_info =")
	var requests atomic.Int32
	server := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if _, implementation := output["analysisInput"]; !implementation {
			return
		}
		n := requests.Add(1)
		output["researchChanges"] = []any{}
		switch n {
		case 1, 2:
			output["_fixturePythonCode"] = source[:boundary]
			output["_fixtureImplementationComplete"] = false
			if n == 1 {
				delete(output, "dependencies")
			}
			if n == 2 && (!strings.Contains(prompt, "$.metadata.dependencies") || !strings.Contains(prompt, "index is STILL 1")) {
				t.Error("lost first-part format correction")
			}
		case 3:
			if !strings.Contains(prompt, "Completed source prefix") || !strings.Contains(prompt, "NOT yet an accepted") {
				t.Error("second part lost saved source")
			}
			output["_fixturePythonCode"] = source[boundary:]
		case 4, 5:
			if !strings.Contains(prompt, "KeyError") || !strings.Contains(prompt, "label_counts") || !strings.Contains(prompt, "submit_implementation_patch") {
				t.Error("runtime error or patch control not carried into automatic repair")
			}
			repair, _ := workflowAITestStageInput(prompt)["_pythonRepair"].(map[string]any)
			prior, _ := repair["priorImplementation"].(map[string]any)
			if prior["code"] != source {
				t.Error("repair does not target actual executed source")
			}
			patch := map[string]any{"baseCodeSha256": chat.ImplementationCodeSHA256(source), "replacements": []any{map[string]any{"old": old, "new": newText}}}
			if n == 5 {
				if !strings.Contains(prompt, "$.researchChanges") {
					t.Error("missing patch declaration was not corrected in place")
				}
				patch["researchChanges"] = []any{}
			}
			output["_fixtureImplementationPatch"] = patch
		default:
			t.Errorf("unexpected implementation request %d", n)
		}
	}, mutateStarterToEmpiricalReportRoute)
	app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "generated-code repair chain"})
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
	if _, err := app.PythonFacade.BindProjectEnvironment(project.ID, filepath.Join(env, "Scripts", "python.exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project.WorkspacePath, "input.csv"), []byte("x,y\n1,2\n3,4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "Analyze the supplied CSV", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planned.Run.ID, RouteID: "empirical_report"})
	if err != nil {
		t.Fatal(err)
	}
	var inputs map[string]any
	_ = json.Unmarshal(adopted.InitialInputs, &inputs)
	inputs["input_paths"] = []string{"input.csv"}
	formal, err := app.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{ProjectID: project.ID, StarterRunID: planned.Run.ID, RouteID: adopted.RouteID, WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID, Inputs: mustBootstrapJSON(t, inputs), ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunCompleted, 45*time.Second)
	if requests.Load() != 5 || len(final.HumanDecisions) != 0 {
		t.Fatal("extra generation or user confirmation", requests.Load(), final.HumanDecisions)
	}
	var kernelErrors, kernelSuccess int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM python_kernel_executions WHERE workflow_run_id=? AND status='error' AND exception_type='KeyError'`, formal.Run.ID).Scan(&kernelErrors); err != nil {
		t.Fatal(err)
	}
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM python_kernel_executions WHERE workflow_run_id=? AND status='success'`, formal.Run.ID).Scan(&kernelSuccess); err != nil {
		t.Fatal(err)
	}
	if kernelErrors != 1 || kernelSuccess != 2 {
		t.Fatal("unexpected preflight/failure/re-execution count", kernelErrors, kernelSuccess)
	}
	repairs := 0
	for _, event := range final.Events {
		if event.Type == "workflow.upstream_revision_queued" {
			repairs++
		}
	}
	if repairs != 1 {
		t.Fatal("format correction used a new Python repair allowance", repairs)
	}
	var implementationRuns []chat.ImplementationDraft
	for _, execution := range final.AIExecutions {
		if !strings.Contains(execution.PromptVersion, "dynamic-implementation") {
			continue
		}
		draft, found, err := sqlite.NewRunRepository(app.store.DB()).ImplementationDraft(context.Background(), execution.ChatRunID)
		if err != nil || !found || !draft.Complete {
			t.Fatal("implementation checkpoint not complete", found, err)
		}
		implementationRuns = append(implementationRuns, draft)
	}
	if len(implementationRuns) != 2 || implementationRuns[0].Code != source || implementationRuns[0].PartCount != 2 || implementationRuns[1].SourcePatch == nil || implementationRuns[1].PartCount != 1 || implementationRuns[1].Code != strings.Replace(source, old, newText, 1) || string(implementationRuns[0].Metadata) != string(implementationRuns[1].Metadata) {
		t.Fatal("patch replaced metadata or regenerated valid source", len(implementationRuns))
	}
	for _, step := range final.Steps {
		if step.NodeID == "python_analysis" {
			var out struct {
				Structured struct {
					Status string `json:"status"`
					Value  struct {
						RowCount       int `json:"rowCount"`
						PartitionCount int `json:"partitionCount"`
					} `json:"value"`
				} `json:"structured"`
			}
			if json.Unmarshal(step.Output, &out) != nil || out.Structured.Status != "success" || out.Structured.Value.RowCount != 2 || out.Structured.Value.PartitionCount != 2 {
				t.Fatal("actual patched Python result not verified", string(step.Output))
			}
		}
		if step.Status != workflow.StepCompleted {
			t.Fatal("report/review/delivery chain incomplete", step.NodeID, step.Status)
		}
	}
	t.Log("fixture chain: missing dependencies -> same part correction -> two-part assembly -> actual KeyError -> patch missing declaration -> same patch correction -> actual Python success -> report/review/delivery")
}

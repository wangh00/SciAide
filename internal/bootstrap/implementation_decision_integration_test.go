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

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/platform/pythonruntime"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Local HTTP model fixtures verify state transport, not scientific reasoning.
// The CSV and Python are real; all stores/workspaces are temporary.
func TestImplementationConfirmationSurvivesRestartAndReachesDownstream(t *testing.T) {
	var implementations, consumers atomic.Int32
	server := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		input := workflowAITestStageInput(prompt)
		if _, implementation := output["analysisInput"]; implementation {
			implementations.Add(1)
			if !strings.Contains(prompt, "actual field parsing/coercion types") {
				t.Error("quality-output guidance not delivered")
			}
			output["researchChanges"] = []any{map[string]any{"before": "unspecified field handling", "after": "parse x and y as numbers", "reason": "make analysis reproducible", "impact": "numeric input treatment"}}
			output["limitations"] = []any{"待研究者确认后继续；不证明方法正确。"}
			output["_fixturePythonCode"] = `import csv
import json
from pathlib import Path
with Path(SCIAIDE_INPUTS[0]).open("r", encoding="utf-8-sig", newline="") as handle:
    rows = list(csv.DictReader(handle))
result = {"rowCount": len(rows), "quality": {"parsed_types": {"x": "float", "y": "float"}}, "x_sum": sum(float(row["x"]) for row in rows)}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\nx and y parsed as float, not a validated measurement scale.", encoding="utf-8")
result`
			return
		}
		// The interpretation receives the actual implementation as methodContext;
		// report/review receive it as implementationContext.
		method, _ := input["implementationContext"].(map[string]any)
		if method == nil {
			candidate, _ := input["methodContext"].(map[string]any)
			if candidate["code"] != nil {
				method = candidate
			}
		}
		if method == nil {
			return
		}
		consumers.Add(1)
		decision, _ := input["implementationDecision"].(map[string]any)
		if decision["status"] != "approved" || decision["decisionId"] == nil || decision["attempt"] != float64(1) {
			t.Errorf("downstream lost exact authorization: %v", decision)
		}
		if !strings.Contains(prompt, "NOT scientific evidence") || !strings.Contains(prompt, "Do not ask the researcher to confirm") {
			t.Error("missing authorization/scientific-review separation")
		}
		if !strings.Contains(string(mustBootstrapJSON(t, method)), "待研究者确认") {
			t.Error("host rewrote original method text")
		}
	}, mutateStarterToEmpiricalReportRoute)
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "confirmed implementation"})
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
	started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "Read numerical columns and deliver reproducible results", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
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
	waiting := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunWaitingHumanConfirmation, 30*time.Second)
	var implementation workflow.Step
	for _, step := range waiting.Steps {
		if step.NodeID == "method_implementation" {
			implementation = step
		}
	}
	if implementation.Status != workflow.StepWaitingHumanConfirmation || consumers.Load() != 0 {
		t.Fatal("advanced before consent")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	if _, err := app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: project.ID, RunID: formal.Run.ID, StepID: implementation.ID, Approved: true}); err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunCompleted, 40*time.Second)
	if implementations.Load() != 1 || consumers.Load() != 3 {
		t.Fatal("unexpected implementation replay/downstream calls", implementations.Load(), consumers.Load())
	}
	if len(final.HumanDecisions) != 1 {
		t.Fatal("durable decision not loaded", final.HumanDecisions)
	}
	var decisionContext map[string]any
	_ = json.Unmarshal(final.HumanDecisions[0].Context, &decisionContext)
	if decisionContext["analysisContentSha256"] == nil {
		t.Fatal("new decision lost content binding")
	}
	// Durable records are immutable. Restart without rewriting the approval.
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopened, err := app.WorkflowFacade.GetRun(project.ID, formal.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.HumanDecisions) != 1 || reopened.Run.Status != workflow.RunCompleted {
		t.Fatal("restart lost decision")
	}
	if string(reopened.HumanDecisions[0].Context) != string(final.HumanDecisions[0].Context) {
		t.Fatal("restart rewrote immutable approval")
	}
}

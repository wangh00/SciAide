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

func TestPythonAutomaticRepairThreeRoundClosedLoop(t *testing.T) {
	for _, succeeds := range []bool{true, false} {
		name := "exhausted"
		if succeeds {
			name = "third_repair_succeeds"
		}
		t.Run(name, func(t *testing.T) {
			var implementations atomic.Int32
			server := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
				if _, ok := output["analysisInput"]; !ok {
					return
				}
				n := implementations.Add(1)
				if n > 1 && (!strings.Contains(prompt, "priorImplementation") || !strings.Contains(prompt, "fixture type error")) {
					t.Error("repair lost actual prior code or exception")
				}
				if !succeeds || n <= 3 {
					output["_fixturePythonCode"] = "raise TypeError('fixture type error')\n" + workflowAITestImplementationCode()
				}
			}, mutateStarterToEmpiricalReportRoute)
			app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			app.Startup(context.Background())
			profile := saveWorkflowAITestProfile(t, app, server.URL)
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: name})
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
			started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "Compare frozen measurements", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			planned := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 15*time.Second)
			adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planned.Run.ID, RouteID: "empirical_report"})
			if err != nil {
				t.Fatal(err)
			}
			var inputs map[string]any
			_ = json.Unmarshal(adopted.InitialInputs, &inputs)
			inputs["input_paths"] = []string{"input.csv"}
			encoded, _ := json.Marshal(inputs)
			formal, err := app.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{ProjectID: project.ID, StarterRunID: planned.Run.ID, RouteID: adopted.RouteID, WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID, Inputs: encoded, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			var final workflow.RunDetail
			for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				final, err = app.WorkflowFacade.GetRun(project.ID, formal.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if final.Run.Status == workflow.RunWaitingHumanConfirmation {
					t.Fatal("technical repair unexpectedly requested confirmation")
				}
				if final.Run.Status.Terminal() {
					break
				}
			}
			if succeeds && final.Run.Status != workflow.RunCompleted {
				t.Fatal(final.Run.Status, final.Run.ErrorCode, final.Run.ErrorMessage)
			}
			if !succeeds && (final.Run.Status != workflow.RunFailed || final.Run.ErrorCode != "WORKFLOW_PYTHON_REPAIR_EXHAUSTED") {
				t.Fatal(final.Run.Status, final.Run.ErrorCode, final.Run.ErrorMessage)
			}
			if implementations.Load() != 4 {
				t.Fatal("expected initial implementation plus three repairs", implementations.Load())
			}
			repairs := 0
			for _, event := range final.Events {
				if event.Type == "workflow.upstream_revision_queued" {
					repairs++
				}
			}
			if repairs != 3 {
				t.Fatal("incorrect durable repair budget", repairs)
			}
		})
	}
}

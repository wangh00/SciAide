package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/reviewcontract"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestComputationRecordRealPythonHTTPAndSQLiteRestart(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is unavailable")
	}
	root, err := os.MkdirTemp("", "sciaide-cr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	var sawRecord atomic.Bool
	server := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if routes, ok := output["routes"].([]any); ok {
			for _, rawRoute := range routes {
				route := rawRoute.(map[string]any)
				if route["routeId"] != "tabular_analysis_when_ready" {
					continue
				}
				stages := route["stagePlans"].([]any)
				updated := []any{}
				for _, rawStage := range stages {
					stage := rawStage.(map[string]any)
					if stage["stageId"] == "independent_review" {
						updated = append(updated, map[string]any{"stageId": "report_drafting", "objective": "Write a concise description from actual results.", "methods": []any{}, "skillNames": []any{}})
					}
					updated = append(updated, stage)
				}
				route["stagePlans"] = updated
			}
		}
		if _, ok := output["markdown"]; ok {
			input := workflowAITestStageInput(prompt)
			if input["computationRecordPolicy"] != "computation-record-v1" {
				t.Error("report HTTP request lacks host computation record")
			}
			record, _ := input["computationRecord"].(map[string]any)
			if record["codeSha256"] == "" || record["bindingError"] != nil {
				t.Error("report received unverified record")
			}
			sawRecord.Store(true)
			output["markdown"] = "# 简短结果\n\n本次标准库计算读取两条记录；这是受控夹具，不是科学结论。"
		}
	})
	defer server.Close()
	options := Options{RootDir: root, EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	defer func() {
		if app != nil {
			_ = app.Close()
		}
	}()
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Computation provenance fixture"})
	if err != nil {
		t.Fatal(err)
	}
	starter, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "Analyze an adopted local CSV with Python and deliver a short report and JSON. The file will be supplied after adoption. No literature.", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
	if err != nil {
		t.Fatal(err)
	}
	waitForWorkflowState(t, app, project.ID, starter.Run.ID, workflow.RunCompleted, 20*time.Second)
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: starter.Run.ID, RouteID: "tabular_analysis_when_ready"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project.WorkspacePath, "data.csv"), []byte("score\n3\n4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var inputs map[string]any
	if err := json.Unmarshal(adopted.InitialInputs, &inputs); err != nil {
		t.Fatal(err)
	}
	inputs["input_paths"] = []string{"data.csv"}
	encoded, _ := json.Marshal(inputs)
	started, err := app.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{ProjectID: project.ID, StarterRunID: adopted.StarterRunID, RouteID: adopted.RouteID, WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID, Inputs: encoded, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
	if err != nil {
		t.Fatal(err)
	}
	var completed workflow.RunDetail
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		completed, err = app.WorkflowFacade.GetRun(project.ID, started.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Run.Status == workflow.RunCompleted {
			break
		}
		if completed.Run.Status == workflow.RunFailed {
			for _, s := range completed.Steps {
				if s.Status == workflow.StepFailed {
					t.Fatalf("stage %s failed: %s %s", s.NodeID, s.ErrorCode, s.ErrorMessage)
				}
			}
			t.Fatal("workflow failed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if completed.Run.Status != workflow.RunCompleted || !sawRecord.Load() {
		t.Fatal("real Python/report/review/gate closure did not complete")
	}
	var report workflow.Step
	for _, s := range completed.Steps {
		if s.NodeID == "report_drafting" {
			report = s
		}
		if s.Attempt != 1 {
			t.Fatal("unexpected retry", s.NodeID)
		}
	}
	var output struct {
		Analysis struct {
			Markdown string `json:"markdown"`
		} `json:"analysis"`
	}
	if json.Unmarshal(report.Output, &output) != nil {
		t.Fatal("invalid report")
	}
	if !strings.HasPrefix(output.Analysis.Markdown, "# 简短结果") || !strings.Contains(output.Analysis.Markdown, "## 附录：计算执行记录") || strings.Count(output.Analysis.Markdown, "## 附录：计算执行记录") != 1 {
		t.Fatal("host did not append exactly one record")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app = nil
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	recovered, err := app.WorkflowFacade.GetRun(project.ID, started.Run.ID)
	if err != nil || recovered.Run.Status != workflow.RunCompleted {
		t.Fatal("completed run did not survive restart", err)
	}
	for _, s := range recovered.Steps {
		if s.NodeID == report.NodeID && (!reviewcontract.Equal(s.Input, report.Input) || !reviewcontract.Equal(s.Output, report.Output)) {
			t.Fatal("frozen report changed after restart")
		}
	}
}

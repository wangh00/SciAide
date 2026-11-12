package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// Actual HTTP model adapters, tools, SQLite/restart and Python. The replies are
// controlled fixtures, not verification of a commercial model's reasoning.
func TestDataBindingAndExplicitPrerequisiteRecoveryEndToEnd(t *testing.T) {
	for _, blockFirst := range []bool{false, true} {
		name := "bound-file-computes"
		if blockFirst {
			name = "explicit-blocker-resumes-after-confirmation"
		}
		t.Run(name, func(t *testing.T) {
			var implementations, consumers, boundStages atomic.Int32
			var repaired atomic.Bool
			stage := newResearchStarterOutputTestServer(t, func(out map[string]any, prompt string) {
				input := workflowAITestStageInput(prompt)
				if _, planning := out["normalizedQuestion"]; planning {
					return
				}
				binding, _ := input["dataBinding"].(map[string]any)
				if binding["status"] != "bound" || binding["workflowRunId"] == "" || !strings.Contains(prompt, "These files need no second binding confirmation") {
					t.Error("formal AI did not receive current adopted input", binding)
				}
				boundStages.Add(1)
				if _, implementation := out["analysisInput"]; implementation {
					implementations.Add(1)
					if !strings.Contains(prompt, "SCIAIDE_REQUIRE_INPUT") {
						t.Error("missing explicit prerequisite protocol")
					}
					out["researchChanges"] = []any{}
					out["dependencies"] = []any{}
					out["analysisInput"] = map[string]any{"method": "sum x", "bindingConfirmedByHost": true}
					if blockFirst && !repaired.Load() {
						out["_fixturePythonCode"] = `inputs = SCIAIDE_INPUTS
outputs = SCIAIDE_OUTPUTS
SCIAIDE_REQUIRE_INPUT("Confirm a genuinely unresolved measurement choice, not CSV binding")`
					} else {
						out["outputDeclarations"] = []any{map[string]any{"name": "analysis_source", "format": "py"}}
						out["_fixturePythonCode"] = `import csv
import json
from pathlib import Path
with Path(SCIAIDE_INPUTS[0]).open("r", encoding="utf-8-sig", newline="") as handle:
    rows = list(csv.DictReader(handle))
result = {"rowCount": len(rows), "sum_x": sum(float(row["x"]) for row in rows), "calculation_started": True}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\nParse x as float and sum exactly the adopted file; no scientific validity claim.", encoding="utf-8")
Path(SCIAIDE_OUTPUTS[2]).write_text("import csv, sys\nwith open(sys.argv[1], newline='') as f: print(sum(float(r['x']) for r in csv.DictReader(f)))\n", encoding="utf-8")
result`
					}
					return
				}
				method, _ := input["implementationContext"].(map[string]any)
				if method == nil {
					candidate, _ := input["methodContext"].(map[string]any)
					if candidate["code"] != nil {
						method = candidate
					}
				}
				if method != nil {
					consumers.Add(1)
				}
			}, func(plan map[string]any) {
				mutateStarterToEmpiricalReportRoute(plan)
				plan["routes"].([]any)[0].(map[string]any)["reason"] = "该表格仍需在正式执行前由用户确认绑定；本次仅准备候选研究路线，不启动计算。"
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
					Tools []struct{ Function struct{ Name string } }
				}
				if json.Unmarshal(body, &req) != nil {
					http.Error(w, "invalid request", 400)
					return
				}
				discussion := false
				for _, tool := range req.Tools {
					discussion = discussion || tool.Function.Name == "research_revision_propose"
				}
				if !discussion {
					r.Body = io.NopCloser(bytes.NewReader(body))
					stage.Config.Handler.ServeHTTP(w, r)
					return
				}
				prompt := workflowAITestPrompt(body)
				if !strings.Contains(prompt, `"dataBinding"`) || !strings.Contains(prompt, "There is no completed report or independent review") {
					t.Error("blocked discussion lacks truthful host lifecycle")
				}
				last := 0
				for i, m := range req.Messages {
					if m.Role == "user" {
						last = i
					}
				}
				hasResult := false
				for _, m := range req.Messages[last+1:] {
					hasResult = hasResult || m.Role == "tool"
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if hasResult {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已保留绑定数据并确定实现阶段返修；请确认，尚未执行。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				command := workflow.ProposeResearchRevisionCommand{NodeID: "method_implementation", Summary: "Keep the bound CSV and perform the agreed sum", Changes: []string{"Resolve the measurement choice using the agreed x parsing; preserve the goal and adopted CSV", "Deliver actual computed results and an independent .py source"}, Reason: "The implementation declared the prerequisite; report-only edits cannot compute the missing result"}
				args, _ := json.Marshal(command)
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "repair-confirmation", "function": map[string]any{"name": "research_revision_propose", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			}))
			defer server.Close()
			root := t.TempDir()
			app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile := saveWorkflowAITestProfile(t, app, server.URL)
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: name})
			if err != nil {
				t.Fatal(err)
			}
			discovery, err := app.PythonFacade.DetectInterpreters()
			if err != nil || len(discovery.Interpreters) == 0 {
				t.Fatal("test Python unavailable", err)
			}
			env := filepath.Join(t.TempDir(), "venv")
			if err := pythonruntime.New(app.pythonexec).CreateEnvironment(context.Background(), discovery.Interpreters[0].ExecutablePath, env); err != nil {
				t.Fatal(err)
			}
			if _, err := app.PythonFacade.BindProjectEnvironment(project.ID, filepath.Join(env, "Scripts", "python.exe")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project.WorkspacePath, "wine.csv"), []byte("x,y\n1,2\n3,4\n"), 0600); err != nil {
				t.Fatal(err)
			}
			started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "Use the wine CSV and calculate, not just plan", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
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
			inputs["input_paths"] = []string{"wine.csv"}
			formal, err := app.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{ProjectID: project.ID, StarterRunID: planned.Run.ID, RouteID: adopted.RouteID, WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID, Inputs: mustBootstrapJSON(t, inputs), ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			if blockFirst {
				blocked := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunFailed, 40*time.Second)
				if blocked.Run.ErrorCode != "WORKFLOW_RESEARCH_PREREQUISITE_BLOCKED" || implementations.Load() != 1 || consumers.Load() != 0 || !blocked.DiscussionRevisionAvailable {
					t.Fatalf("blocker state=%s code=%s step=%d implementations=%d consumers=%d canRevise=%v error=%s", blocked.Run.Status, blocked.Run.ErrorCode, blocked.Run.CurrentStep, implementations.Load(), consumers.Load(), blocked.DiscussionRevisionAvailable, blocked.Run.ErrorMessage)
				}
				for _, step := range blocked.Steps {
					if step.NodeID == "report_drafting" || step.NodeID == "independent_review" {
						if step.Status != workflow.StepQueued || step.Attempt != 0 {
							t.Fatal("empty report advanced", step)
						}
					}
				}
				var blockedStep workflow.Step
				for _, step := range blocked.Steps {
					if step.NodeID == "python_analysis" {
						blockedStep = step
					}
				}
				if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: formal.Run.ID, StepID: blockedStep.ID, ConfirmSideEffect: true}); err == nil {
					t.Fatal("blind retry accepted without resolving the explicit prerequisite")
				}
				if err := app.Close(); err != nil {
					t.Fatal(err)
				}
				// Reconstruct only this temporary fixture's crash gap: Python's
				// failed result persisted, but workflow failure not yet projected.
				db, err := sqlite.OpenExisting(context.Background(), filepath.Join(root, "data", "sciaide.db"), false)
				if err != nil {
					t.Fatal(err)
				}
				var callStatus, callResult string
				if err := db.QueryRow(`SELECT c.status,r.structured_json FROM tool_calls c JOIN tool_results r ON r.tool_call_id=c.id WHERE c.id=?`, blockedStep.ToolCallID).Scan(&callStatus, &callResult); err != nil || callStatus != "failed" || !strings.Contains(callResult, "SciAideResearchBlocked") {
					_ = db.Close()
					t.Fatal("failed tool result not durably recorded", callStatus, err)
				}
				for _, query := range []string{
					`DELETE FROM workflow_events WHERE workflow_run_id=? AND event_type='workflow.failed' AND json_extract(payload_json,'$.errorCode')='WORKFLOW_RESEARCH_PREREQUISITE_BLOCKED'`,
					`UPDATE workflow_runs SET status='running',completed_at=NULL,error_code='',error_message='' WHERE id=?`,
					`UPDATE workflow_steps SET status='running',completed_at=NULL,error_code='',error_message='' WHERE workflow_run_id=? AND node_id='python_analysis'`,
				} {
					if _, err := db.Exec(query, formal.Run.ID); err != nil {
						_ = db.Close()
						t.Fatal(err)
					}
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
				if err != nil {
					t.Fatal(err)
				}
				app.Startup(context.Background())
				recovered := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunFailed, 12*time.Second)
				if recovered.Run.ErrorCode != "WORKFLOW_RESEARCH_PREREQUISITE_BLOCKED" || !recovered.DiscussionRevisionAvailable || implementations.Load() != 1 || consumers.Load() != 0 {
					t.Fatal("crash gap lost the exact prerequisite or reran code", recovered.Run.ErrorCode, implementations.Load(), consumers.Load())
				}
				var persistedStatus, persistedResult string
				if err := app.store.DB().QueryRow(`SELECT c.status,r.structured_json FROM tool_calls c JOIN tool_results r ON r.tool_call_id=c.id WHERE c.id=?`, blockedStep.ToolCallID).Scan(&persistedStatus, &persistedResult); err != nil || persistedStatus != callStatus || persistedResult != callResult {
					t.Fatal("startup rewrote completed Python failure", err)
				}
				chatRun, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: formal.Run.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, Text: "Use the agreed x parsing and repair the method; keep the already bound CSV"})
				if err != nil {
					t.Fatal(err)
				}
				for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
					snapshot, err := app.ChatFacade.GetRunSnapshot(chatRun.ID)
					if err != nil {
						t.Fatal(err)
					}
					if snapshot.Run.Status == chat.RunCompleted {
						for _, call := range snapshot.ToolCalls {
							if call.Status != "completed" {
								t.Fatalf("discussion tool failed: %+v", call)
							}
						}
						break
					}
					if snapshot.Run.Status == chat.RunFailed || time.Now().After(deadline) {
						t.Fatalf("discussion failed or timed out: %+v", snapshot.Run)
					}
				}
				proposals, err := app.WorkflowFacade.ListResearchRevisionProposals(project.ID, formal.Run.ID)
				if err != nil || len(proposals) != 1 || !proposals[0].CanConfirm {
					t.Fatal("proposal not confirmable after restart", proposals, err)
				}
				if implementations.Load() != 1 || repaired.Load() {
					t.Fatal("proposal started work before confirmation")
				}
				repaired.Store(true)
				if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: project.ID, RunID: formal.Run.ID, ProposalID: proposals[0].ID, Confirmed: true}); err != nil {
					t.Fatal(err)
				}
			}
			final := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunCompleted, 40*time.Second)
			for _, step := range final.Steps {
				if step.NodeID == "python_analysis" {
					var envelope struct {
						Structured struct {
							Value struct {
								Rows    int     `json:"rowCount"`
								Sum     float64 `json:"sum_x"`
								Started bool    `json:"calculation_started"`
							}
						}
					}
					if json.Unmarshal(step.Output, &envelope) != nil || envelope.Structured.Value.Rows != 2 || envelope.Structured.Value.Sum != 4 || !envelope.Structured.Value.Started {
						t.Fatal("real calculation missing", string(step.Output))
					}
				}
			}
			if !strings.Contains(string(final.Run.Inputs), "仅准备候选研究路线") || boundStages.Load() < 7 || len(final.HumanDecisions) != 0 {
				t.Fatal("history rewritten or added duplicate approval", boundStages.Load(), final.HumanDecisions)
			}
			var artifacts int
			if err := app.store.DB().QueryRow(`SELECT count(*) FROM python_kernel_executions WHERE workflow_run_id=? AND status='success'`, formal.Run.ID).Scan(&artifacts); err != nil || artifacts < 2 {
				t.Fatal("missing execution audit", artifacts, err)
			}
		})
	}
}

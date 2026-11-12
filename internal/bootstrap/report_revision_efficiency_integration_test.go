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
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/platform/pythonruntime"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Controlled HTTP model + actual isolated Python + temporary SQLite/restart.
// Verifies the transport/confirmation/evidence chain, NOT model research quality.
func TestReportOnlyRevisionKeepsRawResultsThroughResourcePagesAndRestart(t *testing.T) {
	runReportOnlyRevisionFixture(t, false)
}

func TestDeliveryScopeThroughDiscussionToolCardSQLiteRestartAndReview(t *testing.T) {
	runReportOnlyRevisionFixture(t, true)
}

func runReportOnlyRevisionFixture(t *testing.T, changeScope bool) {
	scope := &workflow.DeliveryScopeSelection{Summary: "Deliver a bounded numerical methods note, not external validation", SuccessCriteria: []string{"Report frozen numerical results using full precision", "Disclose that external validation has not been performed"}}
	var revisedReports, revisedReviews, revisedResultReads atomic.Int32
	var reportWrites atomic.Value
	reportWrites.Store([]string{})
	base := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if _, implementation := output["analysisInput"]; implementation {
			output["dependencies"] = []any{}
			output["researchChanges"] = []any{}
			output["_fixturePythonCode"] = `import csv
import json
from pathlib import Path
with Path(SCIAIDE_INPUTS[0]).open("r", encoding="utf-8-sig", newline="") as handle:
    rows = list(csv.DictReader(handle))
result = {"rowCount": len(rows), "baseline": 0.6852791878172588, "model": 0.5169376078243285, "zeroBaseline": 0, "record": "bounded numerical fixture", "padding": "reproducible-result," * 1500}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result, indent=2), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\nA fixed precision fixture, not an empirical research finding.", encoding="utf-8")
result`
		}
		input := workflowAITestStageInput(prompt)
		revision, _ := input["_userRevision"].(map[string]any)
		if revision == nil {
			if _, report := output["markdown"]; report {
				output["markdown"] = "# Original report\nRounded-table example: 0.1684."
				output["limitations"] = []any{"This is only a fixed fixture."}
			}
			return
		}
		if revision["comparisonBaseline"] != nil || revision["reuseContext"] == nil || input["implementationContext"] == nil || input["computedResults"] == nil || input["researchContract"] == nil {
			t.Error("report-only revision lost current evidence or retained duplicate baseline")
		}
		if changeScope {
			contract, _ := input["researchContract"].(map[string]any)
			if contract["deliveryScope"] == nil || string(mustBootstrapJSON(t, contract["successCriteria"])) != string(mustBootstrapJSON(t, scope.SuccessCriteria)) {
				t.Error("confirmed scope lost across report/review", contract)
			}
			if _, review := output["reviewedInputSha256"]; review {
				if !strings.Contains(string(mustBootstrapJSON(t, input["acceptanceCriteria"])), scope.SuccessCriteria[0]) {
					t.Error("review uses old criteria")
				}
			}
		}
		for _, want := range []string{"round only the final displayed value", "not an erroneous number", "concrete promised work still missing", "NOT INCLUDED in this round", "there is no tool-count quota"} {
			if !strings.Contains(prompt, want) {
				t.Error("missing precision/scope/reuse instruction", want)
			}
		}
		if _, report := output["markdown"]; report {
			revisedReports.Add(1)
			prior, _ := revision["priorOutput"].(map[string]any)
			if prior["analysis"] == nil {
				t.Error("previous complete structured report lost")
			}
			// This server's prior text contains distinct commentary before its
			// JSON. It must be retained, not treated as an exact duplicate.
			if text, exists := prior["text"].(string); exists && !strings.HasPrefix(text, "Skill-assisted research stage completed.") {
				t.Error("distinct prior commentary unexpectedly changed")
			}
			output["markdown"] = "# Revised report\nFrozen raw baseline 0.6852791878172588 and model 0.5169376078243285 yield B-M = 0.1683415799929303; displayed reduction 0.1683 (24.6%).\nExternal validation was not included in this round; a promised missing item is not relabeled as optional."
			output["limitations"] = []any{"External validation was not included, limiting generalization."}
		}
		if _, review := output["reviewedInputSha256"]; review {
			revisedReviews.Add(1)
			if input["acceptanceCriteria"] == nil || input["reviewHistory"] == nil || !strings.Contains(prompt, "0.1683") {
				t.Error("review lost criteria, history or revised deliverable")
			}
		}
	}, mutateStarterToEmpiricalReportRoute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			t.Error(err)
			return
		}
		prompt := workflowAITestPrompt(body)
		var req struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
			Tools []struct{ Function struct{ Name string } }
		}
		_ = json.Unmarshal(body, &req)
		for _, candidate := range req.Tools {
			if candidate.Function.Name != "research_revision_propose" {
				continue
			}
			for _, message := range req.Messages {
				if message.Role == "tool" {
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Only the report and its existing review/gate will run after confirmation."}, "finish_reason": "stop"}}})
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
					return
				}
			}
			command := workflow.ProposeResearchRevisionCommand{NodeID: "report_drafting", Summary: "Keep the computation; revise only report conclusions", Changes: []string{"Use the raw frozen results, preserve scientific boundaries; prior conversation example was 0.1684"}, Reason: "The executed analysis does not change; revise presentation only"}
			command.EditTargets = reportWrites.Load().([]string)
			if changeScope {
				command.DeliveryScope = scope
			}
			args := mustBootstrapJSON(t, command)
			reportEfficiencyToolReply(w, candidate.Function.Name, string(args), "proposal")
			return
		}
		input := workflowAITestStageInput(prompt)
		if input["_userRevision"] != nil {
			var options []resource.Option
			start := strings.Index(prompt, "<resource_actions>\n")
			if start >= 0 {
				start += len("<resource_actions>\n")
				if end := strings.Index(prompt[start:], "\n</resource_actions>"); end >= 0 {
					var menu struct{ Actions []resource.Option }
					_ = json.Unmarshal([]byte(prompt[start:start+end]), &menu)
					options = append(options, menu.Actions...)
				}
			}
			var lastRead struct {
				Text       string
				Structured struct {
					Kind, Label string
					Truncated   bool
					Actions     []resource.Option
				}
			}
			hasRead := false
			for _, message := range req.Messages {
				if message.Role != "tool" {
					continue
				}
				var text string
				if json.Unmarshal(message.Content, &text) != nil {
					continue
				}
				text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_tool_result>\n"), "\n</untrusted_tool_result>"))
				var envelope struct {
					Text       string
					Structured struct {
						Kind, Label string
						Truncated   bool
						Actions     []resource.Option
					}
				}
				if json.Unmarshal([]byte(text), &envelope) != nil {
					t.Error("model resource snapshot was truncated/invalid")
					continue
				}
				options = append(options, envelope.Structured.Actions...)
				if envelope.Structured.Kind == "workspace_read" && strings.Contains(envelope.Structured.Label, "results.json") {
					lastRead, hasRead = envelope, true
				}
			}
			var next resource.Option
			if hasRead && lastRead.Structured.Truncated {
				if len(lastRead.Structured.Actions) != 1 {
					t.Error("JSON continuation missing")
					return
				}
				next = lastRead.Structured.Actions[0]
			} else if !hasRead {
				for _, option := range options {
					if option.Kind == "workspace_read" && strings.Contains(option.Label, "results.json") {
						next = option
						break
					}
				}
				if next.ID == "" {
					for _, option := range options {
						if option.Kind == "directory" && strings.Contains(option.Label, "analysis-output") {
							next = option
							break
						}
					}
				}
			}
			if next.ID != "" {
				if next.Kind == "workspace_read" {
					revisedResultReads.Add(1)
				}
				reportEfficiencyToolReply(w, next.Tool, string(mustBootstrapJSON(t, map[string]string{"actionId": next.ID})), fmt.Sprintf("result-page-%d", len(req.Messages)))
				return
			}
			if !hasRead {
				t.Error("revised report/review did not see an exact result file")
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if t.Failed() {
			rows, err := app.store.DB().Query(`SELECT ws.node_id,e.error_message FROM workflow_ai_executions e JOIN workflow_steps ws ON ws.id=e.workflow_step_id WHERE e.status='failed'`)
			if err == nil {
				for rows.Next() {
					var node, message string
					_ = rows.Scan(&node, &message)
					t.Log("failed fixture execution", node, message)
				}
				rows.Close()
			}
			rows, err = app.store.DB().Query(`SELECT error_details FROM runs WHERE status='failed'`)
			if err == nil {
				for rows.Next() {
					var detail string
					_ = rows.Scan(&detail)
					t.Log("failed fixture diagnostic", detail)
				}
				rows.Close()
			}
		}
		_ = app.Close()
	}()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "report-only precision fixture"})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := app.PythonFacade.DetectInterpreters()
	if err != nil || len(discovery.Interpreters) == 0 {
		t.Fatal("isolated actual Python needed", err)
	}
	venv := filepath.Join(t.TempDir(), "python")
	if err = pythonruntime.New(app.pythonexec).CreateEnvironment(context.Background(), discovery.Interpreters[0].ExecutablePath, venv); err != nil {
		t.Fatal(err)
	}
	if _, err = app.PythonFacade.BindProjectEnvironment(project.ID, filepath.Join(venv, "Scripts", "python.exe")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(project.WorkspacePath, "input.csv"), []byte("x,y\n1,2\n3,4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	starter, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "Explain a reproducible numerical result without claiming external validation", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, app, project.ID, starter.Run.ID, workflow.RunCompleted, 20*time.Second)
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
	original := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunCompleted, 40*time.Second)
	reportWrites.Store(reportBodyWriteTargetsFromFrozenReview(t, original))
	countPython := func() int {
		var count int
		if err := app.store.DB().QueryRow(`SELECT count(*) FROM python_kernel_executions WHERE workflow_run_id=?`, formal.Run.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	pythonBefore := countPython()
	messageID, _ := id.New()
	request := "Keep the calculation and change only the report conclusions."
	if changeScope {
		request += " Change this delivery to a bounded numerical methods note: report frozen full-precision results and disclose that external validation has not been performed. Do not claim validation completed."
	}
	discussion, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: original.Run.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, Text: request, ClientMessageID: messageID})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		snapshot, err := app.ChatFacade.GetRunSnapshot(discussion.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Run.Status == chat.RunCompleted {
			break
		}
		if snapshot.Run.Status == chat.RunFailed || time.Now().After(deadline) {
			t.Fatal("discussion proposal failed", snapshot.Run)
		}
	}
	pending, err := app.WorkflowFacade.GetRun(project.ID, formal.Run.ID)
	if err != nil || len(pending.RevisionProposals) != 1 || !pending.RevisionProposals[0].CanConfirm || revisedReports.Load() != 0 || revisedReviews.Load() != 0 || countPython() != pythonBefore {
		t.Fatal("proposal implicitly executed", err)
	}
	proposal := pending.RevisionProposals[0]
	if changeScope && (proposal.DeliveryScope == nil || len(proposal.DeliveryScope.PreviousCriteria) == 0 || strings.Contains(string(tabularAuditFixtureStep(t, pending, "report_drafting").Input), `"deliveryScope"`)) {
		t.Fatal("scope not displayed or applied before confirmation")
	}
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	if _, err = app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: project.ID, RunID: formal.Run.ID, ProposalID: proposal.ID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunCompleted, 40*time.Second)
	if !bytes.Equal(original.Run.Inputs, final.Run.Inputs) {
		t.Fatal("original input overwritten")
	}
	if countPython() != pythonBefore || revisedReports.Load() != 1 || revisedReviews.Load() != 1 || revisedResultReads.Load() != 8 || len(final.HumanDecisions) != len(original.HumanDecisions) {
		t.Fatal("report-only revision recalculated or added model/review/confirmation rounds", revisedReports.Load(), revisedReviews.Load(), revisedResultReads.Load())
	}
	for _, prior := range original.Steps {
		current := tabularAuditFixtureStep(t, final, prior.NodeID)
		if prior.Ordinal < tabularAuditFixtureStep(t, original, "report_drafting").Ordinal && !bytes.Equal(mustBootstrapJSON(t, prior), mustBootstrapJSON(t, current)) {
			t.Fatal("unchanged upstream step replayed/mutated", prior.NodeID)
		}
	}
	if err := workflow.ValidateReviewedOutput(final, "report_draft"); err != nil {
		t.Fatal("current review not bound to new exact input", err)
	}
	for _, step := range final.Steps {
		if step.NodeID != "report_drafting" && step.NodeID != "independent_review" {
			continue
		}
		var oldOutput string
		originalStep := tabularAuditFixtureStep(t, original, step.NodeID)
		if err := app.store.DB().QueryRow(`SELECT output_text FROM workflow_ai_executions WHERE workflow_step_id=? AND attempt=?`, originalStep.ID, originalStep.Attempt).Scan(&oldOutput); err != nil || !strings.Contains(oldOutput, "0.1684") && step.NodeID == "report_drafting" {
			t.Fatal("old audited output not preserved", err)
		}
		var reads, failedTools int
		if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls tc JOIN workflow_ai_chat_runs b ON b.chat_run_id=tc.run_id JOIN workflow_ai_executions e ON e.id=b.execution_id WHERE e.workflow_step_id=? AND e.attempt=? AND tc.status<>'completed'`, step.ID, step.Attempt).Scan(&failedTools); err != nil || failedTools != 0 {
			t.Fatal("resource tool error", err, failedTools)
		}
		rows, err := app.store.DB().Query(`SELECT tr.structured_json,tr.model_context_text FROM tool_results tr JOIN tool_calls tc ON tc.id=tr.tool_call_id JOIN workflow_ai_chat_runs b ON b.chat_run_id=tc.run_id JOIN workflow_ai_executions e ON e.id=b.execution_id WHERE e.workflow_step_id=? AND e.attempt=? AND tc.tool_name='builtin.resource.open' ORDER BY tc.rowid`, step.ID, step.Attempt)
		if err != nil {
			t.Fatal(err)
		}
		var recovered strings.Builder
		for rows.Next() {
			var audit, modelText string
			if err := rows.Scan(&audit, &modelText); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			var envelope struct {
				Kind, Label     string
				SourceArguments struct{ Offset, MaxBytes int }
				SourceResult    struct {
					Content   string
					BytesRead int
				}
			}
			_ = json.Unmarshal([]byte(audit), &envelope)
			if envelope.Kind != "workspace_read" || !strings.Contains(envelope.Label, "results.json") {
				continue
			}
			var visible struct{ Text string }
			if json.Unmarshal([]byte(modelText), &visible) != nil || visible.Text != envelope.Label+"\n\n"+envelope.SourceResult.Content || envelope.SourceArguments.Offset != recovered.Len() {
				rows.Close()
				t.Fatal("persisted page model view incomplete or different from audit")
			}
			recovered.WriteString(envelope.SourceResult.Content)
			reads++
		}
		rowErr := rows.Err()
		rows.Close()
		if rowErr != nil {
			t.Fatal(rowErr)
		}
		var rawResult map[string]json.RawMessage
		if reads != 4 || json.Unmarshal([]byte(recovered.String()), &rawResult) != nil || string(rawResult["baseline"]) != "0.6852791878172588" || string(rawResult["model"]) != "0.5169376078243285" {
			t.Fatal("numeric spellings not preserved through actual persisted pages", reads)
		}
	}
	var snapshot string
	if err := app.store.DB().QueryRow(`SELECT previous_snapshot_json FROM research_revision_proposals WHERE id=? AND status='confirmed'`, proposal.ID).Scan(&snapshot); err != nil || !strings.Contains(snapshot, "0.1684") {
		t.Fatal("full historical snapshot lost", err)
	}
	t.Log("controlled model + actual isolated Python + confirmed report-only revision + restart: no additional Kernel work; full current input/review binding; 4 exact persisted JSON reads per stage; historical output preserved")
}

func reportEfficiencyToolReply(w http.ResponseWriter, name, args, callID string) {
	chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": callID, "function": map[string]any{"name": name, "arguments": args}}}}, "finish_reason": "tool_calls"}}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
}

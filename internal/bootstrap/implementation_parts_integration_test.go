package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real adapters and immutable SQLite events; two explicit cancellations and
// restarts must retain the prefix even when no new part completed in attempt 2.
// The remote model is a local fixture, not a commercial model quality test.
func TestImplementationPartsSurviveRepeatedCancellationAndRestart(t *testing.T) {
	testImplementationPartsRepeatedRestart(t, false)
}

func TestImplementationPartsResumeKeepsReadOnceEvidence(t *testing.T) {
	testImplementationPartsRepeatedRestart(t, true)
}

func testImplementationPartsRepeatedRestart(t *testing.T, withEvidence bool) {
	t.Helper()
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			opened := make(chan struct{}, 2)
			ready := make(chan struct{})
			var requests atomic.Int32
			var firstPrefix string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				n := int(requests.Add(1))
				if withEvidence && n == 1 {
					<-ready
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "workspace_read_text", `{"path":"method.txt"}`, 1000, 0)
					return
				}
				if withEvidence {
					n--
					if !strings.Contains(string(body), "READ-ONCE-SCIENTIFIC-CONTRACT-SENTINEL") {
						t.Error("completed evidence missing before/after explicit recovery")
					}
				}
				canonical := contextPipelineCanonicalRequest(t, body)
				if !strings.Contains(string(canonical), "submit_implementation_part") {
					t.Error("control function missing")
				}
				if n == 1 {
					firstPrefix = "import json\nfrom pathlib import Path\nsource = SCIAIDE_INPUTS[0]\n"
					args, _ := json.Marshal(map[string]any{"index": 1, "metadata": map[string]any{"methodSummary": "fixture preserves source", "dependencies": []string{}, "analysisInput": map[string]any{}, "researchChanges": []any{}}, "code": firstPrefix, "complete": false})
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "submit_implementation_part", string(args), 1000, 128)
					return
				}
				if !strings.Contains(string(canonical), `\"enum\":[2]`) && !strings.Contains(string(canonical), `"enum":[2]`) {
					t.Error("did not resume part 2")
				}
				if strings.Contains(string(canonical), "submit_stage_result") || strings.Contains(string(body), "function_call_output") || strings.Contains(string(body), "tool_result") {
					t.Error("orphaned control/tool history in source exchange")
				}
				if !strings.Contains(string(body), "source = SCIAIDE_INPUTS[0]") || !strings.Contains(string(body), "NOT yet an accepted") {
					t.Error("lost saved source/evidence boundary")
				}
				if n == 2 || n == 3 {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					opened <- struct{}{}
					<-r.Context().Done()
					return
				}
				if n != 4 {
					t.Errorf("unexpected request %d", n)
				}
				args, _ := json.Marshal(map[string]any{"index": 2, "code": "result = {'ok': True}\nPath(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding='utf-8')\nPath(SCIAIDE_OUTPUTS[1]).write_text('# Fixture methods', encoding='utf-8')\nresult", "complete": true})
				emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "submit_implementation_part", string(args), 1000, 0)
			}))
			defer server.Close()
			root := t.TempDir()
			app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "parts fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}}, TimeoutSeconds: 5, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "source checkpoint fixture"})
			if err != nil {
				t.Fatal(err)
			}
			schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["code","methodSummary","dependencies","analysisInput","researchChanges"],"properties":{"code":{"type":"string","minLength":1,"maxLength":120000},"methodSummary":{"type":"string"},"dependencies":{"type":"array","items":{"type":"string"}},"analysisInput":{"type":"object"},"researchChanges":{"type":"array"}}}`)
			var allowed []string
			if withEvidence {
				allowed = []string{"builtin.workspace.read_text"}
			}
			saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "completed source parts", Nodes: []workflow.Node{{ID: "method_implementation", Name: "method implementation", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), AllowedTools: allowed, PromptVersion: "dynamic-implementation-v6", ReviewPolicy: workflow.AIReviewAuto, Prompt: "Preserve all frozen scientific checks. Return the fixture Python implementation.", OutputSchema: schema}}, Outputs: []workflow.Output{{Name: "implementation", Type: workflow.TypeObject, FromNode: "method_implementation", FromPort: "analysis", Required: true}}}})
			if err != nil {
				t.Fatal(err)
			}
			started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
			if err != nil {
				t.Fatal(err)
			}
			if withEvidence {
				taskRoot, err := project.ResearchTaskWorkspacePath(p, started.Run.ResearchTaskID)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(taskRoot, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(taskRoot, "method.txt"), []byte("READ-ONCE-SCIENTIFIC-CONTRACT-SENTINEL: preserve paired subject IDs and the frozen test"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			close(ready)
			oldRuns := []string{}
			var before json.RawMessage
			for attempt := 1; attempt <= 2; attempt++ {
				select {
				case <-opened:
				case <-time.After(15 * time.Second):
					d, _ := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
					t.Fatalf("continuation not opened: %d %+v", requests.Load(), d)
				}
				active, err := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				chatRun := active.AIExecutions[len(active.AIExecutions)-1].ChatRunID
				repo := sqlite.NewRunRepository(app.store.DB())
				draft, ok, err := repo.ImplementationDraft(context.Background(), chatRun)
				if err != nil || !ok || draft.Code != firstPrefix || draft.PartCount != 1 || draft.Complete || draft.AdoptionPending {
					t.Fatal("prefix not durable", draft, ok, err)
				}
				if attempt == 1 {
					before, _ = json.Marshal(draft)
				}
				oldRuns = append(oldRuns, chatRun)
				if _, err = app.WorkflowFacade.Cancel(p.ID, started.Run.ID); err != nil {
					t.Fatal(err)
				}
				cancelled := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCancelled, 10*time.Second)
				if err = app.Close(); err != nil {
					t.Fatal(err)
				}
				app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
				if err != nil {
					t.Fatal(err)
				}
				app.Startup(context.Background())
				if _, err = app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: cancelled.Steps[0].ID, Note: "resume saved source parts"}); err != nil {
					t.Fatal(err)
				}
			}
			final := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCompleted, 15*time.Second)
			expectedRequests := int32(4)
			if withEvidence {
				expectedRequests++
			}
			if requests.Load() != expectedRequests || len(final.AIExecutions) != 3 || final.AIExecutions[0].Status != "cancelled" || final.AIExecutions[1].Status != "cancelled" {
				t.Fatal("wrong recovery attempts", requests.Load(), final)
			}
			repo := sqlite.NewRunRepository(app.store.DB())
			old, ok, err := repo.ImplementationDraft(context.Background(), oldRuns[0])
			after, _ := json.Marshal(old)
			if err != nil || !ok || string(before) != string(after) {
				t.Fatal("old checkpoint was rewritten", err)
			}
			last := final.AIExecutions[2]
			draft, ok, err := repo.ImplementationDraft(context.Background(), last.ChatRunID)
			if err != nil || !ok || !draft.Complete || draft.PartCount != 2 || !strings.HasPrefix(draft.Code, firstPrefix) {
				t.Fatal(draft, ok, err)
			}
			var outputs, usage, anchors int
			if err = app.store.DB().QueryRow(`SELECT count(*) FROM model_request_usage WHERE run_id=? AND status_code=200`, last.ChatRunID).Scan(&usage); err != nil {
				t.Fatal(err)
			}
			if err = app.store.DB().QueryRow(`SELECT count(*) FROM run_events WHERE aggregate_id=? AND event_type='run.context_usage'`, last.ChatRunID).Scan(&anchors); err != nil {
				t.Fatal(err)
			}
			if err = app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id=?`, last.ChatRunID).Scan(&outputs); err != nil {
				t.Fatal(err)
			}
			if usage != 1 || anchors != 0 || outputs != 0 {
				t.Fatal("assembly created phantom usage or executed controls", usage, anchors, outputs)
			}
			if withEvidence {
				if len(draft.SourceEvidence) != 1 || !strings.Contains(draft.SourceEvidence[0].Result, "READ-ONCE-SCIENTIFIC-CONTRACT-SENTINEL") {
					t.Fatal("checkpoint lost actual read evidence", draft.SourceEvidence)
				}
				var count int
				if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id IN (?,?,?)`, oldRuns[0], oldRuns[1], last.ChatRunID).Scan(&count); err != nil || count != 1 {
					t.Fatal("read was repeated during recovery", count, err)
				}
			}
			// Frozen source events reject late writes after terminal completion.
			draft.PartCount++
			draft.Code += "\nprint('late')"
			draft.SourceTurnIndex = last.ModelTurns
			if err := repo.SaveImplementationDraft(context.Background(), draft); err == nil {
				t.Fatal("accepted late source checkpoint")
			}
			var result struct {
				Analysis struct {
					Code string `json:"code"`
				} `json:"analysis"`
			}
			if json.Unmarshal(final.Steps[0].Output, &result) != nil || result.Analysis.Code == "" || !strings.Contains(result.Analysis.Code, "Path(SCIAIDE_OUTPUTS[1])") {
				t.Fatal("assembled full stage output missing", string(final.Steps[0].Output))
			}
		})
	}
}

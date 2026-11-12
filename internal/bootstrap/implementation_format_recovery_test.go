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

func TestImplementationFormatCorrectionKeepsEvidenceIndexAndAllowanceAfterRestartAllProtocols(t *testing.T) {
	testImplementationFormatCorrectionRestart(t, false)
}

func TestImplementationFullValidationRejectionThenFormatCorrectionKeepsBothBudgetsAndEvidence(t *testing.T) {
	testImplementationFormatCorrectionRestart(t, true)
}

func testImplementationFormatCorrectionRestart(t *testing.T, rejectComplete bool) {
	t.Helper()
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			ready := make(chan struct{})
			opened := make(chan struct{}, 1)
			var requests atomic.Int32
			const prefix = "import json\nfrom pathlib import Path\nsource = SCIAIDE_INPUTS[0]\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				n := int(requests.Add(1))
				if n == 1 {
					<-ready
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "workspace_read_text", `{"path":"method.txt"}`, 1000, 0)
					return
				}
				if !strings.Contains(string(body), "READ-ONCE-RECOVERY-MATERIAL") {
					t.Error("materials disappeared from control correction/restart")
				}
				canonical := string(contextPipelineCanonicalRequest(t, body))
				var args map[string]any
				controlStep := n
				if rejectComplete {
					if n == 2 {
						metadata := map[string]any{"methodSummary": "fixture preserves method", "dependencies": []string{}, "analysisInput": map[string]any{}, "researchChanges": []any{}}
						raw, _ := json.Marshal(map[string]any{"index": 1, "metadata": metadata, "code": "result = 1\nresult", "complete": true})
						emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "submit_implementation_part", string(raw), 1000, 0)
						return
					}
					controlStep--
					if !strings.Contains(canonical, "unchanged full stage validator") || !strings.Contains(canonical, "$.code") {
						t.Error("full-result rejection diagnostic disappeared during format recovery")
					}
				}
				switch controlStep {
				case 2, 3:
					metadata := map[string]any{"methodSummary": "fixture preserves method", "dependencies": []string{}, "analysisInput": map[string]any{}, "researchChanges": []any{}}
					args = map[string]any{"index": 1, "metadata": metadata, "code": prefix, "complete": false}
					if controlStep == 2 {
						delete(metadata, "dependencies")
					}
					if controlStep == 3 && !strings.Contains(canonical, "$.metadata.dependencies") {
						t.Error("missing-field diagnostic not delivered")
					}
				case 4:
					args = map[string]any{"index": 99, "code": "# REJECTED-WRONG-INDEX\n", "complete": false}
				case 5:
					if !strings.Contains(canonical, "index is STILL 2") || !strings.Contains(canonical, "$.index") {
						t.Error("same part was not retried")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					opened <- struct{}{}
					<-r.Context().Done()
					return
				case 6:
					if !strings.Contains(canonical, "index is STILL 2") || !strings.Contains(canonical, "correction 1/2") || !strings.Contains(string(body), "import json") {
						t.Error("restart lost format budget/diagnostic/prefix")
					}
					args = map[string]any{"index": 2, "code": "result = {'ok': True}\nPath(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding='utf-8')\nPath(SCIAIDE_OUTPUTS[1]).write_text('# Methods', encoding='utf-8')\nresult", "complete": true}
				default:
					t.Errorf("unexpected request %d", n)
					http.Error(w, "unexpected", 400)
					return
				}
				raw, _ := json.Marshal(args)
				emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "submit_implementation_part", string(raw), 1000, 0)
			}))
			defer server.Close()
			root := t.TempDir()
			app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "format recovery fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}}, TimeoutSeconds: 5, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "format recovery"})
			if err != nil {
				t.Fatal(err)
			}
			schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["code","methodSummary","dependencies","analysisInput","researchChanges"],"properties":{"code":{"type":"string","minLength":1,"maxLength":120000},"methodSummary":{"type":"string"},"dependencies":{"type":"array","items":{"type":"string"}},"analysisInput":{"type":"object"},"researchChanges":{"type":"array"}}}`)
			expectedCorrections, expectedRequests := 0, int32(6)
			if rejectComplete {
				// Only this synthetic fixture's frozen contract rejects the short
				// first complete candidate. No production code-size gate is added.
				schema = json.RawMessage(strings.Replace(string(schema), `"minLength":1`, `"minLength":100`, 1))
				expectedCorrections, expectedRequests = 1, 7
			}
			saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "implementation format recovery", Nodes: []workflow.Node{{ID: "method_implementation", Name: "method implementation", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), AllowedTools: []string{"builtin.workspace.read_text"}, PromptVersion: "dynamic-implementation-v6", ReviewPolicy: workflow.AIReviewAuto, Prompt: "Read method.txt once, preserve the frozen method and generate Python.", OutputSchema: schema}}, Outputs: []workflow.Output{{Name: "implementation", Type: workflow.TypeObject, FromNode: "method_implementation", FromPort: "analysis", Required: true}}}})
			if err != nil {
				t.Fatal(err)
			}
			started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
			if err != nil {
				t.Fatal(err)
			}
			taskRoot, err := project.ResearchTaskWorkspacePath(p, started.Run.ResearchTaskID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(taskRoot, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(taskRoot, "method.txt"), []byte("READ-ONCE-RECOVERY-MATERIAL"), 0600); err != nil {
				t.Fatal(err)
			}
			close(ready)
			select {
			case <-opened:
			case <-time.After(20 * time.Second):
				d, _ := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
				t.Fatalf("correction request not opened: requests=%d status=%s %s %s", requests.Load(), d.Run.Status, d.Run.ErrorCode, d.Run.ErrorMessage)
			}
			active, err := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			oldRun := active.AIExecutions[len(active.AIExecutions)-1].ChatRunID
			draft, found, err := sqlite.NewRunRepository(app.store.DB()).ImplementationDraft(context.Background(), oldRun)
			if err != nil || !found || draft.PartCount != 1 || draft.Code != prefix || draft.PartCorrectionCount != 1 || draft.CorrectionCount != expectedCorrections {
				t.Fatal("format checkpoint lost", draft, found, err)
			}
			if _, err := app.WorkflowFacade.Cancel(p.ID, started.Run.ID); err != nil {
				t.Fatal(err)
			}
			cancelled := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCancelled, 10*time.Second)
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: cancelled.Steps[0].ID, Note: "resume unsaved part correction"}); err != nil {
				t.Fatal(err)
			}
			final := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
			if requests.Load() != expectedRequests || len(final.AIExecutions) != 2 {
				t.Fatal(requests.Load(), len(final.AIExecutions))
			}
			newRun := final.AIExecutions[1].ChatRunID
			draft, found, err = sqlite.NewRunRepository(app.store.DB()).ImplementationDraft(context.Background(), newRun)
			if err != nil || !found || !draft.Complete || draft.PartCount != 2 || draft.PartCorrectionCount != 0 || draft.CorrectionCount != expectedCorrections || !strings.HasPrefix(draft.Code, prefix) || strings.Contains(draft.Code, "REJECTED") {
				t.Fatal("invalid/repeated prefix at completion", draft, found, err)
			}
			var reads int
			if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id IN (?,?) AND tool_name='builtin.workspace.read_text'`, oldRun, newRun).Scan(&reads); err != nil || reads != 1 {
				t.Fatal("completed reads were rerun", reads, err)
			}
		})
	}
}

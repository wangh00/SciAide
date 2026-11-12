package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Actual adapters, tool executor, frozen stage validation and temporary SQLite.
// The HTTP model is scripted, never a paid service or the user's formal task.
func TestReadConvergenceHTTPAndSQLiteAllProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []string{"repeat", "new_pages", "exhausted_then_retry"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				var requests, evidenceCount atomic.Int32
				var retrying atomic.Bool
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					n := int(requests.Add(1))
					local := n
					if retrying.Load() {
						local -= 8
					}
					var request struct {
						Tools      []json.RawMessage `json:"tools"`
						ToolChoice json.RawMessage   `json:"tool_choice"`
					}
					if json.Unmarshal(body, &request) != nil {
						t.Error("invalid request")
						return
					}
					readTurns := 5
					if mode == "new_pages" {
						readTurns = 7
					}
					if local <= readTurns {
						if !stageToolSelectionOptionalOnWire(protocol, body) {
							t.Error("new material or cold-start read was cut short")
						}
						offset := 0
						if mode == "new_pages" {
							offset = local - 1
						}
						emitQuestionHandoffFixture(t, w, protocol, n, "材料已齐，现提交完整稿。", "research_task_read", fmt.Sprintf(`{"section":"stage_input","offset":%d}`, offset))
						return
					}
					if mode == "new_pages" {
						if !stageToolSelectionOptionalOnWire(protocol, body) {
							t.Error("one-character new visible range was mistaken for stagnation")
						}
						emitQuestionHandoffFixture(t, w, protocol, n, `{"summary":"verified new-page fixture"}`, "", "")
						return
					}
					if !singleStageToolForcedOnWire(protocol, body) {
						t.Error("repeated reads did not enter the single submission control")
					}
					// Check the REAL wire after ProjectToolReferences: source tool
					// names inside restored fragments must not become API aliases.
					var wire any
					_ = json.Unmarshal(body, &wire)
					found := 0
					var visit func(any)
					visit = func(v any) {
						switch x := v.(type) {
						case string:
							prefix := "Previously exposed frozen input fragments;"
							if at := strings.Index(x, prefix); at >= 0 {
								found++
								_, raw, _ := strings.Cut(x[at:], "\n")
								var fragments []struct {
									Offset, EndOffset int
									Content, SHA256   string
								}
								if json.NewDecoder(strings.NewReader(raw)).Decode(&fragments) != nil || len(fragments) == 0 {
									t.Error("invalid retained fragments")
									return
								}
								text := ""
								for _, f := range fragments {
									text += f.Content
									if f.EndOffset-f.Offset != len([]rune(f.Content)) || len(f.SHA256) != 64 {
										t.Error("restored source range invalid")
									}
								}
								if !strings.Contains(text, "builtin.research.task.read") {
									t.Error("restored research data was aliased or lost on the wire")
								}
							}
						case []any:
							for _, child := range x {
								visit(child)
							}
						case map[string]any:
							for _, child := range x {
								visit(child)
							}
						}
					}
					visit(wire)
					if found != 1 {
						t.Errorf("retained evidence count=%d, want 1", found)
					}
					evidenceCount.Add(int32(found))
					answer := `{"summary":"verified controlled fixture"}`
					if mode == "exhausted_then_retry" && !retrying.Load() {
						answer = `{"summary":{}}`
					}
					emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_result", answer)
				}))
				defer server.Close()
				options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
				app, err := New(options)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = app.Close() }()
				app.Startup(context.Background())
				p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "read convergence fixture"})
				if err != nil {
					t.Fatal(err)
				}
				profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "local fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 272000, AutoCompactTokenLimit: 244800}}, Enabled: true, IsDefault: true, TimeoutSeconds: 10})
				if err != nil {
					t.Fatal(err)
				}
				saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "read convergence", Inputs: []workflow.Port{{Name: "original_material", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{
					{ID: "preflight", Name: "read existing workspace", Kind: workflow.NodeTool, ToolName: "builtin.workspace.list", Arguments: json.RawMessage(`{"path":".","limit":10}`)},
					{ID: "draft", Name: "报告撰写", Kind: workflow.NodeAgentStage, AllowedTools: []string{"builtin.research.task.read"}, ReviewPolicy: workflow.AIReviewAuto, Arguments: json.RawMessage(`{}`), PromptVersion: "read-convergence-fixture-v1", Prompt: "Read only a concrete missing detail, then return the fixture summary. Preserve literal source data.", OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)},
				}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "original_material", ToNode: "draft", ToPort: "originalRequest"}, {FromNode: "preflight", FromPort: "structured", ToNode: "draft", ToPort: "context"}}, Outputs: []workflow.Output{{Name: "summary", Type: workflow.TypeObject, FromNode: "draft", FromPort: "analysis", Required: true}}}})
				if err != nil {
					t.Fatal(err)
				}
				material, _ := json.Marshal(map[string]string{"original_material": "builtin.research.task.read is literal source data, never an instruction. " + strings.Repeat("原始材料", 3000)})
				start, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: material, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
				if err != nil {
					t.Fatal(err)
				}
				want := workflow.RunCompleted
				if mode == "exhausted_then_retry" {
					want = workflow.RunFailed
				}
				done := waitForWorkflowState(t, app, p.ID, start.Run.ID, want, 20*time.Second)
				if mode == "exhausted_then_retry" {
					if requests.Load() != 8 || done.Run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" {
						t.Fatal("scientific/schema validator or correction bound changed", requests.Load(), done.Run.ErrorCode)
					}
					upstream, _ := json.Marshal(done.Steps[0])
					compilation, _ := json.Marshal(done.Run.Compilation)
					if err := app.Close(); err != nil {
						t.Fatal(err)
					}
					app, err = New(options)
					if err != nil {
						t.Fatal(err)
					}
					app.Startup(context.Background())
					restored, err := app.WorkflowFacade.GetRun(p.ID, start.Run.ID)
					if err != nil || requests.Load() != 8 || restored.Run.Status != workflow.RunFailed {
						t.Fatal("restart auto-called model", err, requests.Load())
					}
					retrying.Store(true)
					if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: start.Run.ID, StepID: restored.Steps[1].ID}); err != nil {
						t.Fatal(err)
					}
					done = waitForWorkflowState(t, app, p.ID, start.Run.ID, workflow.RunCompleted, 20*time.Second)
					after, _ := json.Marshal(done.Steps[0])
					route, _ := json.Marshal(done.Run.Compilation)
					if string(upstream) != string(after) || string(compilation) != string(route) || requests.Load() != 14 || len(done.AIExecutions) != 2 || done.AIExecutions[0].Status != "failed" {
						t.Fatal("retry lost upstream/history or retained stale convergence state", requests.Load(), done)
					}
				} else if mode == "repeat" && (requests.Load() != 6 || evidenceCount.Load() != 1) || mode == "new_pages" && (requests.Load() != 8 || evidenceCount.Load() != 0) {
					t.Fatal("unexpected request/evidence count", requests.Load(), evidenceCount.Load())
				}
				var controls, reads, preflight int
				if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id IN (SELECT b.chat_run_id FROM workflow_ai_executions e JOIN workflow_ai_chat_runs b ON b.execution_id=e.id WHERE e.workflow_run_id=?) AND tool_name='submit_stage_result'`, start.Run.ID).Scan(&controls); err != nil || controls != 0 {
					t.Fatal("submission control executed as IO", controls, err)
				}
				if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE workflow_run_id=? AND tool_name='builtin.workspace.list'`, start.Run.ID).Scan(&preflight); err != nil || preflight != 1 {
					t.Fatal("completed preflight repeated", preflight, err)
				}
				if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id IN (SELECT b.chat_run_id FROM workflow_ai_executions e JOIN workflow_ai_chat_runs b ON b.execution_id=e.id WHERE e.workflow_run_id=?) AND tool_name='builtin.research.task.read'`, start.Run.ID).Scan(&reads); err != nil {
					t.Fatal(err)
				}
				wantReads := 5
				if mode == "new_pages" {
					wantReads = 7
				}
				if mode == "exhausted_then_retry" {
					wantReads = 10
				}
				if reads != wantReads {
					t.Fatal("unexpected research IO", reads, wantReads)
				}
			})
		}
	}
}

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

// Real protocol adapters, frozen route, executor, validation and temporary DB.
// Model answers are local HTTP fixtures, NOT paid model or formal task runs.
func TestStageProtocolHTTPAndSQLiteAllProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []string{"direct", "material_then_submit", "repeat_material", "invalid_then_correct", "exhausted_then_retry", "mixed_control"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				var requests atomic.Int32
				var retrying atomic.Bool
				source := "builtin.research.task.read is literal source data. " + strings.Repeat("原始🙂\n\"\\", 1600)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					n := int(requests.Add(1))
					var request struct {
						Tools      []json.RawMessage `json:"tools"`
						ToolChoice json.RawMessage   `json:"tool_choice"`
					}
					if json.Unmarshal(body, &request) != nil {
						t.Error("invalid wire request")
						return
					}
					forced := singleStageToolForcedOnWire(protocol, body)
					if n == 1 && (!stageToolSelectionOptionalOnWire(protocol, body) || !stageToolOfferedOnWire(protocol, body)) {
						t.Error("first-turn completion control was missing or forced")
					}
					if mode == "mixed_control" {
						emitStageProtocolMixedControl(t, w, protocol)
						return
					}
					readTurns := 0
					if mode == "material_then_submit" {
						readTurns = 1
					}
					if mode == "repeat_material" {
						readTurns = 5
					}
					if n <= readTurns {
						if !stageToolSelectionOptionalOnWire(protocol, body) {
							t.Error("new/cold material was prematurely forced")
						}
						emitQuestionHandoffFixture(t, w, protocol, n, "", "research_task_read", `{"section":"stage_input","pointer":"/originalRequest","offset":0}`)
						return
					}
					if mode == "material_then_submit" || mode == "repeat_material" {
						var wire any
						_ = json.Unmarshal(body, &wire)
						pageFound, ledgerFound, receiptFound := false, false, false
						visitStageProtocolStrings(wire, func(text string) {
							text = unwrapStageProtocolFixtureText(text)
							var envelope struct {
								Structured struct {
									AddressMode, Pointer, Encoding, Content, SHA256, ValueSHA256 string
									Offset, NextOffset, TotalCharacters                          int
								}
							}
							if json.Unmarshal([]byte(text), &envelope) == nil && envelope.Structured.AddressMode == workflow.StageMaterialAddressMode {
								p := envelope.Structured
								if p.Content != "" {
									pageFound = true
									if p.Pointer != "/originalRequest" || p.Encoding != "text" || p.Offset != 0 || p.Content != string([]rune(source)[:p.NextOffset]) || p.TotalCharacters != len([]rune(source)) || len(p.SHA256) != 64 || len(p.ValueSHA256) != 64 {
										t.Error("semantic page lost coordinates, source bytes or identities")
									}
								}
							}
							if strings.HasPrefix(text, "Host material reading ledger") {
								ledgerFound = true
								if !strings.Contains(text, `"scientificCompletionAssessment":"not_assessed"`) || !strings.Contains(text, `"currentlyVisibleRanges"`) {
									t.Error("wire ledger misstated reading/science status")
								}
							}
							if at := strings.Index(text, "Previously exposed addressed material fragments;"); at >= 0 {
								receiptFound = true
								_, fragments, _ := strings.Cut(text[at:], "\n")
								var data []struct {
									Pointer, Content, InputSHA256, ValueSHA256 string
									Offset, EndOffset                          int
								}
								if json.NewDecoder(strings.NewReader(fragments)).Decode(&data) != nil || len(data) != 1 {
									t.Error("invalid semantic retained receipt")
									return
								}
								f := data[0]
								if f.Pointer != "/originalRequest" || f.Content != string([]rune(source)[f.Offset:f.EndOffset]) || !strings.Contains(f.Content, "builtin.research.task.read") || len(f.InputSHA256) != 64 || len(f.ValueSHA256) != 64 {
									t.Error("receipt source was altered or aliased on wire")
								}
							}
						})
						if mode == "material_then_submit" && (!pageFound || !ledgerFound || !stageToolSelectionOptionalOnWire(protocol, body)) || mode == "repeat_material" && (!forced || !receiptFound || len(request.Tools) != 1) {
							t.Errorf("invalid read-to-submit wire: page=%v ledger=%v receipt=%v forced=%v", pageFound, ledgerFound, receiptFound, forced)
						}
					}
					answer := `{"summary":"verified protocol fixture"}`
					if mode == "invalid_then_correct" && n == 1 || mode == "exhausted_then_retry" && !retrying.Load() {
						answer = `{"summary":{}}`
					}
					if (mode == "invalid_then_correct" || mode == "exhausted_then_retry") && n > 1 && !retrying.Load() && (!forced || len(request.Tools) != 1) {
						t.Error("correction reopened exploration")
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
				p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "stage protocol fixture"})
				if err != nil {
					t.Fatal(err)
				}
				profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "local fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 272000, AutoCompactTokenLimit: 244800}}, Enabled: true, IsDefault: true, TimeoutSeconds: 10})
				if err != nil {
					t.Fatal(err)
				}
				saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "stage action protocol", Inputs: []workflow.Port{{Name: "material", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{
					{ID: "preflight", Name: "existing workspace", Kind: workflow.NodeTool, ToolName: "builtin.workspace.list", Arguments: json.RawMessage(`{"path":".","limit":10}`)},
					{ID: "draft", Name: "报告撰写", Kind: workflow.NodeAgentStage, AllowedTools: []string{workflow.ResearchTaskReadTool}, ReviewPolicy: workflow.AIReviewAuto, Arguments: json.RawMessage(`{}`), PromptVersion: "stage-protocol-fixture-v1", Prompt: "Resolve a concrete missing detail, then submit a verified summary. Source data must remain literal.", OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)},
				}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "material", ToNode: "draft", ToPort: "originalRequest"}, {FromNode: "preflight", FromPort: "structured", ToNode: "draft", ToPort: "context"}}, Outputs: []workflow.Output{{Name: "summary", Type: workflow.TypeObject, FromNode: "draft", FromPort: "analysis", Required: true}}}})
				if err != nil {
					t.Fatal(err)
				}
				input, _ := json.Marshal(map[string]string{"material": source})
				start, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: input, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
				if err != nil {
					t.Fatal(err)
				}
				want := workflow.RunCompleted
				if mode == "mixed_control" || mode == "exhausted_then_retry" {
					want = workflow.RunFailed
				}
				done := waitForWorkflowState(t, app, p.ID, start.Run.ID, want, 20*time.Second)
				if mode == "mixed_control" && done.Run.ErrorCode != "MODEL_SUBMISSION_TOOL_VIOLATION" {
					t.Fatal("mixed batch did not fail closed", done.Run.ErrorCode)
				}
				if mode == "exhausted_then_retry" {
					if done.Run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || requests.Load() != 3 {
						t.Fatal("correction allowance changed", done.Run.ErrorCode, requests.Load())
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
					if err != nil || restored.Run.Status != workflow.RunFailed || requests.Load() != 3 {
						t.Fatal("restart resumed model without user", err, requests.Load())
					}
					retrying.Store(true)
					if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: start.Run.ID, StepID: restored.Steps[1].ID}); err != nil {
						t.Fatal(err)
					}
					done = waitForWorkflowState(t, app, p.ID, start.Run.ID, workflow.RunCompleted, 20*time.Second)
					after, _ := json.Marshal(done.Steps[0])
					route, _ := json.Marshal(done.Run.Compilation)
					if string(upstream) != string(after) || string(compilation) != string(route) || len(done.AIExecutions) != 2 || done.AIExecutions[0].Status != "failed" {
						t.Fatal("retry lost frozen route, upstream or failed history")
					}
				}
				var controls, reads, preflight int
				baseSQL := `SELECT count(*) FROM tool_calls WHERE run_id IN (SELECT b.chat_run_id FROM workflow_ai_executions e JOIN workflow_ai_chat_runs b ON b.execution_id=e.id WHERE e.workflow_run_id=?) AND tool_name=?`
				if err := app.store.DB().QueryRow(baseSQL, start.Run.ID, "submit_stage_result").Scan(&controls); err != nil || controls != 0 {
					t.Fatal("completion executed as IO", controls, err)
				}
				if err := app.store.DB().QueryRow(baseSQL, start.Run.ID, workflow.ResearchTaskReadTool).Scan(&reads); err != nil {
					t.Fatal(err)
				}
				if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE workflow_run_id=? AND tool_name='builtin.workspace.list'`, start.Run.ID).Scan(&preflight); err != nil || preflight != 1 {
					t.Fatal("completed stage was rerun", preflight, err)
				}
				wantRequests, wantReads := 1, 0
				switch mode {
				case "material_then_submit":
					wantRequests, wantReads = 2, 1
				case "repeat_material":
					wantRequests, wantReads = 6, 5
				case "invalid_then_correct":
					wantRequests = 2
				case "exhausted_then_retry":
					wantRequests = 4
				}
				if int(requests.Load()) != wantRequests || reads != wantReads {
					t.Fatal("unexpected paid-shaped requests or IO", requests.Load(), reads, wantRequests, wantReads)
				}
			})
		}
	}
}

func visitStageProtocolStrings(v any, visit func(string)) {
	switch x := v.(type) {
	case string:
		visit(x)
	case []any:
		for _, child := range x {
			visitStageProtocolStrings(child, visit)
		}
	case map[string]any:
		for _, child := range x {
			visitStageProtocolStrings(child, visit)
		}
	}
}

func unwrapStageProtocolFixtureText(text string) string {
	for _, tag := range []string{"untrusted_tool_result", "untrusted_conversation_content"} {
		start, end := "<"+tag+">\n", "\n</"+tag+">"
		if strings.HasPrefix(text, start) && strings.HasSuffix(text, end) {
			return strings.TrimSuffix(strings.TrimPrefix(text, start), end)
		}
	}
	return text
}

func emitStageProtocolMixedControl(t *testing.T, w http.ResponseWriter, protocol modelprofile.APIProtocol) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(value any) { data, _ := json.Marshal(value); fmt.Fprintf(w, "data: %s\n\n", data) }
	names := []string{"submit_stage_result", "research_task_read"}
	args := []string{`{"summary":"must not run"}`, `{"section":"stage_input","pointer":"/originalRequest","offset":0}`}
	switch protocol {
	case modelprofile.ProtocolOpenAIChat:
		calls := []any{}
		for i := range names {
			calls = append(calls, map[string]any{"index": i, "id": fmt.Sprint("mixed", i), "function": map[string]any{"name": names[i], "arguments": args[i]}})
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
		io.WriteString(w, "data: [DONE]\n\n")
	case modelprofile.ProtocolOpenAIResponses:
		for i := range names {
			send(map[string]any{"type": "response.output_item.done", "output_index": i, "item": map[string]any{"type": "function_call", "id": fmt.Sprint("item", i), "call_id": fmt.Sprint("mixed", i), "name": names[i], "arguments": args[i]}})
		}
		send(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
	case modelprofile.ProtocolAnthropic:
		send(map[string]any{"type": "message_start", "message": map[string]any{}})
		for i := range names {
			var input any
			_ = json.Unmarshal([]byte(args[i]), &input)
			send(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprint("mixed", i), "name": names[i], "input": input}})
			send(map[string]any{"type": "content_block_stop", "index": i})
		}
		send(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}})
		send(map[string]any{"type": "message_stop"})
	}
}

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
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real HTTP adapters, issued resource tools, SQLite, correction and explicit
// restart/retry. Only the model is synthetic, with deliberately missing usage
// so first-turn admission cannot borrow a previous successful token report.
func TestFrozenInputAboveCompactThresholdHTTPRestartRetry(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			input := "FIRST-FROZEN-LIMIT\n" + strings.Repeat("已确认科研要求与原文必须完整保留。", 550) + "\nLAST-FROZEN-LIMIT"
			var requests atomic.Int32
			var frozenPrompt string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				n := int(requests.Add(1))
				canonical := contextPipelineCanonicalRequest(t, body)
				prompt := workflowAITestPrompt(canonical)
				stage := workflowAITestStageInput(prompt)
				if stage["originalRequest"] != input {
					t.Error("wire input silently truncated")
				}
				if n == 1 {
					frozenPrompt = prompt
				}
				switch n {
				case 1, 3:
					name, args, enabled, err := resourceFixtureSelection(canonical, "scientific-writing", true)
					if err != nil || !enabled || name == "" {
						t.Errorf("issued resource missing: %v", err)
						http.Error(w, "missing issued resource", 400)
						return
					}
					emitThresholdFixture(t, w, protocol, n, name, args)
				case 2:
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(400)
					io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"invalid_request_error","message":"fixture permanent error after completed read"}}`)
				case 4:
					// Progress-only answer requires the original dedicated submission.
					emitThresholdFixture(t, w, protocol, n, "", "")
				case 5, 6:
					if !strings.Contains(string(body), "dedicated stage submission interface") || !strings.Contains(string(body), "submit_stage_result") {
						t.Error("dedicated output contract missing")
					}
					args := `{}`
					if n == 6 {
						args = `{"summary":"verified current input and saved resource"}`
						if !strings.Contains(prompt, "Host submission validation failed") {
							t.Error("same-run correction lost validation diagnostic")
						}
					}
					emitThresholdFixture(t, w, protocol, n, "submit_stage_result", args)
				default:
					t.Errorf("unexpected model or compaction request %d", n)
					http.Error(w, "extra request", 400)
				}
			}))
			defer server.Close()
			root := t.TempDir()
			app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "soft threshold fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 100000, AutoCompactTokenLimit: 5000}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "context threshold pipeline"})
			if err != nil {
				t.Fatal(err)
			}
			saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{
				SchemaVersion: 1, Name: "Frozen input above automatic threshold",
				Inputs:  []workflow.Port{{Name: "target", Type: workflow.TypeString, Required: true}},
				Nodes:   []workflow.Node{{ID: "analyze", Name: "Analyze frozen input", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "threshold-pipeline-v1", Prompt: "Read one relevant resource and submit a complete summary without changing the frozen input.", SkillRouting: true, AllowedTools: []string{resource.OpenTool}, ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)}},
				Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "target", ToNode: "analyze", ToPort: "originalRequest"}},
				Outputs: []workflow.Output{{Name: "result", Type: workflow.TypeObject, FromNode: "analyze", FromPort: "analysis", Required: true}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			inputs, _ := json.Marshal(map[string]string{"target": input})
			started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: inputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
			if err != nil {
				t.Fatal(err)
			}
			failed := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 20*time.Second)
			if requests.Load() != 2 || len(failed.AIExecutions) != 1 || failed.Run.ErrorCode == "CONTEXT_BUILD_FAILED" {
				t.Fatal("stage blocked locally before reaching synthetic provider failure", requests.Load(), failed.Run.ErrorCode, failed.Run.ErrorMessage)
			}
			oldRun := failed.AIExecutions[0].ChatRunID
			old, err := app.ChatFacade.GetRunSnapshot(oldRun)
			if err != nil || len(old.ToolCalls) != 1 || old.Run.AutoCompactTokenLimit != 5000 || old.Run.ContextBudgetTokens != 95000 {
				t.Fatal("completed resource or original budgets lost", err)
			}
			before, _ := json.Marshal(old.ToolCalls)
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			if requests.Load() != 2 {
				t.Fatal("restart automatically ran a failed stage")
			}
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: failed.Run.ID, StepID: failed.Steps[0].ID}); err != nil {
				t.Fatal(err)
			}
			complete := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
			if requests.Load() != 6 || complete.Steps[0].Attempt != 2 || len(complete.AIExecutions) != 2 {
				t.Fatal("unexpected requests/attempts after manual retry", requests.Load(), complete.Run.ErrorCode)
			}
			var persisted string
			if err := app.store.DB().QueryRow(`SELECT prompt_text FROM workflow_ai_executions WHERE id=?`, failed.AIExecutions[0].ID).Scan(&persisted); err != nil || !strings.Contains(frozenPrompt, persisted) {
				t.Fatal("original frozen prompt changed on wire or after retry", err)
			}
			old, err = app.ChatFacade.GetRunSnapshot(oldRun)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(old.ToolCalls)
			if string(before) != string(after) {
				t.Fatal("retry modified earlier tool audit")
			}
			var summaries, reads int
			if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM model_request_usage WHERE run_id IN (SELECT l.chat_run_id FROM workflow_ai_chat_runs l JOIN workflow_ai_executions e ON e.id=l.execution_id WHERE e.workflow_run_id=?) AND request_kind='compaction'`, started.Run.ID).Scan(&summaries); err != nil {
				t.Fatal(err)
			}
			if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE run_id IN (SELECT l.chat_run_id FROM workflow_ai_chat_runs l JOIN workflow_ai_executions e ON e.id=l.execution_id WHERE e.workflow_run_id=?)`, started.Run.ID).Scan(&reads); err != nil || summaries != 0 || reads != 2 {
				t.Fatal("correction caused compaction or replayed completed reads", summaries, reads, err)
			}
		})
	}
}

func emitThresholdFixture(t *testing.T, w http.ResponseWriter, protocol modelprofile.APIProtocol, n int, name, args string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	text := "已读取当前资源，下面提交完整阶段结果。"
	switch protocol {
	case modelprofile.ProtocolOpenAIChat:
		delta, finish := map[string]any{"content": text}, "stop"
		if name != "" {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", n), "function": map[string]any{"name": name, "arguments": args}}}}
			finish = "tool_calls"
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
		io.WriteString(w, "data: [DONE]\n\n")
	case modelprofile.ProtocolOpenAIResponses:
		item := map[string]any{"type": "message", "id": fmt.Sprintf("message_%d", n), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}
		if name != "" {
			item = map[string]any{"type": "function_call", "id": fmt.Sprintf("function_%d", n), "call_id": fmt.Sprintf("call_%d", n), "name": name, "arguments": args}
		}
		send(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
	case modelprofile.ProtocolAnthropic:
		send(map[string]any{"type": "message_start", "message": map[string]any{}})
		block, reason := map[string]any{"type": "text", "text": text}, "end_turn"
		if name != "" {
			var parsed any
			_ = json.Unmarshal([]byte(args), &parsed)
			block, reason = map[string]any{"type": "tool_use", "id": fmt.Sprintf("call_%d", n), "name": name, "input": parsed}, "tool_use"
		}
		send(map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
		send(map[string]any{"type": "content_block_stop", "index": 0})
		send(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}})
		send(map[string]any{"type": "message_stop"})
	}
}

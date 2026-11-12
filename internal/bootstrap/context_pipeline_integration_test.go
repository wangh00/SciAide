package bootstrap

import (
	"context"
	"encoding/json"
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

// Real adapters, issued resource tools, workflow correction/retry and reopened
// SQLite. Only the remote model is synthetic; all data/workspaces are temporary.
func TestFrozenStagePromptSurvivesToolsSubmissionCorrectionAndRestartRetry(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			frozen := strings.Repeat("冻结研究目标完整保留。", 7000)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				n := int(requests.Add(1))
				if !strings.Contains(string(body), frozen) {
					t.Error("wire request changed or dropped the complete frozen target")
				}
				switch n {
				case 1, 3:
					canonical := contextPipelineCanonicalRequest(t, body)
					name, args, enabled, err := resourceFixtureSelection(canonical, "scientific-writing", true)
					if err != nil || !enabled || name == "" {
						t.Errorf("issued resource selection failed: %v", err)
						http.Error(w, "missing issued menu", 400)
						return
					}
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, name, args, 61420, 512)
				case 2:
					// Permanent model-side fixture error: no hidden automatic retry.
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(400)
					io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"invalid_request_error","message":"fixture permanent failure after completed read"}}`)
				case 4:
					// A progress-only answer triggers the same-run SUBMIT exchange.
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "", "", 61420, 0)
				case 5, 6:
					if !strings.Contains(string(body), "submit_stage_result") || !strings.Contains(string(body), "dedicated stage submission interface") {
						t.Error("missing dedicated frozen submission schema")
					}
					args := `{}` // One invalid candidate exercises content correction.
					if n == 6 {
						args = `{"summary":"verified frozen target and completed resource"}`
						if !strings.Contains(string(body), "Host submission validation failed") {
							t.Error("content repair lost exact validation diagnostic")
						}
					}
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "submit_stage_result", args, 61420, 0)
				default:
					t.Errorf("unexpected model, compaction or tool-replay request %d", n)
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
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "frozen pipeline fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 272000, AutoCompactTokenLimit: 244800}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "frozen prompt pipeline"})
			if err != nil {
				t.Fatal(err)
			}
			saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{
				SchemaVersion: 1, Name: "Frozen input pipeline",
				Inputs:  []workflow.Port{{Name: "target", Type: workflow.TypeString, Required: true}},
				Nodes:   []workflow.Node{{ID: "analyze", Name: "Analyze frozen target", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "frozen-pipeline-v1", Prompt: "Read one relevant resource, preserve the complete target and submit a summary.", SkillRouting: true, AllowedTools: []string{resource.OpenTool}, ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)}},
				Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "target", ToNode: "analyze", ToPort: "originalRequest"}},
				Outputs: []workflow.Output{{Name: "result", Type: workflow.TypeObject, FromNode: "analyze", FromPort: "analysis", Required: true}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			inputs, _ := json.Marshal(map[string]string{"target": frozen})
			started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: inputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
			if err != nil {
				t.Fatal(err)
			}
			failed := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 20*time.Second)
			if requests.Load() != 2 || len(failed.AIExecutions) != 1 || failed.Run.ErrorCode == "CONTEXT_BUILD_FAILED" {
				t.Fatal("did not reach permanent fixture error after tool read", requests.Load(), failed.Run.ErrorCode, failed.Run.ErrorMessage)
			}
			priorRun := failed.AIExecutions[0].ChatRunID
			priorSnapshot, err := app.ChatFacade.GetRunSnapshot(priorRun)
			if err != nil || len(priorSnapshot.ToolCalls) != 1 {
				t.Fatal("completed resource not saved", err)
			}
			before, _ := json.Marshal(priorSnapshot.ToolCalls)
			if err = app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			if requests.Load() != 2 {
				t.Fatal("restart automatically reran the failed research stage")
			}
			if _, err = app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: failed.Run.ID, StepID: failed.Steps[0].ID}); err != nil {
				t.Fatal(err)
			}
			complete := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
			if requests.Load() != 6 || complete.Steps[0].Attempt != 2 || len(complete.AIExecutions) != 2 {
				t.Fatal("unexpected attempt or extra model request", requests.Load(), complete.Steps[0].Attempt)
			}
			old, err := app.ChatFacade.GetRunSnapshot(priorRun)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(old.ToolCalls)
			if string(before) != string(after) {
				t.Fatal("retry changed failed attempt's completed tool audit")
			}
			var summaries, reads int
			if err = app.store.DB().QueryRow(`SELECT COUNT(*) FROM model_request_usage WHERE run_id IN (SELECT binding.chat_run_id FROM workflow_ai_chat_runs binding JOIN workflow_ai_executions execution ON execution.id=binding.execution_id WHERE execution.workflow_run_id=?) AND request_kind='compaction'`, started.Run.ID).Scan(&summaries); err != nil {
				t.Fatal(err)
			}
			if err = app.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE run_id IN (SELECT binding.chat_run_id FROM workflow_ai_chat_runs binding JOIN workflow_ai_executions execution ON execution.id=binding.execution_id WHERE execution.workflow_run_id=?)`, started.Run.ID).Scan(&reads); err != nil || summaries != 0 || reads != 2 {
				t.Fatal("correction replayed completed tools or compacted frozen input", summaries, reads, err)
			}
		})
	}
}

// Normalize only fixture inspection; production requests still use each real
// protocol's adapter. No plaintext/legacy protocol is substituted on the wire.
func contextPipelineCanonicalRequest(t *testing.T, body []byte) []byte {
	t.Helper()
	var req struct {
		System       json.RawMessage                     `json:"system"`
		Instructions json.RawMessage                     `json:"instructions"`
		Messages     []struct{ Content json.RawMessage } `json:"messages"`
		Input        []struct{ Content json.RawMessage } `json:"input"`
		Tools        []struct {
			Name, Description string
			Parameters        json.RawMessage `json:"parameters"`
			InputSchema       json.RawMessage `json:"input_schema"`
			Function          struct {
				Name, Description string
				Parameters        json.RawMessage
			}
		}
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	messages := []any{}
	if len(req.System) > 0 {
		messages = append(messages, map[string]any{"content": req.System})
	}
	if len(req.Instructions) > 0 {
		messages = append(messages, map[string]any{"content": req.Instructions})
	}
	for _, msg := range append(req.Messages, req.Input...) {
		messages = append(messages, map[string]any{"content": msg.Content})
	}
	tools := []any{}
	for _, d := range req.Tools {
		if d.Function.Name != "" {
			tools = append(tools, map[string]any{"function": d.Function})
			continue
		}
		schema := d.Parameters
		if len(schema) == 0 {
			schema = d.InputSchema
		}
		tools = append(tools, map[string]any{"function": map[string]any{"name": d.Name, "description": d.Description, "parameters": schema}})
	}
	canonical, err := json.Marshal(map[string]any{"messages": messages, "tools": tools})
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

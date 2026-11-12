package bootstrap

import (
	"context"
	"crypto/sha256"
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

// Full production HTTP adapters, workflow, journal and SQLite. The model is
// ONLY a local fixture; each subtest starts with a new temporary application.
func TestProtocolFailureRestartAndExplicitRetryPreserveUpstreamAllProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []string{"closed_incomplete", "malformed", "native_error", "transport_eof", "standalone_identity_error"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				bad := `{"response":"PRIVATE_SENTINEL`
				if mode == "malformed" {
					bad = `{"response":,"output":[],"thinking":"PRIVATE_SENTINEL"}`
				} else if mode == "native_error" {
					bad = `{"type":"error","error":{"type":"authentication_error","code":"invalid_api_key","message":"PRIVATE_SENTINEL"}}`
				} else if mode == "standalone_identity_error" {
					bad = "[error: no_userid]"
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if calls.Add(1) == 1 {
						if mode == "standalone_identity_error" {
							emitQuestionHandoffFixture(t, w, protocol, 2, bad, "", "")
						} else if mode == "transport_eof" {
							fmt.Fprintf(w, "data: %s", bad)
						} else {
							fmt.Fprintf(w, "data: %s\n\n", bad)
						}
						return
					}
					writeProtocolRecoveryAnswer(w, protocol)
				}))
				defer server.Close()
				root := t.TempDir()
				app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = app.Close() }()
				app.Startup(context.Background())
				p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "protocol recovery fixture"})
				if err != nil {
					t.Fatal(err)
				}
				profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "protocol fixture", BaseURL: server.URL, APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}}, TimeoutSeconds: 5, Enabled: true, IsDefault: true})
				if err != nil {
					t.Fatal(err)
				}
				saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "protocol fixture", Inputs: []workflow.Port{{Name: "original_material", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{
					{ID: "preflight", Name: "read workspace", Kind: workflow.NodeTool, ToolName: "builtin.workspace.list", Arguments: json.RawMessage(`{"path":".","limit":10}`)},
					{ID: "draft", Name: "报告撰写", Kind: workflow.NodeAgentStage, ReviewPolicy: workflow.AIReviewAuto, Arguments: json.RawMessage(`{}`), PromptVersion: "protocol-recovery-fixture-v1", Prompt: "Return only the fixture summary object.", OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)},
				}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "original_material", ToNode: "draft", ToPort: "originalRequest"}, {FromNode: "preflight", FromPort: "structured", ToNode: "draft", ToPort: "context"}}, Outputs: []workflow.Output{{Name: "summary", Type: workflow.TypeObject, FromNode: "draft", FromPort: "analysis", Required: true}}}})
				if err != nil {
					t.Fatal(err)
				}
				start, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"original_material":"preserved-fixture"}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "transport_eof" {
					done := waitForWorkflowState(t, app, p.ID, start.Run.ID, workflow.RunCompleted, 15*time.Second)
					if calls.Load() != 2 || len(done.AIExecutions) != 1 || done.AIExecutions[0].Attempt != 1 || done.Steps[0].Attempt != 1 {
						t.Fatal("EOF replayed stage rather than unfinished request", calls.Load(), done)
					}
					snapshot, err := app.ChatFacade.GetRunSnapshot(done.AIExecutions[0].ChatRunID)
					if err != nil || snapshot.Run.ModelTurns != 1 {
						t.Fatal("EOF changed logical model turn", snapshot, err)
					}
					var audit string
					if err := app.store.DB().QueryRow(`SELECT payload_json FROM run_events WHERE aggregate_id=? AND event_type='run.model_attempt_failed'`, snapshot.Run.ID).Scan(&audit); err != nil || !strings.Contains(audit, "MODEL_STREAM_INTERRUPTED") || strings.Contains(audit, "PRIVATE_SENTINEL") {
						t.Fatal("EOF diagnostic invalid", audit, err)
					}
					var count int
					if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE workflow_run_id=?`, start.Run.ID).Scan(&count); err != nil || count != 1 {
						t.Fatal("EOF repeated upstream tool", count, err)
					}
					return
				}
				failed := waitForWorkflowState(t, app, p.ID, start.Run.ID, workflow.RunFailed, 15*time.Second)
				wantCode := "MODEL_STREAM_INVALID"
				if mode == "native_error" || mode == "standalone_identity_error" {
					wantCode = "MODEL_REQUEST_REJECTED"
				}
				if calls.Load() != 1 || failed.Run.ErrorCode != wantCode || len(failed.AIExecutions) != 1 || failed.Steps[0].Status != workflow.StepCompleted || len(failed.Steps[1].Output) > 0 && string(failed.Steps[1].Output) != "{}" {
					t.Fatalf("protocol failure was retried/accepted: calls=%d run=%+v", calls.Load(), failed)
				}
				if failed.Recovery == nil || failed.Recovery.Kind != "retry_stage" || !failed.Recovery.CallsModel || (mode != "native_error" && mode != "standalone_identity_error" && failed.Recovery.FailureKind != "model_protocol") {
					t.Fatal("wrong recovery projection", failed.Recovery)
				}
				hash := fmt.Sprintf("%x", sha256.Sum256([]byte(bad)))
				oldChat := failed.AIExecutions[0].ChatRunID
				var audit string
				if err := app.store.DB().QueryRow(`SELECT payload_json FROM run_events WHERE aggregate_id=? AND event_type='run.model_attempt_failed'`, oldChat).Scan(&audit); err != nil || !strings.Contains(audit, hash) || strings.Contains(audit, "PRIVATE_SENTINEL") {
					t.Fatal("unsafe/lost recovery audit", audit, err)
				}
				public, err := app.ChatFacade.GetRunSnapshot(oldChat)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(public.Run.ErrorDetails, hash) {
					t.Fatal("original event fingerprint missing", public.Run.ErrorDetails)
				}
				for _, value := range []any{failed, public} {
					encoded, _ := json.Marshal(value)
					if strings.Contains(string(encoded), "PRIVATE_SENTINEL") {
						t.Fatal("native payload escaped public facade")
					}
				}
				if err := app.Close(); err != nil {
					t.Fatal(err)
				}
				app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
				if err != nil {
					t.Fatal(err)
				}
				app.Startup(context.Background())
				restored, err := app.WorkflowFacade.GetRun(p.ID, start.Run.ID)
				if err != nil || restored.Run.Status != workflow.RunFailed || calls.Load() != 1 {
					t.Fatal("restart replayed failed request", calls.Load(), err)
				}
				if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: start.Run.ID, StepID: restored.Steps[1].ID}); err != nil {
					t.Fatal(err)
				}
				done := waitForWorkflowState(t, app, p.ID, start.Run.ID, workflow.RunCompleted, 15*time.Second)
				if calls.Load() != 2 || len(done.AIExecutions) != 2 || done.AIExecutions[0].Status != "failed" || done.AIExecutions[1].Attempt != 2 || done.Recovery != nil {
					t.Fatal("retry lost history or repeated work", calls.Load(), done)
				}
				upstreamBefore, _ := json.Marshal(failed.Steps[0])
				upstreamAfter, _ := json.Marshal(done.Steps[0])
				compilationBefore, _ := json.Marshal(failed.Run.Compilation)
				compilationAfter, _ := json.Marshal(done.Run.Compilation)
				if string(upstreamBefore) != string(upstreamAfter) || string(compilationBefore) != string(compilationAfter) || string(failed.Run.Inputs) != string(done.Run.Inputs) || done.Run.ResearchTaskID != failed.Run.ResearchTaskID {
					t.Fatal("retry changed upstream/materials/frozen route or created a new task")
				}
				var keptAudit string
				if err := app.store.DB().QueryRow(`SELECT payload_json FROM run_events WHERE aggregate_id=? AND event_type='run.model_attempt_failed'`, oldChat).Scan(&keptAudit); err != nil || keptAudit != audit {
					t.Fatal("retry rewrote original failure audit", err)
				}
				var count int
				if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE workflow_run_id=?`, start.Run.ID).Scan(&count); err != nil || count != 1 {
					t.Fatal("completed upstream tool re-executed", count, err)
				}
			})
		}
	}
}

func writeProtocolRecoveryAnswer(w io.Writer, protocol modelprofile.APIProtocol) {
	answer := `{"summary":"verified recovery fixture"}`
	send := func(value any) { payload, _ := json.Marshal(value); fmt.Fprintf(w, "data: %s\n\n", payload) }
	switch protocol {
	case modelprofile.ProtocolOpenAIResponses:
		send(map[string]any{"type": "response.output_text.delta", "delta": answer})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
	case modelprofile.ProtocolOpenAIChat:
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": "stop"}}})
	case modelprofile.ProtocolAnthropic:
		send(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		send(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": answer}})
		send(map[string]any{"type": "content_block_stop", "index": 0})
		send(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}})
		send(map[string]any{"type": "message_stop"})
	}
}

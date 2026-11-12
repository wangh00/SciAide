package bootstrap

import (
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
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Controlled HTTP, actual three provider adapters, bootstrap and temporary
// SQLite. Closing is not a user cancel; restarting must not replay the model.
func TestApplicationCloseInterruptsResearchAndExplicitRetryKeepsUpstream(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var requests atomic.Int32
			opened := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				n := requests.Add(1)
				if n == 1 {
					w.Header().Set("Content-Type", "text/event-stream")
					// No answer has completed. A live connection is enough to test
					// that shutdown reaches it without a model recovery delay.
					fmt.Fprint(w, ": fixture waiting\n\n")
					w.(http.Flusher).Flush()
					opened <- struct{}{}
					<-r.Context().Done()
					return
				}
				if n != 2 {
					t.Errorf("unexpected model replay: %d", n)
				}
				emitQuestionHandoffFixture(t, w, protocol, int(n), `{"summary":"explicit retry completed"}`, "", "")
			}))
			defer server.Close()
			options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
			app, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "shutdown recovery fixture"})
			if err != nil {
				t.Fatal(err)
			}
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "shutdown fixture", APIProtocol: protocol, BaseURL: server.URL, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}}, Enabled: true, IsDefault: true, TimeoutSeconds: 5, CustomHeaders: map[string]string{}})
			if err != nil {
				t.Fatal(err)
			}
			saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "shutdown route", Nodes: []workflow.Node{
				{ID: "preflight", Name: "read workspace", Kind: workflow.NodeTool, ToolName: "builtin.workspace.list", Arguments: json.RawMessage(`{"path":".","limit":10}`)},
				{ID: "review", Name: "review fixture", Kind: workflow.NodeAgentStage, PromptVersion: "shutdown-fixture-v1", Prompt: "Return a summary.", Arguments: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)},
			}, Edges: []workflow.Edge{{FromNode: "preflight", FromPort: "structured", ToNode: "review", ToPort: "context"}}, Outputs: []workflow.Output{{Name: "summary", Type: workflow.TypeObject, FromNode: "review", FromPort: "analysis", Required: true}}}})
			if err != nil {
				t.Fatal(err)
			}
			start, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-opened:
			case <-time.After(10 * time.Second):
				t.Fatal("fixture model connection did not open")
			}
			active, err := app.WorkflowFacade.GetRun(p.ID, start.Run.ID)
			if err != nil || len(active.AIExecutions) != 1 || active.Steps[0].Status != workflow.StepCompleted {
				t.Fatal("stage did not start", active, err)
			}
			upstream := string(mustBootstrapJSON(t, active.Steps[0]))
			oldChatID := active.AIExecutions[0].ChatRunID
			started := time.Now()
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("shutdown waited for a model retry budget")
			}
			app, err = New(options)
			if err != nil {
				t.Fatal(err)
			}
			stopped := waitForWorkflowState(t, app, p.ID, start.Run.ID, workflow.RunInterrupted, 10*time.Second)
			chatSnapshot, err := app.ChatFacade.GetRunSnapshot(oldChatID)
			if err != nil || chatSnapshot.Run.Status != chat.RunInterrupted || chatSnapshot.Run.ErrorCode != "APP_CLOSING" || requests.Load() != 1 {
				t.Fatal("shutdown became a user cancel or restart replayed the model", chatSnapshot.Run, err, requests.Load())
			}
			if stopped.Steps[1].Status != workflow.StepInterrupted || stopped.Run.ErrorCode != "WORKFLOW_AI_INTERRUPTED" || !strings.Contains(stopped.Run.ErrorMessage, "程序已关闭") || stopped.Run.CancelRequested {
				t.Fatal("workflow lost lifecycle interruption", stopped.Run, stopped.Steps)
			}
			// Optional actual facade projection for the React/browser harness;
			// never read a real user task or reclassify cancellation in the UI.
			if destination := os.Getenv("SCIAIDE_SHUTDOWN_UI_FIXTURE"); destination != "" && protocol == modelprofile.ProtocolOpenAIResponses {
				plan, err := app.WorkflowFacade.Get(p.ID, saved.Workflow.ID)
				if err != nil {
					t.Fatal(err)
				}
				conv, err := app.ConversationFacade.GetConversation(stopped.Run.ConversationID)
				if err != nil {
					t.Fatal(err)
				}
				timeline, err := app.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: p.ID, TaskID: stopped.Run.ResearchTaskID, Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.MarshalIndent(map[string]any{"detail": stopped, "timeline": timeline, "plan": plan, "conversation": conv}, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(destination, encoded, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var cancelled, interrupted int
			if err := app.store.DB().QueryRow(`SELECT count(*) FROM run_events WHERE aggregate_id=? AND event_type='run.cancelled'`, oldChatID).Scan(&cancelled); err != nil {
				t.Fatal(err)
			}
			if err := app.store.DB().QueryRow(`SELECT count(*) FROM run_events WHERE aggregate_id=? AND event_type='run.interrupted'`, oldChatID).Scan(&interrupted); err != nil || cancelled != 0 || interrupted != 1 {
				t.Fatal("wrong terminal event", cancelled, interrupted, err)
			}
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: start.Run.ID, StepID: stopped.Steps[1].ID}); err != nil {
				t.Fatal(err)
			}
			done := waitForWorkflowState(t, app, p.ID, start.Run.ID, workflow.RunWaitingHumanConfirmation, 10*time.Second)
			if requests.Load() != 2 || len(done.AIExecutions) != 2 || done.AIExecutions[0].Status != "interrupted" || done.AIExecutions[1].Attempt != 2 || done.AIExecutions[1].Status != "completed" || string(mustBootstrapJSON(t, done.Steps[0])) != upstream {
				t.Fatal("retry rewrote history or reran completed work", done.AIExecutions, done.Steps, requests.Load())
			}
		})
	}
}

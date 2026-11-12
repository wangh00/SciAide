package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

// Real HTTP adapter/bootstrap/SQLite and restart, only the remote model is
// scripted. Cancel the stalled live body, then explicitly resume the same AI
// stage: completed upstream work and the old cancelled audit must not change.
func TestCancelledResearchStreamResumesSameStageAfterRestart(t *testing.T) {
	var calls atomic.Int32
	opened := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"discarded incomplete draft\"}\n\n")
			w.(http.Flusher).Flush()
			opened <- struct{}{}
			<-r.Context().Done()
			return
		}
		if n != 2 {
			t.Errorf("unexpected repeated execution %d", n)
		}
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"summary\\\":\\\"verified recovery fixture\\\"}\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":4}}}\n\n")
	}))
	defer server.Close()
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { app.Close() }()
	app.Startup(context.Background())
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "cancelled stream fixture"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "stream fixture", BaseURL: server.URL, APIProtocol: modelprofile.ProtocolOpenAIResponses, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}}, TimeoutSeconds: 5, Enabled: true, IsDefault: true, CustomHeaders: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "stage recovery fixture", Nodes: []workflow.Node{
		{ID: "preflight", Name: "read existing workspace", Kind: workflow.NodeTool, ToolName: "builtin.workspace.list", Arguments: json.RawMessage(`{"path":".","limit":10}`)},
		{ID: "implementation", Name: "generate analysis", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "stream-recovery-fixture-v1", Prompt: "Return the verified fixture summary.", OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)},
	}, Edges: []workflow.Edge{{FromNode: "preflight", FromPort: "structured", ToNode: "implementation", ToPort: "context"}}, Outputs: []workflow.Output{{Name: "summary", Type: workflow.TypeObject, FromNode: "implementation", FromPort: "analysis", Required: true}}}})
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
		detail, readErr := app.WorkflowFacade.GetRun(p.ID, start.Run.ID)
		t.Fatalf("stream did not open: requests=%d run=%s steps=%+v executions=%+v readErr=%v", calls.Load(), detail.Run.Status, detail.Steps, detail.AIExecutions, readErr)
	}
	active, err := app.WorkflowFacade.GetRun(p.ID, start.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Steps[0].Status != workflow.StepCompleted {
		t.Fatal("upstream missing", active)
	}
	upstream := active.Steps[0]
	oldChat := active.AIExecutions[0].ChatRunID
	if _, err := app.WorkflowFacade.Cancel(p.ID, start.Run.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitForWorkflowState(t, app, p.ID, start.Run.ID, workflow.RunCancelled, 10*time.Second)
	for deadline := time.Now().Add(5 * time.Second); ; {
		snapshot, err := app.ChatFacade.GetRunSnapshot(oldChat)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Run.Status == chat.RunCancelled {
			if snapshot.Retry != nil {
				t.Fatal("retry persisted over cancellation")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("model body cancellation did not terminate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cancelled.Steps[1].Status != workflow.StepCancelled {
		t.Fatal(cancelled)
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
	if err != nil || restored.Run.Status != workflow.RunCancelled || calls.Load() != 1 {
		t.Fatal(restored, err, calls.Load())
	}
	timeline, err := app.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: p.ID, TaskID: restored.Run.ResearchTaskID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	// Optional bridge into the actual React App browser test. These are the
	// real facade projections from this temporary DB, not hand-written active
	// flags. The remote model is still only the local HTTP fixture above.
	if destination := os.Getenv("SCIAIDE_CANCELLED_UI_FIXTURE"); destination != "" {
		plan, err := app.WorkflowFacade.Get(p.ID, saved.Workflow.ID)
		if err != nil {
			t.Fatal(err)
		}
		conv, err := app.ConversationFacade.GetConversation(restored.Run.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.MarshalIndent(map[string]any{"detail": restored, "timeline": timeline, "plan": plan, "conversation": conv}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var cancelEntry workflow.ResearchTimelineEntry
	activeRecoveries := 0
	for _, entry := range timeline.Entries {
		if entry.EventType == "workflow.cancel_requested" && entry.Active {
			cancelEntry = entry
			activeRecoveries++
		}
	}
	if activeRecoveries != 1 || cancelEntry.Step == nil || cancelEntry.Step.ID != restored.Steps[1].ID || cancelEntry.Step.Attempt != 1 {
		t.Fatalf("cancelled AI stage has no unique active timeline recovery: %+v", timeline)
	}
	// Legacy cancel events have no stepId. Projection must derive the current
	// cancelled AI stage without rewriting that persisted event or snapshot.
	if !strings.Contains(string(cancelEntry.Snapshot), `"event":{}`) {
		t.Fatal("fixture no longer covers the existing empty cancel payload")
	}
	if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: start.Run.ID, StepID: cancelled.Steps[1].ID, Note: "explicitly resume cancelled current AI stage"}); err != nil {
		t.Fatal(err)
	}
	complete := waitForWorkflowState(t, app, p.ID, start.Run.ID, workflow.RunWaitingHumanConfirmation, 10*time.Second)
	if len(complete.AIExecutions) != 2 || complete.AIExecutions[0].Status != "cancelled" || complete.AIExecutions[1].Attempt != 2 || calls.Load() != 2 {
		t.Fatal("old stage audit lost", complete, calls.Load())
	}
	if complete.Steps[0].ToolCallID != upstream.ToolCallID || complete.Steps[0].Attempt != upstream.Attempt || !complete.Steps[0].CompletedAt.Equal(*upstream.CompletedAt) || string(complete.Steps[0].Output) != string(upstream.Output) {
		t.Fatal("completed upstream stage rerun or changed")
	}
	if strings.Contains(complete.AIExecutions[1].OutputText, "discarded") {
		t.Fatal("failed draft leaked into committed result")
	}
	after, err := app.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: p.ID, TaskID: restored.Run.ResearchTaskID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	foundCancellation := false
	for _, entry := range after.Entries {
		if entry.ID == cancelEntry.ID {
			foundCancellation = true
			if entry.Active || string(entry.Snapshot) != string(cancelEntry.Snapshot) {
				t.Fatal("retry kept recovery active or rewrote the cancellation history")
			}
		}
	}
	if !foundCancellation {
		t.Fatal("retry lost the cancellation history")
	}
	var count int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE workflow_run_id=?`, start.Run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("completed tool reexecuted", count, err)
	}
}

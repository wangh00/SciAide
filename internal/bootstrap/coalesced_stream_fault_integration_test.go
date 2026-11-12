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

// Only the provider is scripted. Original HTTP adapter, request recovery,
// workflow stage, SQLite audit and restart remain in the path.
func TestCoalescedStreamFaultUsesOriginalRecoveryWithoutAcceptingPrefix(t *testing.T) {
	for _, tc := range []struct{ name, channel, prefix string }{
		{"delta", "response.output_text.delta", `{"type":"response.output_text.delta","delta":"PRIVATE_PREFIX_MUST_NOT_RETURN"}`},
		{"native-item", "response.output_item.done", `{"sequence_number":100,"type":"response.output_item.done","item":{"id":"r1","type":"reasoning","status":"completed","summary":[],"encrypted_content":"PRIVATE_STATE_MUST_NOT_RETURN"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) { checkCoalescedOriginalRecovery(t, tc.channel, tc.prefix) })
	}
}

func checkCoalescedOriginalRecovery(t *testing.T, channel, prefix string) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "PRIVATE_") {
			t.Error("failed draft was replayed to the model")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls.Add(1) {
		case 1:
			fmt.Fprintf(w, "event: %s\ndata: %s\nevent: error\ndata: {\"type\":\"error\",\"sequence_number\":0,\"code\":\"upstream_stream_read_error\",\"message\":\"PRIVATE_REASON\",\"param\":null}\n\n", channel, prefix)
		case 2:
			writeProtocolRecoveryAnswer(w, modelprofile.ProtocolOpenAIResponses)
		default:
			t.Error("unexpected additional request")
		}
	}))
	defer server.Close()
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, DisableAuxiliaryModels: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { app.Close() }()
	app.Startup(context.Background())
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "coalesced failure fixture"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "fixture", BaseURL: server.URL, APIProtocol: modelprofile.ProtocolOpenAIResponses, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}}, TimeoutSeconds: 5, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "original recovery fixture", Nodes: []workflow.Node{
		{ID: "preflight", Name: "workspace read", Kind: workflow.NodeTool, ToolName: "builtin.workspace.list", Arguments: json.RawMessage(`{"path":".","limit":10}`)},
		{ID: "draft", Name: "draft", Kind: workflow.NodeAgentStage, ReviewPolicy: workflow.AIReviewAuto, Arguments: json.RawMessage(`{}`), PromptVersion: "coalesced-fault-fixture-v1", Prompt: "Return only the verified fixture summary.", OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)},
	}, Edges: []workflow.Edge{{FromNode: "preflight", FromPort: "structured", ToNode: "draft", ToPort: "context"}}, Outputs: []workflow.Output{{Name: "summary", Type: workflow.TypeObject, FromNode: "draft", FromPort: "analysis", Required: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
	if err != nil {
		t.Fatal(err)
	}
	done := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCompleted, 15*time.Second)
	if calls.Load() != 2 || len(done.AIExecutions) != 1 || done.Steps[0].Attempt != 1 || done.Steps[1].Attempt != 1 {
		t.Fatal("replayed a stage or changed recovery budget", calls.Load())
	}
	if strings.Contains(string(done.Steps[1].Output), "PRIVATE_") || !strings.Contains(string(done.Steps[1].Output), "verified") {
		t.Fatal("failed content accepted")
	}
	var audit string
	if err := app.store.DB().QueryRow(`SELECT payload_json FROM run_events WHERE aggregate_id=? AND event_type='run.model_attempt_failed'`, done.AIExecutions[0].ChatRunID).Scan(&audit); err != nil || !strings.Contains(audit, "MODEL_UNAVAILABLE") || !strings.Contains(audit, "missing_separator_before_terminal_error") || strings.Contains(audit, "PRIVATE_") {
		t.Fatal("wrong safe audit", err, audit)
	}
	var toolCount int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE workflow_run_id=?`, started.Run.ID).Scan(&toolCount); err != nil || toolCount != 1 {
		t.Fatal("upstream tool was rerun", toolCount, err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, DisableAuxiliaryModels: true})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	restored, err := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
	if err != nil || restored.Run.Status != workflow.RunCompleted || restored.Steps[0].InputSHA256 != done.Steps[0].InputSHA256 || restored.Steps[1].Attempt != 1 || calls.Load() != 2 {
		t.Fatal("restart lost the original checkpoint", err)
	}
}

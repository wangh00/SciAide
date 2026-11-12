package bootstrap

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/debugapi"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	w "github.com/wangh00/SciAide/internal/transport/wails"
)

func (f *debugFixture) waitForDebugPlanWithFixtureApprovals(t *testing.T, c debugapi.Case) map[string]json.RawMessage {
	t.Helper()
	for step := 0; step < 20; step++ {
		value := f.wait(t, c, func(v map[string]json.RawMessage) bool {
			s := debugState(v)
			return s == "plan_available" || s == "waiting_approval" || s == "chat_failed" || s == "workflow_failed"
		})
		if debugState(value) == "plan_available" {
			return value
		}
		if debugState(value) != "waiting_approval" {
			t.Fatalf("planning failed: %s", value["chats"])
		}
		var chats []w.RunSnapshot
		_ = json.Unmarshal(value["chats"], &chats)
		approved := 0
		for _, snap := range chats {
			for _, p := range snap.PendingApprovals {
				if p.ToolName != workflow.ResearchPlanPrepareTool && p.ToolName != "builtin.skill.load" && p.ToolName != "builtin.workflow.state.read" {
					t.Fatal("unexpected fixture tool; do not auto-approve arbitrary operations", p.ToolName)
				}
				code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/approve", map[string]any{"runId": snap.Run.ID, "approvalId": p.ID, "allow": true})
				if code != 200 {
					t.Fatalf("explicit fixture approval failed: %s", result["error"])
				}
				approved++
			}
		}
		if approved == 0 {
			t.Fatal("waiting approval was not an expected fixture chat tool")
		}
	}
	t.Fatal("too many fixture approvals")
	return nil
}

func TestResearchDebugPlanPermissionsSurvivePreparationClarificationAndReplan(t *testing.T) {
	model := newDebugPlanningModel(t, func(plan map[string]any) {
		plan["clarification"] = map[string]any{"needsUserInput": true, "intro": "Choose test scope.", "questions": []any{
			map[string]any{"id": "scope", "kind": "research_direction", "impact": "Test delivery scope.", "text": "Which scope?", "required": true, "selectionMode": "single", "options": []any{
				map[string]any{"id": "narrow", "label": "Narrow"}, map[string]any{"id": "wide", "label": "Wide"},
			}},
		}}
	})
	defer model.Close()
	f := newDebugFixture(t, model.URL, modelprofile.ProtocolOpenAIChat, 80, false)
	rid, _ := id.New()
	c, _ := f.create(t, rid, false)
	value := f.waitForDebugPlanWithFixtureApprovals(t, c)
	var current workflow.RunDetail
	_ = json.Unmarshal(value["workflow"], &current)
	assertPlan := func(d workflow.RunDetail) {
		t.Helper()
		if d.Run.PermissionMode != conversation.PermissionPlan || len(d.AIExecutions) == 0 {
			t.Fatalf("planner permission escalated or AI was bypassed: %s", d.Run.PermissionMode)
		}
		for _, execution := range d.AIExecutions {
			snap, e := f.app.ChatFacade.GetRunSnapshot(execution.ChatRunID)
			if e != nil || snap.Run.PermissionMode != conversation.PermissionPlan {
				t.Fatal("derived AI chat escalated", e, snap.Run.PermissionMode)
			}
		}
	}
	assertPlan(current)
	code, response := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/clarify", map[string]any{"starterRunId": current.Run.ID, "answers": map[string][]string{"scope": {"narrow"}}})
	if code != 200 {
		t.Fatalf("clarify failed: %s", response["error"])
	}
	var next workflow.RunDetail
	_ = json.Unmarshal(response["run"], &next.Run)
	next = waitForWorkflowState(t, f.app, c.ProjectID, next.Run.ID, workflow.RunCompleted, 20*time.Second)
	assertPlan(next)
	code, response = f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/replan", map[string]any{"starterRunId": next.Run.ID})
	if code != 200 {
		t.Fatalf("replan failed: %s", response["error"])
	}
	_ = json.Unmarshal(response["run"], &next.Run)
	next = waitForWorkflowState(t, f.app, c.ProjectID, next.Run.ID, workflow.RunCompleted, 20*time.Second)
	assertPlan(next)
}

func TestResearchDebugSourceDirectCreateAndInterruptedInitializationReplay(t *testing.T) {
	var count atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, int(count.Add(1)), "Fixture response, no scientific claim.", "", "")
	}))
	defer model.Close()
	// The source is another temporary real app, never a personal/formal DB.
	sourceRoot := t.TempDir()
	source, e := New(Options{RootDir: sourceRoot, EventPublisher: workflowAITestPublisher{}})
	if e != nil {
		t.Fatal(e)
	}
	profile := saveWorkflowAITestProfile(t, source, model.URL)
	legacy := profile
	legacy.ID, legacy.Enabled, legacy.BaseURL = "unsafe-legacy-disabled", false, "https://legacy.invalid/v1?key=PRIVATE_QUERY_SENTINEL"
	if e = sqlite.NewModelProfileRepository(source.store.DB()).Save(source.lifecycle.Context(), legacy); e != nil {
		t.Fatal(e)
	}
	_ = source.Close()
	root := t.TempDir()
	f := openDebugFixture(t, root, sourceRoot, profile.ID, "", modelprofile.ProtocolOpenAIChat, 4, false, true)
	rid, _ := id.New()
	// Intentionally do NOT call GET /models before creation.
	c, body := f.create(t, rid, false)
	f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "discussion_idle" })
	if count.Load() != 1 {
		t.Fatal("direct source create failed or duplicated model call", count.Load())
	}
	// Recovery must not mistake absence from a bounded UI projection for
	// absence from the durable run/message transaction.
	tx, e := f.app.store.DB().Begin()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 202; i++ {
		_, e = tx.Exec(`INSERT INTO messages(id,conversation_id,role,status,created_at,updated_at) VALUES (?,?,'user','complete',?,?)`, fmt.Sprintf("later-test-message-%d", i), c.ConversationID, time.Now().Add(time.Duration(i+1)*time.Second).UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
		if e != nil {
			_ = tx.Rollback()
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	// Seed a second durable case at the exact pre-StartChat crash boundary.
	project, e := f.app.ProjectFacade.CreateProject(w.CreateProjectRequest{Name: "[debug] interrupted initialization"})
	if e != nil {
		t.Fatal(e)
	}
	entry, e := f.app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: body["prompt"].(string), ModelProfileID: profile.ID, ModelID: f.modelID, DiscussionOnly: true, PermissionMode: conversation.PermissionPlan})
	if e != nil {
		t.Fatal(e)
	}
	pendingBody := map[string]any{}
	for k, v := range body {
		pendingBody[k] = v
	}
	pendingRID, _ := id.New()
	pendingBody["requestId"] = pendingRID
	// Match the server's canonical typed request encoding, not map key order.
	encoded, _ := json.Marshal(struct {
		Name, Prompt, ModelProfileID, ModelID, RequestID string
		PermissionMode                                   conversation.PermissionMode
	}{pendingBody["name"].(string), pendingBody["prompt"].(string), profile.ID, f.modelID, pendingRID, conversation.PermissionPlan})
	caseID, _ := id.New()
	clientID, _ := id.New()
	pending := debugapi.Case{RequestID: pendingRID, RequestSHA256: fmt.Sprintf("%x", sha256.Sum256(encoded)), ID: caseID, Name: pendingBody["name"].(string), ProjectID: project.ID, TaskID: entry.Run.ResearchTaskID, ConversationID: entry.Run.ConversationID, StarterRunID: entry.Run.ID, ModelProfileID: profile.ID, ModelID: f.modelID, InitialClientMessageID: clientID}
	_ = f.server.Close()
	c.InitialRunID = "" // Emulate crash after app transaction but before index save.
	saved, _ := json.Marshal(map[string]debugapi.Case{c.ID: c, pending.ID: pending})
	if e = os.WriteFile(filepath.Join(root, "cases.json"), saved, 0600); e != nil {
		t.Fatal(e)
	}
	// A disabled restart must inspect/recover an existing run without a call,
	// but refuse pending model initialization and preserve its identity.
	f = openDebugFixture(t, root, sourceRoot, profile.ID, "", modelprofile.ProtocolOpenAIChat, 4, false, false)
	code, result := f.request(t, "POST", "/v1/cases", body)
	if code != 200 || string(result["replayed"]) != "true" {
		t.Fatal("lost index did not recover existing initial run", code, string(result["error"]))
	}
	code, result = f.request(t, "POST", "/v1/cases", pendingBody)
	if code == 200 || string(result["result"]) == "null" {
		t.Fatal("pending initialization reported success or hid case identity")
	}
	if count.Load() != 1 {
		t.Fatal("restart/replay made an unauthorized duplicate call")
	}
	_ = f.server.Close()
	f = openDebugFixture(t, root, sourceRoot, profile.ID, "", modelprofile.ProtocolOpenAIChat, 4, false, true)
	time.Sleep(30 * time.Millisecond)
	if count.Load() != 1 {
		t.Fatal("startup automatically resumed a pending case")
	}
	code, result = f.request(t, "POST", "/v1/cases", pendingBody)
	if code != 200 || string(result["initialState"]) != `"started"` {
		t.Fatalf("explicit pending initialization replay failed: %s", result["error"])
	}
	_ = json.Unmarshal(result["case"], &pending)
	f.wait(t, pending, func(v map[string]json.RawMessage) bool { return debugState(v) == "discussion_idle" })
	code, _ = f.request(t, "POST", "/v1/cases", pendingBody)
	if code != 200 || count.Load() != 2 {
		t.Fatal("replay did not create exactly one initial run", count.Load())
	}
}

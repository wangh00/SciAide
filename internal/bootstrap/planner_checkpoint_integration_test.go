package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Actual HTTP adapters, Plan permission/denial and persisted SQLite state.
// The only external request is to this scripted loopback model. The pending
// builtin.web.search is explicitly denied and never invokes a search provider.
func TestPlannerCheckpointApprovalResumeAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, kind := range []string{"format", "skills", "missing"} {
			t.Run(string(protocol)+"/"+kind, func(t *testing.T) { testPlannerCheckpointApprovalResume(t, protocol, kind, false) })
		}
	}
}

func TestPlannerCheckpointApplicationRestartDoesNotAutomaticallyResume(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) { testPlannerCheckpointApprovalResume(t, protocol, "format", true) })
	}
}

func testPlannerCheckpointApprovalResume(t *testing.T, protocol modelprofile.APIProtocol, kind string, restartOnly bool) {
	t.Helper()
	good := `{"normalizedQuestion":"CHECKPOINT-BASELINE-SENTINEL","selectedSkills":[],"routes":[]}`
	text, corrections, wantCode := "```json\n"+good+"}\n```", 2, "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED"
	if kind == "skills" {
		text = "```json\n" + strings.Replace(good, `"selectedSkills":[]`, `"selectedSkills":[{"name":"scientific-writing"}]`, 1) + "\n```"
		wantCode = "WORKFLOW_PLANNER_SKILLS_INVALID"
	}
	if kind == "missing" {
		text = "I will submit the complete research plan next."
		corrections = 3
		wantCode = "WORKFLOW_AI_OUTPUT_MISSING"
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := int(requests.Add(1))
		if kind == "missing" && n > 1 {
			prompt := workflowAITestPrompt(contextPipelineCanonicalRequest(t, body))
			if !strings.Contains(prompt, "otherwise end with the complete fenced JSON object") || strings.Contains(prompt, "Continue the SAME stage using the current native action protocol") {
				t.Error("custom legacy planning/approval continuation was silently upgraded")
			}
		}
		if n > corrections+2 {
			t.Error("approval resume renewed a planner correction allowance")
		}
		if n == corrections+1 {
			if !strings.Contains(string(body), "web_search") && !strings.Contains(string(body), "builtin.web.search") {
				t.Error("frozen moderate-risk tool not offered before approval")
			}
			emitQuestionHandoffFixture(t, w, protocol, n, "", "builtin.web.search", `{"query":"synthetic checkpoint fixture - must be denied"}`)
			return
		}
		if n == corrections+2 && kind != "missing" {
			prompt := workflowAITestPrompt(contextPipelineCanonicalRequest(t, body))
			if !strings.Contains(prompt, "CHECKPOINT-BASELINE-SENTINEL") {
				t.Error("rejected baseline absent after approval resume")
			}
			if kind == "format" && !strings.Contains(prompt, "Host submission validation failed") {
				t.Error("format diagnosis absent after approval resume")
			}
			if kind == "skills" && !strings.Contains(prompt, "WORKFLOW_PLANNER_SKILLS_MISSING") {
				t.Error("actual remaining Skill check absent after approval resume")
			}
		}
		emitQuestionHandoffFixture(t, w, protocol, n, text, "", "")
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, DisableAuxiliaryModels: true}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "planner recovery fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 272000, AutoCompactTokenLimit: 244800}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "isolated planner checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["normalizedQuestion","selectedSkills","routes"],"properties":{"normalizedQuestion":{"type":"string"},"selectedSkills":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}}}},"routes":{"type":"array","items":{"type":"object"}}}}`)
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{SchemaVersion: 1, Name: "Planner checkpoint fixture", Inputs: []workflow.Port{{Name: "context", Type: workflow.TypeObject, Required: true}}, Nodes: []workflow.Node{{ID: "explore", Name: "Planning", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "planner-checkpoint-fixture-v1", Prompt: "Plan the frozen synthetic request; do not fabricate evidence or accepted conclusions.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{"builtin.web.search"}, OutputSchema: schema}}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "context", ToNode: "explore", ToPort: "context"}}, Outputs: []workflow.Output{{Name: "result", Type: workflow.TypeObject, FromNode: "explore", FromPort: "analysis", Required: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"context":{"evidence":"only synthetic fixture"}}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionPlan})
	if err != nil {
		t.Fatal(err)
	}
	// Chat-tool approval keeps its owning Workflow running while the AI node
	// waits. Workflow-tool approval is a different state machine.
	var waiting workflow.RunDetail
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		waiting, err = app.WorkflowFacade.GetRun(project.ID, started.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(waiting.AIExecutions) == 1 {
			snapshot, e := app.ChatFacade.GetRunSnapshot(waiting.AIExecutions[0].ChatRunID)
			if e == nil && snapshot.Run.Status == chat.RunWaitingApproval {
				break
			}
			if e == nil && snapshot.Run.Status == chat.RunFailed {
				t.Fatal("chat failed before approval", snapshot.Run.ErrorCode, snapshot.Run.ErrorMessage)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(waiting.AIExecutions) != 1 || requests.Load() != int32(corrections+1) {
		t.Fatal("unexpected pre-approval state", len(waiting.AIExecutions), requests.Load())
	}
	runID := waiting.AIExecutions[0].ChatRunID
	before, found, err := sqlite.NewRunRepository(app.store.DB()).PlannerCheckpoint(context.Background(), runID)
	if err != nil || !found || before.RequestInFlight {
		t.Fatal("planning decision was not saved before approval", found, err)
	}
	if before.ContentCorrections+before.SkillCorrections+before.ProgressCorrections != corrections {
		t.Fatal("spent corrections were not recorded", before.ContentCorrections, before.SkillCorrections, before.ProgressCorrections)
	}
	if kind != "missing" && before.Candidate != text {
		t.Fatal("checkpoint baseline differs from exact provider candidate")
	}
	// A fresh SQLite connection must recover exact state, not a process cache.
	store, err := sqlite.Open(context.Background(), filepath.Join(options.RootDir, "data", "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, found, err := sqlite.NewRunRepository(store.DB()).PlannerCheckpoint(context.Background(), runID)
	if err != nil || !found || after.CandidateSHA256 != before.CandidateSHA256 || after.Candidate != before.Candidate || requests.Load() != int32(corrections+1) {
		t.Fatal("restart changed baseline or silently resumed model", found, err, requests.Load())
	}
	if restartOnly {
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		app, err = New(options)
		if err != nil {
			t.Fatal(err)
		}
		app.Startup(context.Background())
		after, found, err = sqlite.NewRunRepository(app.store.DB()).PlannerCheckpoint(context.Background(), runID)
		if err != nil || !found || !chat.SamePlannerCheckpoint(after, before) || after.RequestInFlight || requests.Load() != int32(corrections+1) {
			t.Fatal("application restart changed audit or silently called model", found, err, requests.Load())
		}
		detail, err := app.WorkflowFacade.GetRun(project.ID, started.Run.ID)
		if err != nil || (detail.Run.Status != workflow.RunInterrupted && detail.Run.Status != workflow.RunRunning) {
			t.Fatal("restart accepted or replaced the waiting Workflow", detail.Run.Status, err)
		}
		snapshot, err := app.ChatFacade.GetRunSnapshot(runID)
		if err != nil || snapshot.Run.Status != chat.RunInterrupted {
			t.Fatal("shutdown did not preserve the interrupted Chat lifecycle", snapshot.Run.Status, err)
		}
		var callStatus string
		if err := app.store.DB().QueryRow(`SELECT status FROM tool_calls WHERE run_id=?`, runID).Scan(&callStatus); err != nil || callStatus != "interrupted" {
			t.Fatal("restart failed to close the unexecuted approval through its original interruption policy", callStatus, err)
		}
		var results int
		if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_results x JOIN tool_calls c ON c.id=x.tool_call_id WHERE c.run_id=?`, runID).Scan(&results); err != nil || results != 0 {
			t.Fatal("restart executed the unapproved fixture tool", results, err)
		}
		return
	}
	pending, err := app.PermissionFacade.ListPendingApprovals(runID)
	if err != nil || len(pending) != 1 || pending[0].ToolName != "builtin.web.search" {
		t.Fatal("expected denied loopback fixture approval", len(pending), err)
	}
	if _, err := app.PermissionFacade.ResolveApproval(permission.ResolveCommand{ApprovalID: pending[0].ID, Allow: false, Scope: permission.ScopeCall}); err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 25*time.Second)
	if final.Run.ErrorCode != wantCode || requests.Load() != int32(corrections+2) || final.AIExecutions[0].ChatRunID != runID || final.AIExecutions[0].Attempt != 1 {
		t.Fatal("resume renewed budget or moved to another attempt", final.Run.ErrorCode, requests.Load(), wantCode)
	}
	var calls, denied int
	if err := app.store.DB().QueryRow(`SELECT count(*),sum(CASE WHEN status='denied' THEN 1 ELSE 0 END) FROM tool_calls WHERE run_id=?`, runID).Scan(&calls, &denied); err != nil || calls != 1 || denied != 1 {
		t.Fatal("approval repeated or executed web I/O", calls, denied, err)
	}
	var raw string
	if err := app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=?`, runID, corrections).Scan(&raw); err != nil || raw != text {
		t.Fatal("original rejected response was rewritten", err)
	}
}

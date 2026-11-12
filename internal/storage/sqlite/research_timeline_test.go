package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestResearchTimelinePreservesDecisionFailureAndRetry(t *testing.T) {
	var calls atomic.Int32
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.timeline", risk: tool.RiskLow, idempotent: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		if calls.Add(1) == 1 {
			return tool.Result{}, fmt.Errorf("first attempt failed")
		}
		return tool.Result{Status: tool.ResultSuccess, Text: "ok", Structured: json.RawMessage(`{"answer":"fixed"}`)}, nil
	}})
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Timeline", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{
		{ID: "confirm", Name: "Confirm research", Kind: workflow.NodeHumanConfirmation, Prompt: "Confirm scope?", Arguments: json.RawMessage(`{}`)},
		{ID: "execute", Name: "Analyze", Kind: workflow.NodeTool, ToolName: "fixture.timeline", Arguments: json.RawMessage(`{}`)},
	}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "confirm", ToPort: "context"}, {FromNode: "$input", FromPort: "topic", ToNode: "execute", ToPort: "query"}}})
	ctx := context.Background()
	started, err := h.runtime.Start(ctx, workflow.StartCommand{ProjectID: h.project.ID, ResearchTaskID: "__new_research_task__", WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"bounded scope"}`), PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	read := func() []workflow.ResearchTimelineEntry {
		t.Helper()
		page, err := h.runtime.ResearchTimeline(ctx, workflow.ResearchTimelineQuery{ProjectID: h.project.ID, TaskID: waiting.Run.ResearchTaskID, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		return page.Entries
	}
	var checkpoint workflow.ResearchTimelineEntry
	for _, entry := range read() {
		if entry.EventType == "workflow.human_confirmation_requested" {
			checkpoint = entry
		}
	}
	if !checkpoint.Active || checkpoint.Step == nil || !strings.Contains(string(checkpoint.Step.Input), "bounded scope") {
		t.Fatal("missing active checkpoint", checkpoint)
	}
	if _, err := h.runtime.Decide(ctx, workflow.HumanDecisionCommand{ProjectID: h.project.ID, RunID: waiting.Run.ID, StepID: waiting.Steps[0].ID, Approved: true, Note: "scope reviewed", Context: json.RawMessage(`{"scope":"unchanged"}`)}); err != nil {
		t.Fatal(err)
	}
	failed := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunFailed)
	var failure workflow.ResearchTimelineEntry
	decisionFound := false
	for _, entry := range read() {
		if entry.ID == checkpoint.ID && (entry.Active || string(entry.Snapshot) != string(checkpoint.Snapshot)) {
			t.Fatal("processed checkpoint changed")
		}
		if entry.EventType == "workflow.human_decided" {
			decisionFound = strings.Contains(string(entry.Snapshot), "scope reviewed") && strings.Contains(string(entry.Snapshot), "unchanged")
		}
		if entry.EventType == "workflow.failed" {
			failure = entry
		}
	}
	if !decisionFound || !failure.Active || failure.Step == nil {
		t.Fatal("decision/failure not captured")
	}
	if _, err := h.runtime.Retry(ctx, workflow.RetryCommand{ProjectID: h.project.ID, RunID: failed.Run.ID, StepID: failed.Steps[1].ID}); err != nil {
		t.Fatal(err)
	}
	waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCompleted)
	tools := 0
	for _, entry := range read() {
		if entry.ID == failure.ID && (entry.Active || string(entry.Snapshot) != string(failure.Snapshot)) {
			t.Fatal("retry changed failure evidence")
		}
		if entry.Tool != nil {
			tools++
		}
	}
	if tools != 2 {
		t.Fatal("tool attempts not retained", tools)
	}
	if _, err := h.store.DB().Exec(`UPDATE research_timeline SET snapshot_json='{}'`); err == nil {
		t.Fatal("timeline snapshot mutable")
	}
	// Replay migration against persisted source evidence in this isolated DB.
	if _, err := h.store.DB().Exec(`DROP TRIGGER research_timeline_chat_insert; DROP TRIGGER research_timeline_event_insert; DROP TRIGGER research_timeline_proposal_insert; DROP TRIGGER research_timeline_registration_insert; DROP TABLE research_timeline; DELETE FROM schema_migrations WHERE version=85`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, h.store.DB()); err != nil {
		t.Fatal(err)
	}
	if len(read()) < 5 {
		t.Fatal("migration lost source history")
	}
	if err := h.runtime.Delete(ctx, h.project.ID, started.Run.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM research_timeline`).Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted task retained timeline", count, err)
	}
}

func TestResearchTimelineCancelledAIRecoveryProjection(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Cancellation projection", Nodes: []workflow.Node{
		{ID: "current", Name: "Current AI", Kind: workflow.NodeAIAnalysis, Prompt: "Return summary", PromptVersion: "fixture-v1", OutputSchema: json.RawMessage(`{"type":"object"}`), Arguments: json.RawMessage(`{}`)},
		{ID: "later", Name: "Later checkpoint", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)},
	}, Edges: []workflow.Edge{{FromNode: "current", FromPort: "analysis", ToNode: "later", ToPort: "context"}}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, ModelProfileID: "fixture", ModelID: "fixture", Inputs: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	// No AI executor: produce a durable run and then replay cancellation state
	// only in this temporary DB. The bootstrap test covers live Cancel/restart.
	detail := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunFailed)
	h.runtime.Wait()
	repo := NewWorkflowRuntimeRepository(h.store.DB())
	ctx := context.Background()
	setStatus := func(status string) {
		t.Helper()
		if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET status=?,completed_at=NULL WHERE id=?`, status, detail.Run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET status='cancelled' WHERE workflow_run_id=?`, detail.Run.ID); err != nil {
			t.Fatal(err)
		}
	}
	setStatus("cancelled")
	appendCancellation := func(eventID string, payload json.RawMessage) {
		t.Helper()
		event := workflow.RuntimeEvent{ID: eventID, WorkflowRunID: detail.Run.ID, Type: "workflow.cancel_requested", Payload: payload, CreatedAt: time.Now().UTC()}
		if err := repo.RecordEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	appendCancellation("legacy-empty-cancel", json.RawMessage(`{}`))
	read := func(wantID string) workflow.ResearchTimelinePage {
		t.Helper()
		page, err := repo.ReadResearchTimeline(ctx, workflow.ResearchTimelineQuery{ProjectID: h.project.ID, TaskID: detail.Run.ResearchTaskID, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, entry := range page.Entries {
			if entry.EventType == "workflow.cancel_requested" && entry.Active {
				count++
				if entry.ID != "event:"+wantID || entry.Step == nil || entry.Step.ID != detail.Steps[0].ID {
					t.Fatalf("wrong recovery projection: %+v", entry)
				}
			}
		}
		wantCount := 0
		if wantID != "" {
			wantCount = 1
		}
		if count != wantCount {
			t.Fatalf("active cancellation count=%d, want %d", count, wantCount)
		}
		return page
	}
	read("legacy-empty-cancel")
	appendCancellation("captured-cancel", json.RawMessage(fmt.Sprintf(`{"stepId":%q}`, detail.Steps[0].ID)))
	read("captured-cancel") // empty-payload predecessor must become history too.
	if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET attempt=attempt+1 WHERE id=?`, detail.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	read("") // a captured old attempt is not current authorization.
	appendCancellation("latest-empty-cancel", json.RawMessage(`{}`))
	read("latest-empty-cancel")
	for _, kind := range []workflow.NodeKind{workflow.NodeTool, workflow.NodePython, workflow.NodeHumanConfirmation} {
		if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET node_kind=? WHERE id=?`, string(kind), detail.Steps[0].ID); err != nil {
			t.Fatal(err)
		}
		read("")
	}
	if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET node_kind='ai_analysis' WHERE id=?`, detail.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "queued", "completed", "failed", "interrupted"} {
		setStatus(status)
		read("")
	}
	setStatus("cancelled")
	if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET current_step_ordinal=1 WHERE id=?`, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	read("") // cancelling the later human checkpoint cannot recover the AI.
	if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET current_step_ordinal=0 WHERE id=?`, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	// A newer run of this task makes every earlier run's action historical.
	newer := detail.Run
	newer.ID, newer.ConversationID, newer.Status = "newer-cancelled-run", "newer-conversation", workflow.RunCancelled
	newer.CreatedAt, newer.UpdatedAt = time.Now().UTC().Add(time.Second), time.Now().UTC().Add(time.Second)
	conv := conversation.Conversation{ID: newer.ConversationID, ProjectID: h.project.ID, Title: "Newer run", PermissionMode: newer.PermissionMode, ReasoningLevel: "medium", CreatedAt: newer.CreatedAt, UpdatedAt: newer.UpdatedAt}
	if err := repo.CreateResearchRun(ctx, newer, nil, workflow.RuntimeEvent{ID: "newer-created", WorkflowRunID: newer.ID, Type: "workflow.created", Payload: json.RawMessage(`{}`), CreatedAt: newer.CreatedAt}, conv); err != nil {
		t.Fatal(err)
	}
	read("")
}

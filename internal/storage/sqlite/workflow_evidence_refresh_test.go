package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestEvidenceRefreshIsAtomicAndRejectsStaleAttempt(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Evidence checkpoint", Nodes: []workflow.Node{
		{ID: "evidence_import", Name: "Import", Kind: workflow.NodeHumanConfirmation, Prompt: "fixture", Arguments: json.RawMessage(`{}`)},
		{ID: "evidence_sync", Name: "Sync", Kind: workflow.NodeHumanConfirmation, Prompt: "fixture", Arguments: json.RawMessage(`{}`)},
		{ID: "evidence_search", Name: "Search", Kind: workflow.NodeHumanConfirmation, Prompt: "fixture", Arguments: json.RawMessage(`{}`)},
		{ID: "evidence_screening", Name: "Synthesis", Kind: workflow.NodeHumanConfirmation, Prompt: "fixture", Arguments: json.RawMessage(`{}`)},
		{ID: "report", Name: "Report", Kind: workflow.NodeHumanConfirmation, Prompt: "fixture", Arguments: json.RawMessage(`{}`)},
	}, Edges: []workflow.Edge{
		{FromNode: "evidence_import", FromPort: "approved", ToNode: "evidence_sync", ToPort: "context"},
		{FromNode: "evidence_sync", FromPort: "approved", ToNode: "evidence_search", ToPort: "context"},
		{FromNode: "evidence_search", FromPort: "approved", ToNode: "evidence_screening", ToPort: "context"},
		{FromNode: "evidence_screening", FromPort: "approved", ToNode: "report", ToPort: "context"},
	}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, ResearchTaskID: workflow.NewResearchTaskID, Inputs: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	if err := h.runtime.Close(); err != nil {
		t.Fatal(err)
	}
	db := h.store.DB()
	// Establish the precise pre-commit state in this isolated database.
	_, err = db.Exec("UPDATE workflow_runs SET status='running',current_step_ordinal=3 WHERE id=?", waiting.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE workflow_steps SET status=CASE WHEN ordinal<3 THEN 'completed' WHEN ordinal=3 THEN 'running' ELSE 'queued' END,attempt=1 WHERE workflow_run_id=?", waiting.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkflowRuntimeRepository(db)
	event := workflow.RuntimeEvent{ID: "literature-checkpoint", WorkflowRunID: waiting.Run.ID, Type: "workflow.evidence_refreshed", Payload: json.RawMessage(`{"state":{"round":1}}`), CreatedAt: time.Now().UTC()}
	if err := repo.QueueEvidenceRefresh(context.Background(), waiting.Run.ID, waiting.Steps[0].ID, waiting.Steps[3].ID, 2, event.CreatedAt, event); err == nil {
		t.Fatal("stale attempt accepted")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM workflow_events WHERE id=?", event.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed transition wrote checkpoint", err, count)
	}
	for _, state := range []struct {
		status string
		cancel int
	}{{"paused", 0}, {"cancelled", 1}, {"running", 1}} {
		if _, err := db.Exec(`UPDATE workflow_runs SET status=?,cancel_requested=? WHERE id=?`, state.status, state.cancel, waiting.Run.ID); err != nil {
			t.Fatal(err)
		}
		if err := repo.QueueEvidenceRefresh(context.Background(), waiting.Run.ID, waiting.Steps[0].ID, waiting.Steps[3].ID, 1, event.CreatedAt, event); err == nil {
			t.Fatalf("user stop overwritten: %+v", state)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM workflow_events WHERE id=?", event.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("rejected refresh wrote event", err, count)
		}
	}
	if _, err := db.Exec(`UPDATE workflow_runs SET status='running',cancel_requested=0 WHERE id=?`, waiting.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.QueueEvidenceRefresh(context.Background(), waiting.Run.ID, waiting.Steps[0].ID, waiting.Steps[3].ID, 1, event.CreatedAt, event); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewWorkflowRuntimeRepository(db).GetRun(context.Background(), h.project.ID, waiting.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Run.Status != workflow.RunQueued || recovered.Run.CurrentStep != 0 || recovered.Steps[0].Status != workflow.StepQueued || recovered.Steps[3].Status != workflow.StepQueued || recovered.Steps[4].Attempt != 1 {
		t.Fatal("bad recovered checkpoint", recovered.Run)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM research_timeline WHERE source_id=? AND event_type='workflow.evidence_refreshed'", event.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("refresh missing from timeline", err, count)
	}
	if err := repo.QueueEvidenceRefresh(context.Background(), waiting.Run.ID, waiting.Steps[0].ID, waiting.Steps[3].ID, 1, event.CreatedAt, event); err == nil {
		t.Fatal("duplicate rewind accepted")
	}
}

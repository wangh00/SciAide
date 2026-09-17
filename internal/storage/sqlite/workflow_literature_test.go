package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestLiteratureContinuationIsAtomicAndRejectsStaleAttempt(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Literature checkpoint", Nodes: []workflow.Node{
		{ID: "discovery", Name: "Discovery", Kind: workflow.NodeHumanConfirmation, Prompt: "fixture", Arguments: json.RawMessage(`{}`)},
		{ID: "screening", Name: "Screening", Kind: workflow.NodeHumanConfirmation, Prompt: "fixture", Arguments: json.RawMessage(`{}`)},
	}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	if err := h.runtime.Close(); err != nil {
		t.Fatal(err)
	}
	db := h.store.DB()
	// Establish the precise pre-commit state in this isolated database.
	_, err = db.Exec("UPDATE workflow_runs SET status='running',current_step_ordinal=1 WHERE id=?", waiting.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE workflow_steps SET status=CASE WHEN ordinal=0 THEN 'completed' ELSE 'running' END,attempt=1 WHERE workflow_run_id=?", waiting.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewWorkflowRuntimeRepository(db)
	event := workflow.RuntimeEvent{ID: "literature-checkpoint", WorkflowRunID: waiting.Run.ID, Type: "workflow.literature_checkpoint", Payload: json.RawMessage(`{"state":{"round":1}}`), CreatedAt: time.Now().UTC()}
	if err := repo.QueueLiteratureContinuation(context.Background(), waiting.Run.ID, waiting.Steps[1].ID, waiting.Steps[0].ID, 2, event.CreatedAt, event); err == nil {
		t.Fatal("stale attempt accepted")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM workflow_events WHERE id=?", event.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed transition wrote checkpoint", err, count)
	}
	if err := repo.QueueLiteratureContinuation(context.Background(), waiting.Run.ID, waiting.Steps[1].ID, waiting.Steps[0].ID, 1, event.CreatedAt, event); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewWorkflowRuntimeRepository(db).GetRun(context.Background(), h.project.ID, waiting.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Run.Status != workflow.RunQueued || recovered.Run.CurrentStep != 0 || recovered.Steps[0].Status != workflow.StepQueued || recovered.Steps[1].Status != workflow.StepQueued {
		t.Fatal("bad recovered checkpoint", recovered.Run)
	}
	if err := repo.QueueLiteratureContinuation(context.Background(), waiting.Run.ID, waiting.Steps[1].ID, waiting.Steps[0].ID, 1, event.CreatedAt, event); err == nil {
		t.Fatal("duplicate rewind accepted")
	}
}

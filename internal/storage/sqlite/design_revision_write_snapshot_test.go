package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestPrivateDesignRevisionSnapshotIntegrityAndCascade(t *testing.T) {
	ctx := context.Background()
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.snapshot", risk: tool.RiskLow, idempotent: true})
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Private revision snapshot", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{{ID: "search", Name: "Search", Kind: workflow.NodeTool, ToolName: "fixture.snapshot", Arguments: json.RawMessage(`{}`)}}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "search", ToPort: "query"}}, Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "search", FromPort: "structured.answer", Required: true}}})
	started, err := h.runtime.Start(ctx, workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"topic":"fixture"}`)})
	if err != nil {
		t.Fatal(err)
	}
	d := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunCompleted)
	bytes, err := json.Marshal(map[string]any{"run": d.Run, "steps": d.Steps})
	if err != nil {
		t.Fatal(err)
	}
	digest := workflow.ResearchRevisionSnapshot(d)
	for _, row := range []struct{ id, hash string }{{"valid", digest}, {"bad-column", "changed"}} {
		if _, err := h.store.DB().Exec(`INSERT INTO workflow_revision_write_snapshots(event_id,workflow_run_id,snapshot_sha256,snapshot_json) VALUES(?,?,?,?)`, row.id, d.Run.ID, row.hash, string(bytes)); err != nil {
			t.Fatal(err)
		}
	}
	r := NewWorkflowRuntimeRepository(h.store.DB())
	got, err := r.ReadDesignRevisionWriteSnapshot(ctx, d.Run.ID, "valid")
	if err != nil || string(got) != string(bytes) {
		t.Fatal("private snapshot changed", err)
	}
	if _, err := r.ReadDesignRevisionWriteSnapshot(ctx, d.Run.ID, "bad-column"); err == nil {
		t.Fatal("corrupt stored digest was ignored")
	}
	if _, err := r.ReadDesignRevisionWriteSnapshot(ctx, "different-run", "valid"); err == nil {
		t.Fatal("snapshot crossed task boundary")
	}
	if _, err := h.store.DB().Exec(`UPDATE workflow_revision_write_snapshots SET snapshot_sha256='changed' WHERE event_id='valid'`); err == nil {
		t.Fatal("private snapshot not immutable")
	}
	if err := r.DeleteRun(ctx, h.project.ID, d.Run.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.store.DB().QueryRow(`SELECT count(*) FROM workflow_revision_write_snapshots`).Scan(&count); err != nil || count != 0 {
		t.Fatal("private snapshot leaked after run deletion", count, err)
	}
}

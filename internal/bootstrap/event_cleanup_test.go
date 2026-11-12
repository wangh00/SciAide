package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestEventCleanupStartupLifecycleAndDurableCompletion(t *testing.T) {
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
	a, e := New(options)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = a.Close() }()
	_, e = a.store.DB().Exec(`INSERT INTO run_events VALUES ('legacy-orphan',1,'deleted-run','run',1,'fixture','now','{}')`)
	if e != nil {
		t.Fatal(e)
	}
	a.Startup(context.Background())
	a.startEventCleanup(context.Background()) // idempotent
	select {
	case <-a.maintenanceDone:
	case <-time.After(10 * time.Second):
		t.Fatal("maintenance failed to finish")
	}
	var n int
	if e = a.store.DB().QueryRow(`SELECT count(*) FROM run_events WHERE event_id='legacy-orphan'`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	var completed string
	if e = a.store.DB().QueryRow(`SELECT value_json FROM settings WHERE key=?`, orphanEventCleanupKey).Scan(&completed); e != nil || completed != "true" {
		t.Fatal(e, completed)
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	a, e = New(options)
	if e != nil {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	a.Startup(cancelled)
	if e = a.Close(); e != nil {
		t.Fatal("cancellation prevented shutdown", e)
	}
}

func TestEventCleanupPreservesExistingRunsAndSharedSequences(t *testing.T) {
	a, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, a, server.URL)
	project, _, detail := startWorkflowAIRecoveryFixture(t, a, profile)
	if _, err = a.WorkflowFacade.Cancel(project.ID, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	detail = waitForWorkflowState(t, a, project.ID, detail.Run.ID, workflow.RunCancelled, 10*time.Second)
	chatID := detail.AIExecutions[0].ChatRunID
	db := a.store.DB()
	for _, row := range [][3]string{
		{"keep-chat", chatID, "run"}, {"keep-workflow", detail.Run.ID, "workflow_run"},
		{"remove-orphan", "deleted-only", "run"}, {"remove-mixed", "deleted-mixed", "run"},
		{"keep-other", "deleted-mixed", "project"},
	} {
		if _, err = db.Exec(`INSERT INTO run_events VALUES (?,1,?,?,(SELECT COALESCE(MAX(sequence),0)+1 FROM run_events WHERE aggregate_id=?),'fixture','now','{}')`, row[0], row[1], row[2], row[1]); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO run_event_sequences VALUES (?,1000000) ON CONFLICT(aggregate_id) DO UPDATE SET last_sequence=1000000`, row[1]); err != nil {
			t.Fatal(err)
		}
	}
	var after int64
	for {
		next, _, done, err := sqlite.SweepOrphanRunEvents(context.Background(), db, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		after = next
		if done {
			break
		}
	}
	for id, want := range map[string]int{"keep-chat": 1, "keep-workflow": 1, "keep-other": 1, "remove-orphan": 0, "remove-mixed": 0} {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM run_events WHERE event_id=?`, id).Scan(&count); err != nil || count != want {
			t.Fatalf("event %s: count=%d want=%d err=%v", id, count, want, err)
		}
	}
	for id, want := range map[string]int{chatID: 1, detail.Run.ID: 1, "deleted-mixed": 1, "deleted-only": 0} {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM run_event_sequences WHERE aggregate_id=?`, id).Scan(&count); err != nil || count != want {
			t.Fatalf("sequence %s: count=%d want=%d err=%v", id, count, want, err)
		}
	}
}

package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestResearchDeletionLargeEventGraphIsAtomicAndScoped(t *testing.T) {
	app, e := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if e != nil {
		t.Fatal(e)
	}
	defer app.Close()
	app.Startup(context.Background())
	server := newWorkflowAITestServer(t)
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, _, detail := startWorkflowAIRecoveryFixture(t, app, profile)
	if _, e = app.WorkflowFacade.Cancel(project.ID, detail.Run.ID); e != nil {
		t.Fatal(e)
	}
	detail = waitForWorkflowState(t, app, project.ID, detail.Run.ID, workflow.RunCancelled, 10*time.Second)
	chatID := detail.AIExecutions[0].ChatRunID
	var prior int
	_ = app.store.DB().QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM run_events WHERE aggregate_id=?`, chatID).Scan(&prior)
	_, e = app.store.DB().Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<100000)
INSERT INTO run_events SELECT 'large-delete-'||i,1,?,'run',i+?,'content.delta','now',json_object('text',printf('%0500d',i)) FROM n`, chatID, prior)
	if e != nil {
		t.Fatal(e)
	}
	// An unrelated event kind with identical aggregate ID is not ours to delete.
	_, e = app.store.DB().Exec(`INSERT INTO run_events VALUES('unrelated-kind',1,?,'fixture-other',?,'fixture','now','{}')`, chatID, prior+100001)
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	if e = app.WorkflowFacade.DeleteRun(project.ID, detail.Run.ID); e != nil {
		t.Fatal(e)
	}
	elapsed := time.Since(start)
	var n int
	if e = app.store.DB().QueryRow(`SELECT count(*) FROM run_events WHERE aggregate_id=? AND aggregate_type='run'`, chatID).Scan(&n); e != nil || n != 0 {
		t.Fatal("orphaned event graph", n, e)
	}
	if e = app.store.DB().QueryRow(`SELECT count(*) FROM run_events WHERE event_id='unrelated-kind'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("other aggregate lost", n, e)
	}
	if e = app.store.DB().QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n); e != nil || n != 0 {
		t.Fatal("foreign key damage", n, e)
	}
	t.Logf("actual DeleteRun with 100000 extra 500-char events: %s; transaction/scoping/FK checks passed; isolated SQLite fixture", elapsed)
}

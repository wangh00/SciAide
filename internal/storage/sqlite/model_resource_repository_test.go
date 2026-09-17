package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/resource"
)

func TestModelResourceRepositoryReopenIntegrityAndCascade(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	var sequence int
	var dbName, path string
	if err := store.DB().QueryRowContext(ctx, `PRAGMA database_list`).Scan(&sequence, &dbName, &path); err != nil {
		t.Fatal(err)
	}
	projectID, err := NewRunRepository(store.DB()).ProjectIDForRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewModelResourceRepository(store.DB())
	scope := resource.Scope{RunID: run.ID, ProjectID: projectID, TaskID: "task", WorkflowStepID: "step", ToolContracts: map[string]string{"builtin.workspace.read_text": strings.Repeat("a", 64)}}
	session := resource.Session{Scope: scope, ScopeHash: scope.Hash(), Facts: json.RawMessage(`{"inputState":"resources_present"}`), CreatedAt: time.Now()}
	seed := resource.Seed{Label: "读取中文资料", Kind: "workspace_read", ToolName: "builtin.workspace.read_text", Arguments: json.RawMessage(`{"path":"中文 数据.csv","offset":0}`)}
	action := resource.NewAction(scope, seed, true, time.Now())
	if err = repo.CreateSession(ctx, session, []resource.Action{action}); err != nil {
		t.Fatal(err)
	}
	if err = repo.CreateSession(ctx, session, nil); err == nil {
		t.Fatal("overwrote immutable session")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	repo = NewModelResourceRepository(reopened.DB())
	saved, found, err := repo.GetSession(ctx, run.ID)
	if err != nil || !found || saved.ScopeHash != scope.Hash() {
		t.Fatalf("reopen session: %v", err)
	}
	actual, found, err := repo.GetAction(ctx, run.ID, action.ID)
	if err != nil || !found || actual.Validate(scope) != nil || string(actual.Seed.Arguments) != string(seed.Arguments) {
		t.Fatalf("reopen action: %+v %v", actual, err)
	}
	if _, found, err = repo.GetAction(ctx, "other-run", action.ID); err != nil || found {
		t.Fatal("cross-run lookup resolved")
	}
	changed := scope
	changed.TaskID = "different"
	if err = repo.PutActions(ctx, changed, []resource.Action{resource.NewAction(changed, seed, false, time.Now())}); err == nil {
		t.Fatal("accepted rebound scope")
	}
	if _, err = reopened.DB().ExecContext(ctx, `UPDATE model_resource_sessions SET facts_json='{}' WHERE run_id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = repo.GetSession(ctx, run.ID); err == nil {
		t.Fatal("tampered input facts accepted")
	}
	if _, err = reopened.DB().ExecContext(ctx, `DELETE FROM runs WHERE id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"model_resource_sessions", "model_resource_actions"} {
		var count int
		if err = reopened.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("catalog survived run deletion: %s %d %v", table, count, err)
		}
	}
}

func TestModelResourceCapabilitiesAreNotExportedOrReissuedByArchive(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	projectID, err := NewRunRepository(store.DB()).ProjectIDForRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewModelResourceRepository(store.DB())
	scope := resource.Scope{RunID: run.ID, ProjectID: projectID, TaskID: "task", WorkflowStepID: "step", ToolContracts: map[string]string{}}
	s := resource.Session{Scope: scope, ScopeHash: scope.Hash(), Facts: json.RawMessage(`{}`), CreatedAt: time.Now()}
	a := resource.NewAction(scope, resource.Seed{Kind: "menu", Label: "private action"}, true, time.Now())
	if err = repo.CreateSession(ctx, s, []resource.Action{a}); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual export pruning policy on a disposable SQLite fixture.
	if err = pruneArchiveSnapshot(ctx, store.DB(), projectID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"model_resource_actions", "model_resource_sessions"} {
		var count int
		if err = store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("export retained resource capabilities: %s %d %v", table, count, err)
		}
	}
	if _, found, err := repo.GetAction(ctx, run.ID, a.ID); err != nil || found {
		t.Fatal("export still resolves resource handle")
	}
}

func TestModelResourceDiscoveryDoesNotEvictIssuedActions(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	projectID, err := NewRunRepository(store.DB()).ProjectIDForRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewModelResourceRepository(store.DB())
	scope := resource.Scope{RunID: run.ID, ProjectID: projectID, WorkflowStepID: "step", ToolContracts: map[string]string{}}
	if err = repo.CreateSession(ctx, resource.Session{Scope: scope, ScopeHash: scope.Hash(), Facts: json.RawMessage(`{}`), CreatedAt: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	first := ""
	for i := 0; i < 3; i++ {
		actions := []resource.Action{}
		for j := 0; j < 70; j++ {
			a := resource.NewAction(scope, resource.Seed{Kind: "menu", Label: fmt.Sprintf("item-%d-%d", i, j)}, false, time.Now())
			if first == "" {
				first = a.ID
			}
			actions = append(actions, a)
		}
		if err = repo.PutActions(ctx, scope, actions); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := repo.ListActions(ctx, run.ID, resource.MaxSessionActions+1)
	if err != nil || len(saved) != 210 {
		t.Fatalf("%d %v", len(saved), err)
	}
	// A new repository instance (resume) resolves the first issued capability.
	if a, ok, err := NewModelResourceRepository(store.DB()).GetAction(ctx, run.ID, first); err != nil || !ok || a.Validate(scope) != nil {
		t.Fatal("old action expired", err)
	}
}

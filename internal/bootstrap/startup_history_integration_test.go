package bootstrap

import (
	"os"
	"testing"

	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

// Opt-in full bootstrap against an isolated historical database home. The
// caller must relocate workspaces and disable external connectors beforehand.
// This verifies the real startup/facade path, not a native Wails window.
func TestIsolatedHistoricalHomeStartsAndReadsExistingTasks(t *testing.T) {
	root := os.Getenv("SCIAIDE_TEST_ISOLATED_HISTORY_HOME")
	if root == "" {
		t.Skip("set SCIAIDE_TEST_ISOLATED_HISTORY_HOME to an isolated, disabled-connector backup home")
	}
	app, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	projects, err := app.ProjectFacade.ListProjects()
	if err != nil || len(projects) != 1 {
		t.Fatalf("historical project: count=%d, %v", len(projects), err)
	}
	conversations, err := app.ConversationFacade.ListConversations(projects[0].ID)
	// Six workflow-bound conversations intentionally live in task timelines,
	// not in the four-item ordinary-chat list.
	if err != nil || len(conversations) != 4 {
		t.Fatalf("historical conversations: count=%d, %v", len(conversations), err)
	}
	rows, err := app.store.DB().Query(`SELECT id FROM conversations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 10 {
		t.Fatalf("historical total conversations=%d", len(ids))
	}
	tasks, err := app.WorkflowFacade.ListResearchTasks(projects[0].ID, 200)
	if err != nil || len(tasks) != 27 {
		t.Fatalf("historical task list=%d, %v", len(tasks), err)
	}
	loadedRuns := 0
	for _, task := range tasks {
		if task.LatestRunID == "" {
			continue
		}
		if _, err := app.WorkflowFacade.GetRun(projects[0].ID, task.LatestRunID); err != nil {
			t.Fatalf("read historical task run: %v", err)
		}
		loadedRuns++
	}
	if loadedRuns != 3 {
		t.Fatalf("historical tasks with current runs=%d", loadedRuns)
	}
	// Six runs belong to three tasks (initial routes and revision history).
	rows, err = app.store.DB().Query(`SELECT id FROM workflow_runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var runIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(runIDs) != 6 {
		t.Fatalf("historical total workflow runs=%d", len(runIDs))
	}
	for _, id := range runIDs {
		if _, err := app.WorkflowFacade.GetRun(projects[0].ID, id); err != nil {
			t.Fatalf("read historical route/revision: %v", err)
		}
	}
	messages := 0
	for _, id := range ids {
		items, err := app.ConversationFacade.ListMessages(id)
		if err != nil {
			t.Fatal(err)
		}
		messages += len(items)
	}
	if messages != 172 {
		t.Fatalf("historical message count=%d", messages)
	}
	for table, want := range map[string]int{"research_tasks": 27, "workflow_runs": 6, "artifact_versions": 47, "artifact_lineage": 152} {
		var count int
		if err := app.store.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatalf("historical %s: count=%d, %v", table, count, err)
		}
	}
	var latest int
	currentVersion, err := sqlite.CurrentSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.DB().QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&latest); err != nil || latest != currentVersion {
		t.Fatal("historical database not upgraded", latest, err)
	}
	t.Logf("full bootstrap succeeded; projects=%d ordinary chats=%d total conversations=%d messages=%d; tasks/runs/artifacts retained", len(projects), len(conversations), len(ids), messages)
}

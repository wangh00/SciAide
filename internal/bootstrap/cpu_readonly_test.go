package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/app/workflowai"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

// Opt-in inspection only: never boot the app, migrate storage, or call a model.
func TestCPUReadOnlyActivityComparison(t *testing.T) {
	path, runID := os.Getenv("SCIAIDE_CPU_DB"), os.Getenv("SCIAIDE_CPU_WORKFLOW")
	if path == "" || runID == "" {
		t.Skip("requires explicit read-only task fixture")
	}
	db, err := sqlite.OpenExisting(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	var projectID string
	if err := db.QueryRow(`SELECT project_id FROM workflow_runs WHERE id=?`, runID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	detail, err := sqlite.NewWorkflowRuntimeRepository(db).GetRun(ctx, projectID, runID)
	if err != nil {
		t.Fatal(err)
	}
	runs := sqlite.NewRunRepository(db)
	conversations := sqlite.NewConversationRepository(db)
	service := chat.NewService(runs, conversations, runs, nil)
	service.SetSnapshotToolCalls(sqlite.NewToolRepository(db))
	service.SetSnapshotRunSteps(runs)
	bridge, err := workflowai.New(service, conversations)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range detail.AIExecutions {
		full, err := service.Snapshot(ctx, e.ChatRunID)
		if err != nil {
			t.Fatal(err)
		}
		light, err := service.ActivitySnapshot(ctx, e.ChatRunID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(full.Run, light.Run) || !reflect.DeepEqual(full.ToolCalls, light.ToolCalls) {
			t.Fatal("activity state changed")
		}
		scoped, err := service.StageSnapshot(ctx, e.ChatRunID)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range scoped.Messages {
			if m.RunID != e.ChatRunID {
				t.Fatal("stage read unrelated message")
			}
		}
		a, err := bridge.Activity(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		b, err := bridge.Activity(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatal("cached activity differs")
		}
	}
	measure := func(name string, f func(workflow.AIExecution)) {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		for pass := 0; pass < 3; pass++ {
			for _, e := range detail.AIExecutions {
				f(e)
			}
		}
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		t.Logf("%s: stages=%d passes=3 elapsed=%v allocations=%d bytes=%d", name, len(detail.AIExecutions), elapsed, after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc)
	}
	measure("full conversation snapshot per stage", func(e workflow.AIExecution) {
		if _, err := service.Snapshot(ctx, e.ChatRunID); err != nil {
			t.Fatal(err)
		}
	})
	measure("versioned activity projection", func(e workflow.AIExecution) {
		if _, err := bridge.Activity(ctx, e); err != nil {
			t.Fatal(err)
		}
	})
	before, _ := json.Marshal(detail)
	other, err := sqlite.NewWorkflowRuntimeRepository(db).GetRun(ctx, projectID, runID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(other)
	if string(before) != string(after) {
		t.Fatal("task changed during read-only comparison")
	}
}

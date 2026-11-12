package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
)

func TestRuntimeActivityRecoveryReadsLatestEventOnlyForRequestedRun(t *testing.T) {
	db, path := workflowReasoningHistory(t)
	ctx := context.Background()
	r := NewRunRepository(db)
	if _, found, err := r.LatestRuntimeActivity(ctx, "chat"); found || err != nil {
		t.Fatal(found, err)
	}
	for i, phase := range []string{"model_wait", "compaction"} {
		data, _ := json.Marshal(map[string]any{"activity": chat.RuntimeActivity{RunID: "chat", TurnIndex: 4, Phase: phase, StartedAt: time.Now().UTC()}})
		if _, err := db.Exec(`INSERT INTO run_events VALUES (?,1,'chat','run',?,'run.activity','2026-10-09',?)`, phase, i+1, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	// Close/reopen the real database: no in-memory observer state survives.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := OpenExisting(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r = NewRunRepository(db)
	a, found, err := r.LatestRuntimeActivity(ctx, "chat")
	if err != nil || !found || a.Phase != "compaction" || a.TurnIndex != 4 {
		t.Fatal(a, found, err)
	}
	if _, found, err := r.LatestRuntimeActivity(ctx, "other-chat"); found || err != nil {
		t.Fatal("cross-run activity", found, err)
	}
}

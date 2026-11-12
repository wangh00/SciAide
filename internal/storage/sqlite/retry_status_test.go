package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
)

func TestRetryStatusRestoresReceivingAndClearsOnActualCompletion(t *testing.T) {
	db, path := workflowReasoningHistory(t)
	defer func() { db.Close() }()
	r := NewRunRepository(db)
	ctx := context.Background()
	insert := func(id, kind string, sequence int, retry *chat.ModelRetryStatus) {
		data, _ := json.Marshal(map[string]any{"retry": retry})
		if _, err := db.Exec(`INSERT INTO run_events VALUES (?,1,'chat','run',?,?, '2026-10-11',?)`, id, sequence, kind, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	if _, sequence, found, err := r.LatestModelRetryStatus(ctx, "chat"); found || sequence != 0 || err != nil {
		t.Fatal(found, err)
	}
	insert("wait", "run.retrying", 1, &chat.ModelRetryStatus{Phase: "stream", Reason: "stream_stalled", Attempt: 2, MaxAttempts: 5, Message: "model stalled"})
	insert("receiving", "run.retrying", 2, &chat.ModelRetryStatus{Phase: "stream", State: "receiving", Attempt: 2, MaxAttempts: 5, Message: "reconnected, incomplete"})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	db, err = OpenExisting(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	r = NewRunRepository(db)
	status, sequence, found, err := r.LatestModelRetryStatus(ctx, "chat")
	if err != nil || !found || sequence != 2 || status.State != "receiving" || status.Attempt != 2 {
		t.Fatal(status, found, err)
	}
	if _, _, found, err := r.LatestModelRetryStatus(ctx, "other-chat"); found || err != nil {
		t.Fatal("cross run", found, err)
	}
	insert("unrelated", "run.activity", 3, nil)
	if status, sequence, found, err := r.LatestModelRetryStatus(ctx, "chat"); !found || sequence != 2 || status.State != "receiving" || err != nil {
		t.Fatal("unrelated event changed retry revision", status, sequence, found, err)
	}
	for i, kind := range []string{"run.retry.recovered", "run.completed", "run.failed", "run.cancelled", "run.interrupted"} {
		insert(kind+"-retry", "run.retrying", 4+i*2, &chat.ModelRetryStatus{Attempt: 1, MaxAttempts: 5, Message: "retry"})
		insert(kind, kind, 5+i*2, nil)
		if _, sequence, found, err := r.LatestModelRetryStatus(ctx, "chat"); found || sequence != int64(5+i*2) || err != nil {
			t.Fatal(kind, found, err)
		}
	}
}

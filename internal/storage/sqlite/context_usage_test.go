package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
)

func TestContextUsagePersistenceRestoresOnlyMatchingSuccessfulConversation(t *testing.T) {
	db, path := workflowReasoningHistory(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := NewRunRepository(db)
	run, err := r.Get(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := func(turn int, kind string, input int) {
		t.Helper()
		_, _, err := r.RecordModelUsage(ctx, chat.RequestUsage{ID: fmt.Sprintf("chat:%d", turn), RunID: "chat", TurnIndex: turn, RequestKind: kind, InputTokens: input, StatusCode: 200, StartedAt: now, CompletedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	value := chat.ContextUsageObservation{Version: 1, RunID: "chat", TurnIndex: 1, ModelProfileID: run.ModelProfileID, ModelID: run.ModelID, APIProtocol: run.APIProtocol, InputTokens: 55824, CharacterEstimate: 244000, Components: map[string]int{strings.Repeat("a", 64): 2}}
	value.StageSourceIDs = []string{strings.Repeat("b", 64)}
	if err := r.SaveContextUsage(ctx, value); err == nil {
		t.Fatal("observation without successful request saved")
	}
	record(1, "conversation", 55824)
	if err := r.SaveContextUsage(ctx, value); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload_json FROM run_events WHERE event_type='run.context_usage'`).Scan(&payload); err != nil || strings.Contains(payload, "encrypted_content") || strings.Contains(payload, "messages") {
		t.Fatal("observation event contains protocol/content", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenExisting(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r = NewRunRepository(db)
	loaded, found, err := r.LatestContextUsage(ctx, "chat")
	if err != nil || !found || loaded.InputTokens != value.InputTokens || loaded.Components[strings.Repeat("a", 64)] != 2 || len(loaded.StageSourceIDs) != 1 || loaded.StageSourceIDs[0] != value.StageSourceIDs[0] {
		t.Fatal("restart lost usage anchor", found, err)
	}
	if _, found, err := r.LatestContextUsage(ctx, "other-chat"); found || err != nil {
		t.Fatal("anchor crossed chat run", err)
	}
	record(2, "compaction", 55824)
	bad := value
	bad.TurnIndex = 2
	if err := r.SaveContextUsage(ctx, bad); err == nil {
		t.Fatal("compaction usage used to calibrate normal conversation")
	}
	bad = value
	bad.InputTokens = 1
	if err := r.SaveContextUsage(ctx, bad); err == nil {
		t.Fatal("observation does not match real input usage")
	}
	bad = value
	bad.ModelID = "another-model"
	if err := r.SaveContextUsage(ctx, bad); err == nil {
		t.Fatal("observation model mismatch accepted")
	}
	if _, err := db.Exec(`UPDATE model_request_usage SET model_id='different-wire-model' WHERE run_id='chat' AND turn_index=1`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LatestContextUsage(ctx, "chat"); found || err != nil {
		t.Fatal("wire model mismatch accepted", err)
	}
	if _, err := db.Exec(`UPDATE model_request_usage SET model_id=? WHERE run_id='chat' AND turn_index=1`, run.ModelID); err != nil {
		t.Fatal(err)
	}
	// Restart must not resurrect a previous successful anchor after a later
	// ordinary request failed, omitted usage, or has not saved its anchor.
	for _, newer := range []struct {
		name   string
		input  int
		status int
		code   string
	}{{"missing usage", 0, 200, ""}, {"failed stream", 0, 502, "MODEL_STREAM_FAILED"}, {"unsaved latest anchor", 60000, 200, ""}} {
		if _, err := db.Exec(`UPDATE model_request_usage SET request_kind='conversation',input_tokens=?,status_code=?,error_code=? WHERE run_id='chat' AND turn_index=2`, newer.input, newer.status, newer.code); err != nil {
			t.Fatal(err)
		}
		if _, found, err := r.LatestContextUsage(ctx, "chat"); found || err != nil {
			t.Fatal("stale observation restored after", newer.name, err)
		}
		if err := r.SaveContextUsage(ctx, value); err == nil {
			t.Fatal("stale observation saved after", newer.name)
		}
	}
	if _, err := db.Exec(`UPDATE model_request_usage SET request_kind='compaction',input_tokens=55824,status_code=200,error_code='' WHERE run_id='chat' AND turn_index=2`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LatestContextUsage(ctx, "chat"); !found || err != nil {
		t.Fatal("summary usage invalidated normal request anchor", err)
	}
	// Corrupt/unknown new-version metadata falls back rather than reusing an
	// older record. Original model_request_usage and dialogue remain untouched.
	data, _ := json.Marshal(map[string]any{"version": 99})
	if _, err := db.Exec(`INSERT INTO run_events VALUES ('corrupt-usage',1,'chat','run',999,'run.context_usage','2026-10-09',?)`, string(data)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LatestContextUsage(ctx, "chat"); found || err != nil {
		t.Fatal("unknown metadata version did not fall back", err)
	}
	if _, err := db.Exec(`UPDATE run_events SET payload_json=? WHERE event_id='corrupt-usage'`, payload); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LatestContextUsage(ctx, "chat"); !found || err != nil {
		t.Fatal("valid latest metadata not restored", err)
	}
	checkpoint := contextmemory.Checkpoint{ConversationID: run.ConversationID, ThroughMessageID: run.UserMessageID, Summary: "preserved context", SourceMessageCount: 1, SourceEstimatedTokens: 100, ModelProfileID: run.ModelProfileID, ModelID: run.ModelID, APIProtocol: run.APIProtocol}
	if _, err := contextmemory.NewService(NewContextCheckpointRepository(db)).Save(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.LatestContextUsage(ctx, "chat"); found || err != nil {
		t.Fatal("checkpoint replacement resurrected pre-compaction anchor", err)
	}
	assertDatabaseHealthy(t, db)
}

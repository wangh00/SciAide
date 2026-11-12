package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestOrphanEventSweepIsBoundedResumableAndPreservesOtherAggregates(t *testing.T) {
	ctx := context.Background()
	s, e := Open(ctx, filepath.Join(t.TempDir(), "events.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	tx, e := s.DB().BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2505; i++ {
		kind := "run"
		if i%2 == 0 {
			kind = "workflow_run"
		}
		if _, e = tx.Exec(`INSERT INTO run_events(event_id,version,aggregate_id,aggregate_type,sequence,event_type,timestamp,payload_json) VALUES (?,1,?,?,1,'fixture','now','{}')`, fmt.Sprint(i), fmt.Sprint(i), kind); e != nil {
			t.Fatal(e)
		}
	}
	_, e = tx.Exec(`INSERT INTO run_events VALUES ('keep',1,'absent','project',1,'fixture','now','{}')`)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var after, total int64
	for i := 0; i < 4; i++ {
		next, count, done, e := SweepOrphanRunEvents(ctx, s.DB(), after, 1000)
		if e != nil || count > 1000 {
			t.Fatal(count, e)
		}
		after = next
		total += count
		if done {
			break
		}
	}
	if total != 2505 {
		t.Fatal("incomplete sweep", total)
	}
	var count int
	if e = s.DB().QueryRow(`SELECT count(*) FROM run_events WHERE event_id='keep'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("other event type deleted", e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, e = SweepOrphanRunEvents(cancelled, s.DB(), 0, 1000); e == nil {
		t.Fatal("ignored cancellation")
	}
	if _, _, _, e = SweepOrphanRunEvents(ctx, s.DB(), 0, 2001); e == nil {
		t.Fatal("unbounded batch")
	}
}

func TestResearchDeleteHasIndexedCascadeAndRollbackSafeEventCleanup(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	// All identified missing child lookups must now use an index.
	for _, pair := range [][2]string{{"runs", "user_message_id"}, {"runs", "assistant_message_id"}, {"permission_grants", "run_id"}, {"message_citations", "tool_call_id"}, {"research_revision_proposals", "workflow_run_id"}, {"research_timeline", "workflow_run_id"}, {"workflow_human_decisions", "workflow_run_id"}} {
		rows, e := h.store.DB().Query(`EXPLAIN QUERY PLAN SELECT rowid FROM `+pair[0]+` WHERE `+pair[1]+`=?`, "fixture")
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var a, b, c int
			var plan string
			_ = rows.Scan(&a, &b, &c, &plan)
			if len(plan) >= 4 && plan[:4] == "SCAN" {
				t.Error("unindexed child", pair, plan)
			}
		}
		rows.Close()
	}
	// An unrelated aggregate with the same ID must never be removed by triggers.
	_, e := h.store.DB().Exec(`INSERT INTO run_events VALUES('other',1,'absent','project',1,'fixture','now','{}')`)
	if e != nil {
		t.Fatal(e)
	}
	_, _, _, e = SweepOrphanRunEvents(context.Background(), h.store.DB(), 0, 1000)
	if e != nil {
		t.Fatal(e)
	}
}

func TestLargeOrphanEventCleanupPerformanceFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("large database fixture")
	}
	s, e := Open(context.Background(), filepath.Join(t.TempDir(), "large.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	_, e = s.DB().Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<100000)
INSERT INTO run_events SELECT 'event-'||i,1,'deleted-'||(i%43),'run',i,'content.delta','now',json_object('text',printf('%0500d',i)) FROM n`)
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	var after, total int64
	var longest time.Duration
	for {
		before := time.Now()
		next, count, done, e := SweepOrphanRunEvents(context.Background(), s.DB(), after, 1000)
		if e != nil {
			t.Fatal(e)
		}
		if d := time.Since(before); d > longest {
			longest = d
		}
		after = next
		total += count
		if done {
			break
		}
	}
	if total != 100000 {
		t.Fatal(total)
	}
	t.Logf("fixture 100000 events, total=%s longest1000rowBatch=%s; native driver, no model", time.Since(start), longest)
}

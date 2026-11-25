package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestResearchDiscussionMigrationPreservesNotesAndFrozenHistory(t *testing.T) {
	for _, phase := range []string{"exploring", "execution", "archived"} {
		t.Run(phase, func(t *testing.T) {
			f := newWorkingStateFixtureAtVersion(t, 105)
			f.call("saved", "chat", workingCommand(0, "authentic pre-106 note"))
			f.exec(`INSERT INTO research_working_states(id,project_id,research_task_id,workflow_run_id,source_chat_run_id,source_user_message_id,source_call_id,version,current_focus,rationale,next_step,unknowns_json,created_at)
 VALUES ('legacy-note','p','task','task','chat','human','saved',1,'authentic pre-106 note','unverified','read','["unknown"]',?)`, formatTime(f.now))
			if err := f.finish("saved", true); err != nil {
				t.Fatal(err)
			}
			f.planner("failed")
			if phase == "execution" {
				f.exec(`INSERT INTO workflow_events(id,workflow_run_id,sequence,event_type,payload_json,created_at) VALUES ('old-adopt','planner',1,'research.route_adopted','{}',?)`, formatTime(f.now))
			}
			if phase == "archived" {
				f.exec(`UPDATE research_tasks SET status='archived',archived_at=? WHERE id='task'`, formatTime(f.now))
			}
			beforeRuns := databaseRows(t, f.store.DB(), "workflow_runs")
			beforeNotes := databaseRows(t, f.store.DB(), "research_working_states")
			beforeProposals := databaseRows(t, f.store.DB(), "research_working_state_proposals")
			if err := Migrate(context.Background(), f.store.DB()); err != nil {
				t.Fatal(err)
			}
			for table, before := range map[string]any{"workflow_runs": beforeRuns, "research_working_states": beforeNotes, "research_working_state_proposals": beforeProposals} {
				if !reflect.DeepEqual(before, databaseRows(t, f.store.DB(), table)) {
					t.Fatalf("migration rewrote immutable %s", table)
				}
			}
			for _, conv := range []string{"conv", "planner-conv"} {
				d, found, err := f.repo.ResearchDiscussionForConversation(context.Background(), conv)
				if err != nil || !found || d.CanExplore != (phase == "exploring") || d.PrimaryConversationID != "conv" {
					t.Fatal("migration guessed legacy authority or lost primary discussion", d, found, err)
				}
			}
			rows, err := f.store.DB().Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			if rows.Next() {
				t.Fatal("migration broke provenance foreign keys")
			}
			_ = rows.Close()
			if phase == "exploring" {
				if err := f.discussionRead("post-upgrade", "chat", workflow.ResearchDiscoveryReadTool); err != nil {
					t.Fatal("failed legacy planner still blocked the explicit task discussion", err)
				}
			}
		})
	}
}

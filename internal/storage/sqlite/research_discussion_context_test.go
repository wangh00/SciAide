package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func (f *workingStateFixture) planner(status string) {
	f.t.Helper()
	at := formatTime(f.now.Add(time.Second))
	h := strings.Repeat("a", 64)
	f.exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,created_at,updated_at,research_task_id,workflow_name,workflow_purpose)
 VALUES ('planner','p','wf','wv',?,'{}',?,'{}',?,'{}',?,?,'task','Plan','research_starter')`, status, h, h, at, at)
	f.exec(`INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES ('planner-conv','p','Plan',?,?)`, at, at)
	f.exec(`INSERT INTO workflow_conversations(workflow_run_id,conversation_id,created_at) VALUES ('planner','planner-conv',?)`, at)
}

func (f *workingStateFixture) discussionRead(call, chat, name string) error {
	f.t.Helper()
	at := formatTime(f.now)
	f.exec(`INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at)
 VALUES (?,?,?,?,'1','{}','running','low','[]',1,?,?)`, call, chat, "provider-"+call, name, at, at)
	return f.repo.ValidateResearchDiscussionTool(context.Background(), "p", "task", chat, call, name)
}

// Creating a candidate is an optional task activity, not a change to the
// human discussion's identity or authorization. Exercise the original and
// candidate conversations separately, including the write completion guard.
func TestResearchDiscussionIndependentOfPlannerStatus(t *testing.T) {
	for _, status := range []string{"running", "failed", "cancelled", "completed"} {
		t.Run(status, func(t *testing.T) {
			f := newWorkingStateFixture(t)
			f.planner(status)
			for i, name := range []string{workflow.ResearchStateReadTool, workflow.ResearchDiscoverySearchTool, workflow.ResearchDiscoveryReadTool} {
				if err := f.discussionRead("read-"+string(rune('0'+i)), "chat", name); err != nil {
					t.Fatalf("candidate %s removed discussion capability %s: %v", status, name, err)
				}
			}
			command := workingCommand(0, "explore while a candidate changes")
			f.call("save", "chat", command)
			if _, err := f.save("save", "chat", command); err != nil {
				t.Fatal("candidate status blocked working note", err)
			}
			if err := f.finish("save", true); err != nil {
				t.Fatal("candidate status blocked note completion", err)
			}
			f.exec(`UPDATE runs SET status='completed' WHERE id='chat'`)
			f.chat("candidate-chat", "planner-conv", "candidate-human")
			if err := f.discussionRead("candidate-read", "candidate-chat", workflow.ResearchDiscoveryReadTool); err != nil {
				t.Fatal("candidate conversation lost task discussion identity", err)
			}
			command = workingCommand(1, "continue in candidate conversation")
			f.call("candidate-save", "candidate-chat", command)
			if _, err := f.save("candidate-save", "candidate-chat", command); err != nil {
				t.Fatal(err)
			}
			if err := f.finish("candidate-save", true); err != nil || f.read().Version != 2 {
				t.Fatal("candidate conversation failed to share task note", err)
			}
		})
	}
}

func TestResearchDiscussionNewHumanAcrossConversationsInvalidatesOldNote(t *testing.T) {
	f := newWorkingStateFixture(t)
	command := workingCommand(0, "old proposal")
	f.call("old", "chat", command)
	if _, err := f.save("old", "chat", command); err != nil {
		t.Fatal(err)
	}
	f.planner("failed")
	f.chat("new-chat", "planner-conv", "new-human")
	if err := f.finish("old", true); err == nil {
		t.Fatal("late note completed after a newer human turn in the task")
	}
	if _, err := f.save("old", "chat", command); err == nil {
		t.Fatal("old call borrowed a newer task conversation's user turn")
	}
	command = workingCommand(0, "new human requirements")
	f.call("new", "new-chat", command)
	if _, err := f.save("new", "new-chat", command); err != nil {
		t.Fatal("stale pending note blocked current human turn", err)
	}
	if err := f.finish("new", true); err != nil {
		t.Fatal(err)
	}
}

func TestResearchDiscussionAdoptionClosesAllConversationsPermanently(t *testing.T) {
	f := newWorkingStateFixture(t)
	f.planner("completed")
	command := workingCommand(0, "provisional before adoption")
	f.call("late", "chat", command)
	if _, err := f.save("late", "chat", command); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO workflow_events(id,workflow_run_id,sequence,event_type,payload_json,created_at) VALUES ('adopt','planner',1,'research.route_adopted','{}',?)`, formatTime(f.now))
	if err := f.finish("late", true); err == nil {
		t.Fatal("adopted task accepted a late note completion")
	}
	for _, conv := range []string{"conv", "planner-conv"} {
		d, found, err := f.repo.ResearchDiscussionForConversation(context.Background(), conv)
		if err != nil || !found || d.CanExplore {
			t.Fatal("adoption did not close task-wide exploration", d, found, err)
		}
	}
	f.exec(`DELETE FROM workflow_events WHERE id='adopt'`)
	f.exec(`DELETE FROM workflow_runs WHERE id='planner'`)
	if err := f.discussionRead("after-delete", "chat", workflow.ResearchDiscoveryReadTool); err == nil {
		t.Fatal("deleting adoption history reopened exploration")
	}
	if _, err := f.store.DB().Exec(`UPDATE research_tasks SET discussion_phase='exploring' WHERE id='task'`); err == nil {
		t.Fatal("execution phase was reopened")
	}
}

func TestResearchDiscussionWorkflowAIHasNoHumanRole(t *testing.T) {
	f := newWorkingStateFixture(t)
	f.attachWorkflowAI()
	if _, found, err := f.repo.ResearchDiscussionForChat(context.Background(), "chat"); err != nil || found {
		t.Fatal("internal Workflow AI acquired a human exploration context", found, err)
	}
	if err := f.discussionRead("ai-read", "chat", workflow.ResearchDiscoveryReadTool); err == nil {
		t.Fatal("internal Workflow AI acquired discovery capability")
	}
}

func TestResearchDiscussionOwnerSurvivesOptionalSourceRunDeletion(t *testing.T) {
	f := newWorkingStateFixture(t)
	f.planner("failed")
	command := workingCommand(0, "keep authentic note")
	f.call("first", "chat", command)
	if _, err := f.save("first", "chat", command); err != nil {
		t.Fatal(err)
	}
	if err := f.finish("first", true); err != nil {
		t.Fatal(err)
	}
	f.exec(`DELETE FROM workflow_runs WHERE id='task'`)
	d, found, err := f.repo.ResearchDiscussionForChat(context.Background(), "chat")
	if err != nil || !found || !d.CanExplore || d.SourceWorkflowRunID != "task" || d.PrimaryConversationID != "conv" {
		t.Fatal("provenance was used as the discussion owner", d, found, err)
	}
	taskID, err := f.repo.ResearchTaskIDForSubject(context.Background(), tool.SubjectChatRun, "chat")
	if err != nil || taskID != "task" || f.read().SourceCallID != "first" {
		t.Fatal("task scope or authentic note disappeared with optional history", taskID, err)
	}
	command = workingCommand(1, "still exploring")
	f.call("second", "chat", command)
	if _, err := f.save("second", "chat", command); err != nil {
		t.Fatal(err)
	}
	if err := f.finish("second", true); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.repo = NewWorkflowRuntimeRepository(f.store.DB())
	if got := f.read(); got.Version != 2 || got.SourceCallID != "second" {
		t.Fatal("restart lost the detached-source task note", got)
	}
	if err := f.discussionRead("reopened-read", "chat", workflow.ResearchDiscoveryReadTool); err != nil {
		t.Fatal(err)
	}
}

func TestResearchDiscussionPrepareUsesTaskIdentityButNotParallelPlanners(t *testing.T) {
	f := newWorkingStateFixture(t)
	at := formatTime(f.now)
	f.exec(`INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at)
 VALUES ('prepare','chat','prepare-provider',?,'1','{}','running','low','[]',1,?,?)`, workflow.ResearchPlanPrepareTool, at, at)
	check := func(prior string) error {
		return f.repo.ValidateResearchPlanPreparation(context.Background(), "p", "task", "chat", "prepare", "human", prior)
	}
	if err := check("task"); err != nil {
		t.Fatal(err)
	}
	f.planner("running")
	if err := check("planner"); err == nil {
		t.Fatal("parallel planner was permitted")
	}
	f.exec(`UPDATE workflow_runs SET status='failed' WHERE id='planner'`)
	if err := check("planner"); err != nil {
		t.Fatal("a failed optional planner revoked the original discussion's planning capability", err)
	}
	if err := check("task"); err == nil {
		t.Fatal("preparation inherited a superseded candidate's choices")
	}
	f.chat("new-human-chat", "planner-conv", "latest-human")
	if err := check("planner"); err == nil {
		t.Fatal("preparation borrowed another task conversation's newer human")
	}
}

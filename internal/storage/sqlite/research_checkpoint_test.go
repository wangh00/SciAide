package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/agent"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	projectapp "github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestResearchCheckpointMatchesTaskHistoryScope(t *testing.T) {
	db, _ := workflowReasoningHistory(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var current, prior, project string
	if err := db.QueryRow(`SELECT conversation_id FROM runs WHERE id='chat'`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT conversation_id FROM runs WHERE id='other-chat'`).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT project_id FROM workflow_runs WHERE id='wr'`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(t, `INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at,research_task_id)
		SELECT 'prior-wr',project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at,research_task_id FROM workflow_runs WHERE id='wr'`)
	exec(t, `INSERT INTO workflow_conversations VALUES ('prior-wr',?,'2026-10-09')`, prior)
	service := contextmemory.NewService(NewContextCheckpointRepository(db))
	save := func(boundary string) (contextmemory.Checkpoint, error) {
		return service.Save(ctx, contextmemory.Checkpoint{ConversationID: current, ThroughMessageID: boundary, Summary: "# History\nPreserve planning choices.", SourceMessageCount: 2, SourceEstimatedTokens: 200, ModelProfileID: "profile", ModelID: "fixture", APIProtocol: modelcap.ProtocolOpenAIResponses})
	}
	// The released predicate rejects exactly this legitimate cross-run boundary.
	var oldMatches int
	if err := db.QueryRow(`SELECT count(*) FROM messages WHERE id='other-chat-a' AND conversation_id=?`, current).Scan(&oldMatches); err != nil || oldMatches != 0 {
		t.Fatal("old failure not reproduced", err)
	}
	checkVisible := func(t *testing.T, want bool) {
		t.Helper()
		messages, err := NewWorkflowRuntimeRepository(db).ResearchTaskDiscussionMessages(ctx, current)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range messages {
			found = found || m.ID == "other-chat-a"
		}
		if found != want {
			t.Fatal("history scope mismatch")
		}
	}
	checkVisible(t, true)
	first, err := save("other-chat-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.ThroughMessageID != "other-chat-a" || first.ConversationID != current {
		t.Fatal("boundary reassigned")
	}
	loaded, exists, err := service.Latest(ctx, current)
	if err != nil || !exists || loaded.ID != first.ID {
		t.Fatal("cannot reload cross-run checkpoint", err)
	}
	// Immutable run identities are never changed to construct negative cases.
	for _, tc := range []struct {
		name string
		task any
	}{
		{"unbound source", "task"},
		{"empty source task", ""},
		{"both empty tasks", ""},
		{"other task", "another-task"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "both empty tasks" {
				exec(t, `DELETE FROM workflow_conversations WHERE workflow_run_id='wr'`)
				exec(t, `INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at,research_task_id)
				 SELECT 'empty-current',project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at,'' FROM workflow_runs WHERE id='wr'`)
				exec(t, `INSERT INTO workflow_conversations VALUES ('empty-current',?,'2026-10-09')`, current)
				t.Cleanup(func() {
					exec(t, `DELETE FROM workflow_conversations WHERE workflow_run_id='empty-current'`)
					exec(t, `INSERT INTO workflow_conversations VALUES ('wr',?,'2026-10-09')`, current)
				})
			}
			exec(t, `DELETE FROM workflow_conversations WHERE workflow_run_id='prior-wr'`)
			t.Cleanup(func() { exec(t, `INSERT INTO workflow_conversations VALUES ('prior-wr',?,'2026-10-09')`, prior) })
			if tc.name != "unbound source" {
				exec(t, `INSERT OR IGNORE INTO research_tasks(id,project_id,title,origin_kind,created_at,updated_at) VALUES ('another-task',?,'other','ai_route','2026-10-09','2026-10-09')`, project)
				exec(t, `INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at,research_task_id)
				 SELECT ?,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at,? FROM workflow_runs WHERE id='wr'`, tc.name, tc.task)
				exec(t, `INSERT INTO workflow_conversations VALUES (?,?,'2026-10-09')`, tc.name, prior)
				t.Cleanup(func() { exec(t, `DELETE FROM workflow_conversations WHERE workflow_run_id=?`, tc.name) })
			}
			checkVisible(t, false)
			if _, err := save("other-chat-a"); err == nil {
				t.Fatal("unrelated boundary accepted")
			}
		})
	}
	p, err := projectapp.NewService(NewProjectRepository(db), t.TempDir(), t.TempDir()).Create(ctx, "unrelated", "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := conversation.NewService(NewConversationRepository(db)).Create(ctx, p.ID, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := NewConversationRepository(db).CreateMessage(ctx, conversation.Message{ID: "foreign-boundary", ConversationID: c.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := save("foreign-boundary"); err == nil {
		t.Fatal("other project boundary accepted")
	}
	if _, err := save("nonexistent"); err == nil {
		t.Fatal("unknown boundary accepted")
	}
	second, err := save("chat-a")
	if err != nil || second.Revision != first.Revision+1 {
		t.Fatal("failed saves advanced revision", err)
	}
	assertDatabaseHealthy(t, db)
}

// Exercise the real compaction loop and disk repository, not just Save. The
// deterministic provider deliberately does not claim to validate model quality.
type checkpointTestResearch struct{ db *sql.DB }

func (r checkpointTestResearch) GuidanceForConversation(context.Context, string) (workflow.ResearchGuidance, bool, error) {
	return workflow.ResearchGuidance{}, true, nil
}
func (r checkpointTestResearch) DiscussionMessagesForConversation(ctx context.Context, id string) ([]conversation.Message, bool, error) {
	messages, err := NewWorkflowRuntimeRepository(r.db).ResearchTaskDiscussionMessages(ctx, id)
	return messages, true, err
}

type checkpointTestResolver struct{ provider model.ChatModel }

func (r checkpointTestResolver) Resolve(context.Context, string, string) (model.ResolvedChatModel, error) {
	return model.ResolvedChatModel{Model: r.provider, APIProtocol: modelcap.ProtocolOpenAIChat}, nil
}
func TestResearchCheckpointCompactsAcrossRunsAndResumesAfterReopen(t *testing.T) {
	db, path := workflowReasoningHistory(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var current, prior string
	if err := db.QueryRow(`SELECT conversation_id FROM runs WHERE id='chat'`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT conversation_id FROM runs WHERE id='other-chat'`).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM workflow_ai_chat_runs WHERE chat_run_id='chat'`)
	exec(`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at,research_task_id)
 SELECT 'prior-wr',project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at,research_task_id FROM workflow_runs WHERE id='wr'`)
	exec(`INSERT INTO workflow_conversations VALUES ('prior-wr',?,'2026-10-09')`, prior)
	exec(`UPDATE runs SET auto_compact_token_limit=5000 WHERE id='chat'`)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		conv, run := prior, fmt.Sprintf("prior-%d", i)
		if i == 0 {
			run = "other-chat"
		}
		if i == 5 {
			conv, run = current, "chat"
		}
		for j, role := range []conversation.Role{conversation.RoleUser, conversation.RoleAssistant} {
			suffix := "-u"
			if j == 1 {
				suffix = "-a"
			}
			mid := run + suffix
			at := base.Add(time.Duration(i*2+j) * time.Second)
			part := conversation.MessagePart{ID: mid + "-part", MessageID: mid, Type: "text", Text: fmt.Sprintf("TURN-%d-", i) + strings.Repeat("x", 1200), CreatedAt: at}
			if i == 0 || i == 5 {
				exec(`UPDATE messages SET created_at=? WHERE id=?`, formatTime(at), mid)
				exec(`INSERT INTO message_parts(id,message_id,ordinal,part_type,text_content,created_at) VALUES (?,?,0,'text',?,?)`, part.ID, mid, part.Text, formatTime(at))
			} else {
				if err := NewConversationRepository(db).CreateMessage(ctx, conversation.Message{ID: mid, ConversationID: conv, RunID: run, Role: role, Status: conversation.MessageComplete, CreatedAt: at, UpdatedAt: at, Parts: []conversation.MessagePart{part}}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	before := databaseRows(t, db, "messages")
	partsBefore := databaseRows(t, db, "message_parts")
	scripts := make([][]fake.Step, 6)
	for i := range scripts {
		scripts[i] = []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: fmt.Sprintf("# Research checkpoint %d: preserve confirmed decisions.", i)}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	}
	provider := fake.New(scripts...)
	newLoop := func() *agent.Loop {
		return agent.NewLoop(NewRunRepository(db), NewConversationRepository(db), nil, nil, nil, nil, checkpointTestResolver{provider}, nil, agent.Options{Checkpoints: contextmemory.NewService(NewContextCheckpointRepository(db)), Research: checkpointTestResearch{db}})
	}
	first, err := newLoop().CompactConversation(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	if first.Complete || first.Passes != 3 || first.ThroughMessageID == "chat-a" {
		t.Fatalf("expected partial cross-run checkpoint: %+v", first)
	}
	var owner string
	if err := db.QueryRow(`SELECT conversation_id FROM messages WHERE id=?`, first.ThroughMessageID).Scan(&owner); err != nil || owner != prior {
		t.Fatal("not an old-stage boundary", owner, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loaded, exists, err := NewContextCheckpointRepository(db).Latest(ctx, current)
	if err != nil || !exists || loaded.ThroughMessageID != first.ThroughMessageID {
		t.Fatal("restart lost boundary", err)
	}
	second, err := newLoop().CompactConversation(ctx, current)
	if err != nil || !second.Complete || second.ThroughMessageID != "chat-a" || second.SourceMessageCount != 12 {
		t.Fatalf("continuation = %+v, %v", second, err)
	}
	requests := provider.Requests()
	for i, request := range requests {
		if len(request.Tools) != 0 {
			t.Fatal("compaction exposed tools")
		}
		encoded := fmt.Sprint(request.Messages)
		if !strings.Contains(encoded, fmt.Sprintf("TURN-%d-", i)) || (i > 0 && strings.Contains(encoded, fmt.Sprintf("TURN-%d-", i-1))) {
			t.Fatalf("batch repeated or skipped at %d", i)
		}
	}
	repeated, err := newLoop().CompactConversation(ctx, current)
	if err != nil || repeated.Passes != 0 || len(provider.Requests()) != 6 {
		t.Fatal("repeated compaction not idempotent", err)
	}
	if !reflect.DeepEqual(before, databaseRows(t, db, "messages")) || !reflect.DeepEqual(partsBefore, databaseRows(t, db, "message_parts")) {
		t.Fatal("compaction mutated original history")
	}
	assertDatabaseHealthy(t, db)
}

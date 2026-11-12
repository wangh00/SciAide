package sqlite

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/agent"
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

// Real disk persistence, cumulative summaries and restart; deterministic
// provider responses validate host behavior, not semantic summary quality.
func TestIncrementalCheckpointPersistsCumulativeSummaryAfterRestart(t *testing.T) {
	db, path := workflowReasoningHistory(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM workflow_ai_chat_runs WHERE chat_run_id='chat'`); err != nil {
		t.Fatal(err)
	}
	run, err := NewRunRepository(db).Get(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET context_window_tokens=272000,context_budget_tokens=258400,auto_compact_token_limit=244800 WHERE id='chat'`); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	for _, mid := range []string{"chat-u", "chat-a"} {
		if _, err := db.Exec(`INSERT INTO message_parts(id,message_id,ordinal,part_type,text_content,created_at) VALUES (?,?,0,'text',?,?)`, mid+"-part", mid, strings.Repeat("原", 12873), formatTime(base)); err != nil {
			t.Fatal(err)
		}
	}
	provider := fake.New(
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("旧", 4122)}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}},
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("摘", 3305)}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}},
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("续", 4000)}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}},
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("拒", 4500)}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}},
	)
	newLoop := func() *agent.Loop {
		return agent.NewLoop(NewRunRepository(db), NewConversationRepository(db), nil, nil, nil, nil, checkpointTestResolver{provider}, nil,
			agent.Options{Checkpoints: contextmemory.NewService(NewContextCheckpointRepository(db))})
	}
	checkLatest := func(revision, count, source int, boundary, summary string) contextmemory.Checkpoint {
		t.Helper()
		checkpoint, exists, err := NewContextCheckpointRepository(db).Latest(ctx, run.ConversationID)
		if err != nil || !exists || checkpoint.Revision != revision || checkpoint.SourceMessageCount != count || checkpoint.SourceEstimatedTokens != source || checkpoint.ThroughMessageID != boundary || checkpoint.Summary != summary {
			t.Fatalf("unexpected checkpoint: revision=%d count=%d source=%d boundary=%s error=%v", checkpoint.Revision, checkpoint.SourceMessageCount, checkpoint.SourceEstimatedTokens, checkpoint.ThroughMessageID, err)
		}
		if err := contextmemory.Verify(checkpoint); err != nil {
			t.Fatal(err)
		}
		return checkpoint
	}
	compact := func() {
		t.Helper()
		before, partsBefore := databaseRows(t, db, "messages"), databaseRows(t, db, "message_parts")
		if result, err := newLoop().CompactConversation(ctx, run.ConversationID); err != nil || !result.Complete || result.Passes != 1 {
			t.Fatalf("compaction=%+v error=%v", result, err)
		}
		if !reflect.DeepEqual(before, databaseRows(t, db, "messages")) || !reflect.DeepEqual(partsBefore, databaseRows(t, db, "message_parts")) {
			t.Fatal("compaction changed original message history")
		}
	}
	addTurn := func(id string, increment, index int) {
		t.Helper()
		at := base.Add(time.Duration(index) * time.Minute)
		next := chat.Run{ID: id, ConversationID: run.ConversationID, UserMessageID: id + "-u", AssistantMessageID: id + "-a", ModelProfileID: "profile", ModelID: "fixture", Status: chat.RunCompleted, APIProtocol: modelcap.ProtocolOpenAIChat, ContextWindowTokens: 272000, ContextBudgetTokens: 258400, AutoCompactTokenLimit: 244800, CreatedAt: at, UpdatedAt: at}
		message := func(mid string, role conversation.Role, length int) conversation.Message {
			return conversation.Message{ID: mid, ConversationID: next.ConversationID, RunID: next.ID, Role: role, Status: conversation.MessageComplete, CreatedAt: at, UpdatedAt: at,
				Parts: []conversation.MessagePart{{ID: mid + "-part", MessageID: mid, Type: "text", Text: strings.Repeat("新", length), CreatedAt: at}}}
		}
		if err := NewRunRepository(db).CreateWithMessages(ctx, next, message(next.UserMessageID, conversation.RoleUser, 47), message(next.AssistantMessageID, conversation.RoleAssistant, increment-47)); err != nil {
			t.Fatal(err)
		}
	}
	compact()
	checkLatest(1, 2, 25746, "chat-a", strings.Repeat("旧", 4122))
	addTurn("second", 1969, 1)
	compact()
	second := checkLatest(2, 4, 27715, "second-a", strings.Repeat("摘", 3305))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if loaded := checkLatest(2, 4, 27715, "second-a", strings.Repeat("摘", 3305)); loaded.ID != second.ID {
		t.Fatal("restart lost persisted checkpoint identity")
	}
	addTurn("third", 1000, 2)
	before, partsBefore := databaseRows(t, db, "messages"), databaseRows(t, db, "message_parts")
	compact()
	third := checkLatest(3, 6, 28715, "third-a", strings.Repeat("续", 4000))
	if result, err := newLoop().CompactConversation(ctx, run.ConversationID); err != nil || result.Passes != 0 || len(provider.Requests()) != 3 {
		t.Fatal("compaction repeated already summarized history", err)
	}
	if !reflect.DeepEqual(before, databaseRows(t, db, "messages")) || !reflect.DeepEqual(partsBefore, databaseRows(t, db, "message_parts")) {
		t.Fatal("compaction changed original message history")
	}
	addTurn("fourth", 500, 3)
	before, partsBefore = databaseRows(t, db, "messages"), databaseRows(t, db, "message_parts")
	if _, err := newLoop().CompactConversation(ctx, run.ConversationID); err == nil || !strings.Contains(err.Error(), "previous summary plus new source") {
		t.Fatal("no-reduction checkpoint accepted", err)
	}
	if latest := checkLatest(3, 6, 28715, "third-a", strings.Repeat("续", 4000)); latest.ID != third.ID {
		t.Fatal("rejected summary replaced good checkpoint")
	}
	if !reflect.DeepEqual(before, databaseRows(t, db, "messages")) || !reflect.DeepEqual(partsBefore, databaseRows(t, db, "message_parts")) {
		t.Fatal("rejection changed original message history")
	}
	for i, request := range provider.Requests() {
		if len(request.Tools) != 0 || len(request.ProviderTurns) != 0 {
			t.Fatal("summary request exposed tool execution")
		}
		if i == 1 && !strings.Contains(request.Messages[1].Content, strings.Repeat("旧", 4122)) || i == 2 && !strings.Contains(request.Messages[1].Content, strings.Repeat("摘", 3305)) {
			t.Fatal("previous summary not completely supplied")
		}
	}
	assertDatabaseHealthy(t, db)
}

package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/events"
)

func questionStorageFixture(t *testing.T) (*Store, chat.Run, tool.Call) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "question.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	p, err := project.NewService(NewProjectRepository(store.DB()), t.TempDir(), t.TempDir()).Create(ctx, "question fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, p.ID, "question")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "fixture", TimeoutSeconds: 60, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err = NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "run", ConversationID: c.ID, UserMessageID: "user", AssistantMessageID: "assistant", ModelProfileID: "profile", ModelID: "fixture", Status: chat.RunRunning, CreatedAt: now, UpdatedAt: now}
	u := conversation.Message{ID: "user", ConversationID: c.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "up", Type: "text", Text: "目标模糊", CreatedAt: now}}}
	a := conversation.Message{ID: "assistant", ConversationID: c.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "ap", Type: "text", CreatedAt: now}}}
	if err = NewRunRepository(store.DB()).CreateWithMessages(ctx, run, u, a); err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "question", RunID: run.ID, ToolName: userinput.ToolName, ToolVersion: "1", ProviderCallID: "question", Status: tool.CallPending, Risk: tool.RiskLow, Idempotent: true, Arguments: []byte(`{"questions":[{"id":"q","question":"目标？","required":true,"options":[{"id":"a","label":"报告"},{"id":"b","label":"方案"}]}]}`), CreatedAt: now, UpdatedAt: now}
	if err = NewToolRepository(store.DB()).Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	return store, run, call
}

func TestUserInputConcurrentExactReplyCommitsOnce(t *testing.T) {
	store, run, call := questionStorageFixture(t)
	ctx := context.Background()
	repo := NewRunRepository(store.DB())
	now := time.Now().UTC()
	if _, err := repo.WaitForUserInput(ctx, run.ID, call.ID, now); err != nil {
		t.Fatal(err)
	}
	response := userinput.Response{ConversationID: run.ConversationID, RunID: run.ID, CallID: call.ID, Answers: userinput.Answers{"q": {Text: "自由方向"}}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := repo.RespondToUserInput(ctx, response, now); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	_ = store.DB().QueryRow(`SELECT count(*) FROM messages WHERE run_id=? AND role='user'`, run.ID).Scan(&count)
	if count != 2 {
		t.Fatal("duplicate answer", count)
	}
	current, _ := repo.Get(ctx, run.ID)
	if current.Status != chat.RunRunning || current.UserMessageID != run.UserMessageID || current.PendingUserInputCallID != "" {
		t.Fatal(current)
	}
	loaded, _ := NewToolRepository(store.DB()).Get(ctx, call.ID)
	if loaded.Result == nil || loaded.Result.Status != tool.ResultSuccess {
		t.Fatal(loaded)
	}
	response.Answers["q"] = userinput.Answer{OptionIDs: []string{"not-offered"}}
	if _, err := repo.RespondToUserInput(ctx, response, now); err == nil {
		t.Fatal("invalid option accepted")
	}
}

func TestUserInputCancelReplyRaceHasOneCoherentOutcome(t *testing.T) {
	store, run, call := questionStorageFixture(t)
	ctx := context.Background()
	repo := NewRunRepository(store.DB())
	now := time.Now().UTC()
	if _, err := repo.WaitForUserInput(ctx, run.ID, call.ID, now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, err := repo.CancelRun(ctx, run.ID, "USER_CANCELLED", "cancel", now, events.New("cancel", run.ID, "run", "run.cancelled", 0, []byte(`{}`)))
		if err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = repo.RespondToUserInput(ctx, userinput.Response{ConversationID: run.ConversationID, RunID: run.ID, CallID: call.ID, Dismiss: true}, now)
	}()
	wg.Wait()
	current, err := repo.Get(ctx, run.ID)
	if err != nil || current.Status != chat.RunCancelled || current.PendingUserInputCallID != "" {
		t.Fatal(current, err)
	}
	loaded, _ := NewToolRepository(store.DB()).Get(ctx, call.ID)
	if !loaded.Status.Terminal() {
		t.Fatal("dangling question", loaded)
	}
	var count int
	_ = store.DB().QueryRow(`SELECT count(*) FROM messages WHERE run_id=? AND role='user'`, run.ID).Scan(&count)
	if count > 2 {
		t.Fatal("duplicate human reply")
	}
}

func TestUserInputMigrationPreserves94HistoryAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err = migrateToVersion(ctx, db, 94); err != nil {
		t.Fatal(err)
	}
	const at = "2026-10-08T00:00:00Z"
	if _, err = db.ExecContext(ctx, `INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('p','History','','C:/fixture','external',?,?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES ('c','p','History',?,?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO messages(id,conversation_id,role,status,created_at,updated_at) VALUES ('m','c','user','complete',?,?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var role string
	if err = db.QueryRow(`SELECT role FROM messages WHERE id='m'`).Scan(&role); err != nil || role != "user" {
		t.Fatal(role, err)
	}
	if _, err = db.Exec(`SELECT pending_user_input_call_id FROM runs`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke foreign keys")
	}
}

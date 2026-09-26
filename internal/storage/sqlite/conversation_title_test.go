package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
)

func TestAutomaticTitleMigrationPreservesLegacyTitle(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrateToVersion(ctx, db, 90); err != nil {
		t.Fatal(err)
	}
	const at = "2026-09-24T00:00:00Z"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('p','P','','C:/fixture','external',?,?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES ('c','p','新会话',?,?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	got, err := NewConversationRepository(db).GetConversation(ctx, "c")
	if err != nil || got.Title != "新会话" || got.AutoTitlePending {
		t.Fatalf("legacy changed: %+v %v", got, err)
	}
}

func TestConversationAutomaticTitleIsAtomicAndOneShot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	p, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	cr := NewConversationRepository(store.DB())
	cs := conversation.NewService(cr)
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	n := 0
	send := func(c conversation.Conversation, text string, valid bool) error {
		n++
		rid := fmt.Sprintf("run-%d", n)
		uid, aid := rid+"-user", rid+"-assistant"
		user := conversation.Message{ID: uid, ConversationID: c.ID, RunID: rid, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}
		if text != "" {
			user.Parts = []conversation.MessagePart{{ID: uid + "-part", MessageID: uid, Type: "text", Text: text, CreatedAt: now}}
		}
		assistant := conversation.Message{ID: aid, ConversationID: c.ID, RunID: rid, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now}
		profileID := "profile"
		if !valid {
			profileID = "missing-profile"
		}
		run := chat.Run{ID: rid, ConversationID: c.ID, UserMessageID: uid, AssistantMessageID: aid, ModelProfileID: profileID, ModelID: "fixture", Status: chat.RunCompleted, CreatedAt: now, UpdatedAt: now}
		run.WebSearchDisabled = n%2 == 0
		repo := NewRunRepository(store.DB())
		if err := repo.CreateWithMessages(ctx, run, user, assistant); err != nil {
			return err
		}
		loaded, err := repo.Get(ctx, rid)
		if err == nil && loaded.WebSearchDisabled != run.WebSearchDisabled {
			t.Fatal("web search policy lost during persistence")
		}
		return err
	}
	check := func(c conversation.Conversation, title string, pending bool) {
		t.Helper()
		got, err := cr.GetConversation(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Title != title || got.AutoTitlePending != pending {
			t.Fatalf("got %q pending=%v; want %q pending=%v", got.Title, got.AutoTitlePending, title, pending)
		}
	}
	c, err := cs.Create(ctx, p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	check(c, "新会话", true)
	if err := send(c, "发送失败不应命名", false); err == nil {
		t.Fatal("expected foreign key failure")
	}
	check(c, "新会话", true)
	if err := send(c, "", true); err != nil {
		t.Fatal(err)
	}
	check(c, "新会话", true)
	if err := send(c, "  如何\n分析数据？  ", true); err != nil {
		t.Fatal(err)
	}
	check(c, "如何 分析数据？", false)
	if err := send(c, "第二次追问", true); err != nil {
		t.Fatal(err)
	}
	check(c, "如何 分析数据？", false)
	for _, title := range []string{"旧标题", "新会话"} {
		manual, err := cs.Create(ctx, p.ID, title)
		if err != nil {
			t.Fatal(err)
		}
		if err := send(manual, "不能覆盖手动标题", true); err != nil {
			t.Fatal(err)
		}
		check(manual, title, false)
	}
	list, err := cr.ListConversations(ctx, p.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %v %v", list, err)
	}
}

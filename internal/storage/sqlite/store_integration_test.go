package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/project"
)

func TestProjectRepositoryPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sciaide.db")

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	root := t.TempDir()
	service := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	created, err := service.Create(ctx, "RNA 研究", "P0 persistence test")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	loaded, err := NewProjectRepository(reopened.DB()).Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if loaded.Name != created.Name || loaded.Description != created.Description {
		t.Fatalf("loaded project = %#v, want %#v", loaded, created)
	}

	var migrations int
	if err := reopened.DB().QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrations != 12 {
		t.Fatalf("migration count = %d, want 12", migrations)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	var enabled int
	if err := store.DB().QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys = %d, want 1", enabled)
	}
}

func TestConversationPermissionModePersistsAndCannotChangeDuringRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	createdProject, err := projects.Create(ctx, "Mode", "")
	if err != nil {
		t.Fatal(err)
	}
	conversations := NewConversationRepository(store.DB())
	now := time.Now().UTC()
	value := conversation.Conversation{ID: "conversation", ProjectID: createdProject.ID, Title: "mode", PermissionMode: conversation.PermissionPlan, CreatedAt: now, UpdatedAt: now}
	if err := conversations.CreateConversation(ctx, value); err != nil {
		t.Fatal(err)
	}
	if err := conversations.UpdatePermissionMode(ctx, value.ID, conversation.PermissionFullAccess, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	loaded, err := conversations.GetConversation(ctx, value.ID)
	if err != nil || loaded.PermissionMode != conversation.PermissionFullAccess {
		t.Fatalf("loaded mode = %#v, %v", loaded, err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO model_profiles(id,name,provider_type,base_url,model_id,secret_ref,timeout_seconds,custom_headers_json,enabled,is_default,created_at,updated_at) VALUES ('profile','fixture','openai_compatible','https://example.test/v1','model','secret',60,'{}',1,1,?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "run", ConversationID: value.ID, UserMessageID: "user", AssistantMessageID: "assistant", ModelProfileID: "profile", ModelID: "model", PermissionMode: conversation.PermissionFullAccess, Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now}
	user := conversation.Message{ID: "user", ConversationID: value.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "user-part", MessageID: "user", Type: "text", Text: "q", CreatedAt: now}}}
	assistant := conversation.Message{ID: "assistant", ConversationID: value.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now.Add(time.Nanosecond), UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "assistant-part", MessageID: "assistant", Type: "text", CreatedAt: now}}}
	if err := NewRunRepository(store.DB()).CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	if err := conversations.UpdatePermissionMode(ctx, value.ID, conversation.PermissionPlan, now.Add(2*time.Second)); err == nil {
		t.Fatal("permission mode changed while a run was active")
	}
}

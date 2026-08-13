package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/events"
)

func TestChatSchemaPersistsAndRecoversActiveRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "P1", "chat")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "问题")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "user", ConversationID: createdConversation.ID, RunID: "run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "user-part", MessageID: "user", Type: "text", Text: "hello", CreatedAt: now}}}
	assistant := conversation.Message{ID: "assistant", ConversationID: createdConversation.ID, RunID: "run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "assistant-part", MessageID: "assistant", Type: "text", CreatedAt: now}}}
	repository := NewRunRepository(store.DB())
	run := chat.Run{ID: "run", ConversationID: createdConversation.ID, UserMessageID: "user", AssistantMessageID: "assistant", ModelProfileID: "profile", ModelID: "fixture", Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	if affected, err := repository.InterruptActive(ctx, now.Add(time.Second)); err != nil || affected != 1 {
		t.Fatalf("InterruptActive()=(%d,%v)", affected, err)
	}
	loaded, err := repository.Get(ctx, "run")
	if err != nil || loaded.Status != chat.RunInterrupted {
		t.Fatalf("run=%#v err=%v", loaded, err)
	}
	messages, err := NewConversationRepository(store.DB()).ListMessages(ctx, createdConversation.ID, 10)
	var assistantStatus conversation.MessageStatus
	for _, message := range messages {
		if message.ID == "assistant" {
			assistantStatus = message.Status
		}
	}
	if err != nil || assistantStatus != conversation.MessageIncomplete {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func TestProfileStoresMultipleModelsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sciaide.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "multi", Name: "gateway", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "model-b", Models: []modelprofile.ProfileModel{{ID: "model-a", OwnedBy: "lab", Enabled: true}, {ID: "model-b", Enabled: true, IsDefault: true}}, SecretRef: "secret-multi", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := NewModelProfileRepository(store.DB()).Get(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ModelID != "model-b" || len(loaded.Models) != 2 || !loaded.Models[0].IsDefault {
		t.Fatalf("loaded profile = %#v", loaded)
	}
}

func TestConversationDeleteCleansRunEventsAndRejectsActiveRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "P1", "delete")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "delete me")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "delete-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "delete-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "delete-user", ConversationID: createdConversation.ID, RunID: "delete-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "delete-user-part", MessageID: "delete-user", Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: "delete-assistant", ConversationID: createdConversation.ID, RunID: "delete-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "delete-assistant-part", MessageID: "delete-assistant", Type: "text", CreatedAt: now}}}
	runs := NewRunRepository(store.DB())
	run := chat.Run{ID: "delete-run", ConversationID: createdConversation.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunRunning, CreatedAt: now, UpdatedAt: now}
	if err := runs.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	if err := NewConversationRepository(store.DB()).DeleteConversation(ctx, createdConversation.ID); err == nil {
		t.Fatal("active run deletion was accepted")
	}
	run.Status = chat.RunCompleted
	if err := runs.Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	event := events.Envelope{EventID: "delete-event", Version: 1, AggregateID: run.ID, AggregateType: "run", Sequence: 1, Type: "run.completed", Timestamp: now, Payload: []byte(`{}`)}
	if err := runs.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := NewConversationRepository(store.DB()).DeleteConversation(ctx, createdConversation.ID); err != nil {
		t.Fatal(err)
	}
	var eventsLeft int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM run_events WHERE aggregate_id = ?`, run.ID).Scan(&eventsLeft); err != nil || eventsLeft != 0 {
		t.Fatalf("events left = %d, %v", eventsLeft, err)
	}
}

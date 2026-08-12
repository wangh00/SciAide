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
)

func TestChatSchemaPersistsAndRecoversActiveRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	createdProject, err := project.NewService(NewProjectRepository(store.DB())).Create(ctx, "P1", "chat")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "问题")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "user", ConversationID: createdConversation.ID, RunID: "run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "user-part", MessageID: "user", Type: "text", Text: "hello", CreatedAt: now}}}
	assistant := conversation.Message{ID: "assistant", ConversationID: createdConversation.ID, RunID: "run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "assistant-part", MessageID: "assistant", Type: "text", CreatedAt: now}}}
	repository := NewRunRepository(store.DB())
	run := chat.Run{ID: "run", ConversationID: createdConversation.ID, UserMessageID: "user", AssistantMessageID: "assistant", ModelProfileID: "profile", Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now}
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

package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestContextCheckpointRepositoryPersistsLatestVerifiedRevision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	projectValue, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "checkpoint", "")
	if err != nil {
		t.Fatal(err)
	}
	conversationRepository := NewConversationRepository(store.DB())
	conversationValue, err := conversation.NewService(conversationRepository).Create(ctx, projectValue.ID, "memory")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	message := conversation.Message{ID: "checkpoint-boundary", ConversationID: conversationValue.ID, RunID: "old-run", Role: conversation.RoleAssistant, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "checkpoint-boundary-part", MessageID: "checkpoint-boundary", Type: "text", Text: "verified result", CreatedAt: now}}}
	if err := conversationRepository.CreateMessage(ctx, message); err != nil {
		t.Fatal(err)
	}
	service := contextmemory.NewService(NewContextCheckpointRepository(store.DB()))
	first, err := service.Save(ctx, contextmemory.Checkpoint{ConversationID: conversationValue.ID, ThroughMessageID: message.ID, Summary: "# Progress\n- verified result", SourceMessageCount: 1, SourceEstimatedTokens: 10, ModelProfileID: "profile", ModelID: "model", APIProtocol: modelcap.ProtocolOpenAIResponses})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Save(ctx, contextmemory.Checkpoint{ConversationID: conversationValue.ID, ThroughMessageID: message.ID, Summary: "# Progress\n- revised result", SourceMessageCount: 1, SourceEstimatedTokens: 10, ModelProfileID: "profile", ModelID: "model", APIProtocol: modelcap.ProtocolOpenAIResponses})
	if err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := service.Latest(ctx, conversationValue.ID)
	if err != nil || !exists || loaded.ID != second.ID || loaded.Revision != first.Revision+1 {
		t.Fatalf("latest checkpoint = %#v, %v, %v", loaded, exists, err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE conversation_context_checkpoints SET summary_text=? WHERE id=?`, "tampered", loaded.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Latest(ctx, conversationValue.ID); err == nil || !strings.Contains(err.Error(), "payload hash mismatch") {
		t.Fatalf("tampered checkpoint error = %v", err)
	}
}

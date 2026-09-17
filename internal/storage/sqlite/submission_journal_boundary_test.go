package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
)

// RunStep is a trimmed, bounded display projection, not a verbatim audit.
// The existing journal is the storage contract used by agent submissions.
func TestSubmissionJournalPreservesRawAcrossPreviewAndStorageBoundaries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "submission.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	p, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "audit", "submission")
	if err != nil {
		t.Fatal(err)
	}
	c, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, p.ID, "audit")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "user", ConversationID: c.ID, RunID: "run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "user-part", MessageID: "user", Type: "text", Text: "audit", CreatedAt: now}}}
	assistant := conversation.Message{ID: "assistant", ConversationID: c.ID, RunID: "run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "assistant-part", MessageID: "assistant", Type: "text", CreatedAt: now}}}
	repo := NewRunRepository(store.DB())
	run := chat.Run{ID: "run", ConversationID: c.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now}
	if err := repo.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	raw := " \n\t" + strings.Repeat("原", 150000) + "\r\n "
	if err := repo.BeginModelTurn(ctx, run.ID, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateModelTurnDraft(ctx, run.ID, 1, raw, 0, now); err != nil {
		t.Fatal(err)
	}
	preview := string([]rune(strings.TrimSpace(raw))[:100000])
	if err := repo.SaveRunStep(ctx, chat.RunStep{RunID: run.ID, TurnIndex: 1, Commentary: preview, CreatedAt: now, CompletedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishModelTurn(ctx, run.ID, 1, chat.ModelTurnCompleted, "stop", 0, now); err != nil {
		t.Fatal(err)
	}
	journal, found, err := repo.LatestModelTurnJournal(ctx, run.ID)
	if err != nil || !found || journal.DraftText != raw || journal.Status != chat.ModelTurnCompleted {
		t.Fatalf("journal lost exact raw: found=%v length=%d err=%v", found, len([]rune(journal.DraftText)), err)
	}
	steps, err := repo.ListRunSteps(ctx, run.ID)
	if err != nil || len(steps) != 1 || steps[0].Commentary != preview {
		t.Fatalf("bounded preview: %v", err)
	}
	if err := repo.BeginModelTurn(ctx, run.ID, 2, now); err != nil {
		t.Fatal(err)
	}
	boundary := " " + strings.Repeat("界", 199998) + "\n"
	if err := repo.UpdateModelTurnDraft(ctx, run.ID, 2, boundary, 0, now); err != nil {
		t.Fatalf("200000-rune journal boundary rejected: %v", err)
	}
	if err := repo.UpdateModelTurnDraft(ctx, run.ID, 2, boundary+"x", 0, now); err == nil {
		t.Fatal("oversize raw silently accepted")
	}
	journal, found, err = repo.LatestModelTurnJournal(ctx, run.ID)
	if err != nil || !found || journal.DraftText != boundary {
		t.Fatal("failed oversize update destroyed previous raw")
	}
	if err := repo.SaveRunStep(ctx, chat.RunStep{RunID: run.ID, TurnIndex: 2, Commentary: strings.Repeat("x", 100001), CreatedAt: now, CompletedAt: now}); err == nil {
		t.Fatal("RunStep no longer enforces display bound")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var persisted string
	if err := reopened.DB().QueryRowContext(ctx, `SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=1`, run.ID).Scan(&persisted); err != nil || persisted != raw {
		t.Fatal("reopen lost original whitespace or long content")
	}
}

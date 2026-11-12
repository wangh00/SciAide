package sqlite

import (
	"context"
	"encoding/json"
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
	commentary := "\t" + strings.Repeat("述", 199998) + "\n"
	if err := repo.SaveModelTurnSubmissionCommentary(ctx, run.ID, 2, "", now); err == nil {
		t.Fatal("empty commentary ambiguously bound the audit slot")
	}
	for i := 0; i < 2; i++ {
		if err := repo.SaveModelTurnSubmissionCommentary(ctx, run.ID, 2, commentary, now); err != nil {
			t.Fatal("independent commentary boundary or idempotent write rejected", err)
		}
	}
	if err := repo.SaveModelTurnSubmissionCommentary(ctx, run.ID, 2, commentary+"x", now); err == nil {
		t.Fatal("oversize commentary was accepted")
	}
	if err := repo.SaveModelTurnSubmissionCommentary(ctx, run.ID, 2, "replacement", now); err == nil {
		t.Fatal("original commentary was overwritten")
	}
	journal, found, err = repo.LatestModelTurnJournal(ctx, run.ID)
	if err != nil || !found || journal.DraftText != boundary || journal.SubmissionCommentary != commentary {
		t.Fatal("failed oversize update destroyed previous raw")
	}
	if err := repo.FinishModelTurn(ctx, run.ID, 2, chat.ModelTurnCompleted, "tool_calls", 0, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveModelTurnSubmissionCommentary(ctx, run.ID, 2, "after terminal", now); err == nil {
		t.Fatal("terminal commentary was mutable")
	}
	// Valid JSON containing escapes must not be encoded inside another JSON
	// string merely to retain the accompanying visible text.
	escaped := `{"summary":"` + strings.Repeat(`\u003c`, 30000) + `","limitations":[]}`
	if !json.Valid([]byte(escaped)) || len(escaped) >= 200000 {
		t.Fatal("invalid large-candidate fixture")
	}
	if err := repo.BeginModelTurn(ctx, run.ID, 3, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveModelTurnSubmissionCommentary(ctx, run.ID, 3, "original visible text", now); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateModelTurnDraft(ctx, run.ID, 3, escaped, 0, now); err != nil {
		t.Fatal("previously admitted escaped candidate no longer fits", err)
	}
	encodedEnvelope, _ := json.Marshal(map[string]string{"visibleText": "x", "arguments": escaped})
	if len(encodedEnvelope) <= 200000 {
		t.Fatal("fixture does not reproduce old re-escaping expansion")
	}
	if err := repo.UpdateModelTurnDraft(ctx, run.ID, 3, string(encodedEnvelope), 0, now); err == nil {
		t.Fatal("original journal limit was widened to hide encoding expansion")
	}
	journal, found, err = repo.LatestModelTurnJournal(ctx, run.ID)
	if err != nil || !found || journal.DraftText != escaped || journal.SubmissionCommentary != "original visible text" {
		t.Fatal("rejected expanded envelope corrupted separately stored originals", err)
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
	if err := reopened.DB().QueryRowContext(ctx, `SELECT submission_commentary FROM model_turn_journal WHERE run_id=? AND turn_index=2`, run.ID).Scan(&persisted); err != nil || persisted != commentary {
		t.Fatal("reopen lost independent original commentary", err)
	}
}

package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestMessageCitationsPersistAcrossReopen(t *testing.T) {
	testMessageCitationsPersistAcrossReopen(t, citation.KnowledgeToolName, nil)
}

func TestIndexedReadMessageCitationsPersistAcrossReopen(t *testing.T) {
	for _, name := range []string{"builtin.knowledge.read", "builtin.resource.open"} {
		t.Run(name, func(t *testing.T) {
			testMessageCitationsPersistAcrossReopen(t, name, json.RawMessage(`{"sourceTool":"builtin.knowledge.read"}`))
		})
	}
}

func testMessageCitationsPersistAcrossReopen(t *testing.T, toolName string, structured json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "citations.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	createdProject, err := projects.Create(ctx, "Citations", "")
	if err != nil {
		t.Fatal(err)
	}
	conversationRepository := NewConversationRepository(store.DB())
	createdConversation, err := conversation.NewService(conversationRepository).Create(ctx, createdProject.ID, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO model_profiles(id,name,provider_type,base_url,model_id,secret_ref,timeout_seconds,custom_headers_json,enabled,is_default,created_at,updated_at) VALUES ('profile','fixture','openai_compatible','https://example.test/v1','model','secret',60,'{}',1,1,?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "run", ConversationID: createdConversation.ID, UserMessageID: "user", AssistantMessageID: "assistant", ModelProfileID: "profile", ModelID: "model", PermissionMode: conversation.PermissionPlan, Status: chat.RunRunning, CreatedAt: now, UpdatedAt: now}
	user := conversation.Message{ID: "user", ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "user-part", MessageID: "user", Type: "text", Text: "question", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	assistant := conversation.Message{ID: "assistant", ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{ID: "assistant-part", MessageID: "assistant", Type: "text", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	runRepository := NewRunRepository(store.DB())
	if err := runRepository.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(time.Second)
	quote := "verifiable source excerpt"
	quoteSHA256 := citation.QuoteSHA256(quote)
	reference := citation.KnowledgeReference(run.ID, "index-v3", "chunk", quoteSHA256)
	value := conversation.Citation{ID: "citation", MessageID: assistant.ID, RunID: run.ID, ToolCallID: "call", ProjectID: createdProject.ID, Reference: reference, Ordinal: 0, IndexVersionID: "index-v3", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "paper.pdf", MIMEType: "application/pdf", Locator: "page:4", Title: "Results", Quote: quote, QuoteSHA256: quoteSHA256, SourceStart: 10, SourceEnd: 36, CreatedAt: completedAt}
	toolRepository := NewToolRepository(store.DB())
	if err := toolRepository.Create(ctx, tool.Call{ID: "call", RunID: run.ID, ProviderCallID: "provider-call", ToolName: toolName, ToolVersion: "3", Arguments: json.RawMessage(`{"query":"question"}`), Status: tool.CallRunning, Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{}, Idempotent: true, CreatedAt: now, StartedAt: &now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	toolCitation := tool.CitationRef{Kind: citation.KindKnowledgeChunk, Reference: reference, ProjectID: createdProject.ID, IndexVersionID: value.IndexVersionID, DocumentID: value.DocumentID, AttachmentID: value.AttachmentID, ChunkID: value.ChunkID, SourceName: value.SourceName, MIMEType: value.MIMEType, Locator: value.Locator, Title: value.Title, Quote: quote, QuoteSHA256: quoteSHA256, SourceStart: value.SourceStart, SourceEnd: value.SourceEnd}
	if toolName != citation.KnowledgeToolName {
		// The failure seen in production also occurs when an old seed and a
		// later actual read issue the same marker: Resolve selects the reader.
		seed := tool.Call{ID: "seed", RunID: run.ID, ProviderCallID: "seed-provider", ToolName: citation.WorkflowSeedToolName, ToolVersion: "1", Arguments: json.RawMessage(`{}`), Status: tool.CallRunning, Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{}, CreatedAt: now.Add(-time.Second), UpdatedAt: now}
		if err := toolRepository.Create(ctx, seed); err != nil {
			t.Fatal(err)
		}
		if err := toolRepository.Finish(ctx, seed.ID, tool.CallRunning, tool.CallCompleted, tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{toolCitation}, CreatedAt: completedAt}, "", "", completedAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := toolRepository.Finish(ctx, "call", tool.CallRunning, tool.CallCompleted, tool.Result{Status: tool.ResultSuccess, Text: reference + "\n" + quote, Structured: structured, Citations: []tool.CitationRef{toolCitation}, CreatedAt: completedAt}, "", "", completedAt); err != nil {
		t.Fatal(err)
	}
	answer := "answer " + reference
	calls, err := toolRepository.ListByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolved := citation.Resolve(run.ID, assistant.ID, answer, calls, completedAt)
	if len(resolved) != 1 || resolved[0].ToolCallID != "call" {
		t.Fatal("actual reader did not resolve", resolved)
	}
	value = resolved[0]
	run.Status, run.FinishReason, run.UpdatedAt, run.CompletedAt = chat.RunCompleted, "stop", completedAt, &completedAt
	invalid := value
	invalid.Reference = "[K-000000000000]"
	if err := runRepository.Complete(ctx, run, answer, []conversation.Citation{invalid}); err == nil {
		t.Fatal("invalid citation unexpectedly completed the run")
	}
	unchangedRun, err := runRepository.Get(ctx, run.ID)
	if err != nil || unchangedRun.Status != chat.RunRunning {
		t.Fatalf("failed completion changed run = %#v, %v", unchangedRun, err)
	}
	unchangedMessages, err := conversationRepository.ListMessages(ctx, createdConversation.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range unchangedMessages {
		if message.ID == assistant.ID && (message.Status != conversation.MessageStreaming || message.Parts[0].Text != "" || len(message.Citations) != 0) {
			t.Fatalf("failed completion changed assistant message = %#v", message)
		}
	}
	// Existing provenance/identity checks must still roll back the whole save.
	for _, mutation := range []struct {
		query string
		value any
	}{
		{`UPDATE tool_calls SET tool_name=? WHERE id='call'`, "builtin.workspace.read_text"},
		{`UPDATE tool_calls SET status=? WHERE id='call'`, "failed"},
		{`UPDATE tool_results SET status=? WHERE tool_call_id='call'`, "error"},
	} {
		if _, err := store.DB().ExecContext(ctx, mutation.query, mutation.value); err != nil {
			t.Fatal(err)
		}
		if err := runRepository.Complete(ctx, run, answer, []conversation.Citation{value}); err == nil {
			t.Fatal("untrusted source completed the run")
		}
		if _, err := store.DB().ExecContext(ctx, `UPDATE tool_calls SET tool_name=?,status='completed',run_id=? WHERE id='call'`, toolName, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().ExecContext(ctx, `UPDATE tool_results SET status='success' WHERE tool_call_id='call'`); err != nil {
			t.Fatal(err)
		}
	}
	if toolName == "builtin.resource.open" {
		for _, body := range []string{`{}`, `{"sourceTool":"builtin.document.read"}`, `{"sourceTool":42}`, `{"sourceTool":"builtin.workspace.read_text"}`} {
			if _, err := store.DB().ExecContext(ctx, `UPDATE tool_results SET structured_json=? WHERE tool_call_id='call'`, body); err != nil {
				t.Fatal(err)
			}
			if err := runRepository.Complete(ctx, run, answer, []conversation.Citation{value}); err == nil {
				t.Fatal("non-indexed resource citation accepted")
			}
		}
		if _, err := store.DB().ExecContext(ctx, `UPDATE tool_results SET structured_json=? WHERE tool_call_id='call'`, string(structured)); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []conversation.Citation{func() conversation.Citation { v := value; v.ProjectID = "other-project"; return v }(), func() conversation.Citation { v := value; v.QuoteSHA256 = citation.QuoteSHA256("changed"); return v }()} {
		if err := runRepository.Complete(ctx, run, answer, []conversation.Citation{invalid}); err == nil {
			t.Fatal("changed citation accepted")
		}
	}
	otherConversation, err := conversation.NewService(conversationRepository).Create(ctx, createdProject.ID, "other run")
	if err != nil {
		t.Fatal(err)
	}
	otherRun := chat.Run{ID: "other-run", ConversationID: otherConversation.ID, UserMessageID: "other-user", AssistantMessageID: "other-assistant", ModelProfileID: "profile", ModelID: "model", Status: chat.RunRunning, CreatedAt: now, UpdatedAt: now}
	otherUser, otherAssistant := user, assistant
	otherUser.ID, otherUser.RunID, otherUser.ConversationID = otherRun.UserMessageID, otherRun.ID, otherRun.ConversationID
	otherUser.Parts = []conversation.MessagePart{{ID: "other-user-part", MessageID: otherUser.ID, Type: "text", CreatedAt: now}}
	otherAssistant.ID, otherAssistant.RunID, otherAssistant.ConversationID = otherRun.AssistantMessageID, otherRun.ID, otherRun.ConversationID
	otherAssistant.Parts = []conversation.MessagePart{{ID: "other-assistant-part", MessageID: otherAssistant.ID, Type: "text", CreatedAt: now}}
	if err := runRepository.CreateWithMessages(ctx, otherRun, otherUser, otherAssistant); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE tool_calls SET run_id=? WHERE id='call'`, otherRun.ID); err != nil {
		t.Fatal(err)
	}
	if err := runRepository.Complete(ctx, run, answer, []conversation.Citation{value}); err == nil {
		t.Fatal("foreign Run source completed answer")
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE tool_calls SET run_id=? WHERE id='call'`, run.ID); err != nil {
		t.Fatal(err)
	}
	unchangedRun, err = runRepository.Get(ctx, run.ID)
	if err != nil || unchangedRun.Status != chat.RunRunning {
		t.Fatal("rejected citation changed run", err)
	}
	unchangedMessages, err = conversationRepository.ListMessages(ctx, createdConversation.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range unchangedMessages {
		if message.ID == assistant.ID && (message.Status != conversation.MessageStreaming || message.Parts[0].Text != "" || len(message.Citations) != 0) {
			t.Fatal("rejected citation changed message")
		}
	}
	if err := runRepository.Complete(ctx, run, answer, []conversation.Citation{value}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	messages, err := NewConversationRepository(reopened.DB()).ListMessages(ctx, createdConversation.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var loaded []conversation.Citation
	for _, message := range messages {
		if message.ID == assistant.ID {
			if message.Status != conversation.MessageComplete || message.Parts[0].Text != answer {
				t.Fatalf("reloaded assistant message = %#v", message)
			}
			loaded = message.Citations
		}
	}
	if len(loaded) != 1 || loaded[0].Reference != value.Reference || loaded[0].Quote != quote || loaded[0].Locator != "page:4" || loaded[0].Ordinal != 0 {
		t.Fatalf("reloaded citations = %#v", loaded)
	}
	loadedRun, err := NewRunRepository(reopened.DB()).Get(ctx, run.ID)
	if err != nil || loadedRun.Status != chat.RunCompleted || loadedRun.CompletedAt == nil {
		t.Fatalf("reloaded run = %#v, %v", loadedRun, err)
	}
	if err := NewRunRepository(reopened.DB()).Complete(ctx, run, "changed", nil); err == nil {
		t.Fatal("terminal run citations were unexpectedly replaceable")
	}
	messages, err = NewConversationRepository(reopened.DB()).ListMessages(ctx, createdConversation.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == assistant.ID && (message.Parts[0].Text != answer || len(message.Citations) != 1 || message.Citations[0].Reference != reference) {
			t.Fatalf("terminal citation snapshot changed: %#v", message)
		}
	}
}

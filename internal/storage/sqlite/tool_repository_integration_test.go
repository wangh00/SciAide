package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestToolRepositoryPersistsResultAndUsesOptimisticTransition(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	repository := NewToolRepository(store.DB())
	now := time.Now().UTC()
	call := tool.Call{ID: "call", RunID: run.ID, ProviderCallID: "provider-call", ToolName: "builtin.workspace.read_file", ToolVersion: "1", Arguments: json.RawMessage(`{"path":"paper.md"}`), Status: tool.CallPending, Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "paper.md"}}, Idempotent: true, IdempotencyKey: "provider-call", CreatedAt: now, UpdatedAt: now}
	if err := repository.Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	duplicate := call
	duplicate.ID, duplicate.ProviderCallID = "call-duplicate", "provider-call-duplicate"
	if err := repository.Create(ctx, duplicate); err == nil {
		t.Fatal("duplicate run-scoped idempotency key was accepted")
	}
	if err := repository.Transition(ctx, call.ID, tool.CallPending, tool.CallRunning, "", "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Transition(ctx, call.ID, tool.CallPending, tool.CallFailed, "STALE", "", now.Add(time.Second)); !errors.Is(err, tool.ErrTransitionConflict) {
		t.Fatalf("stale transition = %v", err)
	}
	result := tool.Result{Status: tool.ResultSuccess, Text: "paper", Structured: json.RawMessage(`{"bytes":5}`), Artifacts: []tool.ArtifactRef{{ID: "artifact"}}, Citations: []tool.CitationRef{{ID: "source", Locator: "p.1"}}, Meta: tool.ResultMeta{DurationMillis: 12}, CreatedAt: now.Add(2 * time.Second)}
	if err := repository.Finish(ctx, call.ID, tool.CallRunning, tool.CallCompleted, result, "", "", result.CreatedAt); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Get(ctx, call.ID)
	if err != nil || loaded.Status != tool.CallCompleted || loaded.Result == nil || len(loaded.Result.Artifacts) != 1 || string(loaded.Result.Structured) != `{"bytes":5}` || !loaded.Idempotent || len(loaded.Permissions) != 1 {
		t.Fatalf("loaded = %#v, %v", loaded, err)
	}
	if err := repository.Finish(ctx, call.ID, tool.CallRunning, tool.CallCompleted, result, "", "", result.CreatedAt); !errors.Is(err, tool.ErrTransitionConflict) {
		t.Fatalf("duplicate finish = %v", err)
	}
}

func TestToolServiceCommitsStateAndAuditEventTogether(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	service := tool.NewService(NewToolRepository(store.DB()), tool.JSONSchemaValidator{})
	definition := tool.Definition{QualifiedName: "builtin.workspace.list", Description: "List workspace", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), Risk: tool.RiskLow, Idempotent: true, Version: "1"}
	call, err := service.Propose(ctx, definition, tool.CreateCommand{RunID: run.ID, ProviderCallID: "service-provider", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(ctx, call.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, call.ID, tool.Result{Status: tool.ResultSuccess, Text: "done"}, "", ""); err != nil {
		t.Fatal(err)
	}
	rows, err := store.DB().QueryContext(ctx, `SELECT sequence, event_type FROM run_events WHERE aggregate_id=? ORDER BY sequence`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	var expected int64 = 1
	for rows.Next() {
		var sequence int64
		var eventType string
		if err := rows.Scan(&sequence, &eventType); err != nil {
			t.Fatal(err)
		}
		if sequence != expected {
			t.Fatalf("event sequence=%d want=%d", sequence, expected)
		}
		expected++
		types = append(types, eventType)
	}
	if len(types) != 3 || types[0] != "tool.proposed" || types[1] != "tool.running" || types[2] != "tool.completed" {
		t.Fatalf("event types=%v", types)
	}
}

func TestToolRepositoryRecoversActiveCallsAndCascadesWithRun(t *testing.T) {
	ctx := context.Background()
	store, run := createToolFixture(t)
	defer store.Close()
	repository := NewToolRepository(store.DB())
	now := time.Now().UTC()
	for index, status := range []tool.CallStatus{tool.CallPending, tool.CallAwaitingApproval, tool.CallRunning} {
		id := string(rune('a' + index))
		if err := repository.Create(ctx, tool.Call{ID: id, RunID: run.ID, ProviderCallID: "provider-" + id, ToolName: "builtin.workspace.list", ToolVersion: "1", Arguments: json.RawMessage(`{}`), Status: status, Risk: tool.RiskLow, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := repository.InterruptActive(ctx, now.Add(time.Second)); err != nil || count != 3 {
		t.Fatalf("InterruptActive() = %d, %v", count, err)
	}
	values, err := repository.ListByRun(ctx, run.ID)
	if err != nil || len(values) != 3 {
		t.Fatalf("ListByRun() = %#v, %v", values, err)
	}
	for _, value := range values {
		if value.Status != tool.CallInterrupted || value.CompletedAt == nil {
			t.Fatalf("not interrupted: %#v", value)
		}
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM tool_calls WHERE run_id = ?`, run.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("remaining calls = %d, %v", remaining, err)
	}
}

func createToolFixture(t *testing.T) (*Store, chat.Run) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "P2", "tools")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "tool run")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "tool-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "tool-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		store.Close()
		t.Fatal(err)
	}
	user := conversation.Message{ID: "tool-user", ConversationID: createdConversation.ID, RunID: "tool-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "tool-user-part", MessageID: "tool-user", Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: "tool-assistant", ConversationID: createdConversation.ID, RunID: "tool-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "tool-assistant-part", MessageID: "tool-assistant", Type: "text", CreatedAt: now}}}
	run := chat.Run{ID: "tool-run", ConversationID: createdConversation.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunRunning, CreatedAt: now, UpdatedAt: now}
	if err := NewRunRepository(store.DB()).CreateWithMessages(ctx, run, user, assistant); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, run
}

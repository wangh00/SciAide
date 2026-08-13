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
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/modelcap"
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

func TestModelProfileAndRunProtocolPersistAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "protocol.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Protocol", "")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "messages")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "protocol-profile", Name: "Anthropic", ProviderType: modelprofile.ProviderOpenAICompatible, APIProtocol: modelcap.ProtocolAnthropic, BaseURL: "https://api.anthropic.com/v1", ModelID: "claude", Models: []modelprofile.ProfileModel{{ID: "claude", Enabled: true, IsDefault: true}}, SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "protocol-run", ConversationID: createdConversation.ID, UserMessageID: "protocol-user", AssistantMessageID: "protocol-assistant", ModelProfileID: profile.ID, ModelID: "claude", APIProtocol: modelcap.ProtocolAnthropic, Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "protocol-user-part", MessageID: run.UserMessageID, Type: "text", Text: "q", CreatedAt: now}}}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "protocol-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}}
	if err := NewRunRepository(store.DB()).CreateWithMessages(ctx, run, user, assistant); err != nil {
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
	loadedProfile, err := NewModelProfileRepository(store.DB()).Get(ctx, profile.ID)
	if err != nil || loadedProfile.APIProtocol != modelcap.ProtocolAnthropic {
		t.Fatalf("profile = %#v, %v", loadedProfile, err)
	}
	loadedRun, err := NewRunRepository(store.DB()).Get(ctx, run.ID)
	if err != nil || loadedRun.APIProtocol != modelcap.ProtocolAnthropic {
		t.Fatalf("run = %#v, %v", loadedRun, err)
	}
}

func TestRunRepositoryPersistsAndAggregatesCacheUsage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "cache-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Cache", "")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "usage")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "cache-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "cache-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "cache-run", ConversationID: createdConversation.ID, UserMessageID: "cache-user", AssistantMessageID: "cache-assistant", ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunRunning, InputTokens: 100, FreshInputTokens: 24, OutputTokens: 20, CachedInputTokens: 64, CacheWriteTokens: 12, CacheReportedTurns: 2, CacheReportedFreshInputTokens: 24, CacheHitTurns: 1, ModelTurns: 2, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "cache-user-part", MessageID: run.UserMessageID, Type: "text", Text: "q", CreatedAt: now}}}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now.Add(time.Nanosecond), UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "cache-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}}
	repository := NewRunRepository(store.DB())
	if err := repository.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Get(ctx, run.ID)
	if err != nil || loaded.CachedInputTokens != 64 || loaded.CacheReportedTurns != 2 || loaded.CacheHitTurns != 1 {
		t.Fatalf("loaded run = %#v, err=%v", loaded, err)
	}
	statistics, err := repository.UsageDashboard(ctx, chat.UsageQuery{ModelProfileID: profile.ID})
	if err != nil || statistics.Summary.RunCount != 1 || statistics.Summary.FreshInputTokens != 24 || statistics.Summary.CacheReadTokens != 64 || statistics.Summary.CacheCreationTokens != 12 || statistics.Summary.CacheHitTurns != 1 || statistics.Summary.RealTotalTokens != 120 || statistics.Summary.CacheHitRate != 0.64 {
		t.Fatalf("statistics = %#v, err=%v", statistics, err)
	}
}

func TestUsageDashboardAggregatesGloballyAndFiltersByLocalDateAndModel(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "usage-dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Usage", "")
	if err != nil {
		t.Fatal(err)
	}
	profiles := []modelprofile.Profile{
		{ID: "profile-a", Name: "Gateway A", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://a.test/v1", ModelID: "model-a", Models: []modelprofile.ProfileModel{{ID: "model-a", Enabled: true, IsDefault: true}}, SecretRef: "secret-a", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true},
		{ID: "profile-b", Name: "Gateway B", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://b.test/v1", ModelID: "model-b", Models: []modelprofile.ProfileModel{{ID: "model-b", Enabled: true, IsDefault: true}}, SecretRef: "secret-b", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true},
	}
	base := time.Date(2026, 8, 12, 2, 0, 0, 0, time.UTC)
	for index := range profiles {
		profiles[index].CreatedAt, profiles[index].UpdatedAt = base, base
		if err := NewModelProfileRepository(store.DB()).Save(ctx, profiles[index]); err != nil {
			t.Fatal(err)
		}
	}
	runs := NewRunRepository(store.DB())
	fixtures := []chat.Run{
		{ID: "usage-a", ModelProfileID: "profile-a", ModelID: "model-a", FreshInputTokens: 100, OutputTokens: 50, CachedInputTokens: 600, CacheWriteTokens: 300, CacheReportedTurns: 1, CacheReportedFreshInputTokens: 100, CacheHitTurns: 1, ModelTurns: 1, CreatedAt: base},
		{ID: "usage-b", ModelProfileID: "profile-b", ModelID: "model-b", FreshInputTokens: 200, OutputTokens: 40, CachedInputTokens: 0, CacheWriteTokens: 0, CacheReportedTurns: 0, ModelTurns: 1, CreatedAt: base.Add(24 * time.Hour)},
	}
	for _, fixture := range fixtures {
		createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, fixture.ID)
		if err != nil {
			t.Fatal(err)
		}
		fixture.ConversationID, fixture.UserMessageID, fixture.AssistantMessageID = createdConversation.ID, fixture.ID+"-user", fixture.ID+"-assistant"
		fixture.Status, fixture.UpdatedAt = chat.RunRunning, fixture.CreatedAt
		user := conversation.Message{ID: fixture.UserMessageID, ConversationID: createdConversation.ID, RunID: fixture.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: fixture.CreatedAt, UpdatedAt: fixture.CreatedAt, Parts: []conversation.MessagePart{{ID: fixture.ID + "-user-part", MessageID: fixture.UserMessageID, Type: "text", CreatedAt: fixture.CreatedAt}}}
		assistant := conversation.Message{ID: fixture.AssistantMessageID, ConversationID: createdConversation.ID, RunID: fixture.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: fixture.CreatedAt, UpdatedAt: fixture.CreatedAt, Parts: []conversation.MessagePart{{ID: fixture.ID + "-assistant-part", MessageID: fixture.AssistantMessageID, Type: "text", CreatedAt: fixture.CreatedAt}}}
		if err := runs.CreateWithMessages(ctx, fixture, user, assistant); err != nil {
			t.Fatal(err)
		}
	}
	dashboard, err := runs.UsageDashboard(ctx, chat.UsageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	// Never average per-model percentages: aggregate reported buckets first.
	// The unreported model-b fresh input is intentionally outside the hit-rate denominator.
	if dashboard.Summary.RealTotalTokens != 1290 || dashboard.Summary.CacheHitRate != 0.6 || len(dashboard.Models) != 2 || len(dashboard.Daily) != 2 {
		t.Fatalf("global dashboard = %#v", dashboard)
	}
	filtered, err := runs.UsageDashboard(ctx, chat.UsageQuery{StartDate: "2026-08-13", EndDate: "2026-08-13", ModelID: "model-b"})
	if err != nil || filtered.Summary.RunCount != 1 || filtered.Summary.FreshInputTokens != 200 || filtered.Summary.CacheDataAvailable {
		t.Fatalf("filtered dashboard = %#v, %v", filtered, err)
	}
}

func TestConversationMessagesKeepUserBeforeAssistantAtSameTimestamp(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "message-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Order", "")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "order")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, message := range []conversation.Message{
		{ID: "aaa-assistant", ConversationID: createdConversation.ID, Role: conversation.RoleAssistant, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "assistant-part", MessageID: "aaa-assistant", Type: "text", Text: "answer", CreatedAt: now}}},
		{ID: "zzz-user", ConversationID: createdConversation.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "user-part", MessageID: "zzz-user", Type: "text", Text: "question", CreatedAt: now}}},
	} {
		if err := NewConversationRepository(store.DB()).CreateMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := NewConversationRepository(store.DB()).ListMessages(ctx, createdConversation.ID, 10)
	if err != nil || len(messages) != 2 || messages[0].Role != conversation.RoleUser || messages[1].Role != conversation.RoleAssistant {
		t.Fatalf("message order = %#v, %v", messages, err)
	}
}

func TestRunRepositoryPersistsAndBoundsModelTurns(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "P2", "budget")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "budget")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "budget-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "budget-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "budget-user", ConversationID: createdConversation.ID, RunID: "budget-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "budget-user-part", MessageID: "budget-user", Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: "budget-assistant", ConversationID: createdConversation.ID, RunID: "budget-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "budget-assistant-part", MessageID: "budget-assistant", Type: "text", CreatedAt: now}}}
	runs := NewRunRepository(store.DB())
	run := chat.Run{ID: "budget-run", ConversationID: createdConversation.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunRunning, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	if err := runs.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	if loaded, err := runs.IncrementModelTurns(ctx, run.ID, 1, now.Add(time.Second)); err != nil || loaded.ModelTurns != 1 {
		t.Fatalf("first model turn = %#v, %v", loaded, err)
	}
	if _, err := runs.IncrementModelTurns(ctx, run.ID, 1, now.Add(2*time.Second)); !errors.Is(err, chat.ErrModelTurnBudgetExceeded) {
		t.Fatalf("second model turn error = %v", err)
	}
}

func TestCancelRunAtomicallyClosesApprovalToolAndMessage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "P2", "cancel")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "cancel-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "cancel-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "cancel-user", ConversationID: createdConversation.ID, RunID: "cancel-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "cancel-user-part", MessageID: "cancel-user", Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: "cancel-assistant", ConversationID: createdConversation.ID, RunID: "cancel-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "cancel-assistant-part", MessageID: "cancel-assistant", Type: "text", Text: "partial", CreatedAt: now}}}
	runs := NewRunRepository(store.DB())
	run := chat.Run{ID: "cancel-run", ConversationID: createdConversation.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunWaitingApproval, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	if err := runs.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "cancel-call", RunID: run.ID, ProviderCallID: "provider-call", ToolName: "builtin.fixture", ToolVersion: "1", Arguments: json.RawMessage(`{}`), Status: tool.CallAwaitingApproval, Risk: tool.RiskModerate, Permissions: []tool.PermissionRequirement{}, CreatedAt: now, UpdatedAt: now}
	if err := NewToolRepository(store.DB()).Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	approval := permission.Approval{ID: "cancel-approval", RunID: run.ID, ToolCallID: call.ID, ProjectID: createdProject.ID, ToolName: call.ToolName, ToolVersion: call.ToolVersion, PermissionKind: tool.PermissionToolInvoke, Resource: call.ToolName, Risk: call.Risk, Status: permission.ApprovalPending, RequestedScope: permission.ScopeProject, CreatedAt: now}
	event := permissionEvent(run.ID, "approval.requested")
	if err := NewPermissionRepository(store.DB()).CreateApprovalWithEvent(ctx, approval, event); err != nil {
		t.Fatal(err)
	}
	cancelAt := now.Add(time.Second)
	cancelEvent := events.New("cancel-run-event", run.ID, "run", "run.cancelled", 0, json.RawMessage(`{}`))
	cancelEvent.Timestamp = cancelAt
	if _, _, err := runs.CancelRun(ctx, run.ID, "RUN_CANCELLED", "cancelled", cancelAt, cancelEvent); err != nil {
		t.Fatal(err)
	}
	loadedRun, _ := runs.Get(ctx, run.ID)
	loadedCall, _ := NewToolRepository(store.DB()).Get(ctx, call.ID)
	loadedApproval, _ := NewPermissionRepository(store.DB()).GetApproval(ctx, approval.ID)
	messages, _ := NewConversationRepository(store.DB()).ListMessages(ctx, createdConversation.ID, 10)
	var assistantStatus conversation.MessageStatus
	for _, message := range messages {
		if message.ID == assistant.ID {
			assistantStatus = message.Status
		}
	}
	if loadedRun.Status != chat.RunCancelled || loadedCall.Status != tool.CallCancelled || loadedCall.Result == nil || loadedCall.Result.Status != tool.ResultCancelled || loadedApproval.Status != permission.ApprovalExpired || assistantStatus != conversation.MessageIncomplete {
		t.Fatalf("cancel state run=%s call=%s approval=%s message=%s", loadedRun.Status, loadedCall.Status, loadedApproval.Status, assistantStatus)
	}
	if _, changed, err := runs.CancelRun(ctx, run.ID, "RUN_CANCELLED", "cancelled", cancelAt, cancelEvent); err != nil || changed {
		t.Fatalf("idempotent cancel: %v", err)
	}
	stale := loadedRun
	stale.Status = chat.RunCompleted
	if err := runs.Update(ctx, stale); err == nil {
		t.Fatal("terminal run was overwritten by stale update")
	}
}

func TestFailRunAtomicallyInterruptsPendingToolCalls(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "P2", "fail")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "fail")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "fail-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "fail-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "fail-user", ConversationID: createdConversation.ID, RunID: "fail-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "fail-user-part", MessageID: "fail-user", Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: "fail-assistant", ConversationID: createdConversation.ID, RunID: "fail-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "fail-assistant-part", MessageID: "fail-assistant", Type: "text", CreatedAt: now}}}
	runs := NewRunRepository(store.DB())
	run := chat.Run{ID: "fail-run", ConversationID: createdConversation.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunRunning, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	if err := runs.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "fail-call", RunID: run.ID, ProviderCallID: "provider-call", ToolName: "builtin.fixture", ToolVersion: "1", Arguments: json.RawMessage(`{}`), Status: tool.CallPending, Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{}, CreatedAt: now, UpdatedAt: now}
	if err := NewToolRepository(store.DB()).Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	failEvent := events.New("fail-run-event", run.ID, "run", "run.failed", 0, json.RawMessage(`{}`))
	failAt := now.Add(time.Second)
	failEvent.Timestamp = failAt
	if _, _, err := runs.FailRun(ctx, run.ID, "TOOL_CALL_REJECTED", "failed", failAt, failEvent); err != nil {
		t.Fatal(err)
	}
	loadedRun, _ := runs.Get(ctx, run.ID)
	loadedCall, _ := NewToolRepository(store.DB()).Get(ctx, call.ID)
	if loadedRun.Status != chat.RunFailed || loadedCall.Status != tool.CallInterrupted || loadedCall.Result == nil || loadedCall.Result.Status != tool.ResultError {
		t.Fatalf("failed state run=%#v call=%#v", loadedRun, loadedCall)
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

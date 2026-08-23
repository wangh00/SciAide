package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/model"
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
	if err := repository.BeginModelTurn(ctx, run.ID, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateModelTurnDraft(ctx, run.ID, 1, "first partial", 2, now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := repository.FinishModelTurn(ctx, run.ID, 1, chat.ModelTurnFailed, "", 0, now.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var firstStatus string
	var firstProviderItems int
	if err := store.DB().QueryRowContext(ctx, `SELECT status,provider_item_count FROM model_turn_journal WHERE run_id=? AND turn_index=1`, run.ID).Scan(&firstStatus, &firstProviderItems); err != nil {
		t.Fatal(err)
	}
	if firstStatus != string(chat.ModelTurnFailed) || firstProviderItems != 2 {
		t.Fatalf("first journal = status:%s provider items:%d", firstStatus, firstProviderItems)
	}
	if err := repository.BeginModelTurn(ctx, run.ID, 2, now.Add(3*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateModelTurnDraft(ctx, run.ID, 2, "latest visible draft", 1, now.Add(4*time.Millisecond)); err != nil {
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
	var journalStatus, journalDraft string
	var journalProviderItems int
	if err := store.DB().QueryRowContext(ctx, `SELECT status,draft_text,provider_item_count FROM model_turn_journal WHERE run_id=? AND turn_index=2`, run.ID).Scan(&journalStatus, &journalDraft, &journalProviderItems); err != nil {
		t.Fatal(err)
	}
	if journalStatus != string(chat.ModelTurnInterrupted) || journalDraft != "latest visible draft" || journalProviderItems != 1 {
		t.Fatalf("recovered journal = status:%s draft:%q provider items:%d", journalStatus, journalDraft, journalProviderItems)
	}
	var terminationPayload string
	if err := store.DB().QueryRowContext(ctx, `SELECT payload_json FROM message_parts WHERE id=?`, run.ID+":termination-context").Scan(&terminationPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminationPayload, "latest visible draft") || !strings.Contains(terminationPayload, `"kind":"run_termination_context"`) {
		t.Fatalf("termination payload = %s", terminationPayload)
	}
}

func TestRunEventSequencesRemainContiguousUnderConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "event-sequences.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewRunRepository(store.DB())
	const count = 24
	sequences := make(chan int64, count)
	errors := make(chan error, count)
	var group sync.WaitGroup
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			event := events.New(fmt.Sprintf("event-%02d", index), "sequence-run", "run", "content.delta", 0, json.RawMessage(`{}`))
			persisted, err := repository.AppendNext(ctx, event)
			if err != nil {
				errors <- err
				return
			}
			sequences <- persisted.Sequence
		}(index)
	}
	group.Wait()
	close(errors)
	close(sequences)
	for err := range errors {
		t.Fatalf("concurrent append: %v", err)
	}
	seen := make(map[int64]bool, count)
	for sequence := range sequences {
		if sequence <= 0 || seen[sequence] {
			t.Fatalf("invalid or duplicate sequence %d", sequence)
		}
		seen[sequence] = true
	}
	for sequence := int64(1); sequence <= count; sequence++ {
		if !seen[sequence] {
			t.Fatalf("missing sequence %d from %#v", sequence, seen)
		}
	}
	latest, err := repository.LatestEventSequence(ctx, "sequence-run")
	if err != nil || latest != count {
		t.Fatalf("latest sequence = %d, %v", latest, err)
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
	run := chat.Run{ID: "protocol-run", ConversationID: createdConversation.ID, UserMessageID: "protocol-user", AssistantMessageID: "protocol-assistant", ModelProfileID: profile.ID, ModelID: "claude", APIProtocol: modelcap.ProtocolAnthropic, ReasoningSummary: "provider-visible summary", Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "protocol-user-part", MessageID: run.UserMessageID, Type: "text", Text: "q", CreatedAt: now}}}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "protocol-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}}
	runs := NewRunRepository(store.DB())
	if err := runs.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	providerTurn := model.ProviderTurn{TurnIndex: 1, Protocol: modelcap.ProtocolAnthropic, Items: []model.ProviderItem{
		{Ordinal: 0, Type: "thinking", Payload: json.RawMessage(`{"type":"thinking","thinking":"inspect","signature":"signed-state"}`)},
		{Ordinal: 1, Type: "tool_use", CallID: "call_1", Payload: json.RawMessage(`{"type":"tool_use","id":"call_1","name":"read","input":{}}`)},
	}}
	if err := runs.SaveProviderTurn(ctx, run.ID, providerTurn, now); err != nil {
		t.Fatal(err)
	}
	runStep := chat.RunStep{RunID: run.ID, TurnIndex: 1, Commentary: "Checking the source.", ReasoningSummary: "Compared the evidence.", ReasoningObserved: true, CreatedAt: now, CompletedAt: now.Add(time.Second)}
	if err := runs.SaveRunStep(ctx, runStep); err != nil {
		t.Fatal(err)
	}
	if err := runs.SaveRunStep(ctx, runStep); err != nil {
		t.Fatalf("idempotent run step save: %v", err)
	}
	conflictingStep := runStep
	conflictingStep.Commentary = "rewritten"
	if err := runs.SaveRunStep(ctx, conflictingStep); err == nil {
		t.Fatal("run step mutation was accepted")
	}
	if err := runs.SaveProviderTurn(ctx, run.ID, providerTurn, now.Add(time.Second)); err != nil {
		t.Fatalf("idempotent provider save: %v", err)
	}
	conflict := providerTurn
	conflict.Items = append([]model.ProviderItem(nil), providerTurn.Items...)
	conflict.Items[0].Payload = json.RawMessage(`{"type":"thinking","thinking":"modified","signature":"signed-state"}`)
	if err := runs.SaveProviderTurn(ctx, run.ID, conflict, now.Add(time.Second)); err == nil {
		t.Fatal("provider item mutation was accepted")
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
	if err != nil || loadedRun.APIProtocol != modelcap.ProtocolAnthropic || loadedRun.ReasoningSummary != "provider-visible summary" {
		t.Fatalf("run = %#v, %v", loadedRun, err)
	}
	loadedMessages, err := NewConversationRepository(store.DB()).ListMessages(ctx, createdConversation.ID, 20)
	if err != nil || len(loadedMessages) != 2 || loadedMessages[1].Reasoning == nil || loadedMessages[1].Reasoning.Summary != "provider-visible summary" {
		t.Fatalf("message reasoning projection = %#v, %v", loadedMessages, err)
	}
	turns, err := NewRunRepository(store.DB()).ListProviderTurns(ctx, run.ID)
	if err != nil || len(turns) != 1 || len(turns[0].Items) != 2 || turns[0].Items[0].Type != "thinking" || turns[0].Items[0].Ordinal != 0 || turns[0].Items[1].CallID != "call_1" {
		t.Fatalf("provider turns = %#v, %v", turns, err)
	}
	steps, err := NewRunRepository(store.DB()).ListRunSteps(ctx, run.ID)
	if err != nil || len(steps) != 1 || steps[0].Commentary != runStep.Commentary || steps[0].ReasoningSummary != runStep.ReasoningSummary {
		t.Fatalf("run steps = %#v, %v", steps, err)
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
	run := chat.Run{ID: "cache-run", ConversationID: createdConversation.ID, UserMessageID: "cache-user", AssistantMessageID: "cache-assistant", ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunRunning, InputTokens: 100, FreshInputTokens: 24, OutputTokens: 20, ReasoningTokens: 8, ReasoningObserved: true, CachedInputTokens: 64, CacheWriteTokens: 12, CacheReportedTurns: 2, CacheReportedFreshInputTokens: 24, CacheHitTurns: 1, ModelTurns: 2, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "cache-user-part", MessageID: run.UserMessageID, Type: "text", Text: "q", CreatedAt: now}}}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now.Add(time.Nanosecond), UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "cache-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}}
	repository := NewRunRepository(store.DB())
	if err := repository.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO model_request_usage(id,run_id,turn_index,request_kind,model_profile_id,model_id,api_protocol,input_tokens,fresh_input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cache_write_tokens,cache_details_reported,started_at,completed_at,duration_millis) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"cache-usage", run.ID, 1, "legacy", run.ModelProfileID, run.ModelID, modelcap.ProtocolOpenAIChat,
		run.InputTokens, run.FreshInputTokens, run.OutputTokens, run.ReasoningTokens, run.CachedInputTokens,
		run.CacheWriteTokens, true, formatTime(now), formatTime(now), 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Get(ctx, run.ID)
	if err != nil || loaded.CachedInputTokens != 64 || loaded.CacheReportedTurns != 2 || loaded.CacheHitTurns != 1 || loaded.ReasoningTokens != 8 || !loaded.ReasoningObserved {
		t.Fatalf("loaded run = %#v, err=%v", loaded, err)
	}
	statistics, err := repository.UsageDashboard(ctx, chat.UsageQuery{ModelProfileID: profile.ID})
	if err != nil || statistics.Summary.RunCount != 1 || statistics.Summary.FreshInputTokens != 24 || statistics.Summary.ReasoningTokens != 8 || statistics.Summary.CacheReadTokens != 64 || statistics.Summary.CacheCreationTokens != 12 || statistics.Summary.CacheHitTurns != 1 || statistics.Summary.RealTotalTokens != 120 || statistics.Summary.CacheHitRate != 0.64 {
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
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO model_request_usage(id,run_id,turn_index,request_kind,model_profile_id,model_id,api_protocol,input_tokens,fresh_input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cache_write_tokens,cache_details_reported,started_at,completed_at,duration_millis) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fixture.ID+"-request", fixture.ID, 1, "legacy", fixture.ModelProfileID, fixture.ModelID, modelcap.ProtocolOpenAIChat,
			fixture.InputTokens, fixture.FreshInputTokens, fixture.OutputTokens, fixture.ReasoningTokens,
			fixture.CachedInputTokens, fixture.CacheWriteTokens, fixture.CacheReportedTurns > 0,
			formatTime(fixture.CreatedAt), formatTime(fixture.CreatedAt), 0); err != nil {
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

func TestRequestUsageIsIdempotentAndTimeFilteredWithDashboard(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "request-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Request usage", "")
	if err != nil {
		t.Fatal(err)
	}
	profile := modelprofile.Profile{ID: "request-profile", Name: "Request API", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "request-model", Models: []modelprofile.ProfileModel{{ID: "request-model", Enabled: true, IsDefault: true}}, SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true}
	base := time.Date(2026, 8, 20, 15, 49, 0, 0, time.Local).UTC()
	profile.CreatedAt, profile.UpdatedAt = base, base
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "request usage")
	if err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "request-run", ConversationID: createdConversation.ID, UserMessageID: "request-user", AssistantMessageID: "request-assistant", ModelProfileID: profile.ID, ModelID: profile.ModelID, APIProtocol: modelcap.ProtocolOpenAIResponses, Status: chat.RunRunning, CreatedAt: base, StartedAt: &base, UpdatedAt: base}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: base, UpdatedAt: base, Parts: []conversation.MessagePart{{ID: "request-user-part", MessageID: run.UserMessageID, Type: "text", CreatedAt: base}}}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: base, UpdatedAt: base, Parts: []conversation.MessagePart{{ID: "request-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: base}}}
	repository := NewRunRepository(store.DB())
	if err := repository.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	fixtures := []chat.RequestUsage{
		{ID: "request-1", RunID: run.ID, TurnIndex: 1, RequestKind: "conversation", StatusCode: 200, IsStreaming: true, InputTokens: 100, FreshInputTokens: 100, OutputTokens: 10, CacheDetailsReported: true, StartedAt: base, CompletedAt: base.Add(500 * time.Millisecond)},
		{ID: "request-2", RunID: run.ID, TurnIndex: 2, RequestKind: "conversation", StatusCode: 200, IsStreaming: true, InputTokens: 100, FreshInputTokens: 20, OutputTokens: 20, CachedInputTokens: 80, CacheDetailsReported: true, StartedAt: time.Date(2026, 8, 20, 16, 30, 0, 0, time.Local).UTC(), CompletedAt: time.Date(2026, 8, 20, 16, 30, 2, 0, time.Local).UTC()},
		{ID: "request-3", RunID: run.ID, TurnIndex: 3, RequestKind: "compaction", StatusCode: 200, IsStreaming: true, InputTokens: 100, FreshInputTokens: 10, OutputTokens: 30, CachedInputTokens: 90, CacheDetailsReported: true, StartedAt: time.Date(2026, 8, 20, 17, 31, 0, 0, time.Local).UTC(), CompletedAt: time.Date(2026, 8, 20, 17, 31, 3, 0, time.Local).UTC()},
		{ID: "request-4", RunID: run.ID, TurnIndex: 4, RequestKind: "conversation", StatusCode: 503, ErrorCode: "MODEL_UNAVAILABLE", ErrorMessage: "upstream unavailable", IsStreaming: true, StartedAt: time.Date(2026, 8, 20, 16, 45, 0, 0, time.Local).UTC(), CompletedAt: time.Date(2026, 8, 20, 16, 45, 4, 0, time.Local).UTC()},
	}
	for _, fixture := range fixtures {
		if _, inserted, err := repository.RecordModelUsage(ctx, fixture); err != nil || !inserted {
			t.Fatalf("record request usage = inserted:%v err:%v", inserted, err)
		}
	}
	if _, inserted, err := repository.RecordModelUsage(ctx, fixtures[1]); err != nil || inserted {
		t.Fatalf("idempotent request usage = inserted:%v err:%v", inserted, err)
	}
	loaded, err := repository.Get(ctx, run.ID)
	if err != nil || loaded.FreshInputTokens != 130 || loaded.CachedInputTokens != 170 || loaded.OutputTokens != 60 || loaded.CacheReportedTurns != 3 || loaded.ModelTurns != 4 {
		t.Fatalf("request usage aggregate = %#v, %v", loaded, err)
	}
	query := chat.UsageQuery{StartDate: "2026-08-20", EndDate: "2026-08-20", StartTime: "15:50", EndTime: "17:30", ModelProfileID: profile.ID}
	dashboard, err := repository.UsageDashboard(ctx, query)
	if err != nil || dashboard.Summary.ModelTurns != 1 || dashboard.Summary.RequestCount != 2 || dashboard.Summary.SuccessfulRequests != 1 || dashboard.Summary.FailedRequests != 1 || dashboard.Summary.SuccessRate != 0.5 || dashboard.Summary.FreshInputTokens != 20 || dashboard.Summary.CacheReadTokens != 80 || dashboard.Summary.OutputTokens != 20 || dashboard.Summary.CacheHitRate != 0.8 {
		t.Fatalf("time-filtered dashboard = %#v, %v", dashboard, err)
	}
	page, err := repository.UsageRequests(ctx, chat.UsageRequestQuery{StartDate: query.StartDate, EndDate: query.EndDate, StartTime: query.StartTime, EndTime: query.EndTime, ModelProfileID: profile.ID, Limit: 20})
	if err != nil || page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != "request-4" || page.Items[1].ID != "request-2" || page.Items[1].DurationMillis != 2000 {
		t.Fatalf("time-filtered request page = %#v, %v", page, err)
	}
	failed, err := repository.UsageRequests(ctx, chat.UsageRequestQuery{StatusCode: 503, Limit: 20})
	if err != nil || failed.Total != 1 || len(failed.Items) != 1 || failed.Items[0].ErrorCode != "MODEL_UNAVAILABLE" || failed.Items[0].ErrorMessage != "upstream unavailable" {
		t.Fatalf("failed request filter = %#v, %v", failed, err)
	}
	all, err := repository.UsageRequests(ctx, chat.UsageRequestQuery{Limit: 20})
	if err != nil || len(all.Items) != 4 || all.Items[0].ID != "request-3" || all.Items[3].ID != "request-1" {
		t.Fatalf("request order = %#v, %v", all, err)
	}
	auxiliary := []chat.RequestUsage{
		{ID: "request-image-probe", RunID: run.ID, TurnIndex: 1, RequestKind: "image_probe", StatusCode: 400, ErrorCode: "MODEL_REQUEST_REJECTED", ErrorMessage: "text-only model", IsStreaming: true, StartedAt: base.Add(time.Second), CompletedAt: base.Add(1200 * time.Millisecond)},
		{ID: "request-multimodal", RunID: run.ID, TurnIndex: 1, RequestKind: "multimodal_fallback", ModelProfileID: "vision-profile", ProfileName: "Vision Fallback", ModelID: "vision-model", APIProtocol: modelcap.ProtocolOpenAIChat, StatusCode: 200, IsStreaming: true, InputTokens: 5, FreshInputTokens: 5, OutputTokens: 2, StartedAt: base.Add(2 * time.Second), CompletedAt: base.Add(2500 * time.Millisecond)},
	}
	for _, fixture := range auxiliary {
		if _, inserted, err := repository.RecordModelUsage(ctx, fixture); err != nil || !inserted {
			t.Fatalf("record same-turn auxiliary request = inserted:%v err:%v", inserted, err)
		}
	}
	loaded, err = repository.Get(ctx, run.ID)
	if err != nil || loaded.FreshInputTokens != 135 || loaded.OutputTokens != 62 || loaded.ModelTurns != 4 {
		t.Fatalf("same-turn auxiliary aggregate = %#v, %v", loaded, err)
	}
	all, err = repository.UsageRequests(ctx, chat.UsageRequestQuery{Limit: 20})
	if err != nil || all.Total != 6 || len(all.Items) != 6 {
		t.Fatalf("same-turn request details = %#v, %v", all, err)
	}
	foundFallback := false
	for _, item := range all.Items {
		if item.ID == "request-multimodal" {
			foundFallback = item.ModelProfileID == "vision-profile" && item.ProfileName == "Vision Fallback" && item.ModelID == "vision-model" && item.APIProtocol == modelcap.ProtocolOpenAIChat
		}
	}
	if !foundFallback {
		t.Fatalf("fallback request model identity was not preserved: %#v", all.Items)
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

func TestRunRepositoryPersistsModelTurnsWithoutRunCap(t *testing.T) {
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
	if loaded, err := runs.IncrementModelTurns(ctx, run.ID, now.Add(time.Second)); err != nil || loaded.ModelTurns != 1 {
		t.Fatalf("first model turn = %#v, %v", loaded, err)
	}
	if loaded, err := runs.IncrementModelTurns(ctx, run.ID, now.Add(2*time.Second)); err != nil || loaded.ModelTurns != 2 {
		t.Fatalf("second model turn = %#v, %v", loaded, err)
	}
}

func TestRunRepositoryRecordsTerminalCompactionWithoutReopeningRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Compact", "")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "compact")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "compact-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "compact-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(time.Second)
	run := chat.Run{ID: "compact-run", ConversationID: createdConversation.ID, UserMessageID: "compact-user", AssistantMessageID: "compact-assistant", ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunCompleted, InputTokens: 10, OutputTokens: 5, ModelTurns: 1, FinishReason: "stop", CreatedAt: now, StartedAt: &now, CompletedAt: &completedAt, UpdatedAt: completedAt}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "compact-user-part", MessageID: run.UserMessageID, Type: "text", Text: "question", CreatedAt: now}}}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageComplete, CreatedAt: completedAt, UpdatedAt: completedAt, Parts: []conversation.MessagePart{{ID: "compact-assistant-part", MessageID: run.AssistantMessageID, Type: "text", Text: "answer", CreatedAt: completedAt}}}
	runs := NewRunRepository(store.DB())
	if err := runs.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	accountedAt := completedAt.Add(time.Second)
	run.ContextCompacted = true
	run.InputTokens = 30
	run.OutputTokens = 12
	run.ModelTurns = 2
	run.UpdatedAt = accountedAt
	if err := runs.RecordCompaction(ctx, run); err != nil {
		t.Fatal(err)
	}
	loaded, err := runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != chat.RunCompleted || loaded.CompletedAt == nil || !loaded.CompletedAt.Equal(completedAt) || loaded.FinishReason != "stop" {
		t.Fatalf("terminal run lifecycle changed: %#v", loaded)
	}
	if !loaded.ContextCompacted || loaded.InputTokens != 30 || loaded.OutputTokens != 12 || loaded.ModelTurns != 2 {
		t.Fatalf("compaction accounting not persisted: %#v", loaded)
	}
	loaded.ModelID = "must-not-change"
	if err := runs.Update(ctx, loaded); err == nil || !strings.Contains(err.Error(), "run is terminal") {
		t.Fatalf("generic terminal update error = %v", err)
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
	var terminationPayload json.RawMessage
	for _, message := range messages {
		if message.ID == assistant.ID {
			assistantStatus = message.Status
			for _, part := range message.Parts {
				if part.Type == "tool_result" {
					terminationPayload = part.Payload
				}
			}
		}
	}
	if loadedRun.Status != chat.RunCancelled || loadedCall.Status != tool.CallCancelled || loadedCall.Result == nil || loadedCall.Result.Status != tool.ResultCancelled || loadedApproval.Status != permission.ApprovalExpired || assistantStatus != conversation.MessageIncomplete {
		t.Fatalf("cancel state run=%s call=%s approval=%s message=%s", loadedRun.Status, loadedCall.Status, loadedApproval.Status, assistantStatus)
	}
	if !strings.Contains(string(terminationPayload), `"kind":"run_termination_context"`) || !strings.Contains(string(terminationPayload), `"name":"builtin.fixture"`) || !strings.Contains(string(terminationPayload), `"status":"cancelled"`) {
		t.Fatalf("termination context = %s", terminationPayload)
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
	run.ErrorDetails = "HTTP status: 400\nProvider payload: fixture"
	if err := runs.Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runs.FailRun(ctx, run.ID, "TOOL_CALL_REJECTED", "failed", failAt, failEvent); err != nil {
		t.Fatal(err)
	}
	loadedRun, _ := runs.Get(ctx, run.ID)
	loadedCall, _ := NewToolRepository(store.DB()).Get(ctx, call.ID)
	if loadedRun.Status != chat.RunFailed || loadedRun.ErrorDetails != run.ErrorDetails || loadedCall.Status != tool.CallInterrupted || loadedCall.Result == nil || loadedCall.Result.Status != tool.ResultError {
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

func TestReasoningObservationsPersistAndSuccessfulLevelClearsRejection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reasoning-observations.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{
		ID: "reasoning-profile", Name: "gateway", ProviderType: modelprofile.ProviderOpenAICompatible,
		APIProtocol: modelcap.ProtocolOpenAIChat, BaseURL: "https://example.test/v1", ModelID: "future-model",
		Models:    []modelprofile.ProfileModel{{ID: "future-model", Enabled: true, IsDefault: true, ReasoningLevels: modelcap.AllReasoningLevels(), ReasoningCapabilitySource: "inferred"}},
		SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	repository := NewModelProfileRepository(store.DB())
	if err := repository.Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordReasoningResult(ctx, profile.ID, profile.ModelID, modelcap.ReasoningResult{
		Requested: modelcap.ReasoningMax, Resolved: modelcap.ReasoningXHigh,
		Rejected: []modelcap.ReasoningLevel{modelcap.ReasoningMax}, WireMode: "openai_effort",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	repository = NewModelProfileRepository(store.DB())
	loaded, err := repository.Get(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	modelValue := loaded.Models[0]
	if len(modelValue.ReasoningVerifiedLevels) != 1 || modelValue.ReasoningVerifiedLevels[0] != modelcap.ReasoningXHigh || len(modelValue.ReasoningRejectedLevels) != 1 || modelValue.ReasoningRejectedLevels[0] != modelcap.ReasoningMax || modelValue.ReasoningWireMode != "openai_effort" {
		t.Fatalf("persisted observation = %#v", modelValue)
	}
	if err := repository.RecordReasoningResult(ctx, profile.ID, profile.ModelID, modelcap.ReasoningResult{Requested: modelcap.ReasoningMax, Resolved: modelcap.ReasoningMax, WireMode: "openai_effort"}); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.Get(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	modelValue = loaded.Models[0]
	if len(modelValue.ReasoningRejectedLevels) != 0 || len(modelValue.ReasoningVerifiedLevels) != 2 || modelValue.ReasoningLastResolvedLevel != modelcap.ReasoningMax {
		t.Fatalf("successful max did not clear rejection = %#v", modelValue)
	}
	if err := repository.RecordReasoningResult(ctx, profile.ID, profile.ModelID, modelcap.ReasoningResult{
		Requested: modelcap.ReasoningMax, Resolved: modelcap.ReasoningHigh,
		Rejected: []modelcap.ReasoningLevel{modelcap.ReasoningMax, modelcap.ReasoningXHigh}, WireMode: "openai_effort",
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.Get(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	modelValue = loaded.Models[0]
	if len(modelValue.ReasoningVerifiedLevels) != 1 || modelValue.ReasoningVerifiedLevels[0] != modelcap.ReasoningHigh || len(modelValue.ReasoningRejectedLevels) != 2 || modelValue.ReasoningRejectedLevels[0] != modelcap.ReasoningXHigh || modelValue.ReasoningRejectedLevels[1] != modelcap.ReasoningMax {
		t.Fatalf("newer rejection did not supersede success = %#v", modelValue)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
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

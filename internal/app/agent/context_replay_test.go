package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

// Exact stored prompt, usage fingerprints, completed tools and native journal;
// fixed host guidance is explicitly synthetic because HTTP bodies are not saved.
// No application startup, migrations, tools, provider or formal-store writes.
func TestReadOnlyStoredFrozenPromptFingerprintIncident(t *testing.T) {
	path, runID := os.Getenv("SCIAIDE_FROZEN_PROMPT_REPLAY_DB"), os.Getenv("SCIAIDE_FROZEN_PROMPT_REPLAY_RUN")
	if path == "" || runID == "" {
		t.Skip("set explicit read-only frozen-prompt incident database and run")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(abs), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repository := sqlite.NewRunRepository(db)
	run, err := repository.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ErrorCode != "CONTEXT_BUILD_FAILED" || run.ErrorDetails != "system, tool definitions and latest conversation message exceed context window" {
		t.Fatal("incident identity changed", run.ErrorCode, run.ErrorDetails)
	}
	load := func() ([]conversation.Message, []tool.Call, []model.ProviderTurn, []byte) {
		t.Helper()
		messages, e := sqlite.NewConversationRepository(db).ListMessages(ctx, run.ConversationID, -1)
		if e != nil {
			t.Fatal(e)
		}
		messages = workflowAIRunMessages(messages, runID)
		calls, e := sqlite.NewToolRepository(db).ListByRun(ctx, runID)
		if e != nil {
			t.Fatal(e)
		}
		turns, e := repository.ListProviderTurns(ctx, runID)
		if e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal([]any{messages, calls, turns})
		if e != nil {
			t.Fatal(e)
		}
		return messages, calls, turns, encoded
	}
	messages, calls, turns, before := load()
	observation, found, err := repository.LatestContextUsage(ctx, runID)
	if err != nil || !found || !observation.Matches(run) || observation.InputTokens != 61420 {
		t.Fatal("successful stored observation unavailable", err)
	}
	var current model.Message
	for _, message := range messages {
		if message.ID == run.UserMessageID && message.Role == conversation.RoleUser {
			current = model.Message{Role: model.RoleUser, Content: conversationText(message), HostToolReferences: true}
		}
	}
	if len([]rune(current.Content)) != 149694 || observation.Components[requestUsageComponents(model.ChatRequest{Messages: []model.Message{current}})[0].key] != 1 {
		t.Fatal("exact prompt did not match the original successful wire fingerprint")
	}
	wrong := current
	wrong.HostToolReferences = false
	if observation.Components[requestUsageComponents(model.ChatRequest{Messages: []model.Message{wrong}})[0].key] != 0 {
		t.Fatal("changed projection unexpectedly matched")
	}
	limits := ContextLimits{EffectiveTokens: run.ContextBudgetTokens, AutoCompactTokens: run.AutoCompactTokenLimit, UsageObservation: &observation}
	build := func(native []model.ProviderTurn, host bool) (model.ChatRequest, ContextBuildInfo, error) {
		limits.CurrentUserHostToolReferences = host
		return NewContextBuilder(0).buildWithResearchState(ctx, messages, run.AssistantMessageID, run.UserMessageID, nil, calls, skill.RunContext{}, "", strings.Repeat("s", 37000), "", limits, contextmemory.Checkpoint{}, native...)
	}
	if _, _, err := build(turns, false); err == nil || err.Error() != run.ErrorDetails {
		t.Fatal("old host projection did not reproduce the stored failure", err)
	}
	request, info, err := build(turns, true)
	if err != nil || info.CompactedThroughMessageID != "" || info.EstimatedTokens > run.AutoCompactTokenLimit || len(request.ProviderTurns) != len(turns) {
		t.Fatal("corrected stored projection still failed", info, err)
	}
	for i := range turns {
		if !reflect.DeepEqual(request.ProviderTurns[i].Items, turns[i].Items) {
			t.Fatal("native journal altered")
		}
	}
	contract, exists, err := repository.WorkflowAIContract(ctx, runID)
	if err != nil || !exists {
		t.Fatal("frozen submission contract missing", err)
	}
	submission, _, err := build(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	submission, err = fitFinalRequest(submission, calls, &observation, run.ContextBudgetTokens, func(r model.ChatRequest) model.ChatRequest {
		return stageSubmissionRequest(r, workflow.ReviewSubmissionSchema(contract.OutputSchema))
	})
	if err != nil {
		t.Fatal("stored schema submission still failed", err)
	}
	exact := false
	for _, m := range submission.Messages {
		exact = exact || reflect.DeepEqual(m, current)
	}
	_, _, _, after := load()
	if !exact || string(before) != string(after) {
		t.Fatal("frozen prompt or formal records changed")
	}
	t.Logf("actual input=%d promptChars=%d completedCalls=%d nativeTurns=%d correctedReserve=%d budget=%d; synthetic fixed guidance=37000; exact stored wire fingerprint matched and source audit unchanged", observation.InputTokens, len([]rune(current.Content)), len(calls), len(turns), info.EstimatedTokens, run.ContextBudgetTokens)
}

// Formal history/native state/usage are read-only and time-filtered. Fixed
// guidance is a stated synthetic footprint, NOT an exact recorded HTTP body.
func TestReadOnlyStoredRefittedResultBudgetIncident(t *testing.T) {
	path, runID := os.Getenv("SCIAIDE_REFIT_REPLAY_DB"), os.Getenv("SCIAIDE_REFIT_REPLAY_RUN")
	if path == "" || runID == "" {
		t.Skip("set explicit read-only result-refit incident database and run")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(abs), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	runs := sqlite.NewRunRepository(db)
	run, err := runs.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ErrorDetails != "provider tool protocol history exceeds context window: required=203315 available=192638" {
		t.Fatal("incident changed", run.ErrorDetails)
	}
	var input int
	var start string
	if err := db.QueryRow(`SELECT input_tokens,started_at FROM model_request_usage WHERE run_id=? AND turn_index=10 AND request_kind='conversation' AND status_code=200`, runID).Scan(&input, &start); err != nil {
		t.Fatal(err)
	}
	requestAt, err := time.Parse(time.RFC3339Nano, start)
	if err != nil || input != 36808 {
		t.Fatal("usage changed", err)
	}
	checkpoint, found, err := sqlite.NewContextCheckpointRepository(db).Latest(ctx, run.ConversationID)
	if err != nil || !found || checkpoint.CreatedAt.After(requestAt) {
		t.Fatal("checkpoint no longer historical", err)
	}
	history, err := sqlite.NewWorkflowRuntimeRepository(db).ResearchTaskDiscussionMessages(ctx, run.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := runs.ListProviderTurns(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := sqlite.NewToolRepository(db).ListByRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal([]any{history, turns, calls, checkpoint})
	projected := []conversation.Message{}
	for _, m := range discussionConversationMessages(history) {
		if !m.CreatedAt.After(requestAt) && m.ID != run.AssistantMessageID {
			projected = append(projected, m)
		}
	}
	priorCalls := []tool.Call{}
	for _, call := range calls {
		if call.CompletedAt != nil && !call.CompletedAt.After(requestAt) {
			priorCalls = append(priorCalls, call)
		}
	}
	priorTurns := []model.ProviderTurn{}
	for _, turn := range turns {
		if turn.TurnIndex < 10 {
			priorTurns = append(priorTurns, turn)
		}
	}
	// No saved HTTP bodies: reconstruct only the exact observed fixed-size
	// reservation (52162), with synthetic host text rather than guessed tools.
	base := len([]rune(fixedSystemRules)) + 2 + estimateMessageTokens(checkpointContextMessage(checkpoint))
	current, _ := requiredConversationMessageTokens(projected, run.AssistantMessageID, run.UserMessageID)
	fixed := strings.Repeat("s", 52162-base-current)
	b := NewContextBuilder(0)
	limits := ContextLimits{EffectiveTokens: run.ContextBudgetTokens, AutoCompactTokens: run.AutoCompactTokenLimit}
	build := func(selected []tool.Call, native []model.ProviderTurn, l ContextLimits) (model.ChatRequest, ContextBuildInfo, error) {
		return b.buildWithResearchState(ctx, projected, run.AssistantMessageID, run.UserMessageID, nil, selected, skill.RunContext{}, "", fixed, "", l, checkpoint, native...)
	}
	anchor, _, err := build(priorCalls, priorTurns, limits)
	if err != nil {
		t.Fatal(err)
	}
	observation := observeContextUsage(run, anchor, model.Usage{InputTokens: input}, true)
	if !observation.Matches(run) {
		t.Fatal("invalid replay anchor")
	}
	_, _, oldErr := build(calls, turns, limits)
	if oldErr == nil || oldErr.Error() != run.ErrorDetails {
		t.Fatal("character fallback did not reproduce exact required/available", oldErr)
	}
	limits.UsageObservation = &observation
	request, info, err := build(calls, turns, limits)
	if err != nil || info.CompactedThroughMessageID != "" || len(request.ProviderTurns) != len(turns) || info.EstimatedTokens > run.AutoCompactTokenLimit {
		t.Fatal("refitted actual incident still fails", info, err)
	}
	for i := range turns {
		if !reflect.DeepEqual(request.ProviderTurns[i].Items, turns[i].Items) {
			t.Fatal("replay mutated native state")
		}
	}
	history, _ = sqlite.NewWorkflowRuntimeRepository(db).ResearchTaskDiscussionMessages(ctx, run.ConversationID)
	turns, _ = runs.ListProviderTurns(ctx, runID)
	calls, _ = sqlite.NewToolRepository(db).ListByRun(ctx, runID)
	checkpoint, _, _ = sqlite.NewContextCheckpointRepository(db).Latest(ctx, run.ConversationID)
	after, _ := json.Marshal([]any{history, turns, calls, checkpoint})
	if string(before) != string(after) {
		t.Fatal("formal records changed")
	}
	t.Logf("exact fallback reproduced: required=203315 available=192638; synthetic fixed reservation=52162; priorActualInput=%d calls=%d nativeTurns=%d newBudget=%d source=%s; official records unchanged", input, len(calls), len(turns), info.EstimatedTokens, info.BudgetSource)
}

func TestReadOnlyStoredContextUsageBudgetIncident(t *testing.T) {
	path, runID := os.Getenv("SCIAIDE_USAGE_REPLAY_DB"), os.Getenv("SCIAIDE_USAGE_REPLAY_RUN")
	if path == "" || runID == "" {
		t.Skip("set explicit read-only usage incident database and run")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(abs), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	runs := sqlite.NewRunRepository(db)
	run, err := runs.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if internal, err := runs.IsWorkflowAIRun(ctx, runID); err != nil || internal || run.ErrorCode != "CONTEXT_COMPACTION_FAILED" {
		t.Fatal("replay requires the failed ordinary discussion", err)
	}
	var start, summaryStart string
	var input int
	if err := db.QueryRow(`SELECT input_tokens,started_at FROM model_request_usage WHERE run_id=? AND turn_index=12 AND request_kind='conversation' AND status_code=200`, runID).Scan(&input, &start); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT started_at FROM model_request_usage WHERE run_id=? AND turn_index=13 AND request_kind='compaction'`, runID).Scan(&summaryStart); err != nil {
		t.Fatal(err)
	}
	requestAt, err := time.Parse(time.RFC3339Nano, start)
	if err != nil {
		t.Fatal(err)
	}
	nextAt, err := time.Parse(time.RFC3339Nano, summaryStart)
	if err != nil || input != 55824 || run.ContextBudgetTokens != 258400 || run.AutoCompactTokenLimit != 244800 {
		t.Fatal("incident usage/window changed", err)
	}
	checkpoint, found, err := sqlite.NewContextCheckpointRepository(db).Latest(ctx, run.ConversationID)
	if err != nil || !found || checkpoint.CreatedAt.After(requestAt) {
		t.Fatal("historical checkpoint unavailable", err)
	}
	history, err := sqlite.NewWorkflowRuntimeRepository(db).ResearchTaskDiscussionMessages(ctx, run.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := runs.ListProviderTurns(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := sqlite.NewToolRepository(db).ListByRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal([]any{history, turns, calls, checkpoint})
	projected := []conversation.Message{}
	for _, m := range discussionConversationMessages(history) {
		if !m.CreatedAt.After(requestAt) && m.ID != run.AssistantMessageID {
			projected = append(projected, m)
		}
	}
	filterCalls := func(at time.Time) []tool.Call {
		selected := []tool.Call{}
		for _, call := range calls {
			if call.CompletedAt != nil && !call.CompletedAt.After(at) {
				selected = append(selected, call)
			}
		}
		return selected
	}
	oldTurns, nextTurns := []model.ProviderTurn{}, []model.ProviderTurn{}
	for _, turn := range turns {
		if turn.TurnIndex < 12 {
			oldTurns = append(oldTurns, turn)
		}
		if turn.TurnIndex <= 12 {
			nextTurns = append(nextTurns, turn)
		}
	}
	// The DB does not store tool-definition/request-body snapshots. Fix one
	// 55K host-guidance footprint to compare only accounting algorithms.
	fixed := strings.Repeat("s", 55000)
	builder := NewContextBuilder(0)
	build := func(at time.Time, native []model.ProviderTurn, limits ContextLimits) (model.ChatRequest, ContextBuildInfo, error) {
		return builder.buildWithResearchState(ctx, projected, run.AssistantMessageID, run.UserMessageID, nil, filterCalls(at), skill.RunContext{}, "", fixed, "", limits, checkpoint, native...)
	}
	anchorRequest, _, err := build(requestAt, oldTurns, ContextLimits{EffectiveTokens: 1000000, AutoCompactTokens: 1000000})
	if err != nil {
		t.Fatal(err)
	}
	run.ModelTurns = 12
	observation := observeContextUsage(run, anchorRequest, model.Usage{InputTokens: input}, true)
	if !observation.Matches(run) {
		t.Fatal("incident did not create a same-run usage anchor")
	}
	limits := ContextLimits{EffectiveTokens: run.ContextBudgetTokens, AutoCompactTokens: run.AutoCompactTokenLimit}
	_, oldInfo, oldErr := build(nextAt, nextTurns, limits)
	if oldErr == nil && oldInfo.CompactedThroughMessageID == "" {
		t.Fatal("old accounting did not reproduce early compaction")
	}
	limits.UsageObservation = &observation
	request, info, err := build(nextAt, nextTurns, limits)
	if err != nil || info.BudgetSource != "observed_input_with_growth_reserve" || info.CompactedThroughMessageID != "" || len(request.ProviderTurns) != len(nextTurns) || info.EstimatedTokens >= limits.AutoCompactTokens {
		t.Fatal("corrected incident accounting still compacts/loses native turns", info, err)
	}
	history, err = sqlite.NewWorkflowRuntimeRepository(db).ResearchTaskDiscussionMessages(ctx, run.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	turns, _ = runs.ListProviderTurns(ctx, runID)
	calls, _ = sqlite.NewToolRepository(db).ListByRun(ctx, runID)
	checkpoint, _, _ = sqlite.NewContextCheckpointRepository(db).Latest(ctx, run.ConversationID)
	after, _ := json.Marshal([]any{history, turns, calls, checkpoint})
	if string(before) != string(after) {
		t.Fatal("read-only replay altered official records")
	}
	t.Logf("read-only incident + synthetic fixed footprint=55000: nativeTurns=%d callsBefore12=%d callsBeforeSummary=%d characterEstimate=%d actualPriorInput=%d calibratedNextReserve=%d autoLimit=%d; official records unchanged", len(nextTurns), len(filterCalls(requestAt)), len(filterCalls(nextAt)), info.CharacterEstimate, input, info.EstimatedTokens, limits.AutoCompactTokens)
}

// Replay the exact persisted incident without application startup, migrations,
// real model requests or writes to the user's database. The synthetic response
// only reproduces the observed returned length, not its semantic quality.
func TestReadOnlyStoredIncrementalCheckpointIncident(t *testing.T) {
	path, runID := os.Getenv("SCIAIDE_INCREMENTAL_REPLAY_DB"), os.Getenv("SCIAIDE_INCREMENTAL_REPLAY_RUN")
	if path == "" || runID == "" {
		t.Skip("set explicit read-only incremental checkpoint database and run")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(abs), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	runs := sqlite.NewRunRepository(db)
	run, err := runs.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if internal, err := runs.IsWorkflowAIRun(ctx, runID); err != nil || internal || run.ErrorCode != "CONTEXT_COMPACTION_FAILED" {
		t.Fatal("replay must target the failed ordinary discussion", err)
	}
	repo := sqlite.NewContextCheckpointRepository(db)
	prior, exists, err := repo.Latest(ctx, run.ConversationID)
	if err != nil || !exists || prior.CreatedAt.After(run.CreatedAt) || len([]rune(prior.Summary)) != 4122 {
		t.Fatal("stored incident checkpoint has changed", err)
	}
	messages, err := sqlite.NewWorkflowRuntimeRepository(db).ResearchTaskDiscussionMessages(ctx, run.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(messages)
	projected := []conversation.Message{}
	target := ""
	for _, message := range discussionConversationMessages(messages) {
		// The loop always retains the current human request and excludes its
		// streaming assistant. Failed draft text is not old checkpoint source.
		if message.RunID == run.ID || message.CreatedAt.After(run.CreatedAt) {
			continue
		}
		projected = append(projected, message)
		if conversationText(message) != "" {
			target = message.ID
		}
	}
	batch, err := buildCheckpointBatch(prior, projected, target, run.AutoCompactTokenLimit, nil)
	if err != nil || batch.estimatedTokens != 1969 || batch.replacedCharacters != 6091 {
		t.Fatalf("incident length mismatch: source=%d replaced=%d error=%v", batch.estimatedTokens, batch.replacedCharacters, err)
	}
	loop, state, provider := newLoopFixture(t, nil,
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("摘", 3305)}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	state.run = run
	memory := &memoryCheckpointRepository{latest: prior}
	loop.checkpoints = contextmemory.NewService(memory)
	saved, err := loop.compactConversation(ctx, &state.run, provider, prior, projected, target, nil, false)
	if err != nil || saved.ThroughMessageID != target || saved.SourceEstimatedTokens != prior.SourceEstimatedTokens+1969 {
		t.Fatal("persisted incident still rejects net smaller summary", err)
	}
	if err := contextmemory.Verify(saved); err != nil {
		t.Fatal(err)
	}
	after, err := sqlite.NewWorkflowRuntimeRepository(db).ResearchTaskDiscussionMessages(ctx, run.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(after)
	latest, _, err := repo.Latest(ctx, run.ConversationID)
	if err != nil || string(before) != string(encoded) || latest.ID != prior.ID || latest.CheckpointSHA256 != prior.CheckpointSHA256 {
		t.Fatal("read-only replay changed official history/checkpoint", err)
	}
	t.Logf("read-only incident replay: previousSummary=4122 newSource=1969 replaced=6091 syntheticSummary=3305 accepted; official data unchanged")
}

// Explicit task/run-scoped read-only replay; never starts the app or a model.
func TestReadOnlyStoredResearchDiscussionSeparatesInternalStages(t *testing.T) {
	path, runID := os.Getenv("SCIAIDE_DISCUSSION_REPLAY_DB"), os.Getenv("SCIAIDE_DISCUSSION_REPLAY_RUN")
	if path == "" || runID == "" {
		t.Skip("set explicit read-only discussion database and run")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(abs), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	runs := sqlite.NewRunRepository(db)
	run, err := runs.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if bound, err := runs.IsWorkflowAIRun(ctx, runID); err != nil || bound {
		t.Fatal("replay must target an ordinary human discussion", err)
	}
	messages, err := sqlite.NewConversationRepository(db).ListMessages(ctx, run.ConversationID, -1)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(messages)
	projected := discussionConversationMessages(messages)
	internalBytes, count := 0, 0
	latest := ""
	for _, m := range messages {
		if m.Internal {
			internalBytes += len(conversationText(m))
			count++
		}
		if m.ID == run.UserMessageID {
			latest = conversationText(m)
		}
	}
	request, _, err := NewContextBuilder(200_000).BuildWithResearchGuidance(ctx, projected, run.AssistantMessageID, run.UserMessageID, nil, nil, skill.RunContext{}, "", workflow.ResearchDiscussionSystemRules, ContextLimits{EffectiveTokens: 200_000, AutoCompactTokens: 180_000}, contextmemory.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range request.Messages {
		if strings.Contains(m.Content, "Research Mode rules:") || strings.Contains(m.Content, "<output_schema>") {
			t.Fatal("internal stage instructions leaked into human dialogue")
		}
		found = found || m.Role == model.RoleUser && m.Content == latest
	}
	after, _ := json.Marshal(messages)
	if !found || count == 0 || internalBytes == 0 || string(before) != string(after) {
		t.Fatal("missing current human request, audit or reproducible internal history")
	}
	for i, m := range projected {
		if m.Internal && conversationText(m) != "" || !m.Internal && m.Role == conversation.RoleUser && conversationText(m) != conversationText(messages[i]) {
			t.Fatal("projection lost human text or retained internal prompt")
		}
	}
	t.Logf("read-only discussion replay: internalMessages=%d internalBytes=%d; latest human retained; audit unchanged", count, internalBytes)
}

// Optional local replay. SQLite is read-only and only the context builder is
// invoked: no application startup, model request, tool execution or migration.
func TestReadOnlyStoredResearchContextBudget(t *testing.T) {
	path, runID := os.Getenv("SCIAIDE_CONTEXT_REPLAY_DB"), os.Getenv("SCIAIDE_CONTEXT_REPLAY_RUN")
	if path == "" || runID == "" {
		t.Skip("set explicit read-only context replay database and run")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(abs), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repository := sqlite.NewRunRepository(db)
	run, err := repository.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := sqlite.NewConversationRepository(db).ListMessages(ctx, run.ConversationID, -1)
	if err != nil {
		t.Fatal(err)
	}
	messages = workflowAIRunMessages(messages, runID)
	calls, err := sqlite.NewToolRepository(db).ListByRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := repository.ListProviderTurns(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(turns)
	inputBefore, _ := json.Marshal(messages)
	// Preserve the observed fixed-state footprint. Its text is irrelevant to
	// this budget regression; exact frozen input and protocol come from storage.
	fixed := strings.Repeat("s", 54780-len([]rune(fixedSystemRules))-2)
	build := func(allow bool) (ContextBuildInfo, error) {
		request, info, err := NewContextBuilder(run.ContextBudgetTokens).buildWithResearchState(ctx, messages, run.AssistantMessageID, run.UserMessageID, nil, calls, skill.RunContext{}, "", fixed, "", ContextLimits{EffectiveTokens: run.ContextBudgetTokens, AutoCompactTokens: run.AutoCompactTokenLimit, AllowProtocolRollover: allow}, contextmemory.Checkpoint{}, turns...)
		if err == nil {
			found := false
			for _, m := range request.Messages {
				for _, original := range messages {
					found = found || original.ID == run.UserMessageID && m.Content == conversationText(original)
				}
			}
			if !found || len(request.ProviderTurns) != 0 || info.EstimatedTokens > run.ContextBudgetTokens {
				t.Fatal("replay lost exact stage input or exceeded budget")
			}
		}
		return info, err
	}
	if _, err := build(false); err == nil || !strings.Contains(err.Error(), "required=30447 available=19150") {
		t.Fatal("stored input no longer matches the reported failure", err)
	}
	info, err := build(true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := repository.ListProviderTurns(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(after)
	inputAfter, _ := json.Marshal(messages)
	if string(before) != string(encoded) || string(inputBefore) != string(inputAfter) {
		t.Fatal("read-only context replay changed audit or frozen input")
	}
	t.Logf("stored failure reproduced and recovered: calls=%d nativeTurns=%d estimated=%d limit=%d", len(calls), len(turns), info.EstimatedTokens, run.ContextBudgetTokens)
}

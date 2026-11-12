package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func usageTestRun() chat.Run {
	return chat.Run{ID: "run", ModelProfileID: "profile", ModelID: "model", APIProtocol: modelcap.ProtocolOpenAIResponses, ModelTurns: 2}
}

func TestContextUsageAnchorsReportedInputAndChargesNewContent(t *testing.T) {
	run := usageTestRun()
	request := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: strings.Repeat("English evidence ", 2000)}}}
	observation := observeContextUsage(run, request, model.Usage{InputTokens: 10000, FreshInputTokens: 100, CachedInputTokens: 9900}, true)
	if !observation.Matches(run) || observedContextReserve(observation) != 12000 || observedRequestTokens(request, observation) != 12000 {
		t.Fatal("cached input was subtracted or cumulative usage used")
	}
	withNew := request
	withNew.Messages = append(append([]model.Message(nil), request.Messages...), model.Message{Role: model.RoleUser, Content: "新增证据😀"})
	if got := observedRequestTokens(withNew, observation); got != 12000+requestUsageComponents(withNew)[1].bytes+32 {
		t.Fatal("new content received blanket historical discount", got)
	}
	for _, usage := range []model.Usage{{}, {InputTokens: -1}, {InputTokens: 10000001}} {
		if got := observeContextUsage(run, request, usage, true); got.Matches(run) {
			t.Fatal("invalid provider usage accepted")
		}
	}
	if got := observeContextUsage(run, request, model.Usage{InputTokens: 10000}, false); got.Matches(run) {
		t.Fatal("unreported usage accepted")
	}
	image := request
	image.Messages = []model.Message{{Role: model.RoleUser, Parts: []model.ContentPart{{Type: "input_image", Data: "fixture"}}}}
	if got := observeContextUsage(run, image, model.Usage{InputTokens: 10000}, true); got.Matches(run) {
		t.Fatal("image token usage transferred to text budget")
	}
	changed := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: strings.Repeat("中文", 2000)}}}
	if got := observedRequestTokens(changed, observation); got != estimateRequestTokens(changed) {
		t.Fatal("unchanged envelope incorrectly counted as content match")
	}
	partial := request
	partial.Messages = append(append([]model.Message(nil), request.Messages...), model.Message{Role: model.RoleUser, Content: "duplicate"})
	partialObservation := observeContextUsage(run, partial, model.Usage{InputTokens: 10000}, true)
	partial.Messages = append(partial.Messages, partial.Messages[1])
	if got := observedRequestTokens(partial, partialObservation); got != 12000+requestUsageComponents(partial)[2].bytes+32 {
		t.Fatal("duplicate component was charged only once", got)
	}
	partial.Messages = partial.Messages[:1]
	if got := observedRequestTokens(partial, partialObservation); got != 12000 {
		t.Fatal("partial overlap discounted measured pool", got)
	}
	low := observeContextUsage(run, request, model.Usage{InputTokens: 1}, true)
	if got := observedRequestTokens(request, low); got != (estimateRequestTokens(request)+3)/4 {
		t.Fatal("unusually low usage bypassed fourfold credit cap", got)
	}
}

func TestContextUsageChargesPartsAndEnvelopeAndRejectsInvalidUTF8(t *testing.T) {
	run := usageTestRun()
	request := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "stable evidence"}}}
	value := observeContextUsage(run, request, model.Usage{InputTokens: 2000}, true)
	newRequest := request
	newRequest.Messages = append(append([]model.Message(nil), request.Messages...), model.Message{Role: model.RoleUser, Parts: []model.ContentPart{{Type: "text", Text: strings.Repeat("新增量表条目😀", 4000), Payload: json.RawMessage(`{"note":"` + strings.Repeat("科研", 1000) + `"}`)}}})
	newRequest.ToolReferenceNames = []string{strings.Repeat("registered_tool_", 3000)}
	got := observedRequestTokens(newRequest, value)
	if got < observedContextReserve(value)+len(newRequest.Messages[1].Parts[0].Text)+len(newRequest.Messages[1].Parts[0].Payload)+len(newRequest.ToolReferenceNames[0]) {
		t.Fatal("parts/payload/envelope bypassed growth accounting", got)
	}
	partsOnly := model.ChatRequest{Messages: []model.Message{newRequest.Messages[1]}}
	partsValue := observeContextUsage(run, partsOnly, model.Usage{InputTokens: 20000}, true)
	if observedRequestTokens(partsOnly, partsValue) != observedContextReserve(partsValue) {
		t.Fatal("unchanged text parts were not matched")
	}
	for _, change := range []func(*model.ChatRequest){
		func(r *model.ChatRequest) {
			r.Messages[0].Parts = []model.ContentPart{{Type: "text", Text: string([]byte{0xff})}}
		},
		func(r *model.ChatRequest) { r.ToolReferenceNames = []string{string([]byte{0xff})} },
		func(r *model.ChatRequest) {
			r.Tools = []model.ToolDefinition{{Name: "tool", Description: string([]byte{0xff}), InputSchema: json.RawMessage(`{}`)}}
		},
		func(r *model.ChatRequest) {
			r.ProviderTurns = []model.ProviderTurn{{Items: []model.ProviderItem{{Payload: json.RawMessage([]byte{'"', 0xff, '"'})}}}}
		},
	} {
		invalid := model.ChatRequest{Messages: append([]model.Message(nil), request.Messages...)}
		change(&invalid)
		if observeContextUsage(run, invalid, model.Usage{InputTokens: 2000}, true).Matches(run) || observedRequestTokens(invalid, value) != estimateRequestTokens(invalid) {
			t.Fatal("invalid UTF8 was normalized into a trusted anchor")
		}
	}
}

func TestContextUsageBuilderPreservesFullDialogueAndNativeProtocol(t *testing.T) {
	ctx := context.Background()
	run := usageTestRun()
	messages := []conversation.Message{checkpointText("old", "old-run", strings.Repeat("旧", 1969)), checkpointText("current", "run", "继续")}
	call := tool.Call{ProviderCallID: "call", ToolName: "read", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: "retrieved evidence"}}
	turn := model.ProviderTurn{TurnIndex: 1, Protocol: run.APIProtocol, Items: []model.ProviderItem{
		{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","encrypted_content":"` + strings.Repeat("x", 242000) + `"}`)},
		{Ordinal: 1, Type: "function_call", CallID: "call", Payload: json.RawMessage(`{"type":"function_call","call_id":"call","arguments":"{}"}`)},
	}}
	builder := NewContextBuilder(0)
	full, _, err := builder.BuildWithRuntimeContext(ctx, messages, "", "current", nil, []tool.Call{call}, skill.RunContext{}, ContextLimits{EffectiveTokens: 300000, AutoCompactTokens: 300000}, contextmemory.Checkpoint{}, turn)
	if err != nil {
		t.Fatal(err)
	}
	observation := observeContextUsage(run, full, model.Usage{InputTokens: 55824}, true)
	limits := ContextLimits{EffectiveTokens: 258400, AutoCompactTokens: 244800}
	newTurn := model.ProviderTurn{TurnIndex: 2, Protocol: run.APIProtocol, Items: []model.ProviderItem{{Ordinal: 0, Type: "message", Payload: json.RawMessage(`{"type":"message","content":"next"}`)}}}
	before, _ := json.Marshal([]model.ProviderTurn{turn, newTurn})
	_, oldInfo, oldErr := builder.BuildWithRuntimeContext(ctx, messages, "", "current", nil, []tool.Call{call}, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turn, newTurn)
	if oldErr == nil && oldInfo.CompactedThroughMessageID == "" {
		t.Fatal("character fixture did not trigger old compaction")
	}
	limits.UsageObservation = &observation
	request, info, err := builder.BuildWithRuntimeContext(ctx, messages, "", "current", nil, []tool.Call{call}, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turn, newTurn)
	if err != nil || info.CompactedThroughMessageID != "" || info.BudgetSource != "observed_input_with_growth_reserve" || info.EstimatedTokens > limits.AutoCompactTokens || len(request.ProviderTurns) != 2 {
		t.Fatal("measured request still prematurely compacted", info, err)
	}
	after, _ := json.Marshal([]model.ProviderTurn{turn, newTurn})
	if string(before) != string(after) || !reflect.DeepEqual(request.ProviderTurns[0].Items, turn.Items) || request.ProviderTurns[0].ToolResults[0].ToolCallID != "call" || len(request.Messages) != 3 || request.Messages[1].Content != strings.Repeat("旧", 1969) {
		t.Fatal("budget correction changed native state or omitted dialogue")
	}
	if info.ContextBudgetTokens != 258400 || info.AutoCompactTokenLimit != 244800 || info.CharacterEstimate <= info.EstimatedTokens {
		t.Fatal("window changed or accounting source was not distinguished")
	}
	// Genuine measured growth must NOT receive this accounting credit.
	observation.InputTokens = 243000
	_, info, err = builder.BuildWithRuntimeContext(ctx, messages, "", "current", nil, []tool.Call{call}, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turn, newTurn)
	if err == nil && (info.BudgetSource != "character_fallback" || info.CompactedThroughMessageID == "") {
		t.Fatal("near-full measured input bypassed original compaction", info)
	}
}

func TestContextUsageLoopAvoidsCheckpointButMissingUsageKeepsProtection(t *testing.T) {
	for _, reported := range []bool{true, false} {
		loop, state, _ := newLoopFixture(t, nil)
		state.messages = append([]conversation.Message{checkpointText("boundary", "earlier", "summarized"), checkpointText("old", "old-run", strings.Repeat("旧", 1969))}, state.messages...)
		repo := &memoryCheckpointRepository{}
		loop.checkpoints = contextmemory.NewService(repo)
		_, err := loop.checkpoints.Save(context.Background(), contextmemory.Checkpoint{ConversationID: "conversation", ThroughMessageID: "boundary", Summary: strings.Repeat("旧", 4122), SourceMessageCount: 5, SourceEstimatedTokens: 25746, ModelProfileID: "profile", ModelID: "model", APIProtocol: modelcap.ProtocolOpenAIResponses})
		if err != nil {
			t.Fatal(err)
		}
		reasoning := model.ProviderItem{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","encrypted_content":"` + strings.Repeat("x", 238000) + `"}`)}
		function := model.ProviderItem{Ordinal: 1, Type: "function_call", CallID: "read", Payload: json.RawMessage(`{"type":"function_call","call_id":"read","arguments":"{\"query\":\"paper\"}"}`)}
		first := []fake.Step{}
		if reported {
			usage := model.Usage{InputTokens: 1000}
			first = append(first, fake.Step{Event: model.Event{Type: model.EventUsage, Usage: &usage}})
		}
		first = append(first, fake.Step{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &reasoning}}, fake.Step{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &function}},
			fake.Step{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "read", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}}, fake.Step{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}})
		usage := model.Usage{InputTokens: 55824}
		second := []fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &usage}}, {Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "another", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"another"}`)}}}, {Event: model.Event{Type: model.EventProviderItem, ProviderItem: &model.ProviderItem{Ordinal: 0, Type: "function_call", CallID: "another", Payload: json.RawMessage(`{"type":"function_call","call_id":"another","arguments":"{}"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
		final := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "continued"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
		provider := fake.New(first, second, final)
		loop.models = incrementalCheckpointResolver{model: provider}
		outcome := loop.Run(context.Background(), state.run.ID)
		if reported {
			if outcome != OutcomeCompleted || len(provider.Requests()) != 3 || len(state.calls) != 2 || repo.latest.Revision != 1 || provider.Requests()[1].Messages[2].Content != strings.Repeat("旧", 1969) {
				t.Fatalf("outcome=%s details=%s requests=%d", outcome, state.run.ErrorDetails, len(provider.Requests()))
			}
		} else if outcome == OutcomeCompleted {
			t.Fatal("missing usage unexpectedly bypassed original compaction")
		}
	}
}

func TestContextUsageHigherThanCharacterEstimateStillCompacts(t *testing.T) {
	messages := []conversation.Message{checkpointText("old", "old-run", strings.Repeat("旧", 20000)), checkpointText("current", "run", "继续")}
	b := NewContextBuilder(0)
	limits := ContextLimits{EffectiveTokens: 60000, AutoCompactTokens: 54000}
	full, _, err := b.BuildWithRuntimeContext(context.Background(), messages, "", "current", nil, nil, skill.RunContext{}, limits, contextmemory.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	usage := observeContextUsage(usageTestRun(), full, model.Usage{InputTokens: 53000}, true)
	limits.UsageObservation = &usage
	request, info, err := b.BuildWithRuntimeContext(context.Background(), messages, "", "current", nil, nil, skill.RunContext{}, limits, contextmemory.Checkpoint{})
	if err != nil || info.CompactedThroughMessageID != "old" || len(request.Messages) != 2 || info.BudgetSource != "observed_input_overflow_reserve" || info.EstimatedTokens > limits.AutoCompactTokens || info.AutoCompactTokenLimit != 54000 {
		t.Fatal("actual high token usage was ignored", info, err)
	}
}

func TestContextUsageActualNearWindowCompactsThenContinues(t *testing.T) {
	usage := model.Usage{InputTokens: 53000}
	loop, state, provider := newLoopFixture(t, nil,
		[]fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &usage}}, {Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "lookup", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}},
		[]fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 22000}}}, {Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("摘", 1000)}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}},
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "正常继续"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	state.messages = append([]conversation.Message{checkpointText("old", "old-run", strings.Repeat("旧", 20000))}, state.messages...)
	repo := &memoryCheckpointRepository{}
	loop.checkpoints = contextmemory.NewService(repo)
	loop.models = budgetResolver{model: provider, budget: modelcap.ResolveContextBudget(60000, 54000, modelcap.ContextWindowSourceManual)}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted {
		t.Fatal("real near-window compaction did not continue", got, state.run.ErrorDetails)
	}
	if len(provider.Requests()) != 3 || repo.latest.ThroughMessageID != "old" || len(state.calls) != 1 || len(provider.Requests()[1].Tools) != 0 || state.messages[0].Parts[0].Text != strings.Repeat("旧", 20000) {
		t.Fatal("compaction rewrote source, re-executed tools or used summary anchor")
	}
}

type contextUsageFailureRuns struct {
	*loopState
	loadCalls, saveCalls int
}

func (r *contextUsageFailureRuns) LatestContextUsage(context.Context, string) (chat.ContextUsageObservation, bool, error) {
	r.loadCalls++
	return chat.ContextUsageObservation{}, false, fmt.Errorf("injected observation read failure")
}
func (r *contextUsageFailureRuns) SaveContextUsage(context.Context, chat.ContextUsageObservation) error {
	r.saveCalls++
	return fmt.Errorf("injected observation save failure")
}

func TestContextUsageOptionalStorageFailureDoesNotBlockConversation(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil,
		[]fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 2000}}}, {Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "read", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}},
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "可以继续"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	runs := &contextUsageFailureRuns{loopState: state}
	loop.runs = runs
	loop.models = protocolResolver{model: provider, protocol: modelcap.ProtocolOpenAIChat}
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeCompleted || runs.loadCalls != 1 || runs.saveCalls != 1 || len(provider.Requests()) != 2 {
		t.Fatal("optional accounting metadata blocked normal dialogue", got, state.run.ErrorDetails)
	}
}

type contextUsageCaptureRuns struct {
	*loopState
	observations []chat.ContextUsageObservation
}

func (r *contextUsageCaptureRuns) LatestContextUsage(context.Context, string) (chat.ContextUsageObservation, bool, error) {
	return chat.ContextUsageObservation{}, false, nil
}
func (r *contextUsageCaptureRuns) SaveContextUsage(_ context.Context, value chat.ContextUsageObservation) error {
	r.observations = append(r.observations, value)
	return nil
}

func TestContextUsageFailedRetryAndCancelNeverCreateAnchor(t *testing.T) {
	for _, failed := range []error{&apperr.Error{Code: "MODEL_STREAM_INTERRUPTED", UserMessage: "fixture stream interrupted", Retryable: true}, context.Canceled} {
		loop, state, provider := newLoopFixture(t, nil,
			[]fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 999999}}}, {Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "discarded", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"discarded"}`)}}}, {Err: failed}},
			[]fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 2500}}}, {Event: model.Event{Type: model.EventTextDelta, Text: "正常答复"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
		runs := &contextUsageCaptureRuns{loopState: state}
		loop.runs = runs
		loop.models = protocolResolver{model: provider, protocol: modelcap.ProtocolOpenAIChat}
		loop.retryDelay = func(int) time.Duration { return 0 }
		got := loop.Run(context.Background(), state.run.ID)
		if failed == context.Canceled {
			if got != OutcomeCancelled || len(runs.observations) != 0 || len(state.calls) != 0 {
				t.Fatal("cancelled request calibrated input or executed tool", got)
			}
		} else if got != OutcomeCompleted || len(runs.observations) != 1 || runs.observations[0].InputTokens != 2500 || state.run.InputTokens != 2500 || len(state.calls) != 0 || len(provider.Requests()) != 2 {
			t.Fatal("failed stream usage or tool entered successful accounting", got, state.run.ErrorDetails)
		}
	}
}

func TestContextUsageRefittedPreviewsReuseOnlyExactSameCallChunks(t *testing.T) {
	content := strings.Repeat("原始证据😀", 1500) + "original middle" + strings.Repeat("科学数据", 1500)
	request := model.ChatRequest{Messages: []model.Message{{Role: model.RoleSystem, Content: "stable"}, {Role: model.RoleTool, ToolCallID: "read-A", Content: content}}}
	value := observeContextUsage(usageTestRun(), request, model.Usage{InputTokens: 15000}, true)
	smaller := request
	smaller.Messages = append([]model.Message(nil), request.Messages...)
	smaller.Messages[1].Content = truncateToolContext(content, 3000)
	shrunk := observedRequestTokens(smaller, value)
	if shrunk >= observedContextReserve(value)+len(smaller.Messages[1].Content)/2 || shrunk < observedContextReserve(value) {
		t.Fatal("refitting a smaller same-call preview charged the entire old text", shrunk)
	}
	other := smaller
	other.Messages = append([]model.Message(nil), smaller.Messages...)
	other.Messages[1].ToolCallID = "different-call"
	if got := observedRequestTokens(other, value); got <= shrunk+5000 {
		t.Fatal("chunk credit crossed provider call identity", got, shrunk)
	}
	changed := smaller
	changed.Messages = append([]model.Message(nil), smaller.Messages...)
	changed.Messages[1].Content = strings.Repeat("新科学结论😀", 3000)
	if got := observedRequestTokens(changed, value); got < observedContextReserve(value)+len(changed.Messages[1].Content) {
		t.Fatal("changed scientific text received old chunk discount", got)
	}
	expanded := request
	expanded.Messages = append([]model.Message(nil), request.Messages...)
	expanded.Messages[1].Content += strings.Repeat("全新结果", 3000)
	if got := observedRequestTokens(expanded, value); got < observedContextReserve(value)+len(strings.Repeat("全新结果", 3000)) {
		t.Fatal("expanded result lost new content charge", got)
	}
}

func TestContextUsageFittingKeepsAllNativeGroupsAndHumanAnswers(t *testing.T) {
	ctx := context.Background()
	msgs := []conversation.Message{checkpointText("u", "run", "补查而不修改路线")}
	calls := []tool.Call{}
	turns := []model.ProviderTurn{}
	for n := 1; n <= 30; n++ {
		id := fmt.Sprintf("call-%d", n)
		call := tool.Call{ID: id, ProviderCallID: id, ToolName: "read", Arguments: json.RawMessage(`{}`), Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("证据描述", 2500)}}
		if n == 1 {
			call.ToolName = "builtin.user_input.request"
			call.Result.Text = strings.Repeat("重要的人类决定", 1000)
		}
		calls = append(calls, call)
		turns = append(turns, model.ProviderTurn{TurnIndex: n, Protocol: modelcap.ProtocolOpenAIResponses, Items: []model.ProviderItem{{Ordinal: 0, CallID: id, Payload: json.RawMessage(fmt.Sprintf(`{"type":"function_call","call_id":%q,"arguments":"{}"}`, id))}}})
	}
	b := NewContextBuilder(0)
	limits := ContextLimits{EffectiveTokens: 150000, AutoCompactTokens: 135000}
	old, _, err := b.BuildWithRuntimeContext(ctx, msgs, "", "u", nil, calls[:20], skill.RunContext{}, limits, contextmemory.Checkpoint{}, turns[:20]...)
	if err != nil {
		t.Fatal(err)
	}
	obs := observeContextUsage(usageTestRun(), old, model.Usage{InputTokens: 12000}, true)
	limits.UsageObservation = &obs
	request, info, err := b.BuildWithRuntimeContext(ctx, msgs, "", "u", nil, calls, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turns...)
	if err != nil || info.CompactedThroughMessageID != "" || info.EstimatedTokens > limits.AutoCompactTokens || len(request.ProviderTurns) != 30 {
		t.Fatal("cannot fit growing result previews", info, err)
	}
	if request.ProviderTurns[0].ToolResults[0].Content != toolCallContext(calls[0]) {
		t.Fatal("fitting truncated human answers")
	}
	for i := range turns {
		if !reflect.DeepEqual(request.ProviderTurns[i].Items, turns[i].Items) {
			t.Fatal("fitting changed native group")
		}
	}
}

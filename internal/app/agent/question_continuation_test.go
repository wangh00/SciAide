package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

const capturedQuestionIntro = "为了帮你继续细化研究问题、问卷和分析方法，我需要先确认两个方面："
const questionExplanation = "先区分睡眠质量与学习投入，问卷相关性不能证明因果。"
const capturedScopeChoiceIntro = "有两个选择会明显影响综述范围："
const capturedScopeChoiceOffer = "如果你希望下一步做成更完整的文献综述（按年份、研究对象、实验设计把证据摊开，并标出研究缺口），我可以按这个方向准备研究方案。"

func TestQuestionHandoffPreservesExplanationAndRecognizesScopeChoices(t *testing.T) {
	for _, item := range []struct {
		name, text, body string
	}{
		{"captured_scope_choices", questionExplanation + "\n\n" + capturedScopeChoiceOffer + capturedScopeChoiceIntro, questionExplanation + "\n\n" + capturedScopeChoiceOffer},
		{"same_paragraph", questionExplanation + "我需要先确认两个方面：", questionExplanation},
		{"single_newline", questionExplanation + "\n" + capturedQuestionIntro, questionExplanation},
		{"windows_newlines", questionExplanation + "\r\n\r\n" + capturedQuestionIntro, questionExplanation},
		{"bold_intro", questionExplanation + "\n\n**" + capturedScopeChoiceIntro + "**", questionExplanation},
		{"english_scope_choices", "The evidence is preliminary. Two choices will affect the review scope:", "The evidence is preliminary."},
		{"closed_quote_before_intro", "如果你希望了解“AI 辅助反馈”，我可以汇总近年文献。" + capturedScopeChoiceIntro, "如果你希望了解“AI 辅助反馈”，我可以汇总近年文献。"},
		{"quoted_period", "先核对“AI.写作”的证据。" + capturedScopeChoiceIntro, "先核对“AI.写作”的证据。"},
		{"inline_code_before_intro", "先说明 `AI.v1` 的适用范围。" + capturedScopeChoiceIntro, "先说明 `AI.v1` 的适用范围。"},
		{"quoted_scope", "有两个选择会明显影响“综述范围”：", ""},
	} {
		t.Run(item.name, func(t *testing.T) {
			body, found := unfinishedQuestionHandoff(item.text)
			if !found || body != item.body {
				t.Fatalf("found=%v body=%q; want body=%q", found, body, item.body)
			}
		})
	}
}

func TestUnfinishedQuestionHandoffDoesNotTreatColonAsDecision(t *testing.T) {
	for _, item := range []struct {
		text string
		want bool
	}{
		{questionExplanation + "\n\n" + capturedQuestionIntro, true},
		{capturedQuestionIntro, true},
		{"I need to clarify two aspects:", true},
		{"你好，随时可以继续聊。", false},
		{"分析结果如下：", false},
		{"需要确认两个方面：", false},
		{"我需要先确认两个方面：你已有数据吗？", false},
		{"我需要先确认两个方面：\n1.你已有数据吗？\n2.产物类型？", false},
		{"我需要先确认两个方面：\n\n已有数据：", false},
		{"> " + capturedQuestionIntro, false},
		{"“" + capturedQuestionIntro + "”", false},
		{"```text\n" + capturedQuestionIntro, false},
		{"~~~text\n" + capturedQuestionIntro, false},
		{"## " + capturedQuestionIntro, false},
		{"我不需要确认两个方面：", false},
		{"原文写道：我需要确认两个方面：", false},
		{"大学生睡眠的结果是：", false},
		{"<think>我需要秘密确认两个方面：</think>你好。", false},
		{"两个选择不会影响综述范围：", false},
		{"有两个选择不会明显影响综述范围：", false},
		{"有两个选择不一定影响综述范围：", false},
		{"Two choices will not affect the review scope:", false},
		{"原文写道：“先研究背景。我需要先确认两个方面：", false},
		{"原文写道：'先研究背景。我需要先确认两个方面：", false},
		{"1.这只是示例。我需要先确认两个方面：", false},
		{"代码示例：`print(\"version.我需要先确认两个方面：", false},
		{"研究设计有两种选择：", false},
		{"> 这只是引用。我需要先确认两个方面：", false},
		{"> 这只是引用。\n我需要先确认两个方面：", false},
		{"- 这只是示例。我需要先确认两个方面：", false},
		{"```text\r\n" + capturedScopeChoiceIntro, false},
	} {
		_, got := unfinishedQuestionHandoff(item.text)
		if got != item.want {
			t.Errorf("%q: got %v want %v", item.text, got, item.want)
		}
	}
}

func TestCapturedScopeChoiceHandoffContinuesOnceAndKeepsCompleteOffer(t *testing.T) {
	body := questionExplanation + "\n\n" + capturedScopeChoiceOffer
	initial := body + capturedScopeChoiceIntro
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(initial), actualQuestionTurn())
	registerQuestionFixture(t, loop, state)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval || state.run.Status != chat.RunWaitingUserInput || state.run.PendingUserInputCallID == "" {
		t.Fatalf("outcome=%s status=%s requests=%d pending=%q", outcome, state.run.Status, len(provider.Requests()), state.run.PendingUserInputCallID)
	}
	if len(provider.Requests()) != 2 || state.messages[1].Parts[0].Text != body || state.journalDrafts[1] != initial {
		t.Fatal("lost explanation, original model draft, or bounded continuation", state.messages[1], len(provider.Requests()))
	}
}

func TestQuestionContinuationInvalidCallsFinishWithoutPendingOrLoop(t *testing.T) {
	for _, calls := range [][]model.ToolCall{
		{{ID: "empty", Name: userinput.ToolName, Arguments: json.RawMessage(`{"questions":[]}`)}},
		{{ID: "json", Name: userinput.ToolName, Arguments: json.RawMessage(`[]`)}},
		{{Name: userinput.ToolName, Arguments: json.RawMessage(`{}`)}},
		{{ID: "same", Name: userinput.ToolName, Arguments: json.RawMessage(`{}`)}, {ID: "same", Name: userinput.ToolName, Arguments: json.RawMessage(`{}`)}},
	} {
		steps := []fake.Step{}
		for i := range calls {
			steps = append(steps, fake.Step{Event: model.Event{Type: model.EventToolCall, ToolCall: &calls[i]}})
		}
		steps = append(steps, fake.Step{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}})
		loop, state, provider := newLoopFixture(t, nil, questionTextTurn(questionExplanation+"\n\n"+capturedQuestionIntro), steps)
		registerQuestionFixture(t, loop, state)
		loop.approvals, loop.executor = forbiddenQuestionCoordinator{t}, forbiddenQuestionExecutor{t}
		if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 2 || state.run.PendingUserInputCallID != "" {
			t.Fatal(outcome, state.run)
		}
		if text := state.messages[1].Parts[0].Text; text != questionExplanation+"\n\n"+questionContinuationFallback {
			t.Fatal(text)
		}
	}
}

func TestQuestionContinuationSmallWindowKeepsExplanationWithoutMoreRequests(t *testing.T) {
	for _, native := range []bool{false, true} {
		body := strings.Repeat("科学分析。", 1200)
		intro := body + "\n\n" + capturedQuestionIntro
		steps := questionTextTurn(intro)
		if native {
			payload, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": intro}}})
			steps = append([]fake.Step{{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &model.ProviderItem{Type: "message", Payload: payload}}}}, steps...)
		}
		loop, state, provider := newLoopFixture(t, nil, steps)
		registerQuestionFixture(t, loop, state)
		loop.models = budgetResolver{model: provider, budget: modelcap.ResolveContextBudget(5000, 0, modelcap.ContextWindowSourceManual)}
		if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 1 || state.messages[1].Parts[0].Text != body+"\n\n"+questionContinuationFallback {
			t.Fatal(native, outcome, state.run)
		}
	}
}

func TestOptionalQuestionContinuationDoesNotRolloverOversizedNativeState(t *testing.T) {
	// A terminal explanation is already useful. Optional intro repair must
	// not abandon its native protocol to start a fresh evidence exchange.
	intro := questionExplanation + "\n\n" + capturedQuestionIntro
	reasoning := model.ProviderItem{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","encrypted_content":"` + strings.Repeat("x", 280000) + `"}`)}
	payload, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": intro}}})
	message := model.ProviderItem{Ordinal: 1, Type: "message", Payload: payload}
	steps := append([]fake.Step{{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &reasoning}}, {Event: model.Event{Type: model.EventProviderItem, ProviderItem: &message}}}, questionTextTurn(intro)...)
	loop, state, provider := newLoopFixture(t, nil, steps)
	registerQuestionFixture(t, loop, state)
	loop.models = incrementalCheckpointResolver{model: provider}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 1 || len(state.calls) != 0 || state.messages[1].Parts[0].Text != questionExplanation+"\n\n"+questionContinuationFallback {
		t.Fatal("optional question repair initiated a protocol rollover", outcome, state.run)
	}
	if len(state.providerTurns) != 1 || !strings.Contains(string(state.providerTurns[0].Items[0].Payload), strings.Repeat("x", 280000)) {
		t.Fatal("optional repair changed native audit")
	}
}

type cancelQuestionHandoffObserver struct {
	NopObserver
	cancel context.CancelFunc
}

func (o cancelQuestionHandoffObserver) Retrying(_ chat.Run, retry RetryStatus) {
	if retry.Phase == "question_handoff" {
		o.cancel()
	}
}

func TestQuestionContinuationCancellationKeepsExplanationAndStops(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(questionExplanation+"\n\n"+capturedQuestionIntro), actualQuestionTurn())
	registerQuestionFixture(t, loop, state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop.observer = cancelQuestionHandoffObserver{cancel: cancel}
	if outcome := loop.Run(ctx, "run"); outcome != OutcomeCancelled || state.run.Status != chat.RunCancelled || len(provider.Requests()) != 1 || state.run.PendingUserInputCallID != "" || state.messages[1].Parts[0].Text != questionExplanation {
		t.Fatal(outcome, state.run)
	}
}

func TestQuestionContinuationNetworkFailureKeepsFinishedFindings(t *testing.T) {
	for _, item := range []struct {
		name, partial string
		retryable     bool
	}{
		{"before_output", "", false},
		{"partial_output", "discard this incomplete question", false},
		{"retries_exhausted", "discard this incomplete question", true},
	} {
		t.Run(item.name, func(t *testing.T) {
			scripts := [][]fake.Step{questionTextTurn(questionExplanation + "\n\n" + capturedQuestionIntro)}
			attempts := 1
			if item.retryable {
				attempts += maxStreamRetries
			}
			for range attempts {
				scripts = append(scripts, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: item.partial}}, {Err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "fixture connection lost", Retryable: item.retryable}}})
			}
			loop, state, provider := newLoopFixture(t, nil, scripts...)
			registerQuestionFixture(t, loop, state)
			loop.retryDelay = func(int) time.Duration { return 0 }
			journal := &questionJournalRuns{loopState: state}
			loop.runs = journal
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || state.run.Status != chat.RunCompleted {
				t.Fatal("optional handoff failure discarded finished findings", outcome, state.run.Status)
			}
			if len(provider.Requests()) != 1+attempts || state.run.PendingUserInputCallID != "" || state.messages[1].Parts[0].Text != questionExplanation+"\n\n"+questionContinuationFallback {
				t.Fatal("failed handoff renewed recovery, kept partial output, or left a fictional pending card", state.run, state.messages[1])
			}
			if journal.statuses[1] != chat.ModelTurnCompleted || journal.statuses[2] != chat.ModelTurnFailed || state.requestUsage[2].ErrorCode != "MODEL_UNAVAILABLE" {
				t.Fatal("failed optional request lost its audit", journal.statuses, state.requestUsage)
			}
		})
	}
}

func TestQuestionContinuationDoesNotHideRefusalProtocolOrStorageFailure(t *testing.T) {
	for _, code := range []string{"MODEL_OUTPUT_REFUSED", "MODEL_PROMPT_REJECTED", "MODEL_PROTOCOL_STATE_SAVE_FAILED", "RUN_STEP_SAVE_FAILED"} {
		t.Run(code, func(t *testing.T) {
			loop, state, _ := newLoopFixture(t, nil, questionTextTurn(questionExplanation+"\n\n"+capturedQuestionIntro), []fake.Step{{Err: &apperr.Error{Code: code, UserMessage: "fixture failure"}}})
			registerQuestionFixture(t, loop, state)
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || state.run.ErrorCode != code {
				t.Fatal("non-network failure hidden as a completed handoff", outcome, state.run)
			}
		})
	}
}

func TestQuestionContinuationDistinguishesRequestTimeoutFromCancellation(t *testing.T) {
	for _, item := range []struct {
		name string
		err  error
		want Outcome
	}{
		{"request_timeout", context.DeadlineExceeded, OutcomeCompleted},
		{"cancelled_stream", context.Canceled, OutcomeCancelled},
	} {
		t.Run(item.name, func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, questionTextTurn(questionExplanation+"\n\n"+capturedQuestionIntro), []fake.Step{{Err: item.err}})
			registerQuestionFixture(t, loop, state)
			if outcome := loop.Run(context.Background(), "run"); outcome != item.want || len(provider.Requests()) != 2 || state.run.PendingUserInputCallID != "" {
				t.Fatal(outcome, state.run.Status, len(provider.Requests()))
			}
			if item.want == OutcomeCompleted && state.messages[1].Parts[0].Text != questionExplanation+"\n\n"+questionContinuationFallback {
				t.Fatal("optional timeout lost the completed findings", state.messages[1])
			}
		})
	}
}

func TestQuestionContinuationCannotRenewBudgetWhenAuditCannotBeSaved(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(questionExplanation+"\n\n"+capturedQuestionIntro), actualQuestionTurn())
	registerQuestionFixture(t, loop, state)
	loop.runs = &faultInjectingRuns{loopState: state, failRunStep: true}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || len(provider.Requests()) != 1 || len(state.calls) != 0 || state.run.ErrorCode != "RUN_STEP_SAVE_FAILED" {
		t.Fatal(outcome, state.run)
	}
}

type questionHandoffEventObserver struct {
	NopObserver
	events []string
}

func (o *questionHandoffEventObserver) ContentStarted(chat.Run) { o.events = append(o.events, "start") }
func (o *questionHandoffEventObserver) ContentDelta(_ chat.Run, text string) {
	o.events = append(o.events, "delta:"+text)
}
func (o *questionHandoffEventObserver) Retrying(_ chat.Run, retry RetryStatus) {
	o.events = append(o.events, "reset:"+retry.Phase)
}

func TestQuestionContinuationResetsStreamBeforeRestoringBody(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil, questionTextTurn(questionExplanation+"\n\n"+capturedQuestionIntro), actualQuestionTurn())
	registerQuestionFixture(t, loop, state)
	observer := &questionHandoffEventObserver{}
	loop.observer = observer
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval {
		t.Fatal(outcome)
	}
	for i, event := range observer.events {
		if event == "reset:question_handoff" {
			if i+2 >= len(observer.events) || observer.events[i+1] != "start" || observer.events[i+2] != "delta:"+questionExplanation {
				t.Fatal("reset erased restored body", observer.events)
			}
			return
		}
	}
	t.Fatal("missing bounded repair event", observer.events)
}

func TestRepeatedHandoffKeepsNewExplanationButCannotRenewRepair(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(questionExplanation+"\n\n"+capturedQuestionIntro), questionTextTurn("补充说明，必须匿名收集。\n\n"+capturedQuestionIntro))
	registerQuestionFixture(t, loop, state)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 2 || !strings.Contains(state.messages[1].Parts[0].Text, "补充说明，必须匿名收集。") {
		t.Fatal(outcome, state.run)
	}
}

func TestQuestionContinuationOversizedExplanationDoesNotStoreTruncatedBudget(t *testing.T) {
	body := strings.Repeat("科学分析。", maxRunStepCommentaryRunes/5+1)
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(body+"\n\n"+capturedQuestionIntro))
	registerQuestionFixture(t, loop, state)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 1 || len(state.runSteps) != 0 || state.messages[1].Parts[0].Text != body+"\n\n"+questionContinuationFallback {
		t.Fatal(outcome, state.run)
	}
}

type questionJournalRuns struct {
	*loopState
	statuses   map[int]chat.ModelTurnStatus
	failFinish bool
}

func (r *questionJournalRuns) BeginModelTurn(ctx context.Context, id string, turn int, at time.Time) error {
	if r.statuses == nil {
		r.statuses = make(map[int]chat.ModelTurnStatus)
	}
	r.statuses[turn] = chat.ModelTurnStreaming
	return r.loopState.BeginModelTurn(ctx, id, turn, at)
}

func (r *questionJournalRuns) FinishModelTurn(_ context.Context, _ string, turn int, status chat.ModelTurnStatus, _ string, _ int, _ time.Time) error {
	if r.failFinish {
		return context.DeadlineExceeded
	}
	r.statuses[turn] = status
	return nil
}

func TestOversizedQuestionHandoffFinishesJournalBeforeCompletion(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "finished", true: "finish_failed"}[fail], func(t *testing.T) {
			body := strings.Repeat("科学分析。", maxRunStepCommentaryRunes/5+1)
			initial := body + "\n\n" + capturedQuestionIntro
			loop, state, provider := newLoopFixture(t, nil, questionTextTurn(initial))
			registerQuestionFixture(t, loop, state)
			journal := &questionJournalRuns{loopState: state, failFinish: fail}
			loop.runs = journal
			outcome := loop.Run(context.Background(), "run")
			if fail {
				if outcome != OutcomeFailed || state.run.Status == chat.RunCompleted {
					t.Fatal("journal finish failed but the answer was declared complete", outcome, state.run.Status)
				}
				return
			}
			if outcome != OutcomeCompleted || journal.statuses[1] != chat.ModelTurnCompleted || state.journalDrafts[1] != initial || len(provider.Requests()) != 1 {
				t.Fatal("completed answer retained a streaming or incomplete journal", outcome, journal.statuses, len(provider.Requests()))
			}
		})
	}
}

func TestRealQuestionIntroDoesNotStartRepairOrPrependAfterAnswer(t *testing.T) {
	steps := append(questionTextTurn(questionExplanation + "\n\n" + capturedQuestionIntro)[:1], actualQuestionTurn()...)
	loop, state, provider := newLoopFixture(t, nil, steps, questionTextTurn("完整回答。"))
	registerQuestionFixture(t, loop, state)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval || len(provider.Requests()) != 1 {
		t.Fatal(outcome)
	}
	call := state.calls[state.run.PendingUserInputCallID]
	call.Status, call.Result = tool.CallCompleted, &tool.Result{Status: tool.ResultSuccess, Text: "方案"}
	state.calls[call.ID] = call
	state.run.Status, state.run.PendingUserInputCallID = chat.RunRunning, ""
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 2 || state.messages[1].Parts[0].Text != "完整回答。" {
		t.Fatal(outcome, state.run)
	}
}

func registerQuestionFixture(t *testing.T, loop *Loop, state *loopState) {
	t.Helper()
	if err := loop.registry.(*tool.MemoryRegistry).Register(context.Background(), fixtureTool{definition: tool.Definition{QualifiedName: userinput.ToolName, Description: "question fixture", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object"}`)}}); err != nil {
		t.Fatal(err)
	}
	loop.userInput = &questionWaitFixture{state: state}
}

func questionTextTurn(text string) []fake.Step {
	return []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: text}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
}
func actualQuestionTurn() []fake.Step {
	return []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "actual-question", Name: userinput.ToolName, Arguments: json.RawMessage(`{"questions":[{"id":"purpose","question":"希望产出什么？","required":true,"options":[{"id":"design","label":"研究方案"},{"id":"review","label":"文献梳理"}]}]}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
}

func TestUnfinishedQuestionContinuesOnceIntoRealWaitAndResumesOriginalRun(t *testing.T) {
	initial := questionExplanation + "\n\n" + capturedQuestionIntro
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(initial), actualQuestionTurn(), questionTextTurn("按你的回答，继续细化研究方案。"))
	registerQuestionFixture(t, loop, state)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval || state.run.Status != chat.RunWaitingUserInput {
		t.Fatal(outcome, state.run)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatal(len(requests))
	}
	second := requests[1]
	if len(second.Tools) != 1 || second.Tools[0].Name != userinput.ToolName || second.ForcedTool != "" {
		t.Fatal(second.Tools, second.ForcedTool)
	}
	tail := second.Messages[len(second.Messages)-1]
	if tail.Role != model.RoleSystem || !tail.ContextTail || !tail.HostToolReferences || !strings.Contains(tail.Content, "one bounded continuation") {
		t.Fatal(tail)
	}
	if state.journalDrafts[1] != initial || state.runSteps[0].Commentary != initial || state.messages[1].Parts[0].Text != questionExplanation || len(state.calls) != 1 {
		t.Fatal("audit/body/wait missing", state)
	}
	// Mirror the normal response transaction, then execute a fresh continuation:
	// its budget/body must be restored from persisted activity, not in-memory.
	call := state.calls[state.run.PendingUserInputCallID]
	call.Status = tool.CallCompleted
	call.Result = &tool.Result{Status: tool.ResultSuccess, Text: "研究方案"}
	state.calls[call.ID] = call
	state.run.Status = chat.RunRunning
	state.run.PendingUserInputCallID = ""
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatal(outcome, state.run)
	}
	if len(provider.Requests()) != 3 || state.messages[1].Parts[0].Text != questionExplanation+"\n\n按你的回答，继续细化研究方案。" {
		t.Fatal("explanation lost or repeated", state.messages[1])
	}
	resumed := provider.Requests()[2]
	explanationIndex, questionIndex, answerIndex, copies := -1, -1, -1, 0
	for index, message := range resumed.Messages {
		if message.Role == model.RoleAssistant && message.Content == initial {
			explanationIndex, copies = index, copies+1
		}
		for _, call := range message.ToolCalls {
			if call.Name == userinput.ToolName {
				questionIndex = index
			}
		}
		if message.Role == model.RoleTool && message.ToolCallID == call.ProviderCallID {
			answerIndex = index
		}
	}
	if copies != 1 || explanationIndex >= questionIndex || questionIndex >= answerIndex || explanationIndex < 0 || strings.Contains(resumed.Messages[len(resumed.Messages)-1].Content, "one bounded continuation") {
		t.Fatal("resumed model lost the pre-question findings or reordered/duplicated the handoff", explanationIndex, questionIndex, answerIndex, copies)
	}
}

func TestUnfinishedQuestionDoesNotLoopOrForceQuestion(t *testing.T) {
	for _, reply := range []string{capturedQuestionIntro, "可以先按现有材料讨论，无需额外提问。"} {
		t.Run(reply, func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, questionTextTurn(questionExplanation+"\n\n"+capturedQuestionIntro), questionTextTurn(reply))
			registerQuestionFixture(t, loop, state)
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 2 || len(state.calls) != 0 {
				t.Fatal(outcome, state.run)
			}
			answer := state.messages[1].Parts[0].Text
			if strings.HasSuffix(answer, "：") || !strings.HasPrefix(answer, questionExplanation) || state.run.ErrorCode != "" {
				t.Fatal(answer, state.run)
			}
			if reply == capturedQuestionIntro && !strings.Contains(answer, questionContinuationFallback) {
				t.Fatal(answer)
			}
		})
	}
}

func TestQuestionContinuationCannotExecuteOtherTools(t *testing.T) {
	unrelated := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "unrelated", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"repeat research"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(capturedQuestionIntro), unrelated)
	registerQuestionFixture(t, loop, state)
	loop.approvals = forbiddenQuestionCoordinator{t}
	loop.executor = forbiddenQuestionExecutor{t}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatal(outcome, state.run)
	}
	if len(provider.Requests()) != 2 || len(state.calls) != 1 || !strings.Contains(state.messages[1].Parts[0].Text, questionContinuationFallback) {
		t.Fatal(state.calls)
	}
	for _, call := range state.calls {
		if call.Status != tool.CallFailed {
			t.Fatal(call)
		}
	}
}

func TestNormalConversationOrUnavailableQuestionToolHasNoExtraRequest(t *testing.T) {
	for _, item := range []struct {
		text      string
		available bool
	}{{"你好。", true}, {"你有多少参与者？", true}, {"方法有两类：", true}, {capturedQuestionIntro, false}} {
		t.Run(item.text, func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, questionTextTurn(item.text))
			if item.available {
				registerQuestionFixture(t, loop, state)
			}
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 1 || len(state.calls) != 0 {
				t.Fatal(outcome, state.run)
			}
		})
	}
}

func TestQuestionContinuationRestoreIgnoresRealToolCommentary(t *testing.T) {
	steps := []chat.RunStep{{RunID: "run", TurnIndex: 1, Commentary: capturedQuestionIntro}}
	if _, used := savedQuestionContinuation(steps, []tool.Call{{IdempotencyKey: "agent-tool:run:1:actual", ToolName: userinput.ToolName}}); used {
		t.Fatal("legitimate tool intro consumes repair budget")
	}
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(capturedQuestionIntro))
	registerQuestionFixture(t, loop, state)
	state.runSteps = steps
	state.run.ModelTurns = 1
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 1 || !strings.Contains(state.messages[1].Parts[0].Text, questionContinuationFallback) {
		t.Fatal(outcome, state.run)
	}
}

func TestQuestionContinuationPreservesNativeItemsAndDoesNotDuplicateText(t *testing.T) {
	item := model.ProviderItem{Type: "message", Payload: json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"我需要确认两个方面："}]}`)}
	request := model.ChatRequest{Tools: []model.ToolDefinition{{Name: userinput.ToolName}, {Name: "builtin.fixture"}}, ProviderTurns: []model.ProviderTurn{{TurnIndex: 1, Items: []model.ProviderItem{item}}}}
	got := questionContinuationRequest(request, "我需要确认两个方面：")
	if len(got.Messages) != 1 || len(got.ProviderTurns) != 1 || string(got.ProviderTurns[0].Items[0].Payload) != string(item.Payload) || len(request.Tools) != 2 {
		t.Fatal(got)
	}
	got = questionContinuationRequest(model.ChatRequest{Tools: request.Tools}, "我需要确认两个方面：")
	if len(got.Messages) != 2 || got.Messages[0].Role != model.RoleAssistant {
		t.Fatal(got)
	}
}

func TestQuestionContinuationPreservesNativeCommentaryAndFinalAnswerWithoutDuplication(t *testing.T) {
	item := func(phase, text string) model.ProviderItem {
		payload, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "phase": phase, "content": []any{map[string]any{"type": "output_text", "text": text}}})
		return model.ProviderItem{Type: "message", Phase: phase, Payload: payload}
	}
	request := model.ChatRequest{Tools: []model.ToolDefinition{{Name: userinput.ToolName}}, ProviderTurns: []model.ProviderTurn{{TurnIndex: 1, Items: []model.ProviderItem{item("commentary", "正在核对研究摘要。"), item("final_answer", capturedScopeChoiceIntro)}}}}
	got := questionContinuationRequest(request, capturedScopeChoiceIntro)
	if len(got.Messages) != 1 || got.Messages[0].Content != questionContinuationInstruction || len(got.ProviderTurns[0].Items) != 2 {
		t.Fatal("native commentary caused a duplicate copy of the final answer", got)
	}
	got = withQuestionHandoffEvidence(request, questionHandoff{text: capturedScopeChoiceIntro, runID: "run", turnIndex: 1}, nil)
	if len(got.Messages) != 0 || string(got.ProviderTurns[0].Items[0].Payload) != string(request.ProviderTurns[0].Items[0].Payload) || string(got.ProviderTurns[0].Items[1].Payload) != string(request.ProviderTurns[0].Items[1].Payload) {
		t.Fatal("resumption duplicated or rewrote native evidence", got)
	}
}

func TestQuestionContinuationDoesNotChangeWorkflowStageContract(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, questionTextTurn(`{"summary":"complete"}`))
	registerQuestionFixture(t, loop, state)
	state.workflowAI = true
	loop.research = &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{AllowedToolNames: []string{"builtin.fixture"}, StructuredOutputRequired: true, StructuredOutputSchema: json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`)}}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted || len(provider.Requests()) != 1 {
		t.Fatal(outcome, state.run)
	}
	for _, d := range provider.Requests()[0].Tools {
		if d.Name == userinput.ToolName {
			t.Fatal("question leaked into workflow")
		}
	}
}

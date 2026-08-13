package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

type loopState struct {
	mu        sync.Mutex
	run       chat.Run
	messages  []conversation.Message
	calls     map[string]tool.Call
	callOrder []string
}

func (s *loopState) Get(context.Context, string) (chat.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run, nil
}
func (s *loopState) Update(_ context.Context, run chat.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = run
	return nil
}
func (s *loopState) IncrementModelTurns(_ context.Context, _ string, maximum int, at time.Time) (chat.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.Status != chat.RunRunning || s.run.ModelTurns >= maximum {
		return chat.Run{}, chat.ErrModelTurnBudgetExceeded
	}
	s.run.ModelTurns++
	s.run.UpdatedAt = at
	return s.run, nil
}
func (s *loopState) ProjectIDForRun(context.Context, string) (string, error) { return "project", nil }
func (s *loopState) transitionRun(expected, next chat.RunStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.Status == expected {
		s.run.Status = next
	}
}
func (s *loopState) ListMessages(context.Context, string, int) ([]conversation.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]conversation.Message(nil), s.messages...), nil
}
func (s *loopState) UpdateMessageText(_ context.Context, id string, status conversation.MessageStatus, text string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.messages {
		if s.messages[index].ID == id {
			s.messages[index].Status, s.messages[index].UpdatedAt = status, at
			s.messages[index].Parts[0].Text = text
			return nil
		}
	}
	return fmt.Errorf("message not found")
}
func (s *loopState) GetCall(_ context.Context, id string) (tool.Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.calls[id]
	if !ok {
		return tool.Call{}, fmt.Errorf("call not found")
	}
	return value, nil
}
func (s *loopState) ListByRun(_ context.Context, runID string) ([]tool.Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]tool.Call, 0, len(s.callOrder))
	for _, id := range s.callOrder {
		if value := s.calls[id]; value.RunID == runID {
			values = append(values, value)
		}
	}
	return values, nil
}
func (s *loopState) InterruptActive(context.Context, time.Time) (int64, error) { return 0, nil }
func (s *loopState) CreateWithEvent(_ context.Context, value tool.Call, _ events.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.calls {
		if existing.RunID == value.RunID && existing.ProviderCallID == value.ProviderCallID {
			return fmt.Errorf("duplicate provider call")
		}
	}
	s.calls[value.ID] = value
	s.callOrder = append(s.callOrder, value.ID)
	return nil
}
func (s *loopState) TransitionWithEvent(_ context.Context, id string, expected, next tool.CallStatus, code, message string, at time.Time, _ events.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.calls[id]
	if value.Status != expected {
		return tool.ErrTransitionConflict
	}
	value.Status, value.ErrorCode, value.ErrorMessage, value.UpdatedAt = next, code, message, at
	if next == tool.CallRunning {
		value.StartedAt = &at
	}
	s.calls[id] = value
	return nil
}
func (s *loopState) FinishWithEvent(_ context.Context, id string, expected, next tool.CallStatus, result tool.Result, code, message string, at time.Time, _ events.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.calls[id]
	if value.Status != expected {
		return tool.ErrTransitionConflict
	}
	value.Status, value.Result, value.ErrorCode, value.ErrorMessage, value.UpdatedAt, value.CompletedAt = next, &result, code, message, at, &at
	s.calls[id] = value
	return nil
}

type fixtureTool struct {
	definition tool.Definition
	invoke     func(tool.Invocation) tool.Result
}

func (f fixtureTool) Definition(context.Context) (tool.Definition, error) { return f.definition, nil }
func (f fixtureTool) Invoke(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
	return f.invoke(invocation), nil
}

type allowCoordinator struct{ service *tool.Service }

func (c allowCoordinator) EvaluateCall(ctx context.Context, _ string, callID string) (permission.Coordination, error) {
	call, err := c.service.Start(ctx, callID)
	return permission.Coordination{Evaluation: permission.Evaluation{Decision: permission.DecisionAllow}, ToolCall: call, Run: chat.Run{ID: call.RunID, Status: chat.RunRunning}}, err
}

type statefulAskCoordinator struct {
	service *tool.Service
	state   *loopState
}

func (c statefulAskCoordinator) EvaluateCall(ctx context.Context, _ string, callID string) (permission.Coordination, error) {
	call, err := c.service.AwaitApproval(ctx, callID)
	c.state.transitionRun(chat.RunRunning, chat.RunWaitingApproval)
	return permission.Coordination{Evaluation: permission.Evaluation{Decision: permission.DecisionAsk}, Approval: &permission.Approval{ID: "approval", ToolCallID: call.ID}, ToolCall: call, Run: chat.Run{ID: call.RunID, Status: chat.RunWaitingApproval}}, err
}

type fakeResolver struct{ model model.ChatModel }

func (r fakeResolver) Resolve(context.Context, string, string) (model.ChatModel, error) {
	return r.model, nil
}

type blockingModel struct{}

func (blockingModel) Capabilities(context.Context) (model.Capabilities, error) {
	return model.Capabilities{Streaming: true}, nil
}
func (blockingModel) Stream(ctx context.Context, _ model.ChatRequest) (model.Stream, error) {
	return blockingStream{ctx: ctx}, nil
}

type blockingStream struct{ ctx context.Context }

func (s blockingStream) Recv() (model.Event, error) {
	<-s.ctx.Done()
	return model.Event{}, s.ctx.Err()
}
func (blockingStream) Close() error { return nil }

type executionAdapter struct{ executor *tool.Executor }

func (a executionAdapter) Execute(ctx context.Context, projectID, callID string) (tool.Execution, error) {
	return a.executor.Execute(ctx, projectID, callID)
}

func newLoopFixture(t *testing.T, coordinator ApprovalCoordinator, scripts ...[]fake.Step) (*Loop, *loopState, *fake.Model) {
	t.Helper()
	now := time.Now().UTC()
	state := &loopState{
		run: chat.Run{ID: "run", ConversationID: "conversation", AssistantMessageID: "assistant", ModelProfileID: "profile", ModelID: "model", Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now},
		messages: []conversation.Message{
			{ID: "user", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "读取论文"}}},
			{ID: "assistant", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{Type: "text"}}},
		},
		calls: map[string]tool.Call{},
	}
	service := tool.NewService(stateAdapter{state}, tool.JSONSchemaValidator{})
	registry := tool.NewRegistry()
	implementation := fixtureTool{definition: tool.Definition{QualifiedName: "builtin.fixture", Description: "fixture", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string"}}}`), Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{}, Idempotent: true, Version: "1"}, invoke: func(invocation tool.Invocation) tool.Result {
		return tool.Result{Status: tool.ResultSuccess, Text: "可信执行，不可信内容", Structured: json.RawMessage(`{"found":true}`)}
	}}
	if err := registry.Register(context.Background(), implementation); err != nil {
		t.Fatal(err)
	}
	provider := fake.New(scripts...)
	if coordinator == nil {
		coordinator = allowCoordinator{service}
	}
	executor := tool.NewExecutor(registry, service, state, tool.ExecutorOptions{})
	loop := NewLoop(state, state, service, registry, coordinator, executionAdapter{executor}, fakeResolver{provider}, nil, Options{})
	return loop, state, provider
}

// stateAdapter avoids a Get method name collision between chat.Run and tool.Call.
type stateAdapter struct{ state *loopState }

func (a stateAdapter) Get(ctx context.Context, id string) (tool.Call, error) {
	return a.state.GetCall(ctx, id)
}
func (a stateAdapter) ListByRun(ctx context.Context, id string) ([]tool.Call, error) {
	return a.state.ListByRun(ctx, id)
}
func (a stateAdapter) InterruptActive(ctx context.Context, at time.Time) (int64, error) {
	return a.state.InterruptActive(ctx, at)
}
func (a stateAdapter) CreateWithEvent(ctx context.Context, call tool.Call, event events.Envelope) error {
	return a.state.CreateWithEvent(ctx, call, event)
}
func (a stateAdapter) TransitionWithEvent(ctx context.Context, id string, expected, next tool.CallStatus, code, message string, at time.Time, event events.Envelope) error {
	return a.state.TransitionWithEvent(ctx, id, expected, next, code, message, at, event)
}
func (a stateAdapter) FinishWithEvent(ctx context.Context, id string, expected, next tool.CallStatus, result tool.Result, code, message string, at time.Time, event events.Envelope) error {
	return a.state.FinishWithEvent(ctx, id, expected, next, result, code, message, at, event)
}

func TestAgentLoopCompletesFakeModelToolRoundTrip(t *testing.T) {
	first := []fake.Step{
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "已读取"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, first, second)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	if state.run.Status != chat.RunCompleted || state.messages[1].Parts[0].Text != "已读取" || len(state.calls) != 1 {
		t.Fatalf("state = run:%#v messages:%#v calls:%#v", state.run, state.messages, state.calls)
	}
	state.mu.Unlock()
	requests := provider.Requests()
	if len(requests) != 2 || len(requests[0].Tools) != 1 {
		t.Fatalf("requests = %#v", requests)
	}
	last := requests[1].Messages
	if len(last) < 3 || last[len(last)-2].Role != model.RoleAssistant || len(last[len(last)-2].ToolCalls) != 1 || last[len(last)-1].Role != model.RoleTool || last[len(last)-1].ToolCallID != "provider-call" || last[len(last)-1].Content == "" {
		t.Fatalf("tool round trip = %#v", last)
	}
}

func TestAgentLoopPersistsReportedCacheUsage(t *testing.T) {
	usage := model.Usage{InputTokens: 120, OutputTokens: 18, CachedInputTokens: 80, CacheWriteTokens: 12, CacheDetailsReported: true}
	script := []fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &usage}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, _ := newLoopFixture(t, nil, script)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.InputTokens != 120 || state.run.OutputTokens != 18 || state.run.CachedInputTokens != 80 || state.run.CacheWriteTokens != 12 || state.run.CacheReportedTurns != 1 || state.run.CacheHitTurns != 1 {
		t.Fatalf("cache usage run = %#v", state.run)
	}
}

func TestAgentLoopPausesBeforeExecutingApprovalTool(t *testing.T) {
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	loop, state, _ := newLoopFixture(t, nil, first)
	service := loop.tools.(*tool.Service)
	loop.approvals = statefulAskCoordinator{service, state}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval {
		t.Fatalf("outcome = %s", outcome)
	}
	calls, _ := service.ListByRun(context.Background(), "run")
	if len(calls) != 1 || calls[0].Status != tool.CallAwaitingApproval || calls[0].Result != nil {
		t.Fatalf("calls = %#v", calls)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	// The production coordinator persists waiting_approval atomically; the test
	// double proves the loop exits without invoking the tool.
	if state.messages[1].Parts[0].Text != "" {
		t.Fatalf("assistant text = %q", state.messages[1].Parts[0].Text)
	}
}

func TestAgentLoopModelTurnBudgetSurvivesApprovalResume(t *testing.T) {
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "should not run"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, _ := newLoopFixture(t, nil, first, second)
	loop.budget.MaxModelTurns = 1
	ask := statefulAskCoordinator{service: loop.tools.(*tool.Service), state: state}
	loop.approvals = ask
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval {
		t.Fatalf("first outcome = %s", outcome)
	}
	state.mu.Lock()
	state.run.Status = chat.RunRunning
	for id, call := range state.calls {
		call.Status = tool.CallDenied
		call.Result = &tool.Result{Status: tool.ResultDenied, Text: "denied"}
		state.calls[id] = call
	}
	state.mu.Unlock()
	if outcome := loop.Resume(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("resume outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.ModelTurns != 1 || state.run.ErrorCode != "MODEL_TURN_BUDGET_EXCEEDED" {
		t.Fatalf("durable budget run = %#v", state.run)
	}
}

func TestAgentLoopResumeExecutesApprovedCallBeforeNextModelTurn(t *testing.T) {
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "审批后完成"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, second)
	service := loop.tools.(*tool.Service)
	definition, _ := loop.registry.Definition(context.Background(), "builtin.fixture")
	call, err := service.Propose(context.Background(), definition, tool.CreateCommand{RunID: "run", ProviderCallID: "approved-call", Arguments: json.RawMessage(`{"query":"paper"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(context.Background(), call.ID); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	started := time.Now().UTC()
	state.run.Status, state.run.StartedAt = chat.RunRunning, &started
	state.mu.Unlock()
	if outcome := loop.Resume(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	calls, _ := service.ListByRun(context.Background(), "run")
	if len(calls) != 1 || calls[0].Status != tool.CallCompleted || calls[0].Result == nil {
		t.Fatalf("calls = %#v", calls)
	}
	requests := provider.Requests()
	if len(requests) != 1 || requests[0].Messages[len(requests[0].Messages)-1].Role != model.RoleTool {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestContextBuilderKeepsLatestMessageAndToolResults(t *testing.T) {
	builder := NewContextBuilder(5)
	messages := []conversation.Message{
		{ID: "old", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "12345"}}},
		{ID: "new", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "67890"}}},
	}
	calls := []tool.Call{{ProviderCallID: "call", ToolName: "fixture", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: "result"}}}
	request, err := builder.Build(context.Background(), messages, "", nil, calls)
	if err != nil {
		t.Fatal(err)
	}
	roles := make([]model.Role, 0, len(request.Messages))
	for _, message := range request.Messages {
		roles = append(roles, message.Role)
	}
	if request.Messages[1].Content != "67890" || roles[len(roles)-1] != model.RoleTool {
		t.Fatalf("request = %#v", request)
	}
}

func TestContextBuilderBoundsCumulativeToolResults(t *testing.T) {
	builder := NewContextBuilder(20)
	calls := []tool.Call{
		{ProviderCallID: "old", ToolName: "fixture", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("o", 100)}},
		{ProviderCallID: "new", ToolName: "fixture", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: "newest"}},
	}
	request, err := builder.Build(context.Background(), nil, "", nil, calls)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 3 || request.Messages[1].ToolCalls[0].ID != "new" || len([]rune(request.Messages[2].Content)) > 20 {
		t.Fatalf("bounded tool context = %#v", request.Messages)
	}
}

func TestAgentLoopDurationBudgetCancelsBlockedModelStream(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventDone}}})
	loop.models = fakeResolver{blockingModel{}}
	loop.budget = RunBudget{MaxModelTurns: 2, MaxToolCalls: 1, MaxDuration: 20 * time.Millisecond}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.ErrorCode != "RUN_DURATION_BUDGET_EXCEEDED" {
		t.Fatalf("run = %#v", state.run)
	}
}

func TestProviderToolCallValidationRejectsDuplicates(t *testing.T) {
	calls := []model.ToolCall{{ID: "same", Name: "one", Arguments: json.RawMessage(`{}`)}, {ID: "same", Name: "two", Arguments: json.RawMessage(`{}`)}}
	if err := ValidateProviderToolCalls(calls); err == nil {
		t.Fatal("duplicate provider ids accepted")
	}
}

func TestFirstUnresolvedToolCallFailsClosed(t *testing.T) {
	calls := []tool.Call{{ID: "completed", Status: tool.CallCompleted}, {ID: "waiting", Status: tool.CallAwaitingApproval}}
	if got := firstUnresolvedToolCall(calls); got == nil || got.ID != "waiting" {
		t.Fatalf("unresolved = %#v", got)
	}
	if got := firstUnresolvedToolCall([]tool.Call{{Status: tool.CallDenied}, {Status: tool.CallFailed}}); got != nil {
		t.Fatalf("terminal call reported unresolved: %#v", got)
	}
}

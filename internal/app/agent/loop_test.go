package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	appskill "github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/skillpkg"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

type loopState struct {
	mu            sync.Mutex
	run           chat.Run
	messages      []conversation.Message
	calls         map[string]tool.Call
	callOrder     []string
	providerTurns []model.ProviderTurn
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
func (s *loopState) Complete(_ context.Context, run chat.Run, text string, citations []conversation.Citation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.Status != chat.RunCompleted || run.ID != s.run.ID || s.run.Status != chat.RunRunning {
		return fmt.Errorf("invalid run completion")
	}
	for index := range s.messages {
		if s.messages[index].ID == run.AssistantMessageID && s.messages[index].RunID == run.ID && s.messages[index].Role == conversation.RoleAssistant {
			s.messages[index].Status, s.messages[index].UpdatedAt = conversation.MessageComplete, run.UpdatedAt
			s.messages[index].Parts[0].Text = text
			s.messages[index].Citations = append([]conversation.Citation(nil), citations...)
			s.run = run
			return nil
		}
	}
	return fmt.Errorf("assistant message not found")
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
func (s *loopState) SaveProviderTurn(_ context.Context, _ string, turn model.ProviderTurn, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerTurns = append(s.providerTurns, turn)
	return nil
}
func (s *loopState) ListProviderTurns(context.Context, string) ([]model.ProviderTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.ProviderTurn(nil), s.providerTurns...), nil
}
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

func (r fakeResolver) Resolve(context.Context, string, string) (model.ResolvedChatModel, error) {
	return model.ResolvedChatModel{Model: r.model, SupportedReasoningLevels: []modelcap.ReasoningLevel{modelcap.ReasoningLow, modelcap.ReasoningMedium, modelcap.ReasoningHigh, modelcap.ReasoningXHigh, modelcap.ReasoningMax}}, nil
}

type protocolResolver struct {
	model    model.ChatModel
	protocol modelcap.APIProtocol
}

type budgetResolver struct {
	model  model.ChatModel
	budget modelcap.ContextBudget
}

func (r budgetResolver) Resolve(context.Context, string, string) (model.ResolvedChatModel, error) {
	return model.ResolvedChatModel{Model: r.model, APIProtocol: modelcap.ProtocolOpenAIChat, ContextBudget: r.budget}, nil
}

type memoryCheckpointRepository struct {
	latest contextmemory.Checkpoint
}

func (r *memoryCheckpointRepository) Latest(context.Context, string) (contextmemory.Checkpoint, bool, error) {
	return r.latest, r.latest.ID != "", nil
}

func (r *memoryCheckpointRepository) Save(_ context.Context, value contextmemory.Checkpoint) (contextmemory.Checkpoint, error) {
	value.Revision = r.latest.Revision + 1
	r.latest = value
	return value, nil
}

func (r protocolResolver) Resolve(context.Context, string, string) (model.ResolvedChatModel, error) {
	return model.ResolvedChatModel{Model: r.model, APIProtocol: r.protocol, SupportedReasoningLevels: []modelcap.ReasoningLevel{modelcap.ReasoningLow, modelcap.ReasoningMedium, modelcap.ReasoningHigh, modelcap.ReasoningXHigh, modelcap.ReasoningMax}}, nil
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

type resolutionModel struct {
	inner    model.ChatModel
	resolved modelcap.ReasoningLevel
}

func (m resolutionModel) Capabilities(ctx context.Context) (model.Capabilities, error) {
	return m.inner.Capabilities(ctx)
}

func (m resolutionModel) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	stream, err := m.inner.Stream(ctx, request)
	if err != nil {
		return nil, err
	}
	return model.WithReasoningResolution(stream, request.RequestedReasoningLevel, m.resolved), nil
}

type executionAdapter struct{ executor *tool.Executor }

func (a executionAdapter) Execute(ctx context.Context, projectID, callID string) (tool.Execution, error) {
	return a.executor.Execute(ctx, projectID, callID)
}

type staticRunSkillContexts struct {
	value appskill.RunContext
	calls int
}

type registrySkillTools struct{ registry *tool.MemoryRegistry }

func (r registrySkillTools) AvailableToolNames(ctx context.Context) ([]string, error) {
	definitions, err := r.registry.Definitions(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(definitions))
	for index, definition := range definitions {
		result[index] = definition.QualifiedName
	}
	return result, nil
}

func (s *staticRunSkillContexts) PrepareRunContext(context.Context, string, string, string, int) (appskill.RunContext, error) {
	s.calls++
	return s.value, nil
}

func agentSkillContext(runID string) appskill.RunContext {
	manifest := appskill.NormalizeManifest(appskill.Manifest{
		SchemaVersion: appskill.CurrentSchemaVersion,
		ID:            "research-review",
		Name:          "Research review",
		Version:       "1.0.0",
		Description:   "Review research evidence",
		Entry:         "SKILL.md",
		Activation:    appskill.Activation{Mode: appskill.ActivationExplicit},
		Permissions:   []string{"destructive"},
		Compatibility: appskill.Compatibility{SciAide: ">=0.2.0 <1.0.0"},
		Context:       appskill.ContextPolicy{MaxTokens: 8_000},
	})
	instructions := "Always inspect the evidence before drawing conclusions."
	contentHash := sha256.Sum256([]byte(instructions))
	return appskill.RunContext{
		SchemaVersion:           appskill.RunContextSchemaVersion,
		RunID:                   runID,
		ProjectID:               "project",
		ContextWindowTokens:     200_000,
		CatalogBudgetTokens:     4_000,
		InstructionBudgetTokens: 40_000,
		Catalog:                 []appskill.RunCatalogSkill{{SkillID: manifest.ID, Version: manifest.Version, Name: manifest.Name, Description: manifest.Description, Activation: manifest.Activation.Mode, Priority: 10}},
		CatalogText:             "<available_skills>\n- $research-review [Research review@1.0.0]\n</available_skills>",
		Skills:                  []appskill.RunSkill{{Manifest: manifest, Priority: 10, Reason: appskill.SelectionExplicit, PackagePath: "research-review/1.0.0", ManifestHash: strings.Repeat("a", 64), ContentHash: fmt.Sprintf("%x", contentHash), PackageHash: strings.Repeat("c", 64), Instructions: instructions}},
		CreatedAt:               time.Now().UTC(),
	}
}

func newLoopFixture(t *testing.T, coordinator ApprovalCoordinator, scripts ...[]fake.Step) (*Loop, *loopState, *fake.Model) {
	t.Helper()
	now := time.Now().UTC()
	state := &loopState{
		run: chat.Run{ID: "run", ConversationID: "conversation", UserMessageID: "user", AssistantMessageID: "assistant", ModelProfileID: "profile", ModelID: "model", Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now},
		messages: []conversation.Message{
			{ID: "user", ConversationID: "conversation", RunID: "run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "读取论文"}}},
			{ID: "assistant", ConversationID: "conversation", RunID: "run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{Type: "text"}}},
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

func TestAgentLoopPersistsOnlyCitedKnowledgeEvidence(t *testing.T) {
	quote := "The measured outcome was reduced."
	reference := citation.KnowledgeReference("run", "index-v3", "chunk-a", citation.QuoteSHA256(quote))
	first := []fake.Step{
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "knowledge-call", Name: citation.KnowledgeToolName, Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "Supported " + reference + "; fabricated [K-000000000000]."}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, _ := newLoopFixture(t, nil, first, second)
	knowledgeTool := fixtureTool{definition: tool.Definition{QualifiedName: citation.KnowledgeToolName, Description: "fixture knowledge", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskLow, Idempotent: true, Version: "3"}, invoke: func(invocation tool.Invocation) tool.Result {
		return tool.Result{Status: tool.ResultSuccess, Text: reference + "\n" + quote, Citations: []tool.CitationRef{{
			Kind: citation.KindKnowledgeChunk, Reference: reference, ProjectID: invocation.ProjectID, IndexVersionID: "index-v3",
			DocumentID: "document-a", AttachmentID: "attachment-a", ChunkID: "chunk-a", SourceName: "paper.pdf",
			MIMEType: "application/pdf", Locator: "page:3", Quote: quote, QuoteSHA256: citation.QuoteSHA256(quote), SourceEnd: len([]rune(quote)),
		}}}
	}}
	if err := loop.registry.(tool.MutableRegistry).Register(context.Background(), knowledgeTool); err != nil {
		t.Fatal(err)
	}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	values := state.messages[1].Citations
	if len(values) != 1 || values[0].Reference != reference || values[0].Locator != "page:3" || values[0].Quote != quote {
		t.Fatalf("persisted citations = %#v", values)
	}
}

func TestAgentLoopPersistsPublicProviderErrorDetails(t *testing.T) {
	detail := "Source: Responses stream event\nProvider payload:\n{\"error\":\"fixture\"}"
	loop, state, _ := newLoopFixture(t, nil, []fake.Step{{Err: &apperr.Error{Code: "MODEL_REQUEST_REJECTED", UserMessage: "请求失败", Details: detail}}})
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.ErrorCode != "MODEL_REQUEST_REJECTED" || state.run.ErrorMessage != "请求失败" || state.run.ErrorDetails != detail {
		t.Fatalf("run error = %#v", state.run)
	}
}

func TestAgentLoopPersistsAndReplaysAnthropicProviderTurn(t *testing.T) {
	thinking := model.ProviderItem{Ordinal: 0, Type: "thinking", Payload: json.RawMessage(`{"type":"thinking","thinking":"inspect","signature":"signed"}`)}
	toolItem := model.ProviderItem{Ordinal: 1, Type: "tool_use", CallID: "provider-call", Payload: json.RawMessage(`{"type":"tool_use","id":"provider-call","name":"fixture","input":{"query":"paper"}}`)}
	first := []fake.Step{
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &thinking}},
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &toolItem}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, first, second)
	loop.models = protocolResolver{model: provider, protocol: modelcap.ProtocolAnthropic}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	if len(state.providerTurns) != 1 || len(state.providerTurns[0].Items) != 2 {
		t.Fatalf("provider turns = %#v", state.providerTurns)
	}
	if !state.run.ReasoningObserved || !state.run.ReasoningSignatureObserved {
		t.Fatalf("reasoning evidence = %#v", state.run)
	}
	state.mu.Unlock()
	requests := provider.Requests()
	if len(requests) != 2 || len(requests[1].ProviderTurns) != 1 || len(requests[1].ProviderTurns[0].ToolResults) != 1 || requests[1].ProviderTurns[0].ToolResults[0].ToolCallID != "provider-call" {
		t.Fatalf("replayed request = %#v", requests)
	}
}

func TestAgentLoopPersistsResponsesReasoningEvidenceAndReplaysProviderTurn(t *testing.T) {
	reasoning := model.ProviderItem{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`)}
	functionCall := model.ProviderItem{Ordinal: 1, Type: "function_call", CallID: "provider-call", Payload: json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"provider-call","name":"fixture","arguments":"{\"query\":\"paper\"}"}`)}
	first := []fake.Step{
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &reasoning}},
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &functionCall}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, first, second)
	loop.models = protocolResolver{model: provider, protocol: modelcap.ProtocolOpenAIResponses}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	if !state.run.ReasoningObserved || state.run.ReasoningSignatureObserved {
		t.Fatalf("reasoning evidence = %#v", state.run)
	}
	if len(state.providerTurns) != 1 || len(state.providerTurns[0].Items) != 2 {
		t.Fatalf("provider turns = %#v", state.providerTurns)
	}
	state.mu.Unlock()
	requests := provider.Requests()
	if len(requests) != 2 || len(requests[1].ProviderTurns) != 1 || len(requests[1].ProviderTurns[0].ToolResults) != 1 || requests[1].ProviderTurns[0].ToolResults[0].ToolCallID != "provider-call" {
		t.Fatalf("replayed request = %#v", requests)
	}
}

func TestAgentLoopRejectsResponsesToolCallWithoutProviderState(t *testing.T) {
	first := []fake.Step{
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	loop, state, provider := newLoopFixture(t, nil, first)
	loop.models = protocolResolver{model: provider, protocol: modelcap.ProtocolOpenAIResponses}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.ErrorCode != "MODEL_PROTOCOL_STATE_MISSING" || len(state.calls) != 0 {
		t.Fatalf("state = run:%#v calls:%#v", state.run, state.calls)
	}
}

func TestAgentLoopPersistsReportedCacheUsage(t *testing.T) {
	usage := model.Usage{InputTokens: 120, FreshInputTokens: 28, OutputTokens: 18, ReasoningTokens: 7, CachedInputTokens: 80, CacheWriteTokens: 12, CacheDetailsReported: true}
	script := []fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &usage}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, _ := newLoopFixture(t, nil, script)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.InputTokens != 120 || state.run.FreshInputTokens != 28 || state.run.OutputTokens != 18 || state.run.ReasoningTokens != 7 || !state.run.ReasoningObserved || state.run.CachedInputTokens != 80 || state.run.CacheWriteTokens != 12 || state.run.CacheReportedTurns != 1 || state.run.CacheReportedFreshInputTokens != 28 || state.run.CacheHitTurns != 1 {
		t.Fatalf("cache usage run = %#v", state.run)
	}
}

func TestAgentLoopPassesRequestedAndResolvedReasoning(t *testing.T) {
	script := []fake.Step{{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, script)
	state.mu.Lock()
	state.run.RequestedReasoningLevel = modelcap.ReasoningMax
	state.mu.Unlock()
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	requests := provider.Requests()
	if len(requests) != 1 || requests[0].RequestedReasoningLevel != modelcap.ReasoningMax || requests[0].ResolvedReasoningLevel != modelcap.ReasoningMax {
		t.Fatalf("reasoning request = %#v", requests)
	}
}

func TestAgentLoopPersistsProviderNegotiatedReasoning(t *testing.T) {
	script := []fake.Step{{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, script)
	state.mu.Lock()
	state.run.RequestedReasoningLevel = modelcap.ReasoningMax
	state.mu.Unlock()
	loop.models = fakeResolver{model: resolutionModel{inner: provider, resolved: modelcap.ReasoningXHigh}}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.ResolvedReasoningLevel != modelcap.ReasoningXHigh {
		t.Fatalf("resolved reasoning = %q", state.run.ResolvedReasoningLevel)
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

func TestAgentLoopSkillInstructionsDoNotBypassToolApproval(t *testing.T) {
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	loop, state, provider := newLoopFixture(t, nil, first)
	service := loop.tools.(*tool.Service)
	loop.approvals = statefulAskCoordinator{service, state}
	contexts := &staticRunSkillContexts{value: agentSkillContext("run")}
	loop.skillContexts = contexts
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeWaitingApproval {
		t.Fatalf("outcome = %s", outcome)
	}
	if contexts.calls != 1 {
		t.Fatalf("Skill snapshot loads = %d", contexts.calls)
	}
	requests := provider.Requests()
	if len(requests) != 1 || len(requests[0].Messages) < 4 || !strings.Contains(requests[0].Messages[2].Content, "inspect the evidence") {
		t.Fatalf("Skill request = %#v", requests)
	}
	calls, _ := service.ListByRun(context.Background(), "run")
	if len(calls) != 1 || calls[0].Status != tool.CallAwaitingApproval || calls[0].Result != nil {
		t.Fatalf("Skill permission bypassed approval: %#v", calls)
	}
}

func TestAgentLoopSQLiteApprovalResumeReusesImmutableSkillSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "agent-skill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	projectService := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	projectValue, err := projectService.Create(ctx, "Skill approval", "")
	if err != nil {
		t.Fatal(err)
	}
	conversationRepository := sqlite.NewConversationRepository(store.DB())
	conversationValue, err := conversation.NewService(conversationRepository).Create(ctx, projectValue.ID, "Skill snapshot")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO model_profiles(id,name,provider_type,base_url,model_id,secret_ref,timeout_seconds,custom_headers_json,enabled,is_default,created_at,updated_at) VALUES ('profile','fixture','openai_compatible','https://example.test/v1','model','secret',60,'{}',1,1,?,?)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "sqlite-skill-run", ConversationID: conversationValue.ID, UserMessageID: "sqlite-skill-user", AssistantMessageID: "sqlite-skill-assistant", ModelProfileID: "profile", ModelID: "model", ContextWindowTokens: 200_000, PermissionMode: conversation.PermissionPlan, Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: conversationValue.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "sqlite-user-part", MessageID: run.UserMessageID, Type: "text", Text: "$approval-skill inspect evidence", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: conversationValue.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{ID: "sqlite-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	runRepository := sqlite.NewRunRepository(store.DB())
	if err := runRepository.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}

	registry := tool.NewRegistry()
	implementation := fixtureTool{definition: tool.Definition{QualifiedName: "builtin.fixture", Description: "fixture", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string"}}}`), Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{}, Idempotent: true, Version: "1"}, invoke: func(tool.Invocation) tool.Result {
		return tool.Result{Status: tool.ResultSuccess, Text: "evidence loaded"}
	}}
	if err := registry.Register(ctx, implementation); err != nil {
		t.Fatal(err)
	}
	skillRepository := sqlite.NewSkillRepository(store.DB())
	skillService := appskill.NewService(skillRepository, skillpkg.NewCatalog(filepath.Join(root, "skills")), registrySkillTools{registry}, "0.3.0-dev")
	packageStore := skillpkg.NewFilePackageStore(filepath.Join(root, "skills"), filepath.Join(root, "skill-staging"), filepath.Join(root, "skill-backups"))
	if err := skillService.SetPackageStore(packageStore); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `schema_version: 1
id: approval-skill
name: Approval Skill
version: 1.0.0
description: Verify immutable approval resume
entry: SKILL.md
activation:
  mode: explicit
requires:
  tools: []
  optional_tools: []
permissions: [destructive]
compatibility:
  sciaide: ">=0.2.0 <1.0.0"
context:
  max_tokens: 2000
`
	const originalInstructions = "ORIGINAL SKILL: inspect evidence before using the tool."
	if err := os.WriteFile(filepath.Join(source, "skill.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(originalInstructions), 0o600); err != nil {
		t.Fatal(err)
	}
	installed, err := skillService.Install(ctx, appskill.InstallCommand{SourcePath: source, SourceKind: appskill.SourceFolder})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skillService.SetProjectSkill(ctx, appskill.SetProjectSkillCommand{ProjectID: projectValue.ID, SkillID: "approval-skill", Version: "1.0.0", Enabled: true, Priority: 10}); err != nil {
		t.Fatal(err)
	}

	toolService := tool.NewService(sqlite.NewToolRepository(store.DB()), tool.JSONSchemaValidator{})
	permissionRepository := sqlite.NewPermissionRepository(store.DB())
	coordinator := permission.NewCoordinator(permission.NewEngine(permissionRepository), toolService, runRepository)
	executor := tool.NewExecutor(registry, toolService, runRepository, tool.ExecutorOptions{})
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	provider := fake.New(first, second)
	loop := NewLoop(runRepository, conversationRepository, toolService, registry, coordinator, executionAdapter{executor}, fakeResolver{provider}, nil, Options{SkillContexts: skillService})
	if outcome := loop.Run(ctx, run.ID); outcome != OutcomeWaitingApproval {
		t.Fatalf("initial outcome = %s", outcome)
	}
	before, err := skillRepository.GetRunContext(ctx, run.ID)
	if err != nil || len(before.Skills) != 1 || before.Skills[0].Instructions != originalInstructions {
		t.Fatalf("initial Skill snapshot = %#v, %v", before, err)
	}
	pending, err := permissionRepository.ListPendingApprovals(ctx, run.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending approvals = %#v, %v", pending, err)
	}
	if _, err := skillService.Uninstall(ctx, appskill.UninstallCommand{SkillID: installed.Skill.Manifest.ID, Version: installed.Skill.Manifest.Version, RemoveProjectLinks: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Resolve(ctx, permission.ResolveCommand{ApprovalID: pending[0].ID, Allow: true, Scope: permission.ScopeCall}); err != nil {
		t.Fatal(err)
	}
	if outcome := loop.Resume(ctx, run.ID); outcome != OutcomeCompleted {
		t.Fatalf("resume outcome = %s", outcome)
	}
	after, err := skillRepository.GetRunContext(ctx, run.ID)
	if err != nil || after.SnapshotHash != before.SnapshotHash || after.Skills[0].Instructions != originalInstructions {
		t.Fatalf("resumed Skill snapshot = %#v, %v", after, err)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d", len(requests))
	}
	for index, request := range requests {
		found := false
		for _, message := range request.Messages {
			found = found || strings.Contains(message.Content, originalInstructions)
		}
		if !found {
			t.Fatalf("request %d did not reuse snapshotted Skill: %#v", index, request.Messages)
		}
	}
}

func TestAgentLoopReusesOneSkillSnapshotAcrossToolTurns(t *testing.T) {
	first := []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, _, provider := newLoopFixture(t, nil, first, second)
	contexts := &staticRunSkillContexts{value: agentSkillContext("run")}
	loop.skillContexts = contexts
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	requests := provider.Requests()
	if contexts.calls != 1 || len(requests) != 2 {
		t.Fatalf("snapshot calls=%d requests=%d", contexts.calls, len(requests))
	}
	for index, request := range requests {
		if len(request.Messages) < 3 || request.Messages[2].Content != requests[0].Messages[2].Content || !strings.Contains(request.Messages[2].Content, "inspect the evidence") {
			t.Fatalf("request %d lost immutable Skill context: %#v", index, request.Messages)
		}
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
	baseTokens := len([]rune(fixedSystemRules))
	builder := NewContextBuilder(baseTokens + 5 + len([]rune("fixture")) + len([]rune(`{}`)) + 20)
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

func TestContextBuilderInjectsOnlySnapshottedSkillBodiesAtUserPriority(t *testing.T) {
	value := agentSkillContext("run")
	request, _, err := NewContextBuilder(20_000).BuildWithSkillContext(context.Background(), []conversation.Message{{ID: "user", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "$research-review"}}}}, "", "user", nil, nil, value)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 4 || request.Messages[0].Role != model.RoleSystem || request.Messages[1].Role != model.RoleUser || request.Messages[2].Role != model.RoleUser || request.Messages[3].Content != "$research-review" {
		t.Fatalf("Skill message order = %#v", request.Messages)
	}
	if strings.Contains(request.Messages[0].Content, value.Skills[0].Instructions) || !strings.Contains(request.Messages[2].Content, value.Skills[0].Instructions) {
		t.Fatalf("Skill body priority = %#v", request.Messages)
	}
	value.Skills = []appskill.RunSkill{}
	request, _, err = NewContextBuilder(20_000).BuildWithSkillContext(context.Background(), nil, "", "", nil, nil, value)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range request.Messages {
		if strings.Contains(message.Content, "inspect the evidence") {
			t.Fatalf("unselected Skill body was injected: %#v", request.Messages)
		}
	}
}

func TestContextBuilderPlacesSelectedSkillAtCurrentTurnAfterCompactedHistory(t *testing.T) {
	value := agentSkillContext("run")
	messages := []conversation.Message{
		{ID: "very-old", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat("x", 30_000)}}},
		{ID: "previous-user", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "previous question"}}},
		{ID: "previous-assistant", Role: conversation.RoleAssistant, Parts: []conversation.MessagePart{{Type: "text", Text: "previous answer"}}},
		{ID: "current-user", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "$research-review current question"}}},
		{ID: "assistant-placeholder", Role: conversation.RoleAssistant, Parts: []conversation.MessagePart{{Type: "text"}}},
	}
	request, info, err := NewContextBuilder(10_000).BuildWithSkillContext(context.Background(), messages, "assistant-placeholder", "current-user", nil, nil, value)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Compacted {
		t.Fatal("old history was not reported as compacted")
	}
	positions := map[string]int{}
	for index, message := range request.Messages {
		switch {
		case message.Content == value.CatalogText:
			positions["catalog"] = index
		case message.Content == "previous question":
			positions["history-user"] = index
		case message.Content == "previous answer":
			positions["history-assistant"] = index
		case strings.Contains(message.Content, value.Skills[0].Instructions):
			positions["skill"] = index
		case message.Content == "$research-review current question":
			positions["current"] = index
		}
		if message.Content == strings.Repeat("x", 30_000) {
			t.Fatal("compacted history remained in request")
		}
	}
	if !(positions["catalog"] < positions["history-user"] && positions["history-user"] < positions["history-assistant"] && positions["history-assistant"] < positions["skill"] && positions["skill"] < positions["current"]) {
		t.Fatalf("current-turn Skill order is wrong: positions=%v messages=%#v", positions, request.Messages)
	}
}

func TestContextBuilderAttachesToolResultToProviderTurnWithoutDuplicateToolMessage(t *testing.T) {
	builder := NewContextBuilder(10_000)
	calls := []tool.Call{{ProviderCallID: "call_1", ToolName: "fixture", Arguments: json.RawMessage(`{"query":"paper"}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: "paper"}}}
	turn := model.ProviderTurn{TurnIndex: 1, Protocol: modelcap.ProtocolAnthropic, Items: []model.ProviderItem{
		{Ordinal: 0, Type: "thinking", Payload: json.RawMessage(`{"type":"thinking","thinking":"inspect","signature":"signed"}`)},
		{Ordinal: 1, Type: "tool_use", CallID: "call_1", Payload: json.RawMessage(`{"type":"tool_use","id":"call_1","name":"fixture","input":{"query":"paper"}}`)},
	}}
	request, info, err := builder.BuildWithInfo(context.Background(), []conversation.Message{{ID: "user", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "read"}}}}, "", nil, calls, turn)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.ProviderTurns) != 1 || len(request.ProviderTurns[0].ToolResults) != 1 || request.ProviderTurns[0].ToolResults[0].ToolCallID != "call_1" {
		t.Fatalf("provider turns = %#v", request.ProviderTurns)
	}
	for _, message := range request.Messages {
		if message.Role == model.RoleTool || len(message.ToolCalls) > 0 {
			t.Fatalf("provider tool state was duplicated in normalized messages: %#v", request.Messages)
		}
	}
	if info.Compacted {
		t.Fatalf("context unexpectedly compacted: %#v", info)
	}
}

func TestContextBuilderDefaultsTo200KCompactionWindow(t *testing.T) {
	builder := NewContextBuilder(0)
	if builder.maxChars != 200_000 {
		t.Fatalf("default context window = %d", builder.maxChars)
	}
	old := strings.Repeat("a", 150_000)
	latest := strings.Repeat("b", 100_000)
	request, info, err := builder.BuildWithInfo(context.Background(), []conversation.Message{
		{ID: "old", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: old}}},
		{ID: "latest", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: latest}}},
	}, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 2 || request.Messages[1].Content != latest {
		t.Fatalf("compacted request contains %d messages", len(request.Messages))
	}
	if !info.Compacted || info.EstimatedTokens < len(latest) {
		t.Fatalf("context info = %#v", info)
	}
}

func TestLoopPersistsCheckpointBeforeReplacingOldConversationHistory(t *testing.T) {
	summaryScript := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "# Research state\n- Objective: retain the verified baseline."}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	answerScript := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "continued"}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	loop, state, provider := newLoopFixture(t, nil, summaryScript, answerScript)
	oldUser := strings.Repeat("old-user-", 140)
	oldAssistant := strings.Repeat("old-assistant-", 100)
	recentUser := strings.Repeat("recent-user-", 110)
	recentAssistant := strings.Repeat("recent-assistant-", 80)
	state.run.UserMessageID = "current-user"
	state.messages = []conversation.Message{
		{ID: "old-user", RunID: "old-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: oldUser}}},
		{ID: "old-assistant", RunID: "old-run", Role: conversation.RoleAssistant, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: oldAssistant}}},
		{ID: "recent-user", RunID: "recent-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: recentUser}}},
		{ID: "recent-assistant", RunID: "recent-run", Role: conversation.RoleAssistant, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: recentAssistant}}},
		{ID: "current-user", RunID: "run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "continue the analysis"}}},
		{ID: "assistant", RunID: "run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{Type: "text"}}},
	}
	repository := &memoryCheckpointRepository{}
	loop.checkpoints = contextmemory.NewService(repository)
	loop.models = budgetResolver{model: provider, budget: modelcap.ResolveContextBudget(5_000, 0, modelcap.ContextWindowSourceManual)}
	if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
	}
	if repository.latest.ThroughMessageID != "old-assistant" || !strings.Contains(repository.latest.Summary, "verified baseline") {
		t.Fatalf("checkpoint = %#v", repository.latest)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want compaction + answer", len(requests))
	}
	encoded, _ := json.Marshal(requests[1])
	if !strings.Contains(string(encoded), "untrusted_conversation_checkpoint") || strings.Contains(string(encoded), oldUser) || !strings.Contains(string(encoded), recentUser) {
		t.Fatalf("replacement request = %s", encoded)
	}
	if state.run.ModelTurns != 2 || !state.run.ContextCompacted {
		t.Fatalf("run compaction audit = %#v", state.run)
	}
}

func TestContextBuilderBoundsCumulativeToolResults(t *testing.T) {
	baseTokens := len([]rune(fixedSystemRules))
	builder := NewContextBuilder(baseTokens + len([]rune("fixture")) + len([]rune(`{}`)) + 20)
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

func TestContextBuilderCompactsOnlyWholeProviderTurns(t *testing.T) {
	oldPayload := json.RawMessage(`{"type":"tool_use","id":"old","name":"fixture","input":{}}`)
	newReasoning := json.RawMessage(`{"type":"thinking","thinking":"inspect","signature":"signed"}`)
	newTool := json.RawMessage(`{"type":"tool_use","id":"new","name":"fixture","input":{}}`)
	latest := "read"
	baseTokens := len([]rune(fixedSystemRules))
	newNativeTokens := len([]rune(string(newReasoning))) + len([]rune(string(newTool)))
	builder := NewContextBuilder(baseTokens + len([]rune(latest)) + newNativeTokens + 20)
	calls := []tool.Call{
		{ProviderCallID: "old", ToolName: "fixture", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("old", 30)}},
		{ProviderCallID: "new", ToolName: "fixture", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("new", 30)}},
	}
	turns := []model.ProviderTurn{
		{TurnIndex: 1, Protocol: modelcap.ProtocolAnthropic, Items: []model.ProviderItem{{Ordinal: 0, Type: "tool_use", CallID: "old", Payload: oldPayload}}},
		{TurnIndex: 2, Protocol: modelcap.ProtocolAnthropic, Items: []model.ProviderItem{{Ordinal: 0, Type: "thinking", Payload: newReasoning}, {Ordinal: 1, Type: "tool_use", CallID: "new", Payload: newTool}}},
	}
	request, info, err := builder.BuildWithInfo(context.Background(), []conversation.Message{{ID: "latest", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: latest}}}}, "", nil, calls, turns...)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.ProviderTurns) != 1 || request.ProviderTurns[0].TurnIndex != 2 || len(request.ProviderTurns[0].Items) != 2 || len(request.ProviderTurns[0].ToolResults) != 1 || request.ProviderTurns[0].ToolResults[0].ToolCallID != "new" {
		t.Fatalf("provider suffix = %#v", request.ProviderTurns)
	}
	for _, message := range request.Messages {
		if message.Role == model.RoleTool || len(message.ToolCalls) > 0 {
			t.Fatalf("dropped provider turn leaked into normalized messages: %#v", request.Messages)
		}
	}
	if !info.Compacted || info.EstimatedTokens > builder.maxChars {
		t.Fatalf("context info = %#v", info)
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

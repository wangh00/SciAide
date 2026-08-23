package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/multimodal"
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
	runSteps      []chat.RunStep
	requestUsage  map[int]chat.RequestUsage
	auxUsage      map[string]chat.RequestUsage
}

type faultInjectingRuns struct {
	*loopState
	failJournal  bool
	failUsage    bool
	failRunStep  bool
	failProvider bool
}

type faultInjectingConversations struct {
	*loopState
	failUpdate bool
}

func (s *faultInjectingConversations) UpdateMessageText(context.Context, string, conversation.MessageStatus, string, time.Time) error {
	if s.failUpdate {
		return fmt.Errorf("injected message update failure")
	}
	return nil
}

type completionListFailureTools struct {
	ToolCalls
	failAt    int
	listCalls int
}

func (s *completionListFailureTools) ListByRun(ctx context.Context, runID string) ([]tool.Call, error) {
	s.listCalls++
	if s.listCalls == s.failAt {
		return nil, fmt.Errorf("injected completion citation load failure")
	}
	return s.ToolCalls.ListByRun(ctx, runID)
}

func (s *faultInjectingRuns) BeginModelTurn(context.Context, string, int, time.Time) error {
	if s.failJournal {
		return fmt.Errorf("injected journal failure")
	}
	return nil
}

func (s *faultInjectingRuns) UpdateModelTurnDraft(context.Context, string, int, string, int, time.Time) error {
	if s.failJournal {
		return fmt.Errorf("injected journal failure")
	}
	return nil
}

func (s *faultInjectingRuns) FinishModelTurn(context.Context, string, int, chat.ModelTurnStatus, string, int, time.Time) error {
	if s.failJournal {
		return fmt.Errorf("injected journal failure")
	}
	return nil
}

func (s *faultInjectingRuns) RecordModelUsage(ctx context.Context, value chat.RequestUsage) (chat.Run, bool, error) {
	if s.failUsage {
		return chat.Run{}, false, fmt.Errorf("injected usage failure")
	}
	return s.loopState.RecordModelUsage(ctx, value)
}

func (s *faultInjectingRuns) SaveRunStep(ctx context.Context, step chat.RunStep) error {
	if s.failRunStep {
		return fmt.Errorf("injected run step failure")
	}
	return s.loopState.SaveRunStep(ctx, step)
}

func (s *faultInjectingRuns) SaveProviderTurn(ctx context.Context, runID string, turn model.ProviderTurn, at time.Time) error {
	if s.failProvider {
		return fmt.Errorf("injected provider turn failure")
	}
	return s.loopState.SaveProviderTurn(ctx, runID, turn, at)
}

func (s *loopState) Get(context.Context, string) (chat.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run, nil
}
func (s *loopState) LatestForConversation(_ context.Context, conversationID string) (chat.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run, s.run.ID != "" && s.run.ConversationID == conversationID, nil
}
func (s *loopState) Update(_ context.Context, run chat.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = run
	return nil
}
func (s *loopState) RecordCompaction(_ context.Context, run chat.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.run.Status {
	case chat.RunCompleted, chat.RunFailed, chat.RunCancelled, chat.RunInterrupted:
	default:
		return fmt.Errorf("compaction accounting requires a terminal run")
	}
	if run.ID != s.run.ID || run.Status != s.run.Status || !run.ContextCompacted {
		return fmt.Errorf("invalid compaction accounting")
	}
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
func (s *loopState) IncrementModelTurns(_ context.Context, _ string, at time.Time) (chat.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.Status != chat.RunRunning {
		return chat.Run{}, fmt.Errorf("run is not running")
	}
	s.run.ModelTurns++
	s.run.UpdatedAt = at
	return s.run, nil
}
func (s *loopState) RecordModelUsage(_ context.Context, value chat.RequestUsage) (chat.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.RunID != s.run.ID || value.TurnIndex <= 0 {
		return chat.Run{}, false, fmt.Errorf("invalid request usage")
	}
	auxiliary := value.RequestKind == "image_probe" || value.RequestKind == "multimodal_fallback"
	if auxiliary && s.auxUsage == nil {
		s.auxUsage = make(map[string]chat.RequestUsage)
	}
	if !auxiliary && s.requestUsage == nil {
		s.requestUsage = make(map[int]chat.RequestUsage)
	}
	var existing chat.RequestUsage
	var ok bool
	if auxiliary {
		existing, ok = s.auxUsage[value.ID]
	} else {
		existing, ok = s.requestUsage[value.TurnIndex]
	}
	if ok {
		if !reflect.DeepEqual(existing, value) {
			return chat.Run{}, false, fmt.Errorf("conflicting request usage")
		}
		return s.run, false, nil
	}
	if auxiliary {
		s.auxUsage[value.ID] = value
	} else {
		s.requestUsage[value.TurnIndex] = value
	}
	if value.StatusCode >= 200 && value.StatusCode < 300 {
		applyUsage(&s.run, model.Usage{
			InputTokens: value.InputTokens, FreshInputTokens: value.FreshInputTokens, OutputTokens: value.OutputTokens,
			ReasoningTokens: value.ReasoningTokens, CachedInputTokens: value.CachedInputTokens,
			CacheWriteTokens: value.CacheWriteTokens, CacheDetailsReported: value.CacheDetailsReported,
		})
	}
	if s.run.ModelTurns < value.TurnIndex {
		s.run.ModelTurns = value.TurnIndex
	}
	s.run.UpdatedAt = value.CompletedAt
	return s.run, true, nil
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
func (s *loopState) SaveRunStep(_ context.Context, step chat.RunStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.runSteps {
		if existing.RunID == step.RunID && existing.TurnIndex == step.TurnIndex {
			if existing.Commentary == step.Commentary && existing.ReasoningSummary == step.ReasoningSummary {
				return nil
			}
			return fmt.Errorf("conflicting run step")
		}
	}
	s.runSteps = append(s.runSteps, step)
	return nil
}
func (s *loopState) ListRunSteps(context.Context, string) ([]chat.RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]chat.RunStep(nil), s.runSteps...), nil
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

type resolutionModel struct {
	inner    model.ChatModel
	resolved modelcap.ReasoningLevel
}

type openingFailureModel struct {
	mu        sync.Mutex
	inner     model.ChatModel
	remaining int
	err       error
	attempts  int
}

func (m *openingFailureModel) Capabilities(ctx context.Context) (model.Capabilities, error) {
	return m.inner.Capabilities(ctx)
}

func (m *openingFailureModel) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	m.mu.Lock()
	m.attempts++
	if m.remaining > 0 {
		m.remaining--
		err := m.err
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Unlock()
	return m.inner.Stream(ctx, request)
}

func (m *openingFailureModel) Attempts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attempts
}

type retryCaptureObserver struct {
	NopObserver
	retrying chan RetryStatus
}

func (o *retryCaptureObserver) Retrying(_ chat.Run, retry RetryStatus) {
	select {
	case o.retrying <- retry:
	default:
	}
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

type imageResolverFixture struct {
	part         model.ContentPart
	attachmentID string
}

func (f *imageResolverFixture) ResolveImage(_ context.Context, projectID, attachmentID string) (model.ContentPart, error) {
	if projectID != "project" {
		return model.ContentPart{}, fmt.Errorf("unexpected project %q", projectID)
	}
	f.attachmentID = attachmentID
	return f.part, nil
}

type multimodalFallbackFixture struct {
	state       multimodal.Capability
	result      multimodal.Result
	calls       int
	images      []model.ContentPart
	unsupported bool
	analyzeErr  error
}

func (f *multimodalFallbackFixture) Capability(string, string, modelcap.APIProtocol) multimodal.Capability {
	return f.state
}
func (f *multimodalFallbackFixture) MarkSupported(string, string, modelcap.APIProtocol) {
	f.state = multimodal.CapabilitySupported
}
func (f *multimodalFallbackFixture) MarkUnsupported(string, string, modelcap.APIProtocol) {
	f.state, f.unsupported = multimodal.CapabilityUnsupported, true
}
func (f *multimodalFallbackFixture) Analyze(_ context.Context, prompt string, images []model.ContentPart) (multimodal.Result, error) {
	if prompt != "读取论文" {
		return multimodal.Result{}, fmt.Errorf("unexpected fallback request")
	}
	f.calls++
	f.images = append([]model.ContentPart(nil), images...)
	if f.analyzeErr != nil {
		return multimodal.Result{}, f.analyzeErr
	}
	return f.result, nil
}

type rejectImageOnceModel struct {
	inner    model.ChatModel
	requests []model.ChatRequest
}

func (m *rejectImageOnceModel) Capabilities(ctx context.Context) (model.Capabilities, error) {
	return m.inner.Capabilities(ctx)
}
func (m *rejectImageOnceModel) Stream(ctx context.Context, request model.ChatRequest) (model.Stream, error) {
	m.requests = append(m.requests, request)
	if len(m.requests) == 1 {
		return nil, &apperr.Error{Code: "MODEL_REQUEST_REJECTED", UserMessage: "Model only supports text input; received unsupported content type 'image_url'.", HTTPStatus: 400}
	}
	return m.inner.Stream(ctx, request)
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

func TestLoopFallsBackAfterExplicitImageRejectionAndPersistsDisclosure(t *testing.T) {
	script := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "图片中的人物有蓝白色头发。"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, script)
	payload, err := json.Marshal(map[string]any{"attachmentId": "image-1", "format": "image"})
	if err != nil {
		t.Fatal(err)
	}
	state.messages[0].Parts = append(state.messages[0].Parts, conversation.MessagePart{Type: "media", Payload: payload})
	imageResolver := &imageResolverFixture{part: model.ContentPart{Type: "input_image", MediaType: "image/jpeg", Data: "AQID", Name: "figure.jpg", AttachmentID: "image-1"}}
	now := time.Now().UTC()
	fallback := &multimodalFallbackFixture{state: multimodal.CapabilityUnknown, result: multimodal.Result{Text: "蓝白色头发，黑色蝴蝶结", ProfileID: "vision-profile", ProfileName: "custom", ModelID: "custom-vision-model", Protocol: modelcap.ProtocolOpenAIChat, StartedAt: now, CompletedAt: now.Add(time.Second)}}
	primary := &rejectImageOnceModel{inner: provider}
	loop.models = protocolResolver{model: primary, protocol: modelcap.ProtocolOpenAIChat}
	loop.images = imageResolver
	loop.multimodal = fallback
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, run = %#v, primary requests = %#v, fallback calls = %d", outcome, state.run, primary.requests, fallback.calls)
	}
	if len(primary.requests) != 2 {
		t.Fatalf("primary request count = %d", len(primary.requests))
	}
	secondRequest := primary.requests[1]
	latestMessage := secondRequest.Messages[len(secondRequest.Messages)-1].Content
	if !requestHasImages(primary.requests[0]) || requestHasImages(secondRequest) || !strings.Contains(latestMessage, "<visual_context>") || !strings.Contains(latestMessage, fallback.result.Text) || strings.Contains(latestMessage, "vision_fallback") || strings.Contains(latestMessage, "untrusted_image_description") || strings.Contains(latestMessage, "untrusted image-derived data") || strings.Contains(latestMessage, "image pixels are supplied") {
		t.Fatalf("primary requests = %#v", primary.requests)
	}
	if !strings.Contains(secondRequest.Messages[0].Content, "answer the user's request directly") || !strings.Contains(secondRequest.Messages[0].Content, "Do not add caveats") {
		t.Fatalf("fallback system rule = %q", secondRequest.Messages[0].Content)
	}
	if imageResolver.attachmentID != "image-1" || fallback.calls != 1 || len(fallback.images) != 1 || !fallback.unsupported {
		t.Fatalf("fallback routing = attachment:%q calls:%d images:%#v unsupported:%v", imageResolver.attachmentID, fallback.calls, fallback.images, fallback.unsupported)
	}
	if len(state.runSteps) != 1 || !strings.Contains(state.runSteps[0].Commentary, "[识图兜底]") || !strings.Contains(state.runSteps[0].Commentary, "custom · custom-vision-model") {
		t.Fatalf("fallback disclosure = %#v", state.runSteps)
	}
	if len(state.requestUsage) != 1 || state.requestUsage[1].RequestKind != "conversation" || len(state.auxUsage) != 2 || state.auxUsage["run:1:image_probe"].StatusCode != 400 || state.auxUsage["run:1:multimodal_fallback"].ModelID != "custom-vision-model" {
		t.Fatalf("request accounting = main:%#v auxiliary:%#v", state.requestUsage, state.auxUsage)
	}
	if got := state.messages[1].Parts[0].Text; got != "图片中的人物有蓝白色头发。" {
		t.Fatalf("assistant answer = %q", got)
	}
	if cached := loop.multimodalResult("run"); cached != nil {
		t.Fatalf("terminal Run retained multimodal cache: %#v", cached)
	}
}

func TestLoopFallsBackAfterSuccessfulUnsupportedImagePlaceholder(t *testing.T) {
	placeholder := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "I received [Unsupported Image] and cannot inspect pixels."}},
		{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 10, FreshInputTokens: 10, OutputTokens: 5}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	answer := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "图片中的人物有蓝白色头发。"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, placeholder, answer)
	payload, err := json.Marshal(map[string]any{"attachmentId": "image-1", "format": "image"})
	if err != nil {
		t.Fatal(err)
	}
	state.messages[0].Parts = append(state.messages[0].Parts, conversation.MessagePart{Type: "media", Payload: payload})
	loop.images = &imageResolverFixture{part: model.ContentPart{Type: "input_image", MediaType: "image/webp", Data: "AQID", Name: "figure.jpg", AttachmentID: "image-1"}}
	now := time.Now().UTC()
	fallback := &multimodalFallbackFixture{state: multimodal.CapabilityUnknown, result: multimodal.Result{Text: "蓝白色头发，黑色蝴蝶结", ProfileID: "vision-profile", ProfileName: "custom", ModelID: "custom-vision-model", Protocol: modelcap.ProtocolOpenAIChat, StartedAt: now, CompletedAt: now.Add(time.Second)}}
	loop.multimodal = fallback
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
	}
	requests := provider.Requests()
	if len(requests) != 2 || !requestHasImages(requests[0]) || requestHasImages(requests[1]) || !strings.Contains(requests[1].Messages[len(requests[1].Messages)-1].Content, fallback.result.Text) {
		t.Fatalf("requests = %#v", requests)
	}
	if fallback.calls != 1 || !fallback.unsupported || state.messages[1].Parts[0].Text != "图片中的人物有蓝白色头发。" {
		t.Fatalf("fallback = %#v, assistant = %#v", fallback, state.messages[1])
	}
	probe := state.auxUsage["run:1:image_probe"]
	if probe.StatusCode != 200 || probe.InputTokens != 10 || probe.OutputTokens != 5 || state.auxUsage["run:1:multimodal_fallback"].ModelID != "custom-vision-model" {
		t.Fatalf("auxiliary usage = %#v", state.auxUsage)
	}
}

func TestLoopDoesNotRecordConversationFailureWhenFallbackFailsBeforeRetry(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	payload, err := json.Marshal(map[string]any{"attachmentId": "image-1", "format": "image"})
	if err != nil {
		t.Fatal(err)
	}
	state.messages[0].Parts = append(state.messages[0].Parts, conversation.MessagePart{Type: "media", Payload: payload})
	loop.images = &imageResolverFixture{part: model.ContentPart{Type: "input_image", MediaType: "image/jpeg", Data: "AQID", Name: "figure.jpg", AttachmentID: "image-1"}}
	loop.multimodal = &multimodalFallbackFixture{analyzeErr: fmt.Errorf("all channels failed")}
	loop.models = protocolResolver{model: &rejectImageOnceModel{inner: provider}, protocol: modelcap.ProtocolOpenAIChat}

	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("outcome = %s", outcome)
	}
	if len(state.requestUsage) != 0 {
		t.Fatalf("fallback failure was recorded as a conversation request: %#v", state.requestUsage)
	}
	if len(state.auxUsage) != 1 || state.auxUsage["run:1:image_probe"].StatusCode != 400 {
		t.Fatalf("image probe accounting = %#v", state.auxUsage)
	}
}

func TestVisionFallbackFailureExplainsTextModelAndUnavailableChannels(t *testing.T) {
	fallback := &multimodalFallbackFixture{analyzeErr: fmt.Errorf("all channels failed")}
	loop := &Loop{multimodal: fallback, fallbackByRun: map[string]multimodal.Result{}}
	_, err := loop.runMultimodalFallback(context.Background(), &chat.Run{ID: "run", ModelProfileID: "profile", ModelID: "model"}, "读取论文", []model.ContentPart{{Type: "input_image", MediaType: "image/png", Data: "AQID"}})
	public := apperr.Public(err)
	if public.Code != "MULTIMODAL_FALLBACK_FAILED" || !strings.Contains(public.Message, "当前模型仅支持文本") || !strings.Contains(public.Message, "识图兜底渠道均不可用") || !strings.Contains(public.Message, "模型与 API") {
		t.Fatalf("public fallback failure = %#v", public)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback calls = %d", fallback.calls)
	}
}

func TestVisionFallbackWithoutCustomChannelPromptsConfiguration(t *testing.T) {
	fallback := &multimodalFallbackFixture{analyzeErr: multimodal.ErrNoEnabledChannels}
	loop := &Loop{multimodal: fallback, fallbackByRun: map[string]multimodal.Result{}}
	_, err := loop.runMultimodalFallback(context.Background(), &chat.Run{ID: "run", ModelProfileID: "profile", ModelID: "model"}, "读取论文", []model.ContentPart{{Type: "input_image", MediaType: "image/png", Data: "AQID"}})
	public := apperr.Public(err)
	if public.Code != "MULTIMODAL_FALLBACK_FAILED" || !strings.Contains(public.Message, "尚未配置并启用") || !strings.Contains(public.Message, "添加多模态模型") {
		t.Fatalf("public no-channel failure = %#v", public)
	}
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
		{Event: model.Event{Type: model.EventTextDelta, Text: "我先读取资料。"}},
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	second := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "已读取"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, first, second)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	if state.run.Status != chat.RunCompleted || state.messages[1].Parts[0].Text != "已读取" || len(state.calls) != 1 || len(state.runSteps) != 1 || state.runSteps[0].Commentary != "我先读取资料。" {
		t.Fatalf("state = run:%#v messages:%#v calls:%#v", state.run, state.messages, state.calls)
	}
	state.mu.Unlock()
	requests := provider.Requests()
	if len(requests) != 2 || len(requests[0].Tools) != 1 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].PromptCacheKey != "conversation" || requests[1].PromptCacheKey != "conversation" {
		t.Fatalf("prompt cache keys = %q, %q", requests[0].PromptCacheKey, requests[1].PromptCacheKey)
	}
	last := requests[1].Messages
	if len(last) < 3 || last[len(last)-2].Role != model.RoleAssistant || len(last[len(last)-2].ToolCalls) != 1 || last[len(last)-1].Role != model.RoleTool || last[len(last)-1].ToolCallID != "provider-call" || last[len(last)-1].Content == "" {
		t.Fatalf("tool round trip = %#v", last)
	}
}

func TestAgentLoopReturnsAnswerWhenAuxiliaryPersistenceFails(t *testing.T) {
	usage := model.Usage{InputTokens: 10, FreshInputTokens: 10, OutputTokens: 2, CacheDetailsReported: true}
	first := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "准备读取。"}},
		{Event: model.Event{Type: model.EventUsage, Usage: &usage}},
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	final := []fake.Step{
		{Event: model.Event{Type: model.EventUsage, Usage: &usage}},
		{Event: model.Event{Type: model.EventTextDelta, Text: "最终回答"}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	loop, state, _ := newLoopFixture(t, nil, first, final)
	loop.runs = &faultInjectingRuns{loopState: state, failJournal: true, failUsage: true, failRunStep: true}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.Status != chat.RunCompleted || state.messages[1].Parts[0].Text != "最终回答" || len(state.calls) != 1 {
		t.Fatalf("run = %#v, assistant = %q, calls = %#v", state.run, state.messages[1].Parts[0].Text, state.calls)
	}
	if len(state.runSteps) != 0 || len(state.requestUsage) != 0 {
		t.Fatalf("injected auxiliary writes unexpectedly succeeded: steps=%#v usage=%#v", state.runSteps, state.requestUsage)
	}
}

func TestAgentLoopCompletesWithoutPrewritingFinalTextOrLoadingCitations(t *testing.T) {
	final := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "final answer"}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	loop, state, _ := newLoopFixture(t, nil, final)
	loop.conversations = &faultInjectingConversations{loopState: state, failUpdate: true}
	tools := &completionListFailureTools{ToolCalls: loop.tools, failAt: 3}
	loop.tools = tools

	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if tools.listCalls != 3 || state.run.Status != chat.RunCompleted || state.messages[1].Status != conversation.MessageComplete || state.messages[1].Parts[0].Text != "final answer" {
		t.Fatalf("list calls=%d run=%#v assistant=%#v", tools.listCalls, state.run, state.messages[1])
	}
}

func TestAgentLoopReturnsFinalAnswerWhenFinalProviderAuditCannotBeSaved(t *testing.T) {
	messageItem := model.ProviderItem{Ordinal: 0, ItemID: "msg", Type: "message", Payload: json.RawMessage(`{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"最终回答"}]}`)}
	script := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "最终回答"}},
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &messageItem}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	loop, state, provider := newLoopFixture(t, nil, script)
	loop.runs = &faultInjectingRuns{loopState: state, failProvider: true}
	loop.models = protocolResolver{model: provider, protocol: modelcap.ProtocolOpenAIResponses}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
	}
	if state.messages[1].Parts[0].Text != "最终回答" || len(state.providerTurns) != 0 {
		t.Fatalf("assistant = %q, provider turns = %#v", state.messages[1].Parts[0].Text, state.providerTurns)
	}
}

func TestAgentLoopRequiresProviderStateForToolContinuation(t *testing.T) {
	functionItem := model.ProviderItem{Ordinal: 0, ItemID: "fc", Type: "function_call", CallID: "provider-call", Payload: json.RawMessage(`{"type":"function_call","id":"fc","call_id":"provider-call","name":"fixture","arguments":"{\"query\":\"paper\"}"}`)}
	script := []fake.Step{
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &functionItem}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	loop, state, provider := newLoopFixture(t, nil, script)
	loop.runs = &faultInjectingRuns{loopState: state, failProvider: true}
	loop.models = protocolResolver{model: provider, protocol: modelcap.ProtocolOpenAIResponses}
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("outcome = %s", outcome)
	}
	if state.run.ErrorCode != "MODEL_PROTOCOL_STATE_SAVE_FAILED" || len(state.calls) != 0 {
		t.Fatalf("run = %#v, calls = %#v", state.run, state.calls)
	}
}

func TestAgentLoopReturnsRejectedToolCallToModel(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		arguments json.RawMessage
	}{
		{name: "unknown tool", toolName: "builtin.missing", arguments: json.RawMessage(`{}`)},
		{name: "schema mismatch", toolName: "builtin.fixture", arguments: json.RawMessage(`{}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := []fake.Step{
				{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: test.toolName, Arguments: test.arguments}}},
				{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
			}
			final := []fake.Step{
				{Event: model.Event{Type: model.EventTextDelta, Text: "已改用可用方案。"}},
				{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
			}
			loop, state, provider := newLoopFixture(t, nil, first, final)
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
				t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
			}
			state.mu.Lock()
			if len(state.calls) != 1 {
				state.mu.Unlock()
				t.Fatalf("calls = %#v", state.calls)
			}
			var rejected tool.Call
			for _, rejected = range state.calls {
			}
			assistant := state.messages[1].Parts[0].Text
			state.mu.Unlock()
			if rejected.Status != tool.CallFailed || rejected.ErrorCode != tool.ErrorCodeCallRejected || rejected.Result == nil || rejected.Result.Status != tool.ResultError {
				t.Fatalf("rejected call = %#v", rejected)
			}
			if assistant != "已改用可用方案。" {
				t.Fatalf("assistant = %q", assistant)
			}
			requests := provider.Requests()
			if len(requests) != 2 {
				t.Fatalf("requests = %d", len(requests))
			}
			last := requests[1].Messages
			if len(last) < 2 || last[len(last)-1].Role != model.RoleTool || !strings.Contains(last[len(last)-1].Content, tool.ErrorCodeCallRejected) {
				t.Fatalf("tool rejection was not replayed: %#v", last)
			}
		})
	}
}

func TestAgentLoopRetriesOpeningFailureWithoutAddingModelTurn(t *testing.T) {
	success := fake.New([]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "recovered"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	provider := &openingFailureModel{inner: success, remaining: 2, err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "temporary", Retryable: true}}
	loop, state, _ := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	loop.models = fakeResolver{model: provider}
	loop.retryDelay = func(int) time.Duration { return 0 }
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	if provider.Attempts() != 3 || state.run.ModelTurns != 1 || state.messages[1].Parts[0].Text != "recovered" {
		t.Fatalf("attempts = %d, run = %#v, messages = %#v", provider.Attempts(), state.run, state.messages)
	}
}

func TestAgentLoopDoesNotRetryPermanentOpeningFailure(t *testing.T) {
	provider := &openingFailureModel{inner: fake.New(), remaining: 1, err: &apperr.Error{Code: "MODEL_AUTH_FAILED", UserMessage: "bad key", Retryable: false}}
	loop, state, _ := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	loop.models = fakeResolver{model: provider}
	loop.retryDelay = func(int) time.Duration { return 0 }
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("outcome = %s", outcome)
	}
	if provider.Attempts() != 1 {
		t.Fatalf("attempts = %d, want 1", provider.Attempts())
	}
	if usage := state.requestUsage[1]; len(state.requestUsage) != 1 || usage.StatusCode != 401 || usage.ErrorCode != "MODEL_AUTH_FAILED" || usage.InputTokens != 0 {
		t.Fatalf("failed request outcome = %#v", state.requestUsage)
	}
}

func TestAgentLoopDiscardsInterruptedAttemptBeforeRetry(t *testing.T) {
	failedUsage := model.Usage{InputTokens: 999, FreshInputTokens: 999, OutputTokens: 999, CacheDetailsReported: true}
	successUsage := model.Usage{InputTokens: 11, FreshInputTokens: 7, OutputTokens: 3, CachedInputTokens: 4, CacheDetailsReported: true}
	failedItem := model.ProviderItem{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"id":"discarded"}`)}
	successItem := model.ProviderItem{Ordinal: 0, Type: "function_call", Payload: json.RawMessage(`{"id":"kept"}`)}
	call := model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}
	failed := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "discarded partial answer"}},
		{Event: model.Event{Type: model.EventUsage, Usage: &failedUsage}},
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &failedItem}},
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &call}},
		{Err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "stream interrupted", Retryable: true}},
	}
	successfulToolTurn := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "kept tool commentary"}},
		{Event: model.Event{Type: model.EventUsage, Usage: &successUsage}},
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &successItem}},
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &call}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	final := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "final answer"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, failed, successfulToolTurn, final)
	loop.models = protocolResolver{model: provider, protocol: modelcap.ProtocolOpenAIResponses}
	loop.retryDelay = func(int) time.Duration { return 0 }
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.ModelTurns != 2 || state.run.InputTokens != 11 || state.run.OutputTokens != 3 || state.run.CacheReportedTurns != 1 {
		t.Fatalf("usage or logical turns include failed attempt: %#v", state.run)
	}
	if state.messages[1].Parts[0].Text != "final answer" || strings.Contains(state.messages[1].Parts[0].Text, "discarded") {
		t.Fatalf("assistant answer = %q", state.messages[1].Parts[0].Text)
	}
	if len(state.calls) != 1 || len(state.providerTurns) != 1 || len(state.providerTurns[0].Items) != 1 || !strings.Contains(string(state.providerTurns[0].Items[0].Payload), "kept") {
		t.Fatalf("calls = %#v, provider turns = %#v", state.calls, state.providerTurns)
	}
	if len(state.runSteps) != 1 || state.runSteps[0].Commentary != "kept tool commentary" {
		t.Fatalf("run steps = %#v", state.runSteps)
	}
	if len(state.requestUsage) != 2 || state.requestUsage[1].InputTokens != 11 || state.requestUsage[2].InputTokens != 0 {
		t.Fatalf("durable request usage includes failed retry: %#v", state.requestUsage)
	}
}

func TestAgentLoopStopsAfterStreamRetriesExhausted(t *testing.T) {
	scripts := make([][]fake.Step, 0, maxStreamRetries+1)
	for range maxStreamRetries + 1 {
		scripts = append(scripts, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "discard"}}, {Err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "stream interrupted", Retryable: true}}})
	}
	loop, state, provider := newLoopFixture(t, nil, scripts...)
	loop.retryDelay = func(int) time.Duration { return 0 }
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("outcome = %s", outcome)
	}
	if len(provider.Requests()) != maxStreamRetries+1 || state.run.ModelTurns != 1 || state.messages[1].Parts[0].Text != "" {
		t.Fatalf("requests = %d, run = %#v, assistant = %q", len(provider.Requests()), state.run, state.messages[1].Parts[0].Text)
	}
	if usage := state.requestUsage[1]; len(state.requestUsage) != 1 || usage.StatusCode != 503 || usage.ErrorCode != "MODEL_UNAVAILABLE" || usage.InputTokens != 0 {
		t.Fatalf("retry-exhausted request outcome = %#v", state.requestUsage)
	}
}

func TestAgentLoopRetriesEmptyCompletedTurn(t *testing.T) {
	empty := []fake.Step{{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	success := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "recovered"}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	loop, state, provider := newLoopFixture(t, nil, empty, success)
	loop.retryDelay = func(int) time.Duration { return 0 }
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
	}
	if len(provider.Requests()) != 2 || state.run.ModelTurns != 1 || state.messages[1].Parts[0].Text != "recovered" {
		t.Fatalf("requests = %d, run = %#v, assistant = %q", len(provider.Requests()), state.run, state.messages[1].Parts[0].Text)
	}
}

func TestAgentLoopDoesNotRetryTruncatedCompletedTurn(t *testing.T) {
	truncated := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "partial"}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "length"}},
	}
	loop, state, provider := newLoopFixture(t, nil, truncated)
	loop.retryDelay = func(int) time.Duration { return 0 }
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("outcome = %s", outcome)
	}
	if len(provider.Requests()) != 1 || state.run.ModelTurns != 1 {
		t.Fatalf("requests = %d, run = %#v", len(provider.Requests()), state.run)
	}
}

func TestAgentLoopCancellationInterruptsRetryDelay(t *testing.T) {
	provider := &openingFailureModel{inner: fake.New(), remaining: 10, err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "temporary", Retryable: true}}
	loop, state, _ := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	loop.models = fakeResolver{model: provider}
	loop.retryDelay = func(int) time.Duration { return time.Hour }
	observer := &retryCaptureObserver{retrying: make(chan RetryStatus, 1)}
	loop.observer = observer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Outcome, 1)
	go func() { done <- loop.Run(ctx, "run") }()
	select {
	case <-observer.retrying:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("retry did not begin")
	}
	select {
	case outcome := <-done:
		if outcome != OutcomeCancelled {
			t.Fatalf("outcome = %s", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt retry delay")
	}
	if usage := state.requestUsage[1]; len(state.requestUsage) != 1 || usage.StatusCode != 499 || usage.ErrorCode != "REQUEST_INTERRUPTED" {
		t.Fatalf("cancelled request outcome = %#v", state.requestUsage)
	}
}

func TestAgentLoopKeepsEveryToolTurnOutOfFinalAnswer(t *testing.T) {
	toolTurn := func(callID, text string) []fake.Step {
		return []fake.Step{
			{Event: model.Event{Type: model.EventTextDelta, Text: text}},
			{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: callID, Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
			{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
		}
	}
	final := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "</think>这是最终回答。"}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	loop, state, _ := newLoopFixture(t, nil, toolTurn("call-1", "先检查文件。"), toolTurn("call-2", "再核对结果。"), final)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if got := state.messages[1].Parts[0].Text; got != "这是最终回答。" {
		t.Fatalf("final answer = %q", got)
	}
	if len(state.runSteps) != 2 || state.runSteps[0].Commentary != "先检查文件。" || state.runSteps[1].Commentary != "再核对结果。" {
		t.Fatalf("run steps = %#v", state.runSteps)
	}
}

func TestVisibleModelTextRemovesLeakedThinkingOnly(t *testing.T) {
	tests := map[string]string{
		"<think>private chain</think>Visible answer": "Visible answer",
		"</think>Visible answer":                     "Visible answer",
		"<think>Unclosed provider text":              "",
		"Plain final answer":                         "Plain final answer",
	}
	for input, want := range tests {
		if got := visibleModelText(input); got != want {
			t.Fatalf("visibleModelText(%q) = %q, want %q", input, got, want)
		}
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

func TestAgentLoopDoesNotPresentFailedTurnTextAsFinalAnswer(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil, []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "正在尝试读取页面……"}},
		{Err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "连接中断"}},
	})
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if got := state.messages[1].Parts[0].Text; got != "" {
		t.Fatalf("failed turn was exposed as final answer: %q", got)
	}
}

func TestAgentLoopDoesNotPresentCancelledTurnTextAsFinalAnswer(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil, []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "尚未完成的输出"}},
		{Err: context.Canceled},
	})
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCancelled {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if got := state.messages[1].Parts[0].Text; got != "" {
		t.Fatalf("cancelled turn was exposed as final answer: %q", got)
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
	reasoning := model.ProviderItem{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Checked the available evidence before answering."}],"encrypted_content":"opaque"}`)}
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
	if state.run.ReasoningSummary != "Checked the available evidence before answering." {
		t.Fatalf("reasoning summary = %q", state.run.ReasoningSummary)
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
	script := []fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &usage}}, {Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, _ := newLoopFixture(t, nil, script)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.InputTokens != 120 || state.run.FreshInputTokens != 28 || state.run.OutputTokens != 18 || state.run.ReasoningTokens != 7 || !state.run.ReasoningObserved || state.run.CachedInputTokens != 80 || state.run.CacheWriteTokens != 12 || state.run.CacheReportedTurns != 1 || state.run.CacheReportedFreshInputTokens != 28 || state.run.CacheHitTurns != 1 {
		t.Fatalf("cache usage run = %#v", state.run)
	}
	if len(state.requestUsage) != 1 || state.requestUsage[1].CachedInputTokens != 80 || state.requestUsage[1].CacheWriteTokens != 12 {
		t.Fatalf("request usage = %#v", state.requestUsage)
	}
}

func TestAgentLoopPassesRequestedAndResolvedReasoning(t *testing.T) {
	script := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
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
	script := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
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
	first := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "准备读取论文。"}}, {Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "provider-call", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
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
	if len(state.runSteps) != 1 || state.runSteps[0].Commentary != "准备读取论文。" {
		t.Fatalf("approval run steps = %#v", state.runSteps)
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

func TestAgentLoopContinuesBeyondLegacyRunLimits(t *testing.T) {
	scripts := make([][]fake.Step, 0, 14)
	for index := 0; index < 13; index++ {
		call := model.ToolCall{ID: fmt.Sprintf("provider-call-%02d", index), Name: "builtin.fixture", Arguments: json.RawMessage(fmt.Sprintf(`{"query":"paper-%02d"}`, index))}
		scripts = append(scripts, []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &call}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}})
	}
	scripts = append(scripts, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "done"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	loop, state, provider := newLoopFixture(t, nil, scripts...)
	if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.run.ModelTurns != 14 || len(state.calls) != 13 || len(provider.Requests()) != 14 {
		t.Fatalf("unbounded run = turns:%d calls:%d requests:%d", state.run.ModelTurns, len(state.calls), len(provider.Requests()))
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

func TestContextBuilderIncludesHiddenRunTerminationRecord(t *testing.T) {
	payload := json.RawMessage(`{"kind":"run_termination_context","status":"cancelled","draft":"正在检查页面状态","activities":[{"turnIndex":2,"commentary":"已定位到导航阶段"}],"tools":[{"name":"mcp.chrome-devtools.navigate_page","arguments":"{\"url\":\"https://example.test\"}","status":"cancelled","errorCode":"TOOL_CANCELLED"}]}`)
	messages := []conversation.Message{
		{ID: "old-user", RunID: "old-run", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "打开页面"}}},
		{ID: "old-assistant", RunID: "old-run", Role: conversation.RoleAssistant, Status: conversation.MessageIncomplete, Parts: []conversation.MessagePart{{Type: "text"}, {Type: "tool_result", Payload: payload}}},
		{ID: "current-user", RunID: "current-run", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "刚才为什么卡住了？"}}},
	}
	request, _, err := NewContextBuilder(20_000).BuildWithSkillContext(context.Background(), messages, "", "current-user", nil, nil, appskill.RunContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 4 || !strings.Contains(request.Messages[2].Content, "run_termination_context") || !strings.Contains(request.Messages[2].Content, "正在检查页面状态") || !strings.Contains(request.Messages[2].Content, "已定位到导航阶段") || !strings.Contains(request.Messages[2].Content, "navigate_page") || !strings.Contains(request.Messages[2].Content, "untrusted historical data") {
		t.Fatalf("request messages = %#v", request.Messages)
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
	failedUsage := model.Usage{InputTokens: 999, FreshInputTokens: 999, OutputTokens: 999, CacheDetailsReported: true}
	failedSummaryScript := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "discarded checkpoint"}},
		{Event: model.Event{Type: model.EventUsage, Usage: &failedUsage}},
		{Err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "stream interrupted", Retryable: true}},
	}
	summaryUsage := model.Usage{InputTokens: 12, FreshInputTokens: 12, OutputTokens: 4, CacheDetailsReported: true}
	summaryScript := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "# Research state\n- Objective: retain the verified baseline."}},
		{Event: model.Event{Type: model.EventUsage, Usage: &summaryUsage}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	answerScript := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "continued"}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	loop, state, provider := newLoopFixture(t, nil, failedSummaryScript, summaryScript, answerScript)
	loop.retryDelay = func(int) time.Duration { return 0 }
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
	if len(requests) != 3 {
		t.Fatalf("model requests = %d, want compaction + answer", len(requests))
	}
	if requests[0].PromptCacheKey != "conversation" || requests[1].PromptCacheKey != "conversation" || requests[2].PromptCacheKey != "conversation" {
		t.Fatalf("compaction cache keys = %q, %q, %q", requests[0].PromptCacheKey, requests[1].PromptCacheKey, requests[2].PromptCacheKey)
	}
	encoded, _ := json.Marshal(requests[2])
	if !strings.Contains(string(encoded), "untrusted_conversation_checkpoint") || strings.Contains(string(encoded), oldUser) || !strings.Contains(string(encoded), recentUser) {
		t.Fatalf("replacement request = %s", encoded)
	}
	if state.run.ModelTurns != 2 || !state.run.ContextCompacted || state.run.InputTokens != 12 || state.run.OutputTokens != 4 || state.run.CacheReportedTurns != 1 {
		t.Fatalf("run compaction audit = %#v", state.run)
	}
}

func TestLoopContinuesAutomaticCompactionWhileCheckpointAdvances(t *testing.T) {
	scripts := make([][]fake.Step, 0, 6)
	for index := 1; index <= 5; index++ {
		scripts = append(scripts, []fake.Step{
			{Event: model.Event{Type: model.EventTextDelta, Text: fmt.Sprintf("# Checkpoint %d\n- Preserve verified state.", index)}},
			{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
		})
	}
	scripts = append(scripts, []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "final answer"}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	})
	loop, state, provider := newLoopFixture(t, nil, scripts...)
	state.run.UserMessageID = "current-user"
	state.messages = make([]conversation.Message, 0, 8)
	for index := 1; index <= 6; index++ {
		state.messages = append(state.messages, conversation.Message{
			ID: fmt.Sprintf("old-%d", index), RunID: fmt.Sprintf("old-run-%d", index), Role: conversation.RoleUser,
			Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat(fmt.Sprintf("history-%d ", index), 240)}},
		})
	}
	state.messages = append(state.messages,
		conversation.Message{ID: "current-user", RunID: "run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "continue"}}},
		conversation.Message{ID: "assistant", RunID: "run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{Type: "text"}}},
	)
	repository := &memoryCheckpointRepository{}
	loop.checkpoints = contextmemory.NewService(repository)
	loop.models = budgetResolver{model: provider, budget: modelcap.ResolveContextBudget(5_000, 0, modelcap.ContextWindowSourceManual)}
	loop.runs = &faultInjectingRuns{loopState: state, failUsage: true}

	if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, run = %#v", outcome, state.run)
	}
	if repository.latest.ThroughMessageID != "old-5" || state.messages[len(state.messages)-1].Parts[0].Text != "final answer" {
		t.Fatalf("checkpoint = %#v, assistant = %#v", repository.latest, state.messages[len(state.messages)-1])
	}
	if requests := provider.Requests(); len(requests) != 6 {
		t.Fatalf("model requests = %d, want five advancing checkpoints plus one answer", len(requests))
	}
}

func TestManualCompactionPersistsLatestConversationBoundary(t *testing.T) {
	summaryScript := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "# Research state\n- The verified result remains available."}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	loop, state, provider := newLoopFixture(t, nil, summaryScript)
	now := time.Now().UTC()
	state.run.Status = chat.RunCompleted
	state.run.APIProtocol = modelcap.ProtocolOpenAIChat
	state.run.ModelTurns = 1
	state.run.CompletedAt = &now
	state.messages = []conversation.Message{
		{ID: "user", ConversationID: "conversation", RunID: "run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "Summarize the verified result."}}},
		{ID: "assistant", ConversationID: "conversation", RunID: "run", Role: conversation.RoleAssistant, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "The verified result is reproducible."}}},
	}
	repository := &memoryCheckpointRepository{}
	loop.checkpoints = contextmemory.NewService(repository)

	result, err := loop.CompactConversation(context.Background(), "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Passes != 1 || result.Revision != 1 || result.ThroughMessageID != "assistant" || result.SourceMessageCount != 2 {
		t.Fatalf("manual compaction result = %#v", result)
	}
	if repository.latest.ThroughMessageID != "assistant" || !strings.Contains(repository.latest.Summary, "verified result") {
		t.Fatalf("manual checkpoint = %#v", repository.latest)
	}
	if state.run.ModelTurns != 2 || !state.run.ContextCompacted || len(provider.Requests()) != 1 {
		t.Fatalf("manual compaction audit = run %#v, requests %d", state.run, len(provider.Requests()))
	}
	if provider.Requests()[0].PromptCacheKey != "conversation" {
		t.Fatalf("manual compaction cache key = %q", provider.Requests()[0].PromptCacheKey)
	}

	unchanged, err := loop.CompactConversation(context.Background(), "conversation")
	if err != nil || !unchanged.Complete || unchanged.Passes != 0 || len(provider.Requests()) != 1 {
		t.Fatalf("idempotent manual compaction = %#v, %v, requests %d", unchanged, err, len(provider.Requests()))
	}
}

func TestManualCompactionDoesNotReplaceHistoryWithLargerSummary(t *testing.T) {
	summaryScript := []fake.Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("expanded summary ", 20)}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
	}
	loop, state, _ := newLoopFixture(t, nil, summaryScript)
	now := time.Now().UTC()
	state.run.Status = chat.RunCompleted
	state.run.CompletedAt = &now
	state.messages = []conversation.Message{
		{ID: "user", ConversationID: "conversation", RunID: "run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "Q"}}},
		{ID: "assistant", ConversationID: "conversation", RunID: "run", Role: conversation.RoleAssistant, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "A"}}},
	}
	repository := &memoryCheckpointRepository{}
	loop.checkpoints = contextmemory.NewService(repository)

	if _, err := loop.CompactConversation(context.Background(), "conversation"); err == nil || !strings.Contains(err.Error(), "not smaller") {
		t.Fatalf("compaction error = %v", err)
	}
	if repository.latest.ID != "" || repository.latest.Summary != "" {
		t.Fatalf("larger checkpoint replaced history: %#v", repository.latest)
	}
}

func TestReceiveCheckpointSummaryRejectsTruncationAndIncompleteTerminal(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens int
		script    []fake.Step
	}{
		{
			name:      "local summary limit",
			maxTokens: 4,
			script: []fake.Step{
				{Event: model.Event{Type: model.EventTextDelta, Text: "12345"}},
				{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}},
			},
		},
		{
			name:      "provider output limit",
			maxTokens: 100,
			script: []fake.Step{
				{Event: model.Event{Type: model.EventTextDelta, Text: "partial"}},
				{Event: model.Event{Type: model.EventDone, FinishReason: "length"}},
			},
		},
		{
			name:      "missing terminal",
			maxTokens: 100,
			script:    []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "partial"}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := fake.New(test.script)
			stream, err := provider.Stream(context.Background(), model.ChatRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if summary, _, _, err := (&Loop{}).receiveCheckpointSummary(context.Background(), stream, test.maxTokens); err == nil || summary != "" {
				t.Fatalf("summary = %q, error = %v", summary, err)
			}
		})
	}
}

func TestManualCompactionRejectsActiveConversation(t *testing.T) {
	loop, _, _ := newLoopFixture(t, nil)
	loop.checkpoints = contextmemory.NewService(&memoryCheckpointRepository{})
	if _, err := loop.CompactConversation(context.Background(), "conversation"); err == nil || !strings.Contains(err.Error(), "仍在运行") {
		t.Fatalf("active compaction error = %v", err)
	}
}

func TestContextBuilderRejectsNormalizedToolCompactionWithoutCheckpointBoundary(t *testing.T) {
	baseTokens := len([]rune(fixedSystemRules))
	builder := NewContextBuilder(baseTokens + len([]rune("fixture")) + len([]rune(`{}`)) + 20)
	calls := []tool.Call{
		{ProviderCallID: "old", ToolName: "fixture", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("o", 100)}},
		{ProviderCallID: "new", ToolName: "fixture", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: "newest"}},
	}
	_, err := builder.Build(context.Background(), nil, "", nil, calls)
	if err == nil || !strings.Contains(err.Error(), "cannot be safely compacted") {
		t.Fatalf("unsafe normalized tool compaction error = %v", err)
	}
}

func TestContextBuilderKeepsLargeProviderToolResultPrefixStable(t *testing.T) {
	builder := NewContextBuilder(200_000)
	oldPayload := json.RawMessage(`{"type":"function_call","id":"fc_old","call_id":"old","name":"fixture","arguments":"{}"}`)
	newPayload := json.RawMessage(`{"type":"function_call","id":"fc_new","call_id":"new","name":"fixture","arguments":"{}"}`)
	calls := []tool.Call{
		{ID: "old-local", ProviderCallID: "old", ToolName: "fixture", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("old-page-", 12_000)}},
		{ID: "new-local", ProviderCallID: "new", ToolName: "fixture", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("new-page-", 12_000)}},
	}
	turns := []model.ProviderTurn{
		{TurnIndex: 1, Protocol: modelcap.ProtocolOpenAIResponses, Items: []model.ProviderItem{{Ordinal: 0, Type: "function_call", CallID: "old", Payload: oldPayload}}},
		{TurnIndex: 2, Protocol: modelcap.ProtocolOpenAIResponses, Items: []model.ProviderItem{{Ordinal: 0, Type: "function_call", CallID: "new", Payload: newPayload}}},
	}

	first, _, err := builder.BuildWithInfo(context.Background(), nil, "", nil, calls[:1], turns[0])
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := builder.BuildWithInfo(context.Background(), nil, "", nil, calls, turns...)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ProviderTurns) != 1 || len(second.ProviderTurns) != 2 {
		t.Fatalf("provider turns = first:%d second:%d", len(first.ProviderTurns), len(second.ProviderTurns))
	}
	firstResult := first.ProviderTurns[0].ToolResults[0].Content
	secondOldResult := second.ProviderTurns[0].ToolResults[0].Content
	secondNewResult := second.ProviderTurns[1].ToolResults[0].Content
	if firstResult != secondOldResult {
		t.Fatal("older model-visible tool result changed after a newer large result was added")
	}
	if len([]rune(firstResult)) != maxToolResultContextTokens || len([]rune(secondNewResult)) != maxToolResultContextTokens {
		t.Fatalf("bounded result sizes = old:%d new:%d", len([]rune(firstResult)), len([]rune(secondNewResult)))
	}
	if !strings.Contains(firstResult, "tool result truncated for model context") || !strings.Contains(secondNewResult, "tool result truncated for model context") {
		t.Fatal("large model-visible results were not marked as truncated")
	}
}

func TestContextBuilderReplaysPersistedModelToolSnapshot(t *testing.T) {
	builder := NewContextBuilder(20_000)
	call := tool.Call{
		ID:                  "local-call",
		ProviderCallID:      "provider-call",
		ToolName:            "fixture",
		Arguments:           json.RawMessage(`{}`),
		ModelContext:        `{"status":"success","text":"original visible result"}`,
		ModelContextVersion: tool.ModelContextSnapshotVersion,
		Result:              &tool.Result{Status: tool.ResultSuccess, Text: "later full result mutation"},
	}
	request, err := builder.Build(context.Background(), nil, "", nil, []tool.Call{call})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 3 || request.Messages[2].Content != call.ModelContext {
		t.Fatalf("persisted model snapshot was not replayed: %#v", request.Messages)
	}
}

func TestCheckpointRequestAppendExtendsStableConversationPrefix(t *testing.T) {
	prefix := []model.Message{
		{Role: model.RoleSystem, Content: fixedSystemRules},
		{Role: model.RoleUser, Content: "stable skill catalog"},
	}
	tools := []model.ToolDefinition{{Name: "builtin.fixture", Description: "fixture", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	messages := []conversation.Message{
		{ID: "user", RunID: "old-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "research question"}}},
		{ID: "assistant", RunID: "old-run", Role: conversation.RoleAssistant, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "verified answer"}}},
	}
	batch, err := buildCheckpointBatch(contextmemory.Checkpoint{}, messages, "assistant", 20_000, prefix, tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.request.Messages) != len(prefix)+len(messages)+1 {
		t.Fatalf("checkpoint messages = %#v", batch.request.Messages)
	}
	for index := range prefix {
		if !reflect.DeepEqual(batch.request.Messages[index], prefix[index]) {
			t.Fatalf("stable prefix changed at %d", index)
		}
	}
	if batch.request.Messages[2].Content != "research question" || batch.request.Messages[3].Content != "verified answer" || !strings.Contains(batch.request.Messages[4].Content, "durable research checkpoint") {
		t.Fatalf("checkpoint append sequence = %#v", batch.request.Messages)
	}
	if len(batch.request.Tools) != 1 || batch.request.Tools[0].Name != tools[0].Name {
		t.Fatalf("stable tools changed: %#v", batch.request.Tools)
	}
}

func TestContextBuilderRejectsProviderTurnCompactionWithoutCheckpointBoundary(t *testing.T) {
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
	_, _, err := builder.BuildWithInfo(context.Background(), []conversation.Message{{ID: "latest", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: latest}}}}, "", nil, calls, turns...)
	if err == nil || !strings.Contains(err.Error(), "cannot be safely compacted") {
		t.Fatalf("unsafe provider compaction error = %v", err)
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

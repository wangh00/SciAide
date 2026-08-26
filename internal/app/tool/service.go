package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

type CreateCommand struct {
	RunID          string          `json:"runId"`
	SubjectKind    SubjectKind     `json:"subjectKind,omitempty"`
	ProviderCallID string          `json:"providerCallId"`
	Arguments      json.RawMessage `json:"arguments"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
}

type Service struct {
	repository Repository
	validator  SchemaValidator
	now        func() time.Time
}

func NewService(repository Repository, validator SchemaValidator) *Service {
	return &Service{repository: repository, validator: validator, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Propose(ctx context.Context, definition Definition, cmd CreateCommand) (Call, error) {
	return s.propose(ctx, definition, cmd, true)
}

func (s *Service) propose(ctx context.Context, definition Definition, cmd CreateCommand, validateSchema bool) (Call, error) {
	cmd.RunID, cmd.ProviderCallID = strings.TrimSpace(cmd.RunID), strings.TrimSpace(cmd.ProviderCallID)
	cmd.SubjectKind = NormalizeSubjectKind(cmd.SubjectKind)
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	if cmd.RunID == "" || cmd.ProviderCallID == "" {
		return Call{}, fmt.Errorf("run and provider call are required")
	}
	if !cmd.SubjectKind.Valid() {
		return Call{}, fmt.Errorf("invalid tool call subject kind")
	}
	if err := ValidateDefinition(definition); err != nil {
		return Call{}, err
	}
	definition = SnapshotDefinition(definition)
	if err := ValidateArguments(cmd.Arguments); err != nil {
		return Call{}, err
	}
	if validateSchema {
		if s.validator == nil {
			return Call{}, fmt.Errorf("tool argument schema validator is not configured")
		}
		if err := s.validator.Validate(definition.InputSchema, cmd.Arguments); err != nil {
			return Call{}, fmt.Errorf("validate tool arguments: %w", err)
		}
	}
	callID, err := id.New()
	if err != nil {
		return Call{}, err
	}
	now := s.now()
	permissions := append([]PermissionRequirement(nil), definition.Permissions...)
	value := Call{ID: callID, RunID: cmd.RunID, SubjectKind: cmd.SubjectKind, ProviderCallID: cmd.ProviderCallID, ToolName: definition.QualifiedName, ToolVersion: definition.Version, Arguments: append(json.RawMessage(nil), cmd.Arguments...), Status: CallPending, Risk: definition.Risk, Permissions: permissions, Idempotent: definition.Idempotent, IdempotencyKey: cmd.IdempotencyKey, CreatedAt: now, UpdatedAt: now}
	event, err := newToolEvent(value.SubjectKind, value.RunID, "tool.proposed", map[string]any{"toolCall": value})
	if err != nil {
		return Call{}, err
	}
	if err := s.repository.CreateWithEvent(ctx, value, event); err != nil {
		return Call{}, fmt.Errorf("create tool call: %w", err)
	}
	return value, nil
}

func (s *Service) ProposeRegistered(ctx context.Context, registry Registry, toolName string, cmd CreateCommand) (Call, error) {
	if registry == nil {
		return Call{}, fmt.Errorf("tool registry is not configured")
	}
	definition, err := registry.Definition(ctx, strings.TrimSpace(toolName))
	if err != nil {
		return Call{}, err
	}
	return s.Propose(ctx, definition, cmd)
}

// RejectProviderCall preserves a model-issued call as a terminal ToolResult so
// the model can correct an unavailable tool name or schema-invalid arguments.
// The synthetic definition is audit-only and can never enter approval or
// execution because the call is finished before this method returns.
func (s *Service) RejectProviderCall(ctx context.Context, registry Registry, toolName string, cmd CreateCommand, message string) (Call, error) {
	if registry == nil {
		return Call{}, fmt.Errorf("tool registry is not configured")
	}
	toolName = strings.TrimSpace(toolName)
	definition, err := registry.Definition(ctx, toolName)
	if err != nil {
		definition = Definition{
			QualifiedName: toolName,
			Description:   "Unavailable model-requested tool",
			InputSchema:   json.RawMessage(`{"type":"object"}`),
			Risk:          RiskLow,
			Permissions:   []PermissionRequirement{},
			Idempotent:    true,
			Version:       "unavailable",
		}
	}
	call, err := s.propose(ctx, definition, cmd, false)
	if err != nil {
		return Call{}, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "工具调用无法执行，请检查可用工具和参数后重试。"
	}
	return s.Finish(context.Background(), call.ID, Result{Status: ResultError, Text: message}, ErrorCodeCallRejected, strings.TrimSuffix(message, "。"))
}

func (s *Service) Get(ctx context.Context, callID string) (Call, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return Call{}, fmt.Errorf("tool call id is required")
	}
	return s.repository.Get(ctx, callID)
}

func (s *Service) ListByRun(ctx context.Context, runID string) ([]Call, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("run id is required")
	}
	return s.repository.ListByRun(ctx, runID)
}

func (s *Service) ListBySubject(ctx context.Context, subjectKind SubjectKind, subjectID string) ([]Call, error) {
	subjectID = strings.TrimSpace(subjectID)
	subjectKind = NormalizeSubjectKind(subjectKind)
	if subjectID == "" || !subjectKind.Valid() {
		return nil, fmt.Errorf("valid tool call subject is required")
	}
	if subjectKind == SubjectChatRun {
		return s.repository.ListByRun(ctx, subjectID)
	}
	repository, ok := s.repository.(SubjectRepository)
	if !ok {
		return nil, fmt.Errorf("tool subject repository is not configured")
	}
	return repository.ListBySubject(ctx, subjectKind, subjectID)
}

func (s *Service) AwaitApproval(ctx context.Context, callID string) (Call, error) {
	return s.transition(ctx, callID, CallAwaitingApproval, "", "")
}

func (s *Service) Start(ctx context.Context, callID string) (Call, error) {
	return s.transition(ctx, callID, CallRunning, "", "")
}

func (s *Service) Interrupt(ctx context.Context, callID, message string) (Call, error) {
	return s.transition(ctx, callID, CallInterrupted, "TOOL_INTERRUPTED", strings.TrimSpace(message))
}

func (s *Service) MarkOutcomeUnknown(ctx context.Context, callID string) (Call, error) {
	return s.transition(ctx, callID, CallInterrupted, ErrorCodeOutcomeUnknown, "工具可能已经产生副作用，但执行结果未能持久化")
}

func (s *Service) Finish(ctx context.Context, callID string, result Result, errorCode, errorMessage string) (Call, error) {
	if err := ValidateResult(result); err != nil {
		return Call{}, err
	}
	next, err := TerminalStatusForResult(result.Status)
	if err != nil {
		return Call{}, err
	}
	value, err := s.repository.Get(ctx, strings.TrimSpace(callID))
	if err != nil {
		return Call{}, err
	}
	if !CanTransition(value.Status, next) {
		return Call{}, fmt.Errorf("%w: %s to %s", ErrTransitionConflict, value.Status, next)
	}
	now := s.now()
	result.CreatedAt = now
	projected := value
	projected.Status, projected.Result, projected.ErrorCode, projected.ErrorMessage, projected.CompletedAt, projected.UpdatedAt = next, &result, strings.TrimSpace(errorCode), strings.TrimSpace(errorMessage), &now, now
	event, err := newToolEvent(value.SubjectKind, value.RunID, "tool."+string(next), map[string]any{"toolCall": projected})
	if err != nil {
		return Call{}, err
	}
	if err := s.repository.FinishWithEvent(ctx, value.ID, value.Status, next, result, projected.ErrorCode, projected.ErrorMessage, now, event); err != nil {
		return Call{}, err
	}
	updated, err := s.repository.Get(ctx, value.ID)
	return updated, err
}

func (s *Service) Recover(ctx context.Context) (int64, error) {
	return s.repository.InterruptActive(ctx, s.now())
}

func (s *Service) transition(ctx context.Context, callID string, next CallStatus, errorCode, errorMessage string) (Call, error) {
	value, err := s.repository.Get(ctx, strings.TrimSpace(callID))
	if err != nil {
		return Call{}, err
	}
	if !CanTransition(value.Status, next) {
		return Call{}, fmt.Errorf("%w: %s to %s", ErrTransitionConflict, value.Status, next)
	}
	now := s.now()
	projected := value
	projected.Status, projected.ErrorCode, projected.ErrorMessage, projected.UpdatedAt = next, errorCode, errorMessage, now
	if next == CallRunning {
		projected.StartedAt = &now
	}
	if next.Terminal() {
		projected.CompletedAt = &now
	}
	event, err := newToolEvent(value.SubjectKind, value.RunID, "tool."+string(next), map[string]any{"toolCall": projected})
	if err != nil {
		return Call{}, err
	}
	if err := s.repository.TransitionWithEvent(ctx, value.ID, value.Status, next, errorCode, errorMessage, now, event); err != nil {
		return Call{}, err
	}
	updated, err := s.repository.Get(ctx, value.ID)
	return updated, err
}

func newToolEvent(subjectKind SubjectKind, runID, eventType string, payload any) (events.Envelope, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return events.Envelope{}, err
	}
	eventID, err := id.New()
	if err != nil {
		return events.Envelope{}, err
	}
	return events.New(eventID, runID, NormalizeSubjectKind(subjectKind).AggregateType(), eventType, 0, data), nil
}

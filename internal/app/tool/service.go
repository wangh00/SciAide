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
	cmd.RunID, cmd.ProviderCallID = strings.TrimSpace(cmd.RunID), strings.TrimSpace(cmd.ProviderCallID)
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	if cmd.RunID == "" || cmd.ProviderCallID == "" {
		return Call{}, fmt.Errorf("run and provider call are required")
	}
	if err := ValidateDefinition(definition); err != nil {
		return Call{}, err
	}
	definition = SnapshotDefinition(definition)
	if err := ValidateArguments(cmd.Arguments); err != nil {
		return Call{}, err
	}
	if s.validator == nil {
		return Call{}, fmt.Errorf("tool argument schema validator is not configured")
	}
	if err := s.validator.Validate(definition.InputSchema, cmd.Arguments); err != nil {
		return Call{}, fmt.Errorf("validate tool arguments: %w", err)
	}
	callID, err := id.New()
	if err != nil {
		return Call{}, err
	}
	now := s.now()
	permissions := append([]PermissionRequirement(nil), definition.Permissions...)
	value := Call{ID: callID, RunID: cmd.RunID, ProviderCallID: cmd.ProviderCallID, ToolName: definition.QualifiedName, ToolVersion: definition.Version, Arguments: append(json.RawMessage(nil), cmd.Arguments...), Status: CallPending, Risk: definition.Risk, Permissions: permissions, Idempotent: definition.Idempotent, IdempotencyKey: cmd.IdempotencyKey, CreatedAt: now, UpdatedAt: now}
	event, err := newToolEvent(value.RunID, "tool.proposed", map[string]any{"toolCall": value})
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

func (s *Service) AwaitApproval(ctx context.Context, callID string) (Call, error) {
	return s.transition(ctx, callID, CallAwaitingApproval, "", "")
}

func (s *Service) Start(ctx context.Context, callID string) (Call, error) {
	return s.transition(ctx, callID, CallRunning, "", "")
}

func (s *Service) Interrupt(ctx context.Context, callID, message string) (Call, error) {
	return s.transition(ctx, callID, CallInterrupted, "TOOL_INTERRUPTED", strings.TrimSpace(message))
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
	event, err := newToolEvent(value.RunID, "tool."+string(next), map[string]any{"toolCall": projected})
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
	event, err := newToolEvent(value.RunID, "tool."+string(next), map[string]any{"toolCall": projected})
	if err != nil {
		return Call{}, err
	}
	if err := s.repository.TransitionWithEvent(ctx, value.ID, value.Status, next, errorCode, errorMessage, now, event); err != nil {
		return Call{}, err
	}
	updated, err := s.repository.Get(ctx, value.ID)
	return updated, err
}

func newToolEvent(runID, eventType string, payload any) (events.Envelope, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return events.Envelope{}, err
	}
	eventID, err := id.New()
	if err != nil {
		return events.Envelope{}, err
	}
	return events.New(eventID, runID, "run", eventType, 0, data), nil
}

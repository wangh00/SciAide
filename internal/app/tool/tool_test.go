package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/events"
)

func TestCallStateMachineRejectsTerminalReplay(t *testing.T) {
	if !CanTransition(CallPending, CallAwaitingApproval) || !CanTransition(CallAwaitingApproval, CallRunning) || !CanTransition(CallRunning, CallCompleted) {
		t.Fatal("expected happy-path transitions to be allowed")
	}
	for _, status := range []CallStatus{CallCompleted, CallFailed, CallDenied, CallCancelled, CallInterrupted} {
		if !status.Terminal() {
			t.Fatalf("%s should be terminal", status)
		}
		if CanTransition(status, CallRunning) {
			t.Fatalf("terminal status %s returned to running", status)
		}
	}
}

func TestJSONSchemaValidatorRejectsUnknownAndInvalidArguments(t *testing.T) {
	validator := JSONSchemaValidator{}
	schema := []byte(`{"type":"object","additionalProperties":false,"required":["path","limit"],"properties":{"path":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":10}}}`)
	if err := validator.Validate(schema, []byte(`{"path":"paper.md","limit":5}`)); err != nil {
		t.Fatalf("valid arguments rejected: %v", err)
	}
	for _, instance := range [][]byte{[]byte(`{"path":"paper.md","limit":0}`), []byte(`{"path":"paper.md","limit":5,"secret":true}`), []byte(`{"path":"paper.md","limit":1.5}`)} {
		if err := validator.Validate(schema, instance); err == nil {
			t.Fatalf("invalid arguments accepted: %s", instance)
		}
	}
	if err := validator.Validate([]byte(`{"type":"object","oneOf":[]}`), []byte(`{}`)); err == nil {
		t.Fatal("unsupported schema keyword was ignored")
	}
	if err := validator.Validate([]byte(`{"type":"object","properties":{"optional":{"type":"string","format":"uri"}}}`), []byte(`{}`)); err == nil {
		t.Fatal("unsupported nested keyword was ignored for an absent property")
	}
}

type memoryToolRepository struct {
	mu     sync.Mutex
	calls  map[string]Call
	events []events.Envelope
}

func newMemoryToolRepository() *memoryToolRepository {
	return &memoryToolRepository{calls: map[string]Call{}}
}
func (r *memoryToolRepository) create(_ context.Context, value Call) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[value.ID] = value
	return nil
}
func (r *memoryToolRepository) Get(_ context.Context, id string) (Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.calls[id]
	if !ok {
		return Call{}, fmt.Errorf("not found")
	}
	return value, nil
}
func (r *memoryToolRepository) ListByRun(_ context.Context, runID string) ([]Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := []Call{}
	for _, value := range r.calls {
		if value.RunID == runID {
			values = append(values, value)
		}
	}
	return values, nil
}
func (r *memoryToolRepository) transition(_ context.Context, id string, expected, next CallStatus, errorCode, errorMessage string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := r.calls[id]
	if value.Status != expected {
		return ErrTransitionConflict
	}
	value.Status, value.ErrorCode, value.ErrorMessage, value.UpdatedAt = next, errorCode, errorMessage, at
	if next == CallRunning {
		value.StartedAt = &at
	}
	if next.Terminal() {
		value.CompletedAt = &at
	}
	r.calls[id] = value
	return nil
}
func (r *memoryToolRepository) finish(_ context.Context, id string, expected, next CallStatus, result Result, errorCode, errorMessage string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := r.calls[id]
	if value.Status != expected {
		return ErrTransitionConflict
	}
	value.Status, value.Result, value.ErrorCode, value.ErrorMessage, value.CompletedAt, value.UpdatedAt = next, &result, errorCode, errorMessage, &at, at
	r.calls[id] = value
	return nil
}
func (r *memoryToolRepository) InterruptActive(_ context.Context, at time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for id, value := range r.calls {
		if !value.Status.Terminal() {
			value.Status, value.CompletedAt, value.UpdatedAt = CallInterrupted, &at, at
			r.calls[id] = value
			count++
		}
	}
	return count, nil
}
func (r *memoryToolRepository) CreateWithEvent(ctx context.Context, value Call, event events.Envelope) error {
	if err := r.create(ctx, value); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	event.Sequence = int64(len(r.events) + 1)
	r.events = append(r.events, event)
	return nil
}
func (r *memoryToolRepository) TransitionWithEvent(ctx context.Context, id string, expected, next CallStatus, code, message string, at time.Time, event events.Envelope) error {
	if err := r.transition(ctx, id, expected, next, code, message, at); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	event.Sequence = int64(len(r.events) + 1)
	r.events = append(r.events, event)
	return nil
}
func (r *memoryToolRepository) FinishWithEvent(ctx context.Context, id string, expected, next CallStatus, result Result, code, message string, at time.Time, event events.Envelope) error {
	if err := r.finish(ctx, id, expected, next, result, code, message, at); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	event.Sequence = int64(len(r.events) + 1)
	r.events = append(r.events, event)
	return nil
}

func TestServiceValidatesBeforePersistingAndCompletes(t *testing.T) {
	repository := newMemoryToolRepository()
	service := NewService(repository, JSONSchemaValidator{})
	definition := Definition{QualifiedName: "builtin.workspace.read_file", Description: "Read one file", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string"}}}`), Risk: RiskLow, Permissions: []PermissionRequirement{{Kind: PermissionWorkspaceRead}}, Idempotent: true, Version: "1"}
	if _, err := service.Propose(context.Background(), definition, CreateCommand{RunID: "run", ProviderCallID: "provider-1", Arguments: json.RawMessage(`{"other":true}`)}); err == nil {
		t.Fatal("invalid arguments were persisted")
	}
	if len(repository.calls) != 0 {
		t.Fatal("repository mutated before validation")
	}
	call, err := service.Propose(context.Background(), definition, CreateCommand{RunID: "run", ProviderCallID: "provider-1", Arguments: json.RawMessage(`{"path":"paper.md"}`), IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	call, err = service.Start(context.Background(), call.ID)
	if err != nil || call.Status != CallRunning || call.StartedAt == nil {
		t.Fatalf("Start() = %#v, %v", call, err)
	}
	call, err = service.Finish(context.Background(), call.ID, Result{Status: ResultSuccess, Text: "content", Structured: json.RawMessage(`{"bytes":7}`)}, "", "")
	if err != nil || call.Status != CallCompleted || call.Result == nil || call.Result.Text != "content" {
		t.Fatalf("Finish() = %#v, %v", call, err)
	}
	if _, err := service.Start(context.Background(), call.ID); !errors.Is(err, ErrTransitionConflict) {
		t.Fatalf("terminal replay error = %v", err)
	}
	if len(repository.events) != 3 || repository.events[0].Type != "tool.proposed" || repository.events[2].Type != "tool.completed" {
		t.Fatalf("events = %#v", repository.events)
	}
}

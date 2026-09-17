package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func TestModelContextSnapshotIsDeterministicAndBounded(t *testing.T) {
	result := Result{Status: ResultSuccess, Text: strings.Repeat("科研结果", 4_000), Structured: json.RawMessage(`{"ok":true}`), Truncated: true}
	first := BuildModelContextSnapshot("call", "", result)
	second := BuildModelContextSnapshot("call", "", result)
	if first != second || len([]rune(first)) != MaxModelContextRunes {
		t.Fatalf("snapshot stability = equal:%v runes:%d", first == second, len([]rune(first)))
	}
	if !strings.Contains(first, "tool result truncated for model context") || !strings.Contains(first, `"structured":{"ok":true}`) {
		t.Fatal("snapshot did not preserve bounded head/tail evidence")
	}
}

func TestDefinitionRejectsDuplicateAndMismatchedSyntheticPermissions(t *testing.T) {
	base := Definition{QualifiedName: "builtin.test", Description: "test", InputSchema: json.RawMessage(`{}`), Risk: RiskLow, Version: "1"}
	duplicate := base
	duplicate.Permissions = []PermissionRequirement{{Kind: PermissionWorkspaceRead, Resource: "paper.md"}, {Kind: PermissionWorkspaceRead, Resource: " paper.md "}}
	if err := ValidateDefinition(duplicate); err == nil {
		t.Fatal("duplicate normalized permission was accepted")
	}
	mismatch := base
	mismatch.Permissions = []PermissionRequirement{{Kind: PermissionToolInvoke, Resource: "builtin.other"}}
	if err := ValidateDefinition(mismatch); err == nil {
		t.Fatal("mismatched tool.invoke resource was accepted")
	}
}

func TestDefinitionFingerprintIgnoresDescriptionButCoversExecutionContract(t *testing.T) {
	base := Definition{QualifiedName: "builtin.test", Description: "first", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Risk: RiskLow, Version: "1", Idempotent: true}
	changedDescription := base
	changedDescription.Description = "display text changed"
	if DefinitionFingerprint(base) != DefinitionFingerprint(changedDescription) {
		t.Fatal("description-only change altered execution contract")
	}
	changedSchema := base
	changedSchema.InputSchema = json.RawMessage(`{"type":"object","required":["path"]}`)
	if DefinitionFingerprint(base) == DefinitionFingerprint(changedSchema) {
		t.Fatal("schema change did not alter execution contract")
	}
	formatted := base
	formatted.InputSchema = json.RawMessage(`{ "type": "object" }`)
	if DefinitionFingerprint(base) != DefinitionFingerprint(formatted) {
		t.Fatal("equivalent JSON formatting altered execution contract")
	}
	reordered := base
	reordered.Permissions = []PermissionRequirement{{Kind: PermissionWorkspaceWrite, Resource: "out"}, {Kind: PermissionWorkspaceRead, Resource: "."}}
	base.Permissions = []PermissionRequirement{{Kind: PermissionWorkspaceRead, Resource: "."}, {Kind: PermissionWorkspaceWrite, Resource: "out"}}
	if DefinitionFingerprint(base) != DefinitionFingerprint(reordered) {
		t.Fatal("permission ordering altered execution contract")
	}
}

func TestDefinitionRejectsUnboundedSecurityMetadata(t *testing.T) {
	base := Definition{QualifiedName: "builtin.test", Description: "test", InputSchema: json.RawMessage(`{}`), Risk: RiskLow, Version: "1"}
	oversized := base
	oversized.InputSchema = json.RawMessage(`{"description":"` + strings.Repeat("x", 256*1024) + `"}`)
	if err := ValidateDefinition(oversized); err == nil {
		t.Fatal("oversized schema was accepted")
	}
	tooMany := base
	for index := 0; index < 33; index++ {
		tooMany.Permissions = append(tooMany.Permissions, PermissionRequirement{Kind: PermissionWorkspaceRead, Resource: fmt.Sprintf("path-%d", index)})
	}
	if err := ValidateDefinition(tooMany); err == nil {
		t.Fatal("too many permission requirements were accepted")
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
	if err := validator.Validate([]byte(`{"type":"object"} {}`), []byte(`{}`)); err == nil {
		t.Fatal("trailing schema JSON was accepted")
	}
	if err := validator.Validate([]byte(`{"type":"object"}`), []byte(`{} {}`)); err == nil {
		t.Fatal("trailing instance JSON was accepted")
	}
}

func TestNormalizeModelArgumentsCanonicalizesOnlySchemaDeclaredIntegers(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","minimum":1,"maximum":7500},"label":{"type":"string"},"values":{"type":"array","items":{"type":"integer"}},"nested":{"type":"object","properties":{"offset":{"type":"integer"}}}}}`)
	input := json.RawMessage(`{"limit":"200","label":"300","values":["4",5],"nested":{"offset":"6"}}`)
	normalized, changed, err := NormalizeModelArguments(schema, input)
	if err != nil || !changed {
		t.Fatalf("NormalizeModelArguments() = %s, %v, %v", normalized, changed, err)
	}
	if string(normalized) != `{"label":"300","limit":200,"nested":{"offset":6},"values":[4,5]}` {
		t.Fatalf("normalized = %s", normalized)
	}
	if err := (JSONSchemaValidator{}).Validate(schema, normalized); err != nil {
		t.Fatalf("normalized arguments failed schema validation: %v", err)
	}

	for _, value := range []string{`{"limit":"01"}`, `{"limit":"1.0"}`, `{"limit":"+1"}`, `{"limit":" 1"}`, `{"limit":"9223372036854775808"}`} {
		normalized, changed, err := NormalizeModelArguments(schema, json.RawMessage(value))
		if err != nil || changed || string(normalized) != value {
			t.Fatalf("non-canonical integer %s changed to %s (%v, %v)", value, normalized, changed, err)
		}
	}
}

type memoryToolRepository struct {
	mu        sync.Mutex
	calls     map[string]Call
	events    []events.Envelope
	finishErr error
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
	if r.finishErr != nil {
		return r.finishErr
	}
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

func TestResourceModelProjectionKeepsResolvedAuditSeparate(t *testing.T) {
	result := Result{Status: ResultSuccess, Text: "full private adapter index", Structured: json.RawMessage(`{"sourceArguments":{"path":"real/data.csv"}}`), ModelProjection: &ModelResultProjection{Text: "selected resource content", Structured: json.RawMessage(`{"actionId":"res_abc","actions":[]}`)}}
	if err := ValidateResult(result); err != nil {
		t.Fatal(err)
	}
	snapshot := BuildModelContextSnapshot("call", "", result)
	if strings.Contains(snapshot, "sourceArguments") || strings.Contains(snapshot, "real/data.csv") || !strings.Contains(snapshot, "selected resource content") {
		t.Fatalf("wrong model view: %s", snapshot)
	}
	if result.Text != "full private adapter index" || !strings.Contains(string(result.Structured), "real/data.csv") {
		t.Fatal("projection destroyed audit")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "selected resource content") {
		t.Fatal("internal projection leaked into UI JSON")
	}
	result.ModelProjection.Structured = json.RawMessage(`{`)
	if ValidateResult(result) == nil {
		t.Fatal("invalid projection accepted")
	}
}

func TestModelProjectionUsesExecutorLimits(t *testing.T) {
	e := &Executor{maxText: 12, maxJSON: 32}
	r := Result{Status: ResultSuccess, Text: "audit", Structured: json.RawMessage(`{}`), ModelProjection: &ModelResultProjection{Text: strings.Repeat("幼", 10), Structured: json.RawMessage(`{}`)}}
	limited, code, _ := e.limitResult(r)
	if code != "" || len(limited.ModelProjection.Text) > 12 || !limited.Truncated {
		t.Fatalf("projection bypassed limit: %+v %s", limited, code)
	}
	if len(r.ModelProjection.Text) != 30 {
		t.Fatal("limiting mutated caller projection")
	}
	r.ModelProjection.Structured = json.RawMessage(`{"large":"` + strings.Repeat("x", 50) + `"}`)
	limited, code, _ = e.limitResult(r)
	if code != ErrorCodeResultTooLarge || limited.ModelProjection != nil {
		t.Fatal("oversize projection accepted")
	}
}

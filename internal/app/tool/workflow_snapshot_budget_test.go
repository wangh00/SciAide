package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowSnapshotArgumentsUseStorageCapacityNotModelBudget(t *testing.T) {
	def := Definition{QualifiedName: "builtin.test.snapshot", Description: "local snapshot", Version: "1", Risk: RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["text"],"properties":{"text":{"type":"string"}}}`)}
	input := json.RawMessage(`{"text":"` + strings.Repeat("科研😀", 100000) + `"}`)
	repo := newMemoryToolRepository()
	s := NewService(repo, JSONSchemaValidator{})
	for _, subject := range []SubjectKind{SubjectChatRun, ""} {
		if _, e := s.Propose(context.Background(), def, CreateCommand{RunID: "r", SubjectKind: subject, ProviderCallID: "model", Arguments: input}); e == nil {
			t.Fatal("model allowance widened")
		}
	}
	call, e := s.Propose(context.Background(), def, CreateCommand{RunID: "r", SubjectKind: SubjectWorkflowRun, ProviderCallID: "host", Arguments: input, IdempotencyKey: "snapshot"})
	if e != nil || string(call.Arguments) != string(input) || len(repo.calls) != 1 {
		t.Fatal("host data changed or refused", e)
	}
	for _, bad := range []json.RawMessage{json.RawMessage(`{"wrong":"` + strings.Repeat("x", 300000) + `"}`), json.RawMessage(`{"text":"` + strings.Repeat("x", MaxWorkflowSnapshotRunes) + `"}`), json.RawMessage(`[]`), json.RawMessage(`{"text":true}`)} {
		if _, e = s.Propose(context.Background(), def, CreateCommand{RunID: "r", SubjectKind: SubjectWorkflowRun, ProviderCallID: "bad", Arguments: bad}); e == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
	if len(repo.calls) != 1 {
		t.Fatal("invalid data persisted")
	}
	prefix, suffix := `{"text":"`, `"}`
	atLimit := json.RawMessage(prefix + strings.Repeat("😀", MaxWorkflowSnapshotRunes-len(prefix)-len(suffix)) + suffix)
	if e := ValidateWorkflowArguments(atLimit); e != nil {
		t.Fatal("Unicode character capacity wrong", e)
	}
	if e := ValidateWorkflowArguments(append(atLimit, ' ')); e == nil {
		t.Fatal("character limit not enforced")
	}
	if e := ValidateWorkflowArguments(json.RawMessage("{\"text\":\"\xff\"}")); e == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}

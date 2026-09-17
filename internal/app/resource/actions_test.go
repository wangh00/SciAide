package resource

import (
	"encoding/json"
	"testing"
	"time"
)

func TestActionIdentityBindsRunScopeAndSealedArguments(t *testing.T) {
	s := Scope{RunID: "r", ProjectID: "p", TaskID: "task", WorkspaceRoot: "root", WorkflowStepID: "step", ToolContracts: map[string]string{"builtin.workspace.read_text": "contract"}}
	seed := Seed{Kind: "workspace_read", Label: "read data", ToolName: "builtin.workspace.read_text", Arguments: json.RawMessage(`{"path":"data.csv"}`)}
	a := NewAction(s, seed, false, time.Now())
	if err := a.Validate(s); err != nil {
		t.Fatal(err)
	}
	for _, alter := range []func(*Scope){func(s *Scope) { s.RunID = "other" }, func(s *Scope) { s.TaskID = "other" }, func(s *Scope) { s.WorkspaceRoot = "other" }, func(s *Scope) { s.WorkflowStepID = "other" }} {
		other := s
		alter(&other)
		if a.Validate(other) == nil {
			t.Fatal("accepted foreign scope")
		}
	}
	changed := a
	changed.Seed.Arguments = json.RawMessage(`{"path":"../data.csv"}`)
	if changed.Validate(s) == nil {
		t.Fatal("accepted modified sealed locator")
	}
	b := NewAction(s, seed, false, time.Now().Add(time.Hour))
	if a.ID != b.ID {
		t.Fatal("reference changed after restart")
	}
}

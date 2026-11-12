package resource

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestResourceOperationMismatchExplainsIssuedCorrectOperation(t *testing.T) {
	v := View{OpenIDs: map[string]bool{"res_open": true}, SearchIDs: map[string]bool{"res_search": true}}
	for _, tt := range []struct{ tool, id, correct string }{{SearchTool, "res_open", OpenTool}, {OpenTool, "res_search", SearchTool}} {
		args, _ := json.Marshal(map[string]string{"actionId": tt.id})
		if v.Allows(tt.tool, args) {
			t.Fatal("wrong operation allowed")
		}
		msg := v.RejectionMessage(tt.tool, args)
		if !strings.Contains(msg, "资源操作类型不匹配") || !strings.Contains(msg, tt.id) || !strings.Contains(msg, tt.correct) {
			t.Fatal(msg)
		}
	}
	if strings.Contains(v.RejectionMessage(SearchTool, json.RawMessage(`{"actionId":"foreign"}`)), "actionId res_open") {
		t.Fatal("guessed replacement for unissued ID")
	}
}

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

func TestUnknownResourceReturnsOnlyIssuedRecoveryChoices(t *testing.T) {
	v := View{OpenIDs: map[string]bool{"res_current": true}, RecoveryOptions: []Option{{ID: "res_foreign", Tool: OpenTool, Label: "foreign"}, {ID: "res_current", Tool: OpenTool, Label: "方法手册"}}}
	args := json.RawMessage(`{"actionId":"res_stale"}`)
	if v.Allows(OpenTool, args) {
		t.Fatal("stale ID accepted")
	}
	message := v.RejectionMessage(OpenTool, args)
	if !strings.Contains(message, "res_current") || strings.Contains(message, "res_foreign") || !strings.Contains(message, "并未执行") {
		t.Fatal(message)
	}
}

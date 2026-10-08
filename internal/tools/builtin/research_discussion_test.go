package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestTaskReadContractSupportsExactHistoryWithoutExecution(t *testing.T) {
	tools := NewResearchDiscussionTools(nil)
	definition, err := tools[0].Definition(context.Background())
	if err != nil || definition.QualifiedName != workflow.ResearchTaskReadTool || definition.Version != "7" || definition.Risk != tool.RiskLow || !definition.Idempotent || len(definition.Permissions) != 0 {
		t.Fatal("task memory changed the read-only contract", definition, err)
	}
	for _, section := range []string{"revision_inputs", "revision_artifacts", "revision_stage_input", "revision_stage_output", "effective_inputs", "source_inputs", "source_content"} {
		args, _ := json.Marshal(map[string]any{"section": section, "revisionId": "confirmed-revision", "offset": 6000})
		if err := (tool.JSONSchemaValidator{}).Validate(definition.InputSchema, args); err != nil {
			t.Fatal(section, err)
		}
	}
	if err := (tool.JSONSchemaValidator{}).Validate(definition.InputSchema, json.RawMessage(`{"section":"overview","runId":"another-task"}`)); err == nil {
		t.Fatal("model can choose an arbitrary research run")
	}
	if _, err := tools[0].Invoke(context.Background(), tool.Invocation{SubjectKind: tool.SubjectWorkflowRun, Arguments: json.RawMessage(`{"section":"overview"}`)}); err == nil {
		t.Fatal("unbound invocation accepted")
	}
	proposal, err := tools[1].Definition(context.Background())
	if err != nil || proposal.QualifiedName != workflow.ResearchRevisionProposeTool || proposal.Version != "5" {
		t.Fatal("read expansion modified the proposal protocol", err)
	}
	for _, selection := range []string{`{"attachmentIds":["sent"]}`, `{"attachmentIds":[]}`} {
		args := json.RawMessage(`{"nodeId":"method_implementation","summary":"Reuse source","changes":["Adapt method"],"reason":"User request","sourceInputs":` + selection + `}`)
		if err := (tool.JSONSchemaValidator{}).Validate(proposal.InputSchema, args); err != nil {
			t.Fatal(err)
		}
	}
	for _, selection := range []string{`{}`, `{"attachmentIds":["a","b"]}`, `{"path":"private.py"}`} {
		args := json.RawMessage(`{"nodeId":"method_implementation","summary":"Reuse source","changes":["Adapt method"],"reason":"User request","sourceInputs":` + selection + `}`)
		if err := (tool.JSONSchemaValidator{}).Validate(proposal.InputSchema, args); err == nil {
			t.Fatal("ambiguous source contract accepted", selection)
		}
	}
}

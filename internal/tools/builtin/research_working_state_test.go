package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestResearchWorkingStateToolContract(t *testing.T) {
	value := NewResearchWorkingStateUpdate(nil)
	definition, err := value.Definition(context.Background())
	if err != nil || tool.ValidateDefinition(definition) != nil {
		t.Fatalf("invalid definition: %+v, %v", definition, err)
	}
	if definition.QualifiedName != workflow.ResearchStateUpdateTool || definition.Idempotent || definition.Risk != tool.RiskModerate {
		t.Fatalf("unexpected scope or concurrency contract: %+v", definition)
	}
	if _, err := value.Invoke(context.Background(), tool.Invocation{SubjectKind: tool.SubjectWorkflowRun}); err == nil {
		t.Fatal("accepted a workflow-run invocation")
	}
}

func TestResearchWorkingStateReadProvenanceContract(t *testing.T) {
	d, err := NewResearchWorkingStateRead(nil).Definition(context.Background())
	if err != nil || tool.ValidateDefinition(d) != nil || d.Version != "2" || d.Risk != tool.RiskLow || !d.Idempotent {
		t.Fatal("invalid on-demand source read definition", d, err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(d.InputSchema, json.RawMessage(`{"expectedVersion":2,"field":"provenance","offset":400,"limit":400}`)); err != nil {
		t.Fatal("existing read tool cannot address per-field submission provenance", err)
	}
	if _, err := NewResearchWorkingStateRead(nil).Invoke(context.Background(), tool.Invocation{SubjectKind: tool.SubjectWorkflowRun}); err == nil {
		t.Fatal("frozen workflow gained source-history access")
	}
}

package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestResearchPlanPrepareOptionalIdeaStillRejectsModelChosenIdentity(t *testing.T) {
	definition, err := NewResearchPlanPrepare(nil).Definition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	validator := tool.JSONSchemaValidator{}
	for _, input := range []string{`{}`, `{"referenceAttachmentIds":[]}`, `{"researchIdea":"explicit goal"}`, `{"researchIdea":"目标","referenceAttachmentIds":["material"]}`} {
		if err := validator.Validate(definition.InputSchema, json.RawMessage(input)); err != nil {
			t.Fatalf("valid preparation rejected: %s: %v", input, err)
		}
	}
	for _, input := range []string{`{"researchIdea":""}`, `{"researchIdea":null}`, `{"researchIdea":4}`, `{"humanId":"other"}`, `{"researchTaskId":"other"}`, `{"discussionHistory":[]}`, `{"runId":"other"}`, `{"referenceAttachmentIds":["same","same"]}`} {
		if err := validator.Validate(definition.InputSchema, json.RawMessage(input)); err == nil {
			t.Fatalf("invalid or model-chosen identity accepted: %s", input)
		}
	}
	if definition.Version != "2" || !definition.Idempotent || definition.Risk != tool.RiskLow {
		t.Fatal("preparation contract version or original safety metadata changed", definition)
	}
	if _, err := NewResearchPlanPrepare(nil).Invoke(context.Background(), tool.Invocation{SubjectKind: tool.SubjectWorkflowRun, Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("missing real human tool-call owner was accepted")
	}
}

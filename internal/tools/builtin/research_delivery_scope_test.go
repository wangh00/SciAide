package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestResearchDiscussionDeliveryScopeToolContract(t *testing.T) {
	d, err := (&ResearchDiscussion{proposal: true}).Definition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	validator := tool.JSONSchemaValidator{}
	if err = validator.ValidateSchema(d.InputSchema); err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"nodeId": "report_drafting", "summary": "bounded report", "changes": []string{"keep scientific limits"}, "reason": "explicit user negotiation"}
	for _, scope := range []any{nil, map[string]any{"summary": "limited report", "successCriteria": []string{"reported outcomes", "unverified work disclosed"}}} {
		if scope != nil {
			base["deliveryScope"] = scope
		}
		b, _ := json.Marshal(base)
		if err = validator.Validate(d.InputSchema, b); err != nil {
			t.Fatal(err)
		}
	}
	base["deliveryScope"] = map[string]any{"summary": "not enough"}
	b, _ := json.Marshal(base)
	if err = validator.Validate(d.InputSchema, b); err == nil {
		t.Fatal("incomplete proposed criteria accepted")
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

func mixedScopeRepairFixture(t *testing.T) (string, json.RawMessage, error, *citationRepair, string) {
	t.Helper()
	var schema map[string]any
	_ = json.Unmarshal(quoteRepairSchema, &schema)
	schema["additionalProperties"] = false
	props := schema["properties"].(map[string]any)
	props["evidenceGaps"] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"field", "status", "required"}, "properties": map[string]any{"field": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"not_retrieved", "not_discussed_in_checked_scope"}}, "required": map[string]any{"type": "boolean"}}}}
	props["coverage"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"sufficientForClaimedScope"}, "properties": map[string]any{"sufficientForClaimedScope": map[string]any{"type": "boolean"}}}
	encoded, _ := json.Marshal(schema)
	base := strings.Replace(quoteRepairBase, `{"count":`, `{"evidenceGaps":[{"field":"effect","status":"not_discussed_in_checked_scope","required":true}],"coverage":{"sufficientForClaimedScope":false},"count":`, 1)
	err := &workflow.EvidenceSubmissionErrors{Scope: errors.New("missing actual read receipt"), Citations: &workflow.CitationQuoteErrors{Issues: []workflow.CitationQuoteError{{Reference: "[K-A]", SupportingQuote: "invented prefix. Exact evidence.", CandidateQuote: "Exact evidence."}, {Reference: "[K-B]", SupportingQuote: "Other evidence.", CandidateQuote: "Source &gt; result.\nThe advantage oftesting is reported."}}}}
	r := newCitationRepairForError(base, encoded, err)
	if r == nil || !r.scopeUpdates || len(r.sources) != 2 {
		t.Fatal("missing combined correction interface")
	}
	selections := []any{}
	for _, source := range r.sources {
		ids := []string{}
		for _, s := range source.Segments {
			ids = append(ids, s.ID)
		}
		selections = append(selections, map[string]any{"reference": source.Reference, "sourceSegmentIds": ids})
	}
	patch, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "citationQuoteSelections": selections, "evidenceScopeUpdates": map[string]any{"evidenceGaps": []any{map[string]any{"field": "effect", "status": "not_retrieved", "required": true}}}})
	return base, encoded, err, r, string(patch)
}

func TestMixedScopeQuoteRepairIsBoundedAndAtomic(t *testing.T) {
	base, schema, _, r, patch := mixedScopeRepairFixture(t)
	if err := (tool.JSONSchemaValidator{}).ValidateSchema(r.schema); err != nil {
		t.Fatal(err)
	}
	merged, err := r.merge(patch)
	if err != nil {
		t.Fatal(err)
	}
	if err := (tool.JSONSchemaValidator{}).Validate(schema, json.RawMessage(merged)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, "9007199254740993") || !strings.Contains(merged, "&gt;") && !strings.Contains(merged, `\u0026gt;`) || !strings.Contains(merged, "oftesting") {
		t.Fatal("source or unmentioned field rewritten")
	}
	var a, b map[string]json.RawMessage
	_ = json.Unmarshal([]byte(base), &a)
	_ = json.Unmarshal([]byte(merged), &b)
	if string(a["count"]) != string(b["count"]) || string(a["coverage"]) != string(b["coverage"]) {
		t.Fatalf("unrequested field update: count %s -> %s; coverage %s -> %s", a["count"], b["count"], a["coverage"], b["coverage"])
	}
	for _, bad := range []string{strings.Replace(patch, r.hash, "wrong", 1), strings.Replace(patch, `"evidenceScopeUpdates":{`, `"evidenceScopeUpdates":{"summary":"automatic replacement",`, 1), strings.Replace(patch, `"required":true`, `"required":"false"`, 1)} {
		if _, err := r.merge(bad); err == nil {
			t.Fatal("invalid scope/hash update accepted")
		}
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(patch), &p)
	delete(p, "evidenceScopeUpdates")
	missing, _ := json.Marshal(p)
	if _, err := r.merge(string(missing)); err == nil {
		t.Fatal("mixed repair hid unresolved scope")
	}
	pure := newCitationRepair(base, schema, &workflow.CitationQuoteError{Reference: "[K-A]"})
	if _, err := pure.merge(patch); err == nil {
		t.Fatal("scope changes allowed without scope diagnosis")
	}
}

func TestFormatThenMixedScopeQuotesCompletesWithinOriginalTwoCorrections(t *testing.T) {
	base, schema, mixed, r, patch := mixedScopeRepairFixture(t)
	merged, err := r.merge(patch)
	if err != nil {
		t.Fatal(err)
	}
	malformed := "```json\n" + base + "}\n```"
	loop, state, provider := newLoopFixture(t, nil, submissionScript(malformed), submissionScript(base), submissionScript(patch))
	state.workflowAI = true
	host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", StructuredOutputRequired: true, StructuredOutputSchema: schema}}, accepted: json.RawMessage(merged), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "unexpected JSON trailing content"}, &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: mixed.Error(), Cause: mixed}}}
	loop.research = host
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted {
		t.Fatal("same-run correction failed", out, state.run.ErrorMessage)
	}
	requests := provider.Requests()
	if len(requests) != 3 || requests[1].ForcedTool != stageSubmissionTool || len(requests[2].Tools) != 2 || len(state.calls) != 0 {
		t.Fatal("correction repeated exploration or changed attempt budget")
	}
	if len(host.validated) != 3 || host.validated[2][3] != merged || state.journalDrafts[3] != patch {
		t.Fatal("raw patch audit or complete revalidation lost")
	}
}

func TestMixedScopeRepairPreservesPermissiveRawJSONBase(t *testing.T) {
	base, schema, mixed, _, _ := mixedScopeRepairFixture(t)
	var root map[string]any
	_ = json.Unmarshal(schema, &root)
	delete(root, "additionalProperties")
	permissive, _ := json.Marshal(root)
	r := newCitationRepairForError(base, permissive, mixed)
	if r == nil || string(r.base) != base {
		t.Fatal("repair inferred a nested object instead of preserving the complete raw base")
	}
	if strings.Contains(researchSubmissionFailureMessage(mixed), "missing actual") || !strings.Contains(researchSubmissionFailureMessage(mixed), "证据范围声明") {
		t.Fatal("failure banner lost category or leaked diagnostic")
	}
}

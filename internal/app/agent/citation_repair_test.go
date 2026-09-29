package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

var quoteRepairSchema = json.RawMessage(`{"type":"object","required":["citationAssessments","count"],"properties":{"count":{"type":"integer"},"citationAssessments":{"type":"array","items":{"type":"object","required":["reference","supportingQuote"],"properties":{"reference":{"type":"string"},"supportingQuote":{"type":"string"}}}}}}`)

const quoteRepairBase = `{"count":9007199254740993,"citationAssessments":[{"reference":"[K-A]","supportingQuote":"invented prefix. Exact evidence."},{"reference":"[K-B]","supportingQuote":"Other evidence."}]}`

func quotePatch(r *citationRepair, quote string) string {
	return fmt.Sprintf(`{"baseSHA256":%q,"citationAssessmentsPatch":[{"reference":%q,"supportingQuote":%q}]}`, r.hash, r.reference, quote)
}

func TestCitationRepairStrictScope(t *testing.T) {
	r := newCitationRepair(quoteRepairBase, quoteRepairSchema, &workflow.CitationQuoteError{Reference: "[K-A]"})
	if r == nil {
		t.Fatal("missing repair")
	}
	patch := quotePatch(r, "Exact evidence.")
	if err := (tool.JSONSchemaValidator{}).ValidateSchema(r.submissionSchema(quoteRepairSchema)); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{patch, quoteRepairBase} {
		if err := (tool.JSONSchemaValidator{}).Validate(r.submissionSchema(quoteRepairSchema), json.RawMessage(candidate)); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := r.merge(patch)
	if err != nil || !strings.Contains(merged, "9007199254740993") || !strings.Contains(merged, "Other evidence.") || strings.Contains(merged, "invented") {
		t.Fatalf("%s %v", merged, err)
	}
	for _, bad := range []string{
		strings.Replace(patch, r.hash, "wrong", 1), strings.Replace(patch, "[K-A]", "[K-B]", 1),
		strings.Replace(patch, `"supportingQuote":`, `"decision":"exclude","supportingQuote":`, 1),
		strings.Replace(patch, `}]}`, `},{"reference":"[K-A]","supportingQuote":"duplicate"}]}`, 1),
		strings.Replace(patch, `"baseSHA256":`, `"count":0,"baseSHA256":`, 1),
	} {
		if _, err := r.merge(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if got, err := r.merge(quoteRepairBase); err != nil || got != quoteRepairBase {
		t.Fatal("full fallback changed")
	}
}

func TestCitationRepairLoopAuditsPatchAndValidatesFullResult(t *testing.T) {
	r := newCitationRepair(quoteRepairBase, quoteRepairSchema, &workflow.CitationQuoteError{Reference: "[K-A]"})
	patch := quotePatch(r, "Exact evidence.")
	merged, _ := r.merge(patch)
	for _, dedicated := range []bool{false, true} {
		loop, state, provider := newLoopFixture(t, nil, submissionScript(quoteRepairBase), submissionScript(patch))
		state.workflowAI = true
		guidance := &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowStepID: "step", StructuredOutputRequired: true, StructuredOutputSchema: quoteRepairSchema}}
		if dedicated {
			state.run.ModelTurns = 12
			loop.resources = &resourceViewFixture{}
			guidance.value.SkillDiscovery = true
			guidance.value.AllowedToolNames = []string{resource.OpenTool}
		}
		host := &submissionHostFixture{staticResearchGuidance: guidance, accepted: json.RawMessage(merged), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.CitationQuoteError{Reference: "[K-A]", CandidateQuote: "Exact evidence.", SupportingQuote: "invented prefix. Exact evidence."}}}}
		loop.research = host
		if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted {
			t.Fatalf("%s %s", out, state.run.ErrorMessage)
		}
		if len(host.validated) != 2 || host.validated[1][3] != merged || state.messages[1].Parts[0].Text != merged {
			t.Fatal("host/final did not receive merged result")
		}
		if state.journalDrafts[state.run.ModelTurns] != patch {
			t.Fatal("raw patch lost from audit")
		}
		req := provider.Requests()[1]
		if dedicated {
			if req.ForcedTool != stageSubmissionTool || len(req.Tools) != 1 {
				t.Fatal("repair escaped submission interface")
			}
			if err := (tool.JSONSchemaValidator{}).Validate(req.Tools[0].InputSchema, json.RawMessage(patch)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCitationRepairCannotBypassLaterBusinessFailure(t *testing.T) {
	r := newCitationRepair(quoteRepairBase, quoteRepairSchema, &workflow.CitationQuoteError{Reference: "[K-A]"})
	patch := quotePatch(r, "Exact evidence.")
	merged, _ := r.merge(patch)
	loop, state, provider := newLoopFixture(t, nil, submissionScript(quoteRepairBase), submissionScript(patch), submissionScript(merged))
	state.workflowAI = true
	bad := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: "other source level is invalid"}
	host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: quoteRepairSchema}}, errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.CitationQuoteError{Reference: "[K-A]"}}, bad, bad}}
	loop.research = host
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != 3 {
		t.Fatalf("%s %+v", out, state.run)
	}
	if host.validated[1][3] != merged {
		t.Fatal("partial patch bypassed full host validation")
	}
}

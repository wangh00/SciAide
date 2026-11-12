package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

func identityRepairFixture() (string, json.RawMessage, error) {
	base := `{"summary":"keep scientific judgment","candidateAssessments":[{"candidateId":"wrong","decision":"support"}],"evidenceNotes":[{"candidateId":"wrong","quotes":[{"field":"title","segmentId":"t0001"}]}],"count":9007199254740993}`
	schema := json.RawMessage(`{"type":"object","required":["summary","candidateAssessments","evidenceNotes"],"properties":{"summary":{"type":"string"},"candidateAssessments":{"type":"array"},"evidenceNotes":{"type":"array"}}}`)
	e := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: &workflow.LiteratureIdentityErrors{Local: true, Cells: []workflow.LiteratureIdentityCell{{Path: "/candidateAssessments/0/candidateId", Value: "wrong", AllowedIDs: []string{"source"}}, {Path: "/evidenceNotes/0/candidateId", Value: "wrong", AllowedIDs: []string{"source"}}}}}
	return base, schema, e
}
func identityPatch(r *literatureIdentityRepair) string {
	b, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "candidateIdentityPatch": []map[string]string{{"path": "/candidateAssessments/0/candidateId", "candidateId": "source"}, {"path": "/evidenceNotes/0/candidateId", "candidateId": "source"}}})
	return string(b)
}
func TestIdentityRepairPreservesOtherFieldsAndRefusesStaleOrUnauthorizedEdits(t *testing.T) {
	base, schema, e := identityRepairFixture()
	r := newLiteratureIdentityRepair(base, schema, e)
	if r == nil {
		t.Fatal("no correction")
	}
	patch := identityPatch(r)
	merged, err := r.merge(patch)
	if err != nil || !strings.Contains(merged, "9007199254740993") || !strings.Contains(merged, "keep scientific judgment") || strings.Contains(merged, "wrong") || string(r.base) != base {
		t.Fatal(merged, err)
	}
	for _, bad := range []string{strings.Replace(patch, r.hash, strings.Repeat("a", 64), 1), strings.Replace(patch, "source", "not-offered", 1), strings.Replace(patch, "/evidenceNotes/0/candidateId", "/candidateAssessments/0/candidateId", 1), strings.Replace(patch, "/evidenceNotes/0/candidateId", "/summary", 1)} {
		if _, e := r.merge(bad); e == nil {
			t.Fatal("bad patch accepted", bad)
		}
	}
}
func TestIdentityCorrectionUsesSameRunSubmissionWithoutRepeatingResearch(t *testing.T) {
	base, schema, e := identityRepairFixture()
	r := newLiteratureIdentityRepair(base, schema, e)
	patch := identityPatch(r)
	merged, _ := r.merge(patch)
	loop, state, provider := newLoopFixture(t, nil, submissionScript(base), submissionScript(patch))
	state.workflowAI = true
	host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: schema}}, errors: []error{e}, accepted: json.RawMessage(merged)}
	loop.research = host
	if out := loop.Run(context.Background(), state.run.ID); out != OutcomeCompleted || len(provider.Requests()) != 2 || len(host.validated) != 2 {
		t.Fatal(out, state.run.ErrorMessage)
	}
	if host.validated[1][3] != merged || state.journalDrafts[2] != patch || len(state.calls) != 0 || len(provider.Requests()[1].Tools) != 2 {
		t.Fatal("patch not audited/full-validated or repeated tools")
	}
}

func TestPromptRejectionUsageIsNotReportedAsServerFailure(t *testing.T) {
	if requestStatusForErrorCode("MODEL_PROMPT_REJECTED") != 400 {
		t.Fatal("policy refusal misreported as server outage")
	}
}

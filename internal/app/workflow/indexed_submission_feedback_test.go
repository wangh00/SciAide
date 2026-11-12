package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/apperr"
)

func TestIndexedSubmissionNewReadsDoNotSuppressSameChatFeedback(t *testing.T) {
	d, step, node, execution, calls := indexedFixture()
	ref := calls.calls[0].Result.Citations[0]
	step.Input = mustJSON(map[string]any{"documentIds": []string{"doc"}, "candidates": []tool.CitationRef{}, "_selectedEvidence": selectedEvidenceInput{Direct: true, Phase: "coverage", Documents: []string{"doc"}}})
	step.InputSHA256 = hashJSON(step.Input)
	execution.InputSHA256 = step.InputSHA256
	d.Steps[2], d.AIExecutions = step, []AIExecution{execution}
	d.Run.Status, d.Run.ConversationID, d.Run.CurrentStep = RunRunning, "conversation", step.Ordinal
	d.Run.Inputs, d.Run.InputsSHA256 = raw(`{}`), hashJSON(raw(`{}`))
	node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
	d.Run.Compilation.Nodes[0] = node
	encoded, err := canonicalJSON(d.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	d.Run.CompilationSHA256 = hashBytes(encoded)
	d.Run.Compilation.CompilationSHA256 = d.Run.CompilationSHA256
	repo := &submissionValidationRepository{detail: d}
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{})}
	output := map[string]any{
		"summary": "A bounded finding from a verified source fragment.", "recommendedReferences": []string{ref.Reference},
		"citationAssessments": []map[string]any{{"reference": ref.Reference, "decision": "support", "sourceLevel": "full_text", "reason": "The fragment reports a mechanism.", "supportingQuote": ref.Quote}},
		"coverage":            map[string]any{"strength": "limited", "sufficientForClaimedScope": false, "independentStudyEstimate": 1, "directPopulationEvidence": false, "fullTextEvidenceAvailable": true, "gaps": []string{"Only a fragment was checked."}},
		"recommendedScope":    "A fragment-level account, not an effectiveness conclusion.", "recommendation": "proceed_limited", "limitations": []string{"Not a full-paper appraisal."},
		"documentAnalyses": []map[string]any{{"documentId": "doc", "finding": "Mechanism described in the checked fragment.", "limitations": "A fragment only.", "references": []string{ref.Reference}}},
	}
	before := mustJSON(repo.detail)
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{name: "valid newly read source"},
		{name: "invented quote", edit: func(v map[string]any) {
			v["citationAssessments"].([]any)[0].(map[string]any)["supportingQuote"] = "Invented result."
		}, want: "supportingQuote"},
		{name: "missing assessment", edit: func(v map[string]any) { v["citationAssessments"] = []any{} }, want: "no corresponding citationAssessments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var candidate map[string]any
			if err := json.Unmarshal(mustJSON(output), &candidate); err != nil {
				t.Fatal(err)
			}
			if tc.edit != nil {
				tc.edit(candidate)
			}
			value, err := s.ValidateResearchSubmission(context.Background(), "conversation", d.Run.ID, step.ID, string(mustJSON(candidate)))
			if tc.want == "" {
				if err != nil || len(value) == 0 {
					t.Fatal("valid complete synthesis of same-Chat new reads was rejected", err)
				}
			} else {
				var classified *apperr.Error
				if !errors.As(err, &classified) || classified.Code != "WORKFLOW_AI_SUBMISSION_INVALID" || !strings.Contains(err.Error(), tc.want) {
					t.Fatal("new evidence hid the original same-Chat scientific submission feedback", err)
				}
			}
			if !rawJSONEqual(before, mustJSON(repo.detail)) {
				t.Fatal("read-only precommit validation changed the task")
			}
		})
	}
}

func TestSelectedEvidenceCollectionOnlyDoesNotHideOtherDefects(t *testing.T) {
	node := CompiledNode{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion}
	missing := &CitationAssessmentErrors{Issues: []CitationAssessmentIssue{{Path: "recommendation", Problem: "proceed requires supported citations"}}}
	draft := raw(`{"recommendation":"proceed","recommendedReferences":[],"citationAssessments":[]}`)
	if !selectedEvidenceCollectionOnly(node, draft, missing) {
		t.Fatal("citation-free collection draft no longer reaches the original new-source checkpoint")
	}
	for _, tc := range []struct {
		name  string
		node  CompiledNode
		value json.RawMessage
		err   error
	}{
		{"valid bounded result", node, draft, nil},
		{"recommended source", node, raw(`{"recommendation":"proceed","recommendedReferences":["[K-A]"],"citationAssessments":[]}`), missing},
		{"source decision", node, raw(`{"recommendation":"proceed","recommendedReferences":[],"citationAssessments":[{}]}`), missing},
		{"missing assessment", node, draft, &CitationAssessmentErrors{Issues: []CitationAssessmentIssue{{Path: "recommendedReferences[0]"}}}},
		{"quotation defect", node, draft, &CitationAssessmentErrors{Issues: missing.Issues, QuoteIssues: []CitationQuoteError{{Reference: "[K-A]"}}}},
		{"additional scope error", node, draft, evidenceSubmissionErrors(errors.New("unverified read scope"), missing)},
		{"old contract", CompiledNode{ID: node.ID, PromptVersion: "selected-evidence-v1"}, draft, missing},
		{"other stage", CompiledNode{ID: "report_drafting", PromptVersion: selectedEvidenceVersion}, draft, missing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if selectedEvidenceCollectionOnly(tc.node, tc.value, tc.err) {
				t.Fatal("another submission defect was mistaken for a collection-only checkpoint")
			}
		})
	}
}

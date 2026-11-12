package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
)

func TestMixedCitationDefectsUseFullCorrectionNotQuoteOnlyPatch(t *testing.T) {
	issues := &workflow.CitationAssessmentErrors{
		Issues:      []workflow.CitationAssessmentIssue{{Path: "recommendedReferences[1]", Reference: "[K-B]", Problem: "recommended citation has no corresponding citationAssessments entry"}},
		QuoteIssues: []workflow.CitationQuoteError{{Reference: "[K-A]", SupportingQuote: "Invented evidence.", CandidateQuote: "Exact evidence."}},
	}
	err := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Details: issues.Error(), Cause: issues}
	for _, succeed := range []bool{true, false} {
		first := quoteRepairBase
		second := quoteRepairBase
		last := quoteRepairBase
		hostErrors := []error{err}
		if !succeed {
			hostErrors = []error{err, err, err}
		}
		loop, state, provider := newLoopFixture(t, nil, submissionScript(first), submissionScript(second), submissionScript(last))
		state.workflowAI = true
		loop.research = &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: quoteRepairSchema}}, accepted: json.RawMessage(quoteRepairBase), errors: hostErrors}
		got := loop.Run(context.Background(), state.run.ID)
		requests := provider.Requests()
		if len(requests) < 2 || len(requests) > 3 {
			t.Fatal("mixed error changed the two-correction budget", len(requests))
		}
		feedback := submissionRequestText(requests[1])
		if requests[1].ForcedTool != stageSubmissionTool || !strings.Contains(feedback, "[K-B]") || !strings.Contains(feedback, "Exact evidence.") || !strings.Contains(feedback, "完整结果") || strings.Contains(feedback, "citationAssessmentsPatch") {
			t.Fatal("mixed defects offered an insufficient local quotation patch", feedback)
		}
		if succeed && (got != OutcomeCompleted || len(requests) != 2) {
			t.Fatal("complete correction did not finish in the same run", got)
		}
		if !succeed && (got != OutcomeFailed || len(requests) != 3 || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || state.messages[1].Parts[0].Text != "") {
			t.Fatal("uncorrected mixed error was accepted or restarted", got, state.run)
		}
		if !succeed && (!strings.Contains(state.run.ErrorMessage, "证据判定") || strings.Contains(state.run.ErrorMessage, "Exact evidence.") || !strings.Contains(state.run.ErrorDetails, "[K-B]")) {
			t.Fatal("short error banner lost its category or leaked source excerpts", state.run)
		}
	}
}

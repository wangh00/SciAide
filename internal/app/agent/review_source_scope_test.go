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

func sharedReviewSourceFixture(t *testing.T) (*reviewRepair, *workflow.ReviewBasisErrors) {
	t.Helper()
	first := "/reviewFindings/0/basis/0/quote"
	second := "/reviewFindings/0/basis/1/quote"
	version := strings.Repeat("f", 64)
	base := strings.Replace(reviewRepairTestBase, "/evidenceScreening/evidenceGaps/0", "/context/markdown", 1)
	d := &workflow.ReviewBasisErrors{Problems: []string{"quote mismatch", "quote mismatch"}, RepairPaths: []string{first, second}, Sources: []workflow.ReviewBasisSource{
		{Cell: first, Pointer: "/context/markdown", InputSHA256: version, Segments: []workflow.ReviewBasisSegment{{ID: "shared-1", Start: 0, End: 9, Text: "Current. "}, {ID: "shared-2", Start: 9, End: 18, Text: "Not old. "}}},
		{Cell: second, Pointer: "/context/markdown", InputSHA256: version, Segments: []workflow.ReviewBasisSegment{{ID: "shared-2", Start: 9, End: 18, Text: "Not old. "}, {ID: "shared-4", Start: 27, End: 32, Text: "Tail."}}},
	}}
	r := newReviewRepair(base, reviewRepairTestSchema, d)
	if r == nil {
		t.Fatal("no repair")
	}
	return r, d
}

func reviewSelectionPatch(r *reviewRepair, cell string, ids ...string) string {
	b, _ := json.Marshal(map[string]any{"baseSHA256": r.hash, "reviewBasisSelections": []any{map[string]any{"path": cell, "sourceSegmentIds": ids}}})
	return string(b)
}

func TestReviewSameSourceCellsShareOnlyOfferedVersionAndOffsets(t *testing.T) {
	r, _ := sharedReviewSourceFixture(t)
	cell := r.sources[1].Cell
	patch := reviewSelectionPatch(r, cell, "shared-1", "shared-2")
	if err := (tool.JSONSchemaValidator{}).Validate(r.schema, []byte(patch)); err != nil {
		t.Fatal(err)
	}
	merged, err := r.merge(patch)
	if err != nil || !strings.Contains(merged, `"quote":"Current. Not old. "`) {
		t.Fatal("same-source selection rejected", err)
	}
	if strings.Count(string(r.schema), `"shared-2"`) != 1 {
		t.Fatal("duplicate source IDs inflate schema")
	}
	if !strings.Contains(r.instruction(), "same pointer and inputSHA256") {
		t.Fatal("shared-source rule absent")
	}
	for _, kind := range []string{"different pointer", "different version", "missing version", "conflicting ID", "gap", "reverse", "repeat"} {
		t.Run(kind, func(t *testing.T) {
			r, _ := sharedReviewSourceFixture(t)
			ids := []string{"shared-1", "shared-2"}
			switch kind {
			case "different pointer":
				r.sources[0].Pointer = "/context/other"
			case "different version":
				r.sources[0].InputSHA256 = "old"
			case "missing version":
				r.sources[0].InputSHA256 = ""
				r.sources[1].InputSHA256 = ""
			case "conflicting ID":
				r.sources[0].Segments[1].Text = "Not same"
			case "gap":
				ids = []string{"shared-2", "shared-4"}
			case "reverse":
				ids = []string{"shared-2", "shared-1"}
			case "repeat":
				ids = []string{"shared-1", "shared-1"}
			}
			before := string(r.base)
			_, err := r.merge(reviewSelectionPatch(r, cell, ids...))
			var selection *reviewRepairSelectionError
			var format *reviewRepairFormatError
			if !errors.As(err, &selection) || errors.As(err, &format) || string(r.base) != before {
				t.Fatal("source boundary or classification lost", err)
			}
		})
	}
}

func TestReviewSharedSelectionPreservesBudgetJournalAndLiveValidation(t *testing.T) {
	r, d := sharedReviewSourceFixture(t)
	good := reviewSelectionPatch(r, r.sources[1].Cell, "shared-1", "shared-2")
	bad := reviewSelectionPatch(r, r.sources[1].Cell, "shared-2", "shared-4")
	merged, err := r.merge(good)
	if err != nil {
		t.Fatal(err)
	}
	for _, success := range []bool{true, false} {
		last := bad
		if success {
			last = good
		}
		loop, state, provider := newLoopFixture(t, nil, submissionScript(string(r.base)), submissionScript(bad), submissionScript(last))
		state.workflowAI = true
		host := &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{StructuredOutputRequired: true, StructuredOutputSchema: reviewRepairTestSchema}}, accepted: json.RawMessage(merged), errors: []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", Cause: d}}}
		loop.research = host
		out := loop.Run(context.Background(), state.run.ID)
		if len(provider.Requests()) != 3 || state.journalDrafts[2] != bad || len(state.calls) != 0 {
			t.Fatal("budget/audit/tools changed")
		}
		if success && (out != OutcomeCompleted || len(host.validated) != 2 || host.validated[1][3] != merged) {
			t.Fatal("live complete validation bypassed")
		}
		if !success && (out != OutcomeFailed || len(host.validated) != 1 || !strings.Contains(state.run.ErrorMessage, "原文选段") || strings.Contains(state.run.ErrorMessage, "修正提交格式")) {
			t.Fatal("false format/scientific verdict", state.run.ErrorMessage)
		}
	}
}

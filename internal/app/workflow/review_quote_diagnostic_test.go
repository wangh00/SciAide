package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedReviewQuoteOnlyDiagnosticReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_QUOTE_ONLY_REPLAY")
	if dir == "" {
		t.Skip("no read-only incident capture supplied")
	}
	input, err := os.ReadFile(filepath.Join(dir, "review6-capture-input.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodeBytes, err := os.ReadFile(filepath.Join(dir, "review6-capture-node.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(dir, "review6-capture-schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var node CompiledNode
	if json.Unmarshal(nodeBytes, &node) != nil || node.ID != "independent_review" || !usesTrackedReview(node) {
		t.Fatal("not the captured tracked review node")
	}
	if !equalRevisionEditBase(node.OutputSchema, schema) {
		t.Fatal("captured node and execution schema differ")
	}
	var allQuoteOnly func(error) bool
	allQuoteOnly = func(err error) bool {
		if err == nil {
			return true
		}
		switch e := err.(type) {
		case *ReviewBasisErrors:
			return e.QuoteOnly && !e.Truncated && len(e.RepairPaths) > 0
		case interface{ Unwrap() []error }:
			for _, child := range e.Unwrap() {
				if !allQuoteOnly(child) {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	for _, turn := range []string{"2", "3", "4"} {
		candidate, err := os.ReadFile(filepath.Join(dir, "review6-capture-turn-"+turn+".json"))
		if err != nil {
			t.Fatal(err)
		}
		structured, _, err := normalizeWorkflowAIStageSubmissionForInput(string(candidate), node, input)
		if err != nil {
			t.Fatal("captured normalization failed", err)
		}
		fullDiagnostic := validateWorkflowAIStageOutput(node, structured, input)
		var diagnostic *ReviewBasisErrors
		if !errors.As(fullDiagnostic, &diagnostic) || !allQuoteOnly(fullDiagnostic) || len(diagnostic.Sources) != 2 {
			t.Fatal("complete captured rejection was not the expected fully navigable quote-only defect", fullDiagnostic)
		}
		proof := map[string]any{"version": "complete-review-replay-v1", "inputSHA256": hashJSON(input), "candidateSHA256": hashJSON(candidate), "nodeSHA256": hashJSON(nodeBytes), "schemaSHA256": hashJSON(schema), "fullValidationQuoteOnly": true, "diagnostic": diagnostic}
		if err := os.WriteFile(filepath.Join(dir, "derived-review6-quote-diagnostic-"+turn+".json"), mustJSON(proof), 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("turn=%s: production normalization + complete scientific validation: quote-only=true, problems=%d, current-source menus=%d; no model, no DB writes", turn, len(diagnostic.Problems), len(diagnostic.Sources))
	}
}

func TestLiveReviewDiagnosticDistinguishesQuoteCopyFromPointerAndMixedFailures(t *testing.T) {
	input := raw(`{"context":{"markdown":"当前誊写文本","n":3,"object":{"field":"text"}}}`)
	for _, tc := range []struct {
		name, pointer, quote string
		quoteOnly            bool
	}{
		{"string_copy", "/context/markdown", "当前誉写文本", true},
		{"missing_pointer", "/context/missing", "text", false},
		{"object_pointer", "/context/object", "text", false},
		{"numeric_scalar", "/context/n", "4", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			findings := []trackedReviewFinding{{ID: "issue-1-1", Basis: []reviewBasis{{Pointer: tc.pointer, Quote: tc.quote}}}}
			var diagnostic *ReviewBasisErrors
			if err := reviewBasisDiagnostics(findings, input); !errors.As(err, &diagnostic) || diagnostic.QuoteOnly != tc.quoteOnly || len(diagnostic.RepairPaths) != 2 {
				t.Fatal("diagnostic classification or legacy repair coordinates changed", err)
			}
		})
	}
}

func TestLiveReviewQuoteOnlyIgnoresValidNonTextBasisButRejectsMixedPointerErrors(t *testing.T) {
	input := raw(`{"context":{"markdown":"当前誊写文本","n":3}}`)
	findings := []trackedReviewFinding{{ID: "issue-1-1", Basis: []reviewBasis{{Pointer: "/context/markdown", Quote: "当前誉写文本"}, {Pointer: "/context/n", Quote: "3"}}}}
	var diagnostic *ReviewBasisErrors
	if err := reviewBasisDiagnostics(findings, input); !errors.As(err, &diagnostic) || !diagnostic.QuoteOnly {
		t.Fatal("valid numeric evidence disabled quote-only diagnosis", err)
	}
	findings[0].Basis = append(findings[0].Basis, reviewBasis{Pointer: "/context/missing", Quote: "text"})
	if err := reviewBasisDiagnostics(findings, input); !errors.As(err, &diagnostic) || diagnostic.QuoteOnly {
		t.Fatal("mixed pointer failure was marked quote-only", err)
	}
}

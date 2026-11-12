package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewCriterionRecoveryStillRequiresCurrentScientificEvidence(t *testing.T) {
	input, output := trackingFixture()
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "not_met", "Mean differs"}, {"criterion-2", "met", "Sources present"}}
	stage := decodeObject(input)
	stage["acceptanceCriteria"] = []any{map[string]any{"text": "Report the correct mean."}, map[string]any{"text": "Keep source provenance."}}
	input = mustJSON(stage)
	output["reviewedInputSha256"] = hashJSON(input)
	findings := output["reviewFindings"].([]trackedReviewFinding)
	findings[0].Basis = []reviewBasis{{"/acceptanceCriteria/1/text", "Report the correct mean."}}
	node := CompiledNode{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()}
	output["revisionPlan"] = []any{}
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(output)), node, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateTrackedReview(value, input); err == nil || !strings.Contains(err.Error(), "仅引用") {
		t.Fatal("requirement relocation became scientific support", err)
	}
	findings[0].Basis = append(findings[0].Basis, reviewBasis{"/context/markdown", "Result: 5"})
	value, _, err = normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(output)), node, input)
	if err != nil || validateTrackedReview(value, input) != nil {
		t.Fatal("valid rejection could not be saved", err)
	}
	output["approved"] = true
	value, _, err = normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(output)), node, input)
	if err != nil || validateTrackedReview(value, input) == nil {
		t.Fatal("relocated requirement allowed false approval", err)
	}
}

func TestReviewBasisDiagnosticsAccompanySchemaErrorWithoutAcceptingIt(t *testing.T) {
	input, output := trackingFixture()
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "not_met", "Mean differs"}}
	output["revisionPlan"] = []any{map[string]any{"correctionIndex": 0, "nodeId": "report_drafting", "reason": "Correct the number", "whyNotLater": "Report is the responsible stage"}}
	findings := output["reviewFindings"].([]trackedReviewFinding)
	findings[0].Basis[1] = reviewBasis{"/researchHandoff/attributionObservations/0/writtenDois/0", "Untrusted derived observation"}
	node := CompiledNode{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()}
	value, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(output)), node, input)
	var basisErrors *ReviewBasisErrors
	if err == nil || !strings.Contains(err.Error(), "needsUserInput") || !strings.Contains(err.Error(), "attributionObservations") || !errors.As(err, &basisErrors) || len(basisErrors.Problems) != 1 || len(value) != 0 || len(changes) != 0 {
		t.Fatal("schema and basis problems were not reported together", err)
	}
	// Fixing the path does NOT fill omitted semantic fields or accept the draft.
	findings[0].Basis[1] = reviewBasis{"/computedResults/mean", "3"}
	// Quotes can still be in the Chat citation domain until schema projection
	// succeeds. Never report that temporary spelling as an evidence mismatch.
	findings[0].Basis[0].Quote = "Finding [K-CHATDOMAIN01]"
	_, _, err = normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(output)), node, input)
	if err == nil || !strings.Contains(err.Error(), "needsUserInput") || errors.As(err, &basisErrors) {
		t.Fatal("schema requirement was silently defaulted", err)
	}
	// Invalid host schemas are not model-attribution errors, even with bad bases.
	badSchema := decodeObject(node.OutputSchema)
	badSchema["required"] = append(badSchema["required"].([]any), "reviewFindings")
	node.OutputSchema = mustJSON(badSchema)
	if !usesTrackedReview(node) || ValidateAIStageSchema(node.OutputSchema) == nil {
		t.Fatal("fixture must retain tracked-review identity and an invalid host schema")
	}
	findings[0].Basis[1].Pointer = "/reviewHistory/issuePrefix"
	_, _, err = normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(output)), node, input)
	if err == nil || errors.As(err, &basisErrors) {
		t.Fatal("host schema defect mislabeled", err)
	}
}

func TestReviewCriterionIndexUsesOnlyUniqueWholeCurrentRequirement(t *testing.T) {
	input := raw(`{"acceptanceCriteria":[{"id":"criterion-1","text":"Report actual screening decisions and exclusion reasons."},{"id":"criterion-2","text":"Separate calorie-matched comparisons."}],"context":{"markdown":"Report actual screening decisions and exclusion reasons."}}`)
	quote := "Report actual screening decisions and exclusion reasons."
	output := basisOnlyOutput("/acceptanceCriteria/1/text", quote)
	got, changes := canonicalReviewReadPaths(output, input)
	if !rawJSONEqual(got, basisOnlyOutput("/acceptanceCriteria/0/text", quote)) || len(changes) != 1 || changes[0].Rule != "frozen_criterion_index" {
		t.Fatalf("whole requirement was not unambiguously relocated: %s %+v", got, changes)
	}
	if changes[0].BeforeSHA256 != hashJSON(mustJSON("/acceptanceCriteria/1/text")) || changes[0].AfterSHA256 != hashJSON(mustJSON("/acceptanceCriteria/0/text")) {
		t.Fatal("missing pointer audit", changes)
	}
	again, changes := canonicalReviewReadPaths(got, input)
	if len(changes) != 0 || !rawJSONEqual(again, got) {
		t.Fatal("not idempotent")
	}
	_, requirement, err := reviewBasisAt(decodeObject(input), "/acceptanceCriteria/0/text")
	if err != nil || !requirement {
		t.Fatal("requirement became scientific evidence", err)
	}
}

func TestReviewCriterionIndexNeverGuessesEvidenceOrPartialRequirements(t *testing.T) {
	quote := "Report actual screening decisions and exclusion reasons."
	for _, tc := range []struct{ name, input, pointer, quote string }{
		{"partial", `{"acceptanceCriteria":[{"text":"Report actual screening decisions and exclusion reasons."},{"text":"Other requirement."}]}`, "/acceptanceCriteria/1/text", "screening decisions"},
		{"duplicate", `{"acceptanceCriteria":[{"text":"Same requirement."},{"text":"Other requirement."},{"text":"Same requirement."}]}`, "/acceptanceCriteria/1/text", "Same requirement."},
		{"invented", `{"acceptanceCriteria":[{"text":"Actual requirement."}]}`, "/acceptanceCriteria/1/text", quote},
		{"history only", `{"acceptanceCriteria":[{"text":"Current requirement."}],"reviewHistory":{"acceptanceCriteria":[{"text":"Old requirement."}]}}`, "/acceptanceCriteria/1/text", "Old requirement."},
		{"leading zero", `{"acceptanceCriteria":[{"text":"Actual requirement."}]}`, "/acceptanceCriteria/01/text", "Actual requirement."},
		{"negative", `{"acceptanceCriteria":[{"text":"Actual requirement."}]}`, "/acceptanceCriteria/-1/text", "Actual requirement."},
		{"invalid escape", `{"acceptanceCriteria":[{"text":"Actual requirement."}]}`, "/acceptanceCriteria/~2/text", "Actual requirement."},
		{"object", `{"acceptanceCriteria":[{"text":"Actual requirement."}]}`, "/acceptanceCriteria/1", "Actual requirement."},
		{"wrong field", `{"acceptanceCriteria":[{"text":"Actual requirement."}]}`, "/acceptanceCriteria/1/id", "Actual requirement."},
		{"scientific conflict", `{"evidenceContext":[{"quote":"Effect 3."},{"quote":"Effect 5."}]}`, "/evidenceContext/1/quote", "Effect 3."},
		{"requirement to evidence", `{"acceptanceCriteria":[{"text":"Actual requirement."}],"context":{"markdown":"Actual requirement."}}`, "/context/missing", "Actual requirement."},
		{"already matches", `{"acceptanceCriteria":[{"text":"Actual requirement."},{"text":"A longer Actual requirement."}]}`, "/acceptanceCriteria/1/text", "Actual requirement."},
		{"not text", `{"acceptanceCriteria":[{"text":false},{"text":"false"}]}`, "/acceptanceCriteria/0/text", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := basisOnlyOutput(tc.pointer, tc.quote)
			got, changes := canonicalReviewReadPaths(output, raw(tc.input))
			if len(changes) != 0 || !rawJSONEqual(got, output) {
				t.Fatal("guessed meaning", string(got))
			}
		})
	}
}

// Optional incident replay. Never reads the live DB or invokes a model.
func TestCapturedReviewCriterionFailurePreservesScientificRejection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_CRITERION_REPLAY")
	if dir == "" {
		t.Skip("no read-only incident capture supplied")
	}
	read := func(name string) json.RawMessage {
		t.Helper()
		value, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	input := read("input.json")
	node := CompiledNode{ID: "independent_review", OutputSchema: read("schema.json")}
	for _, name := range []string{"candidate-1.json", "candidate-2.json", "candidate-3.json"} {
		t.Run(name, func(t *testing.T) {
			original := read(name)
			if name == "candidate-3.json" {
				unrepaired, _, err := projectReviewSubmission(string(original), node)
				if err != nil {
					t.Fatal(err)
				}
				if err = validateWorkflowAIStageOutput(node, unrepaired, input); err == nil || !strings.Contains(err.Error(), "/acceptanceCriteria/5/text") {
					t.Fatal("original incident not reproduced", err)
				}
			}
			value, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(original), node, input)
			if name == "candidate-1.json" {
				if err == nil || !strings.Contains(err.Error(), "needsUserInput") || !strings.Contains(err.Error(), "attributionObservations") {
					t.Fatal("schema error masked independent basis errors", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			err = validateWorkflowAIStageOutput(node, value, input)
			if name == "candidate-2.json" {
				if err == nil || !strings.Contains(err.Error(), "attributionObservations") {
					t.Fatal("host observation accepted as evidence", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			before, after := decodeObject(original), decodeObject(value)
			if after["approved"] != false {
				t.Fatal("scientific rejection changed")
			}
			// Only one wrong requirement pointer and derived categories change.
			before["reviewFindings"].([]any)[3].(map[string]any)["basis"].([]any)[2].(map[string]any)["pointer"] = "/acceptanceCriteria/4/text"
			for _, key := range reviewCategoryFields {
				delete(before, key)
				delete(after, key)
			}
			if !rawJSONEqual(mustJSON(before), mustJSON(after)) {
				t.Fatal("changed conclusion, findings, quote, correction or repair destination")
			}
			found := false
			for _, change := range changes {
				found = found || change.Rule == "frozen_criterion_index"
			}
			if !found {
				t.Fatal("missing relocation audit")
			}
		})
	}
}

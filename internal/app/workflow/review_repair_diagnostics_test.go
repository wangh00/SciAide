package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewReportsMissingReasonsTogetherWithAllInvalidBasisPaths(t *testing.T) {
	input, output := trackingFixture()
	stage := decodeObject(input)
	stage["evidenceScreening"] = map[string]any{"evidenceGaps": []any{
		map[string]any{"field": "Actual energy intake", "required": true, "status": "not_retrieved"},
		map[string]any{"field": "Adverse events", "status": "not_retrieved"},
	}}
	findings := output["reviewFindings"].([]trackedReviewFinding)
	findings[0].ChangeReason = ""
	findings[0].Basis = []reviewBasis{{"/evidenceScreening/evidenceGaps/0", "Actual energy intake"}, {"/evidenceScreening/evidenceGaps/1", "Adverse events"}}
	err := validateTrackedReview(mustJSON(output), mustJSON(stage))
	for _, want := range []string{"/evidenceScreening/evidenceGaps/0/field", "/evidenceScreening/evidenceGaps/1/field", "changeReason", "issue-1-1"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("independent defect masked: %s; %v", want, err)
		}
	}
	var basis *ReviewBasisErrors
	if !errors.As(err, &basis) || len(basis.Problems) != 2 {
		t.Fatal("lost typed basis diagnostics", err)
	}
}

func TestReviewRejectsWholeCandidateAndReportsPlanAlongsideAttribution(t *testing.T) {
	input, output := trackingFixture()
	stage := decodeObject(input)
	stage["researchContract"] = map[string]any{"successCriteria": []string{"Use the correct mean"}}
	stage["acceptanceCriteria"] = []ResearchAcceptanceCriterion{{ID: "criterion-1", Text: "Use the correct mean"}}
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "not_met", "Mean is wrong"}}
	output["revisionPlan"] = []researchRevisionPlanItem{{0, "invented_stage", "Fix mean", "Report is responsible", false, "", nil}}
	findings := output["reviewFindings"].([]trackedReviewFinding)
	findings[0].ChangeReason = ""
	findings[0].Basis[1].Pointer = "/computedResults/missing"
	err := validateWorkflowAIStageOutput(CompiledNode{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()}, mustJSON(output), mustJSON(stage))
	for _, want := range []string{"/computedResults/missing", "changeReason", "invented_stage"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatal("independent review check masked by a prior error", err)
		}
	}
}

func TestReviewReportsCorrespondenceErrorsWithoutHidingThemBehindBasis(t *testing.T) {
	input, output := trackingFixture()
	findings := output["reviewFindings"].([]trackedReviewFinding)
	findings[0].ChangeReason = ""
	findings[0].CorrectionIndex = 5
	findings[0].Basis[1].Pointer = "/computedResults/missing"
	err := validateTrackedReview(mustJSON(output), input)
	for _, want := range []string{"/computedResults/missing", "changeReason", "requiredCorrections"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatal("independent correspondence defect hidden", err)
		}
	}
}

func TestReviewRepairPromptIdentityFitsAllHostMarkers(t *testing.T) {
	prompt := singleSourceReviewInstruction + reviewScopeInstruction + reportConfidenceInstruction + researchHandoffInstruction + researchInputSharingInstruction + implementationDecisionInstruction + ImplementationPartsInstruction + ImplementationPatchInstruction + implementationEfficiencyInstruction + computationAuditInstruction + kernelReviewNavigationInstruction
	for _, id := range []string{"independent_review", "method_implementation"} {
		node := CompiledNode{ID: id, PromptVersion: strings.Repeat("p", 128), OutputSchema: trackedResearchReviewSchema()}
		got := reportExecutionPromptVersion(node, prompt)
		if len(got) > 128 || !strings.Contains(got, "-rr1") && !strings.Contains(got, "-"+reviewRepairVersion) {
			t.Fatal("new prompt identity lost or exceeds storage", got)
		}
		node.PromptVersion = got
		if reportExecutionPromptVersion(node, prompt) != got {
			t.Fatal("prompt identity not idempotent", got)
		}
	}
}

func TestReviewScalarNavigationNeverRelocatesOrInventsEvidence(t *testing.T) {
	for _, tc := range []struct{ name, input, pointer, quote, want string }{
		{"escaped key", `{"context":{"a":{"x/y~z":"Exact", "count":3,"ok":true,"nil":null}}}`, "/context/a", "Exact", "/context/a/x~1y~0z"},
		{"array scalar", `{"context":{"a":[{"field":"Nested"},"Exact",null]}}`, "/context/a", "Exact", "/context/a/1"},
		{"wrong quote", `{"context":{"a":{"field":"Actual"}}}`, "/context/a", "Invented", "/context/a/field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := basisOnlyOutput(tc.pointer, tc.quote)
			got, changes := canonicalReviewReadPaths(original, raw(tc.input))
			if len(changes) != 0 || !rawJSONEqual(got, original) {
				t.Fatal("navigation rewrote a scientific citation", string(got))
			}
			err := reviewBasisDiagnostics([]trackedReviewFinding{{ID: "issue-1-1", Basis: []reviewBasis{{tc.pointer, tc.quote}}}}, raw(tc.input))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("no actual child navigation", err)
			}
		})
	}
}

// This reads an immutable incident capture only, never a live DB or model.
func TestCapturedReviewBasisReasonCorrectionOscillation(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_BASIS_REPLAY")
	if dir == "" {
		t.Skip("no read-only incident capture supplied")
	}
	read := func(name string) json.RawMessage {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	input := read("review-input.json")
	node := CompiledNode{ID: "independent_review", OutputSchema: read("schema.json")}
	for _, name := range []string{"submission-turn-1.json", "submission-turn-2.json", "submission-turn-3.json"} {
		t.Run(name, func(t *testing.T) {
			original := read(name)
			value, _, err := normalizeWorkflowAIStageSubmissionForInput(string(original), node, input)
			if err != nil {
				t.Fatal(err)
			}
			err = validateWorkflowAIStageOutput(node, value, input)
			t.Logf("original candidate rejection: %v", err)
			if err == nil {
				t.Fatal("invalid original incident accepted")
			}
			if name != "submission-turn-2.json" && !strings.Contains(err.Error(), "指向对象或数组") {
				t.Fatal("original basis failure not reproduced", err)
			}
			if name != "submission-turn-3.json" && !strings.Contains(err.Error(), "changeReason") {
				t.Fatal("missing reason hidden behind basis error", err)
			}
			// Combine ONLY the actual model's already supplied corrections.
			// No claim, quote, conclusion or repair destination is invented.
			corrected := decodeObject(read("submission-turn-2.json"))
			reasons := decodeObject(read("submission-turn-3.json"))["reviewFindings"].([]any)
			for i, item := range corrected["reviewFindings"].([]any) {
				item.(map[string]any)["changeReason"] = reasons[i].(map[string]any)["changeReason"]
			}
			value, _, err = normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(corrected)), node, input)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateWorkflowAIStageOutput(node, value, input); err != nil || decodeObject(value)["approved"] != false {
				t.Fatal("combined real corrections cannot preserve rejection", err)
			}
			if local, err := os.ReadFile(filepath.Join(dir, "derived-local-repair.json")); err == nil {
				merged, _, err := normalizeWorkflowAIStageSubmissionForInput(string(local), node, input)
				if err != nil || !rawJSONEqual(merged, value) || validateWorkflowAIStageOutput(node, merged, input) != nil {
					t.Fatal("local repair differs from real corrected cells or bypasses full validator", err)
				}
			}
		})
	}
}

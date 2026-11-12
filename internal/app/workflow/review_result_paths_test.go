package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func reviewKernelFixture() map[string]any {
	return map[string]any{
		"kernelId": "kernel", "executionId": "execution", "codeSha256": "code-hash", "environmentFingerprint": "environment-hash", "status": "success", "valueType": "dict",
		"value": map[string]any{"mean": json.Number("3.25"), "note": "Exact original passage", "ok": false, "a/~b": []any{map[string]any{"count": json.Number("9007199254740993")}}, "details": map[string]any{"method": "paired"}},
	}
}

func basisOnlyOutput(pointer, quote string) json.RawMessage {
	return mustJSON(map[string]any{"approved": false, "requiredCorrections": []string{"Keep this real scientific defect"}, "reviewFindings": []any{map[string]any{"id": "issue-1-1", "basis": []reviewBasis{{Pointer: pointer, Quote: quote}}, "status": "open", "summary": "Unchanged science"}}})
}

func TestReviewKernelAliasRequiresAbsentOriginalAndExactScalar(t *testing.T) {
	for _, tc := range []struct {
		pointer, quote, target string
	}{
		{"/computedResults/mean", "3.25", "/computedResults/value/mean"},
		{"/computedResults/note", "original passage", "/computedResults/value/note"},
		{"/computedResults/ok", "false", "/computedResults/value/ok"},
		{"/computedResults/a~1~0b/0/count", "9007199254740993", "/computedResults/value/a~1~0b/0/count"},
		{"/dataPreflight/details/method", "paired", "/dataPreflight/value/details/method"},
	} {
		t.Run(tc.pointer, func(t *testing.T) {
			input := mustJSON(map[string]any{"computedResults": reviewKernelFixture(), "dataPreflight": reviewKernelFixture(), "indexedReadRanges": []any{}})
			before := string(input)
			output := basisOnlyOutput(tc.pointer, tc.quote)
			got, changes := canonicalReviewReadPaths(output, input)
			if len(changes) != 1 || changes[0].Rule != "frozen_kernel_value_path" || changes[0].Path != "$.reviewFindings[0].basis[0].pointer" || changes[0].BeforeSHA256 != hashJSON(mustJSON(tc.pointer)) || changes[0].AfterSHA256 != hashJSON(mustJSON(tc.target)) {
				t.Fatalf("missing exact pointer audit: %s %+v", got, changes)
			}
			want := basisOnlyOutput(tc.target, tc.quote)
			if !rawJSONEqual(got, want) || string(input) != before {
				t.Fatal("science, quote or input changed")
			}
			again, changes := canonicalReviewReadPaths(got, input)
			if len(changes) != 0 || !rawJSONEqual(again, got) {
				t.Fatal("normalization not idempotent")
			}
		})
	}
}

func TestReviewKernelAliasNeverSearchesOrRepairsScientificContent(t *testing.T) {
	for _, tc := range []struct {
		name, pointer, quote string
		edit                 func(map[string]any)
	}{
		{"forged quote", "/computedResults/note", "paraphrased claim", nil},
		{"number prefix", "/computedResults/mean", "3", nil},
		{"numeric display alias", "/computedResults/mean", "3.250", nil},
		{"blank quote", "/computedResults/note", "  ", nil},
		{"missing path", "/computedResults/missing", "3.25", nil},
		{"object", "/computedResults/details", "paired", nil},
		{"array", "/computedResults/a~1~0b", "9007199254740993", nil},
		{"bad escape", "/computedResults/a~2b", "3.25", nil},
		{"bad index", "/computedResults/a~1~0b/00/count", "9007199254740993", nil},
		{"already wrapped", "/computedResults/value/mean", "3.25", nil},
		{"other root", "/methodContext/mean", "3.25", nil},
		{"existing conflict", "/computedResults/mean", "3.25", func(e map[string]any) { e["mean"] = json.Number("4") }},
		{"existing object", "/computedResults/details", "paired", func(e map[string]any) { e["details"] = map[string]any{} }},
		{"not Kernel", "/computedResults/mean", "3.25", func(e map[string]any) { delete(e, "kernelId") }},
		{"failed Kernel", "/computedResults/mean", "3.25", func(e map[string]any) { e["status"] = "error" }},
		{"not dict", "/computedResults/mean", "3.25", func(e map[string]any) { e["valueType"] = "table" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := reviewKernelFixture()
			if tc.edit != nil {
				tc.edit(e)
			}
			input := mustJSON(map[string]any{"computedResults": e, "methodContext": e})
			output := basisOnlyOutput(tc.pointer, tc.quote)
			got, changes := canonicalReviewReadPaths(output, input)
			if len(changes) != 0 || !rawJSONEqual(got, output) {
				t.Fatal("guessed evidence or silently changed a conflict", string(got))
			}
		})
	}
}

func TestReviewKernelNavigationUsesActualShapeAndPreservesRejection(t *testing.T) {
	input, output := trackingFixture()
	stage := decodeObject(input)
	kernel := reviewKernelFixture()
	kernel["value"].(map[string]any)["mean"] = json.Number("3")
	stage["computedResults"] = kernel
	input = mustJSON(stage)
	output["reviewedInputSha256"] = hashJSON(input)
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{CriterionID: "criterion-1", Status: "not_met", Basis: "Reported mean differs from computed result"}}
	output["revisionPlan"] = []any{map[string]any{"correctionIndex": 0, "nodeId": "report_drafting", "reason": "Fix the reported mean", "whyNotLater": "Report is the responsible stage", "needsUserInput": false, "requiredInput": ""}}
	// The tracking validator does not need revision targets; test the complete
	// normalization plus tracking directly to keep this fixture method-agnostic.
	node := CompiledNode{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()}
	got, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(output)), node, input)
	if err != nil || len(changes) != 1 || validateTrackedReview(got, input) != nil || decodeObject(got)["approved"] != false {
		t.Fatal("legitimate rejection lost", err, string(got))
	}
	bad := decodeObject(got)
	bad["approved"] = true
	if validateTrackedReview(mustJSON(bad), input) == nil {
		t.Fatal("exact path caused false approval")
	}
	locations := reviewKernelLocations(input)
	if len(locations) != 1 || locations[0].ValuePointer != "/computedResults/value" || !strings.Contains(string(mustJSON(locations)), "/computedResults/value/a~1~0b") {
		t.Fatal(locations)
	}
	prompt := buildAIStagePrompt(RunDetail{}, Step{Input: input, InputSHA256: hashJSON(input)}, node)
	if !strings.Contains(prompt, kernelReviewNavigationInstruction) || !strings.Contains(prompt, `"bodyFieldPointers"`) || !strings.Contains(prompt, string(reviewCurrentInputFirst(input))) {
		t.Fatal("navigation not delivered with original frozen input")
	}
	if len(reviewKernelLocations(raw(`{"computedResults":{"value":{"mean":3}}}`))) != 0 {
		t.Fatal("ordinary source object misidentified as Kernel output")
	}
	finding := trackedReviewFinding{ID: "issue-1-1", Basis: []reviewBasis{{Pointer: "/computedResults/mean", Quote: "invented"}}}
	if err := reviewBasisDiagnostics([]trackedReviewFinding{finding}, input); err == nil || !strings.Contains(err.Error(), "/computedResults/value/mean") {
		t.Fatal("missing precise wrapper diagnostic", err)
	}
}

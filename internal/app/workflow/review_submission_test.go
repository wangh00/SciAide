package workflow

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestActualFailedReviewSingleSourceReplay(t *testing.T) {
	original, err := os.ReadFile("testdata/review_submission/failed-review.json")
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile("testdata/review_submission/frozen-input.json")
	if err != nil {
		t.Fatal(err)
	}
	node := CompiledNode{ID: "independent_review", PromptVersion: routeDeliveryReviewVersion, OutputSchema: trackedResearchReviewSchema()}
	if err := validateWorkflowAIStageOutput(node, original, input); err == nil {
		t.Fatalf("original failure not reproduced: %v", err)
	}
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(original), node, input); err == nil || !strings.Contains(err.Error(), "省略") {
		t.Fatalf("conflicting content silently discarded: %v", err)
	}
	compact := decodeObject(original)
	// Fixture input omits unrelated source inventory; bind to this exact snapshot.
	compact["reviewedInputSha256"] = hashJSON(input)
	for _, key := range reviewCategoryFields {
		delete(compact, key)
	}
	result, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(compact)), node, input)
	if err != nil {
		t.Fatal(err)
	}
	// Once duplicate formatting is removed, BOTH real quote errors are returned
	// together, not sequentially consuming the two correction attempts.
	err = validateWorkflowAIStageOutput(node, result, input)
	if err == nil || !strings.Contains(err.Error(), "issue-1-2") || !strings.Contains(err.Error(), "issue-1-3") {
		t.Fatalf("missing batch diagnostics: %v", err)
	}
	// Simulate the model rereading exact frozen fields; production never guesses.
	f := compact["reviewFindings"].([]any)
	f[1].(map[string]any)["basis"].([]any)[0].(map[string]any)["quote"] = "## 后续交接与实施关口"
	source := decodeObject(input)
	f[2].(map[string]any)["basis"].([]any)[1].(map[string]any)["quote"] = source["designContext"].(map[string]any)["analysisPlan"].([]any)[5]
	result, changes, err = normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(compact)), node, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateWorkflowAIStageOutput(node, result, input); err != nil {
		t.Fatal(err)
	}
	fields := decodeObject(result)
	if fields["approved"] != false || len(fields["methodIssues"].([]any)) != 3 || len(fields["citationIssues"].([]any)) != 1 || len(changes) == 0 {
		t.Fatal("lost rejection or findings")
	}
	for _, key := range []string{"reviewFindings", "requiredCorrections", "revisionPlan", "acceptanceChecks", "reviewedInputSha256"} {
		if !rawJSONEqual(mustJSON(fields[key]), mustJSON(compact[key])) {
			t.Fatal("changed substantive field", key)
		}
	}
	again, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(result), node, input)
	if err != nil || len(changes) != 0 || !rawJSONEqual(again, result) {
		t.Fatal("projection not idempotent", err)
	}
	// A forged quote and false approval must still fail, never be repaired away.
	for _, mutate := range []func(map[string]any){func(o map[string]any) { o["approved"] = true }, func(o map[string]any) {
		o["reviewFindings"].([]any)[0].(map[string]any)["basis"].([]any)[0].(map[string]any)["quote"] = "invented quote"
	}} {
		bad := decodeObject(result)
		mutate(bad)
		if validateWorkflowAIStageOutput(node, mustJSON(bad), input) == nil {
			t.Fatal("invalid scientific review accepted")
		}
	}
}

func TestReviewWireSchemaOmitsOnlyDerivedClassification(t *testing.T) {
	frozen := trackedResearchReviewSchema()
	wire := ReviewSubmissionSchema(frozen)
	for _, key := range reviewCategoryFields {
		if schemaDeclaresProperty(wire, key) || !schemaDeclaresProperty(frozen, key) {
			t.Fatal(key)
		}
	}
	if err := (tool.JSONSchemaValidator{}).ValidateSchema(wire); err != nil {
		t.Fatal(err)
	}
	if !rawJSONEqual(ReviewSubmissionSchema(researchDesignSchema()), researchDesignSchema()) {
		t.Fatal("unrelated schema changed")
	}
	input, output := trackingFixture()
	output["revisionPlan"] = []any{}
	for _, key := range reviewCategoryFields {
		delete(output, key)
	}
	output["unknown"] = true
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(output)), CompiledNode{OutputSchema: frozen}, input); err == nil {
		t.Fatal("unknown field erased")
	}
}

func TestReviewReadPathAliasRequiresExactCurrentScalar(t *testing.T) {
	input := raw(`{"researchSourceContext":{"indexedReadRanges":[{"locator":"lines:1-20"}]}}`)
	output := raw(`{"reviewFindings":[{"basis":[{"pointer":"/indexedReadRanges/0/locator","quote":"lines:1-20"}]}]}`)
	normalized, changes := canonicalReviewReadPaths(output, input)
	if len(changes) != 1 || !strings.Contains(string(normalized), "/researchSourceContext/indexedReadRanges/0/locator") {
		t.Fatal(string(normalized))
	}
	_, provenance, err := reviewBasisAt(decodeObject(input), "/researchSourceContext/indexedReadRanges/0/locator")
	if err != nil || !provenance {
		t.Fatal("reading log treated as scientific evidence")
	}
	for _, bad := range []json.RawMessage{raw(`{}`), raw(`{"researchSourceContext":{"indexedReadRanges":[{"locator":"lines:30-40"}]}}`)} {
		same, changes := canonicalReviewReadPaths(output, bad)
		if len(changes) != 0 || !rawJSONEqual(same, output) {
			t.Fatal("guessed read path")
		}
	}
}

func TestTrackedLimitedCoverageRequiresSemanticAssessmentNotBooleanVeto(t *testing.T) {
	input, output := acceptanceFixture(t)
	stage := decodeObject(input)
	criteria, _ := researchAcceptanceCriteria(mustJSON(stage["researchContract"]))
	stage["requireOriginalDelivery"] = true
	stage["acceptanceCriteria"] = appendOriginalDeliveryCriterion(criteria)
	stage["reviewHistory"] = reviewHistory{IssuePrefix: "issue-1-"}
	stage["evidenceScreening"] = map[string]any{"coverage": map[string]any{"sufficientForClaimedScope": false}}
	output["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "对象"}, {"criterion-2", "met", "设计材料完整；文献仅作明确受限背景，不宣称验证"}, {"criterion-3", "met", "本轮约定为设计草稿，材料实际给出"}}
	if err := validateResearchAcceptance(mustJSON(output), mustJSON(stage)); err != nil {
		t.Fatal(err)
	}
	output["acceptanceChecks"].([]ResearchAcceptanceCheck)[1].Status = "not_met"
	if validateResearchAcceptance(mustJSON(output), mustJSON(stage)) == nil {
		t.Fatal("missing deliverable approved")
	}
}

func TestFrozenDesignMaterialsSurviveReportAndRepeatedNormalization(t *testing.T) {
	node := CompiledNode{ID: "report_drafting", PromptVersion: researchReportVersion, OutputSchema: dynamicReportSchema()}
	body := "题干：过去一周是否困倦？\n选项：从不/偶尔/经常；变量 sleepiness，编码 0/1/2；未验证。"
	input := mustJSON(map[string]any{"context": map[string]any{"status": "research_design_not_empirical_result", "deliverables": []any{map[string]any{"title": "问卷初稿", "markdown": body}}}})
	output := raw(`{"markdown":"# 设计报告\n研究关联，不推断因果。","claimSummary":[],"methodSummary":"横断面设计","limitations":["无数据"],"confidence":"medium"}`)
	result, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(output), node, input)
	if err != nil || len(changes) != 1 || !strings.Contains(string(result), "sleepiness") {
		t.Fatal(err, string(result))
	}
	again, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(result), node, input)
	if err != nil || len(changes) != 0 || !rawJSONEqual(again, result) {
		t.Fatal("duplicate material appendix", err)
	}
	if err := validateReportProvenance(node, result, input); err != nil {
		t.Fatal(err)
	}
	// No source material means no invented questionnaire.
	empty, _, err := normalizeWorkflowAIStageSubmissionForInput(string(output), node, raw(`{}`))
	if err != nil || !rawJSONEqual(empty, output) {
		t.Fatal("invented missing materials", err)
	}
}

func TestCanonicalReviewPreflightOnlyOmitsHostIdentityRequirement(t *testing.T) {
	input, wire, schema := issueTransportFixture(t)
	originalSchema := string(schema)
	node := CompiledNode{ID: "independent_review", OutputSchema: schema}
	canonical, _, err := normalizeWorkflowAIStageSubmissionForInput(string(wire), node, input)
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeObject(canonical)
	delete(fields, "reviewedInputSha256")
	text := string(mustJSON(fields))
	if _, _, err := NormalizeAIStageSubmission(text, schema); err == nil {
		t.Fatal("frozen output accepted an unbound review")
	}
	preflight := ReviewCanonicalSubmissionPreflightSchema(schema)
	value, _, err := NormalizeAIStageSubmission(text, preflight)
	if err != nil || decodeObject(value)["reviewedInputSha256"] != nil || decodeObject(value)["approved"] != false || string(schema) != originalSchema {
		t.Fatal("routing preflight changed the schema, decision, or fabricated identity", err)
	}
	for _, mutation := range []string{"missing_decision", "unknown_field", "wrong_identity_type"} {
		t.Run(mutation, func(t *testing.T) {
			bad := decodeObject([]byte(text))
			switch mutation {
			case "missing_decision":
				delete(bad, "approved")
			case "unknown_field":
				bad["foreignPatch"] = true
			case "wrong_identity_type":
				bad["reviewedInputSha256"] = 42
			}
			if _, _, err := NormalizeAIStageSubmission(string(mustJSON(bad)), preflight); err == nil {
				t.Fatal("preflight relaxed a non-host-owned requirement")
			}
		})
	}
	custom := raw(`{"type":"object","required":["reviewedInputSha256"],"properties":{"reviewedInputSha256":{"type":"string"}}}`)
	if string(ReviewCanonicalSubmissionPreflightSchema(custom)) != string(custom) {
		t.Fatal("custom same-named field became host-owned")
	}
}

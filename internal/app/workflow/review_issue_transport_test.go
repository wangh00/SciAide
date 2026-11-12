package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func issueTransportFixture(t *testing.T) (json.RawMessage, json.RawMessage, json.RawMessage) {
	t.Helper()
	input, out := trackingFixture()
	stage := decodeObject(input)
	stage["researchContract"] = map[string]any{"successCriteria": []string{"Use computed mean"}}
	stage["acceptanceCriteria"] = []any{map[string]any{"id": "criterion-1", "text": "Use computed mean"}}
	input = mustJSON(stage)
	out["acceptanceChecks"] = []any{map[string]any{"criterionId": "criterion-1", "status": "not_met", "basis": "Reported mean differs"}}
	out["reviewedInputSha256"] = hashJSON(input)
	out["revisionPlan"] = []any{map[string]any{"correctionIndex": 0, "nodeId": "", "reason": "Inspect current source", "whyNotLater": "No repair stage offered", "needsUserInput": true, "requiredInput": "Source for reported number"}}
	schema := trackedResearchReviewSchema()
	wire, err := IssueReviewSubmission(mustJSON(out), schema)
	if err != nil {
		t.Fatal(err)
	}
	return input, wire, schema
}

func TestIssueTransportDerivesOnlyPositionAndPreservesDecisions(t *testing.T) {
	input, wire, schema := issueTransportFixture(t)
	if err := (tool.JSONSchemaValidator{}).Validate(ReviewSubmissionSchema(schema), wire); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "correctionIndex") || strings.Contains(string(wire), "requiredCorrections") || strings.Contains(string(wire), "revisionPlan") {
		t.Fatal("parallel lists still required")
	}
	node := CompiledNode{ID: "independent_review", OutputSchema: schema}
	canonical, _, err := normalizeWorkflowAIStageSubmissionForInput(string(wire), node, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(node, canonical, input); err != nil {
		t.Fatal(err)
	}
	again, err := IssueReviewSubmission(canonical, schema)
	if err != nil || !rawJSONEqual(again, wire) {
		t.Fatal("lossy mapping", err)
	}
	for _, kind := range []string{"missing plan", "closed with advice", "mixed", "forged quote", "false approval", "bad target"} {
		t.Run(kind, func(t *testing.T) {
			o := decodeObject(wire)
			f := o["reviewFindings"].([]any)[0].(map[string]any)
			switch kind {
			case "missing plan":
				delete(f, "revision")
			case "closed with advice":
				f["status"] = "resolved"
			case "mixed":
				o["requiredCorrections"] = []any{"extra"}
			case "forged quote":
				f["basis"].([]any)[0].(map[string]any)["quote"] = "invented evidence"
			case "false approval":
				o["approved"] = true
			case "bad target":
				f["revision"].(map[string]any)["nodeId"] = "unissued"
			}
			v, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(o)), node, input)
			if err == nil {
				err = validateWorkflowAIStageOutput(node, v, input)
			}
			if err == nil {
				t.Fatal("invalid review accepted")
			}
		})
	}
}

func TestIssueTransportDoesNotPanicOnUnrelatedPartialSchema(t *testing.T) {
	for _, s := range []json.RawMessage{raw(`{}`), raw(`{"type":"object","properties":{"reviewFindings":{"type":"array"},"revisionPlan":{"type":"array"}}}`)} {
		if SupportsIssueReview(s) {
			t.Fatal("custom partial schema opted in")
		}
		_ = ReviewSubmissionSchema(s)
	}
}

func TestReviewSharingAcrossCurrentFieldsIsLossless(t *testing.T) {
	text := strings.Repeat("原始段落 😀 2.3 不代表因果。", 300)
	input := mustJSON(map[string]any{"context": map[string]any{"markdown": text}, "evidenceContext": []any{text}, "researchSourceContext": map[string]any{"body": text, "independent": "保留不同来源"}, "reviewHistory": map[string]any{"old": text}})
	p, instruction := researchInputSharedProjection(input, true)
	if len(p) >= len(input) || !strings.Contains(instruction, "/context/markdown") && !strings.Contains(instruction, "/evidenceContext/0") {
		t.Fatal("no exact current reuse", instruction)
	}
	root := decodeObject(p)
	original := decodeObject(input)
	for _, key := range []string{"context", "evidenceContext", "reviewHistory"} {
		if !rawJSONEqual(mustJSON(root[key]), mustJSON(original[key])) {
			t.Fatal("source/history lost", key)
		}
	}
	if !strings.Contains(string(p), "保留不同来源") {
		t.Fatal("unique evidence omitted")
	}
	ordered := reviewCurrentInputFirst(p)
	if !rawJSONEqual(p, ordered) || strings.Index(string(ordered), `"context"`) > strings.Index(string(ordered), `"researchSourceContext"`) {
		t.Fatal("reading order loss")
	}
}

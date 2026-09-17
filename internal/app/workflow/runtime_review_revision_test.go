package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func reviewRevisionFixture() RunDetail {
	subject := json.RawMessage(`{"title":"prior design","limitations":[]}`)
	review := json.RawMessage(`{"approved":false,"reviewedInputSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","verifiedClaims":[],"unsupportedClaims":[],"citationIssues":[],"numericIssues":[],"methodIssues":["baseline is undefined"],"requiredCorrections":["define the baseline"],"confidence":"medium","limitations":[]}`)
	gateInput, _ := json.Marshal(map[string]json.RawMessage{"subject": subject, "review": review})
	return RunDetail{
		Run: Run{Compilation: Compilation{
			Order: []string{"design", "review", "review_gate"},
			Nodes: []CompiledNode{
				{ID: "design", Kind: NodeAgentStage, OutputSchema: json.RawMessage(`{"type":"object"}`)},
				{ID: "review", Kind: NodeAIAnalysis, OutputSchema: independentReviewSchema()},
				{ID: "review_gate", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.review.gate"}},
			},
			Edges: []Edge{
				{FromNode: "$input", FromPort: "goal", ToNode: "design", ToPort: "context"},
				{FromNode: "design", FromPort: "analysis", ToNode: "review", ToPort: "context"},
				{FromNode: "design", FromPort: "analysis", ToNode: "review_gate", ToPort: "subject"},
				{FromNode: "review", FromPort: "analysis", ToNode: "review_gate", ToPort: "review"},
			},
		}, Inputs: json.RawMessage(`{"goal":"commuting emissions"}`)},
		Steps: []Step{
			{ID: "producer-step", NodeID: "design", Ordinal: 0, NodeKind: NodeAgentStage, Status: StepCompleted, Attempt: 1, Output: json.RawMessage(`{"analysis":{"title":"prior design","limitations":[]}}`)},
			{ID: "review-step", NodeID: "review", Ordinal: 1, NodeKind: NodeAIAnalysis, Status: StepCompleted, Attempt: 1, Output: json.RawMessage(`{"analysis":{"approved":false}}`)},
			{ID: "gate-step", NodeID: "review_gate", Ordinal: 2, NodeKind: NodeTool, Status: StepFailed, Attempt: 1, Input: gateInput},
		},
	}
}

func TestReviewRevisionContextFreezesRejectedSubjectAndReview(t *testing.T) {
	detail := reviewRevisionFixture()
	producer, review, revision, err := reviewRevisionContext(detail, detail.Steps[2])
	if err != nil {
		t.Fatal(err)
	}
	if producer.ID != "producer-step" || review.ID != "review-step" || revision.NextProducerAttempt != 2 {
		t.Fatalf("revision chain = %#v, %#v, %#v", producer, review, revision)
	}
	if hashJSON(revision.PriorSubject) != revision.PriorSubjectSHA256 || hashJSON(revision.IndependentReview) != revision.ReviewOutputSHA256 {
		t.Fatal("revision context hashes do not cover the frozen values")
	}
}

func TestReviewRevisionContextAcceptsDynamicAgentStageReview(t *testing.T) {
	detail := reviewRevisionFixture()
	detail.Run.Compilation.Nodes[1].Kind = NodeAgentStage
	detail.Steps[1].NodeKind = NodeAgentStage
	producer, review, revision, err := reviewRevisionContext(detail, detail.Steps[2])
	if err != nil {
		t.Fatalf("dynamic agent-stage review was rejected: %v", err)
	}
	if producer.ID != "producer-step" || review.ID != "review-step" || revision.NextProducerAttempt != 2 {
		t.Fatalf("dynamic review revision = %#v, %#v, %#v", producer, review, revision)
	}
}

func TestBoundProducerInputIncludesPendingReviewRevision(t *testing.T) {
	detail := reviewRevisionFixture()
	_, _, revision, err := reviewRevisionContext(detail, detail.Steps[2])
	if err != nil {
		t.Fatal(err)
	}
	detail.Steps[0].Status = StepQueued
	payload, _ := json.Marshal(revision)
	detail.Events = []RuntimeEvent{{Type: "workflow.review_revision_queued", Payload: payload}}
	input, err := bindNodeInput(detail, detail.Run.Compilation.Nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(input), `"_reviewRevision"`) || !strings.Contains(string(input), `"baseline is undefined"`) || !strings.Contains(string(input), `"prior design"`) {
		t.Fatalf("revision input = %s", input)
	}
	step := detail.Steps[0]
	step.Input, step.InputSHA256 = input, hashJSON(input)
	prompt := buildAIStagePrompt(detail, step, detail.Run.Compilation.Nodes[0])
	if !strings.Contains(prompt, "corrected replacement") || !strings.Contains(prompt, "_reviewRevision") {
		t.Fatalf("revision prompt = %s", prompt)
	}
}

func TestIndependentReviewOutputRejectsContradictoryApproval(t *testing.T) {
	valid := json.RawMessage(`{"approved":false,"unsupportedClaims":[],"citationIssues":[],"numericIssues":[],"methodIssues":["undefined baseline"],"requiredCorrections":["define baseline"]}`)
	if err := validateIndependentReviewOutput(valid); err != nil {
		t.Fatalf("valid rejection = %v", err)
	}
	contradictory := json.RawMessage(`{"approved":true,"unsupportedClaims":[],"citationIssues":["no citation issues found"],"numericIssues":[],"methodIssues":[],"requiredCorrections":[]}`)
	if err := validateIndependentReviewOutput(contradictory); err == nil || !strings.Contains(err.Error(), "自相矛盾") {
		t.Fatalf("contradictory approval error = %v", err)
	}
	missingCorrection := json.RawMessage(`{"approved":false,"unsupportedClaims":[],"citationIssues":[],"numericIssues":[],"methodIssues":["undefined baseline"],"requiredCorrections":[]}`)
	if err := validateIndependentReviewOutput(missingCorrection); err == nil || !strings.Contains(err.Error(), "requiredCorrections") {
		t.Fatalf("missing correction error = %v", err)
	}
}

func TestReviewGateInputCarriesIndependentReviewSnapshot(t *testing.T) {
	reviewInput := json.RawMessage(`{"context":{"markdown":"# report","evidenceSelectionAudit":{"selected":2}},"evidenceScreening":{"strength":"limited"}}`)
	detail := RunDetail{Run: Run{Compilation: Compilation{
		Edges: []Edge{
			{FromNode: "result", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"},
			{FromNode: "result", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"},
			{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"},
		},
		Nodes: []CompiledNode{
			{ID: "result", Kind: NodeAgentStage},
			{ID: "independent_review", Kind: NodeAgentStage},
			{ID: "delivery_gate", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.review.gate"}},
		},
	}}, Steps: []Step{
		{ID: "result-step", NodeID: "result", Status: StepCompleted, Output: json.RawMessage(`{"analysis":{"markdown":"# report","evidenceSelectionAudit":{"selected":2}}}`)},
		{ID: "review-step", NodeID: "independent_review", Status: StepCompleted, Input: reviewInput, Output: json.RawMessage(`{"analysis":{"approved":true}}`)},
		{ID: "gate-step", NodeID: "delivery_gate", Status: StepQueued},
	}}
	input, err := bindNodeInput(detail, detail.Run.Compilation.Nodes[2])
	if err != nil {
		t.Fatal(err)
	}
	var bound struct {
		Subject json.RawMessage `json:"subject"`
	}
	if err := json.Unmarshal(input, &bound); err != nil {
		t.Fatal(err)
	}
	if got, want := hashJSON(bound.Subject), hashJSON(reviewInput); got != want {
		t.Fatalf("reviewed stage hash = %s, want %s", got, want)
	}
}

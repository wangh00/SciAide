package workflow

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestReportAssessmentAdaptsOnlyNewExecution(t *testing.T) {
	for _, version := range []string{"dynamic-report-v5", "dynamic-report-v5-implementation-v1"} {
		node := CompiledNode{ID: "report_drafting", PromptVersion: version, Prompt: "对已接受的有限证据，必须保持低置信并将交付物定位为范围受限的初稿。没有实证结果时明确标注为研究设计，不得伪装成已经完成的研究报告。", OutputSchema: dynamicReportSchema()}
		before := mustJSON(node)
		prompt := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, node)
		if strings.Contains(prompt, "必须保持低置信") || !strings.Contains(prompt, reportConfidenceInstruction) || !strings.Contains(prompt, "文献比较、证据综述或研究设计") {
			t.Fatal("legacy generation guidance not adapted")
		}
		if string(before) != string(mustJSON(node)) {
			t.Fatal("frozen node mutated")
		}
		if got := reportExecutionPromptVersion(node, prompt); got != version+"-"+claimAssessmentVersion {
			t.Fatal(got)
		}
		node.PromptVersion += "-" + claimAssessmentVersion
		if got := reportExecutionPromptVersion(node, prompt); got != node.PromptVersion {
			t.Fatal("duplicate execution suffix")
		}
	}
	custom := CompiledNode{ID: "custom_report", PromptVersion: "custom-v1", Prompt: "Original prompt"}
	if reportAssessmentPrompt(custom) != custom.Prompt || reportExecutionPromptVersion(custom, reportConfidenceInstruction) != custom.PromptVersion {
		t.Fatal("unrelated stage changed")
	}
}

func TestNewReportRouteAndReviewGuidance(t *testing.T) {
	route := starterDataRoute()
	route.StageIDs = append(route.StageIDs[:len(route.StageIDs)-2], "report_drafting", "independent_review", "delivery_gate")
	route.Layers[len(route.Layers)-1].Stages = append([]ResearchRouteStage{{StageID: "report_drafting", Objective: "report", Outputs: []string{"report"}}}, route.Layers[len(route.Layers)-1].Stages...)
	template, _, _, err := dynamicResearchRouteTemplate(route, ResearchStarterContext{ResearchIdea: "compare literature", StageCatalog: dynamicResearchStageCatalog()}, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range template.Definition.Nodes {
		found = found || node.ID == "report_drafting"
		if node.ID == "report_drafting" && (!strings.HasPrefix(node.PromptVersion, researchReportVersion) || !strings.Contains(node.Prompt, reportConfidenceInstruction) || strings.Contains(node.Prompt, "必须保持低置信")) {
			t.Fatal("new report route retains mechanical rating")
		}
	}
	if !found {
		t.Fatal("test route did not include a report")
	}
	review := CompiledNode{ID: "independent_review", OutputSchema: researchAcceptanceReviewSchema()}
	prompt := buildAIStagePrompt(RunDetail{}, Step{Input: raw(`{}`)}, review)
	if !strings.Contains(prompt, reviewCompletenessInstruction) || !strings.Contains(prompt, reportConfidenceInstruction) {
		t.Fatal("missing sub-requirement or confidence guidance")
	}
	// No new schema fields or forced scientific scores.
	for _, confidence := range []string{"low", "medium", "high"} {
		input, output := acceptanceFixture(t)
		output["confidence"] = confidence
		if err := validateWorkflowAIStageOutput(review, mustJSON(output), input); err != nil {
			t.Fatalf("valid %s rating rejected: %v", confidence, err)
		}
		if err := (tool.JSONSchemaValidator{}).Validate(dynamicReportSchema(), rawObject(map[string]any{"markdown": "# Comparison", "claimSummary": []string{"Bounded finding"}, "methodSummary": "Source comparison", "limitations": []string{"Not empirical"}, "confidence": confidence})); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLimitedEvidenceHostAuditDoesNotGradeByCount(t *testing.T) {
	detail := RunDetail{Run: Run{Inputs: raw(`{}`), Compilation: Compilation{Nodes: []CompiledNode{{ID: "selection", Kind: NodeCitationSelection}}}}, Steps: []Step{{NodeID: "selection", Status: StepCompleted, Output: raw(`{"citations":[],"evidenceStatus":"limited_citations_accepted"}`)}}}
	audit := independentReviewHostAudit(detail)
	if !strings.Contains(audit, "not a scientific score") || !strings.Contains(audit, "do not force low confidence or upgrade confidence") || !strings.Contains(audit, "disclosed missing required results are still missing") {
		t.Fatal("continuation authorization confused with scientific assessment")
	}
}

func TestReviewVersionsPreserveOriginalGoalBinding(t *testing.T) {
	for _, version := range []string{"dynamic-independent-review-v5", "dynamic-independent-review-v6-focused-v1", "dynamic-independent-review-v7-focused-v1"} {
		detail := RunDetail{Run: Run{Inputs: raw(`{}`)}}
		node := CompiledNode{ID: "independent_review", PromptVersion: version, OutputSchema: researchAcceptanceReviewSchema(), Arguments: raw(`{"researchContract":{"researchQuestion":"compare","successCriteria":["metrics and comparators"]},"evidenceScreening":{"coverage":{"strength":"limited"}}}`)}
		value, err := bindNodeInput(detail, node)
		if err != nil {
			t.Fatal(err)
		}
		var input struct {
			Original bool                          `json:"requireOriginalDelivery"`
			Gap      bool                          `json:"requireEvidenceGapCheck"`
			Criteria []ResearchAcceptanceCriterion `json:"acceptanceCriteria"`
		}
		if json.Unmarshal(value, &input) != nil || !input.Original || len(input.Criteria) < 2 || input.Criteria[0].Text != "metrics and comparators" || input.Gap != !strings.HasPrefix(version, "dynamic-independent-review-v5") {
			t.Fatalf("version %s binding: %s", version, value)
		}
	}
}

func TestRecordedReportAssessmentUsesCurrentFrozenSources(t *testing.T) {
	audit := citationSelectionAuditSnapshot{SelectedExcerptCount: 17, MissingRecommendedReferences: make([]string, 12), ScreeningStrength: "limited", RequiresLimitedAcceptance: true, LimitedEvidenceAccepted: true}
	node := CompiledNode{ID: "report_drafting", PromptVersion: "dynamic-report-v5", Prompt: "对已接受的有限证据，必须保持低置信并将交付物定位为范围受限的初稿。"}
	output := raw(`{"markdown":"# Original","confidence":"low"}`)
	producer := Step{ID: "report-step", NodeID: node.ID, Attempt: 5, InputSHA256: hashJSON(raw(`{}`)), Output: rawObject(map[string]any{"analysis": output})}
	execution := AIExecution{WorkflowStepID: producer.ID, Attempt: 5, Status: "completed", PromptVersion: node.PromptVersion, InputSHA256: producer.InputSHA256, Output: output, OutputSHA256: hashJSON(output)}
	detail := RunDetail{Run: Run{Compilation: Compilation{Nodes: []CompiledNode{node}, Outputs: []Output{{Name: "report_draft", FromNode: node.ID, FromPort: "analysis"}, {Name: "delivery_gate", FromNode: "gate"}}, Edges: []Edge{{FromNode: "current_review", ToNode: "gate", ToPort: "review"}}}}, Steps: []Step{producer, {ID: "review-step", NodeID: "current_review", Input: rawObject(map[string]any{"evidenceSelectionAudit": audit})}, {NodeID: "independent_review", Input: raw(`{"evidenceSelectionAudit":{"selectedExcerptCount":99}}`)}}, AIExecutions: []AIExecution{execution}}
	before := mustJSON(detail)
	got := recordedReportAssessment(detail)
	if got.SelectedExcerpts != 17 || got.Unselected != 12 || !got.LimitedDraft || got.Coverage != "limited" || got.ConfidencePolicy != "legacy_limited" {
		t.Fatalf("wrong projection: %+v", got)
	}
	if string(before) != string(mustJSON(detail)) {
		t.Fatal("history rewritten")
	}
	detail.AIExecutions[0].PromptVersion += "-" + claimAssessmentVersion
	if recordedReportAssessment(detail).ConfidencePolicy != "claim_based" {
		t.Fatal("new execution on old route not identifiable")
	}
	detail.AIExecutions[0].Attempt--
	if recordedReportAssessment(detail).ConfidencePolicy != "unknown" {
		t.Fatal("stale attempt used")
	}
	detail.AIExecutions[0].Attempt++
	detail.AIExecutions[0].Output = raw(`{"markdown":"changed"}`)
	if recordedReportAssessment(detail).ConfidencePolicy != "unknown" {
		t.Fatal("mismatched output used")
	}
	audit.ScreeningStrength = "adequate"
	detail.Steps[1].Input = rawObject(map[string]any{"evidenceSelectionAudit": audit})
	if recordedReportAssessment(detail).Coverage != "selection_limited" {
		t.Fatal("screening confused with selection coverage")
	}
	detail.Steps[1].Input = raw(`{}`)
	if got := recordedReportAssessment(detail); got.LimitedDraft || got.Coverage != "unknown" || got.SelectedExcerpts != 0 {
		t.Fatal("missing audit guessed")
	}
}

func TestHistoricalReportKeepsRetrievalProvenance(t *testing.T) {
	for _, version := range []string{"dynamic-report-v5", "dynamic-report-v5-implementation-v1", researchReportVersion} {
		node := CompiledNode{ID: "report_drafting", PromptVersion: version, OutputSchema: dynamicReportSchema()}
		input := raw(`{"researchSourceContext":{"retrieval":{"queries":["actual query"],"candidateCount":17}}}`)
		output := `{"markdown":"# Original","claimSummary":[],"methodSummary":"Source comparison","limitations":[],"confidence":"low"}`
		value, _, err := normalizeWorkflowAIStageSubmissionForInput(output, node, input)
		if err != nil || !strings.Contains(string(value), retrievalAppendixHeading) || validateReportProvenance(node, value, input) != nil {
			t.Fatalf("lost historical provenance %s: %v", version, err)
		}
	}
}

func TestReportAssessmentCannotExposeUnreviewedDelivery(t *testing.T) {
	detail := reviewedDeliveryFixture(t)
	for i := range detail.Run.Compilation.Outputs {
		if detail.Run.Compilation.Outputs[i].Name == "research_design" {
			detail.Run.Compilation.Outputs[i].Name = "report_draft"
		}
	}
	var outputs map[string]json.RawMessage
	_ = json.Unmarshal(detail.Run.Outputs, &outputs)
	outputs["report_draft"] = outputs["research_design"]
	delete(outputs, "research_design")
	detail.Run.Outputs = mustJSON(outputs)
	if assessment := assessResearchDelivery(detail); assessment == nil || assessment.Status != "reviewed" || assessment.Report == nil || assessment.Report.ConfidencePolicy != "unknown" {
		t.Fatalf("verified legacy report projection: %+v", assessment)
	}
	detail.Steps[1].InputSHA256 = "changed"
	if assessment := assessResearchDelivery(detail); assessment == nil || assessment.Status != "unverified" || assessment.Report != nil {
		t.Fatal("quality metadata exposed before delivery verification")
	}
}

// Optional offline replay of a previously captured RunDetail. The fixture is
// supplied explicitly, never fetched from a live database or model.
func TestReportAssessmentCapturedReplay(t *testing.T) {
	path := os.Getenv("SCIAIDE_REPORT_REPLAY_FIXTURE")
	if path == "" {
		t.Skip("no offline RunDetail fixture supplied")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Detail   RunDetail                `json:"detail"`
		Expected ResearchReportAssessment `json:"expected"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	before := mustJSON(fixture.Detail)
	if err := ValidateReviewedOutput(fixture.Detail, "report_draft"); err != nil {
		t.Fatal(err)
	}
	assessment := assessResearchDelivery(fixture.Detail)
	if assessment == nil || assessment.Status != "reviewed" || assessment.Report == nil || *assessment.Report != fixture.Expected {
		t.Fatalf("recorded projection: %+v; expected %+v", assessment, fixture.Expected)
	}
	if string(before) != string(mustJSON(fixture.Detail)) {
		t.Fatal("replay changed recorded state")
	}
	t.Logf("offline original report validated; unchanged projection: %+v", *assessment.Report)
}

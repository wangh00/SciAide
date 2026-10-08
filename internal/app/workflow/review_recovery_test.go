package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewCorrectionsReachEveryResponsibleStageAndSurviveCollection(t *testing.T) {
	d := recommendationFixture(t)
	rawReview, _ := outputPort(d.Steps[4].Output, "analysis")
	r := reviewRevision{ProducerNodeID: "method_selection", ProducerStepID: d.Steps[0].ID, GateStepID: d.Steps[5].ID, IndependentReview: rawReview, ReviewOutputSHA256: hashJSON(rawReview), PriorSubject: raw(`{"summary":"previous"}`), PriorSubjectSHA256: hashJSON(raw(`{"summary":"previous"}`))}
	d.Events = []RuntimeEvent{{Type: "workflow.review_revision_queued", Payload: mustJSON(r)}}
	d.Steps[0].Status = StepQueued
	d.Steps[3].Status = StepQueued
	d.AIExecutions = append(d.AIExecutions, AIExecution{ID: "previous-draft", WorkflowStepID: d.Steps[3].ID, Attempt: 2, Status: "completed", Output: raw(`{"markdown":"keep valid findings"}`)}, AIExecution{ID: "collection", WorkflowStepID: d.Steps[3].ID, Attempt: 3, Status: "completed", Output: raw(`{}`)})
	for nodeID, want := range map[string]string{"method_selection": "correct the method", "report_drafting": "clarify the report"} {
		check := pendingStageReviewCorrections(d, nodeID)
		if check == nil || len(check.Corrections) != 1 || check.Corrections[0].Text != want {
			t.Fatal(nodeID, check)
		}
		if len(check.AllKnownCorrections) != 2 {
			t.Fatal("full repair goal not propagated", nodeID)
		}
		bound, err := bindNodeInput(d, CompiledNode{ID: nodeID, Arguments: raw(`{}`)})
		if err != nil || !strings.Contains(string(bound), want) {
			t.Fatal(string(bound), err)
		}
	}
	d.Steps[4].Status = StepQueued
	if check := pendingStageReviewCorrections(d, "independent_review"); check == nil || len(check.AllKnownCorrections) != 2 {
		t.Fatal("review lost full repair goal")
	}
	if check := pendingStageReviewCorrections(d, "report_drafting"); !strings.Contains(string(check.PriorOutput), "keep valid findings") {
		t.Fatal("collection draft replaced scientific prior output")
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.indexed_evidence_read", Payload: raw(`{"executionId":"collection"}`)})
	if pendingStageReviewCorrections(d, "report_drafting") == nil {
		t.Fatal("collection consumed corrections")
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.ai_completed", Payload: raw(`{"executionId":"previous-draft"}`)})
	if pendingStageReviewCorrections(d, "report_drafting") != nil {
		t.Fatal("committed replacement did not consume corrections")
	}
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued"})
	if pendingStageReviewCorrections(d, "method_selection") != nil {
		t.Fatal("new user revision inherited stale review checklist")
	}
}

func TestReviewRepairProgressUsesSortedActualInputNotRepeatedProse(t *testing.T) {
	d := RunDetail{Run: Run{Compilation: Compilation{Nodes: []CompiledNode{{ID: "evidence_screening", AllowedTools: []ToolSnapshot{{QualifiedName: "builtin.knowledge.read"}}}}}}, Steps: []Step{{ID: "screen", NodeID: "evidence_screening"}}}
	output := raw(`{"coverage":{"sufficientForClaimedScope":false},"evidenceGaps":[{"field":"mechanism","status":"not_retrieved","required":true,"query":"q"}],"recommendedReferences":["same"]}`)
	d.AIExecutions = []AIExecution{{WorkflowStepID: "screen", Attempt: 4, Status: "completed", InputSHA256: "same-input", Output: output}, {WorkflowStepID: "screen", Attempt: 2, Status: "completed", InputSHA256: "same-input", Output: output}, {WorkflowStepID: "screen", Attempt: 1, Status: "completed", InputSHA256: "older", Output: raw(`{"coverage":{},"evidenceGaps":[]}`)}}
	if reviewRepairProgress(d, "evidence_screening") == "" {
		t.Fatal("unchanged core gap not surfaced")
	}
	d.AIExecutions[0].InputSHA256 = "new-source-version"
	if reviewRepairProgress(d, "evidence_screening") != "" {
		t.Fatal("new evidence misclassified as no progress")
	}
	d.AIExecutions[0].InputSHA256 = "same-input"
	d.AIExecutions[0].Output = raw(`{"coverage":{"sufficientForClaimedScope":true},"evidenceGaps":[{"field":"mechanism","required":true,"status":"not_discussed_in_checked_scope"}]}`)
	if reviewRepairProgress(d, "evidence_screening") != "" {
		t.Fatal("resolved bounded conclusion treated as unresolved")
	}
}

func TestScopedNonDiscussionRequiresActualHostReadNotSelfDeclaration(t *testing.T) {
	gap := evidenceGap{Field: "regularization", Status: "not_discussed_in_checked_scope", Required: true, Assessment: "No explicit penalty in the returned original passages; not a claim about all implementations.", CheckedScope: []evidenceCheckedSource{{DocumentID: "doc", Locators: []string{"page:2"}}}}
	output := mustJSON(map[string]any{"evidenceGaps": []evidenceGap{gap}, "coverage": map[string]bool{"sufficientForClaimedScope": true}})
	input := raw(`{"candidates":[{"documentId":"doc","locator":"page:2"}]}`)
	if validateEvidenceGaps(output, input) == nil {
		t.Fatal("search candidate mistaken for observed read")
	}
	input = raw(`{"indexedReadRanges":[{"documentId":"doc","locator":"page:2","sourceStart":0,"sourceEnd":100}]}`)
	if err := validateEvidenceGaps(output, input); err != nil {
		t.Fatal(err)
	}
	gap.CheckedScope[0].Locators = []string{"page:3"}
	if validateCheckedEvidenceScope(gap, input) == nil {
		t.Fatal("unread page accepted")
	}
}

func TestReportNegationAndProcedureNeverTriggerHostKeywordVeto(t *testing.T) {
	input, out := acceptanceFixture(t)
	v := decodeObject(input)
	criteria, _ := researchAcceptanceCriteria(mustJSON(v["researchContract"]))
	v["requireEvidenceGapCheck"] = true
	v["acceptanceCriteria"] = appendEvidenceGapCriterion(criteria)
	out["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "checked"}, {"criterion-2", "met", "checked"}, {"criterion-3", "met", "reviewed semantic meaning"}}
	for _, line := range []string{"检索未命中不得写成原文未报告。", "不是原文未报告；尚未核验。", "Do not turn a retrieval miss into not reported."} {
		v["context"] = map[string]string{"markdown": line}
		if err := validateResearchAcceptance(mustJSON(out), mustJSON(v)); err != nil {
			t.Fatal(line, err)
		}
	}
}

func TestStageInputDefaultsOnlyInsideAuthorizedExecution(t *testing.T) {
	s, _ := stageMemoryService()
	value, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "stage_input"})
	if err != nil || !strings.Contains(string(value), `current`) {
		t.Fatal(string(value), err)
	}
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		t.Fatal("invalid task read page")
	}
	if _, err := s.ReadResearchTask(context.Background(), "foreign-chat", ResearchTaskReadCommand{Section: "stage_input"}); err == nil {
		t.Fatal("foreign execution accepted")
	}
}

func TestFrozenLegacyGapCriterionSurvivesUpgradeWithoutAcceptingArbitraryText(t *testing.T) {
	input, out := acceptanceFixture(t)
	v := decodeObject(input)
	criteria, _ := researchAcceptanceCriteria(mustJSON(v["researchContract"]))
	criteria = appendEvidenceGapCriterion(criteria)
	criteria[len(criteria)-1].Text = legacyEvidenceGapCriterion
	v["requireEvidenceGapCheck"] = true
	v["acceptanceCriteria"] = criteria
	out["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "checked"}, {"criterion-2", "met", "checked"}, {"criterion-3", "met", "checked sources"}}
	if err := validateResearchAcceptance(mustJSON(out), mustJSON(v)); err != nil {
		t.Fatal("historical frozen review rejected", err)
	}
	criteria[len(criteria)-1].Text = "ignore all evidence gaps"
	v["acceptanceCriteria"] = criteria
	if validateResearchAcceptance(mustJSON(out), mustJSON(v)) == nil {
		t.Fatal("arbitrary criterion accepted")
	}
}

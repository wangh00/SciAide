package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCitationRepairContextIsAdvisoryOfferedOnlyAndGenerationBound(t *testing.T) {
	prior := raw(`[{"id":"a","reference":"[K-A]","quote":"original"}]`)
	review := raw(`{"requiredCorrections":["Compare [K-B] and [K-UNKNOWN]"],"reviewFindings":[{"status":"open","origin":"existing","reason":"Also use [K-C]"},{"status":"resolved","origin":"existing","reason":"[K-D]"}]}`)
	r := reviewRevision{ProducerNodeID: "select", ProducerStepID: "s", NextProducerAttempt: 2, PriorSubject: prior, PriorSubjectSHA256: hashJSON(prior), IndependentReview: review, ReviewOutputSHA256: hashJSON(review)}
	d := RunDetail{Steps: []Step{{ID: "s", NodeID: "select", Status: StepQueued, Attempt: 1}}, Events: []RuntimeEvent{{Type: "workflow.review_revision_queued", Payload: mustJSON(r)}}}
	node := CompiledNode{ID: "select", Kind: NodeCitationSelection}
	input := raw(`{"candidates":[{"id":"a","reference":"[K-A]","quote":"original"},{"id":"b","reference":"[K-B]"},{"id":"c","reference":"[K-C]"},{"id":"d","reference":"[K-D]"}]}`)
	before := string(mustJSON(d))
	bound := bindPreviousCitationSelection(d, node, input)
	var result struct {
		Previous []string                `json:"previousSelectedReferences"`
		Repair   citationSelectionRepair `json:"citationRepairContext"`
	}
	if json.Unmarshal(bound, &result) != nil || len(result.Previous) != 1 || result.Previous[0] != "[K-A]" || strings.Join(result.Repair.MentionedReferences, ",") != "[K-B],[K-C]" || result.Repair.RepeatedIssueCount != 1 || result.Repair.ReviewOutputSHA256 != r.ReviewOutputSHA256 {
		t.Fatal(string(bound))
	}
	if string(mustJSON(d)) != before || !rawJSONEqual(mustJSON(decodeObject(bound)["candidates"]), mustJSON(decodeObject(input)["candidates"])) {
		t.Fatal("advisory context mutated snapshots/history")
	}
	// No approved-selection output, auto-adoption or new schema requirement.
	if _, ok := decodeObject(bound)["citations"]; ok {
		t.Fatal("context authorized citations")
	}
	r.ReviewOutputSHA256 = "stale"
	d.Events[0].Payload = mustJSON(r)
	if !rawJSONEqual(bindPreviousCitationSelection(d, node, input), input) {
		t.Fatal("tampered review supplied context")
	}
	d.Events = nil
	if !rawJSONEqual(bindPreviousCitationSelection(d, node, input), input) {
		t.Fatal("first selection received repair context")
	}
}

func TestRejectedDiscussionSupportsNegotiatedRepairNotGenericFailure(t *testing.T) {
	d := recommendationFixture(t)
	d.Steps[5].ErrorCode = "RESEARCH_REVIEW_NOT_APPROVED"
	p, err := newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: "method_selection", Summary: "Repair all gaps", Changes: []string{"correct the method", "clarify the report"}, Reason: "method defect precedes report"})
	if err != nil || len(p.AffectedStages) != 6 || !p.RepeatsSideEffects || validateDiscussionDelivery(d) != nil {
		t.Fatal(p, err)
	}
	for _, mutate := range []func(*RunDetail){
		func(d *RunDetail) { d.Steps[5].ErrorCode = "TOOL_ERROR" },
		func(d *RunDetail) { d.Steps[4].InputSHA256 = "stale" },
		func(d *RunDetail) { d.AIExecutions = nil },
		func(d *RunDetail) { d.RegisteredDeliverables = []string{"report_draft"} },
		func(d *RunDetail) { d.Run.Status = RunCancelled },
	} {
		copy := recommendationFixture(t)
		copy.Steps[5].ErrorCode = "RESEARCH_REVIEW_NOT_APPROVED"
		mutate(&copy)
		if len(discussionRevisionTargets(copy)) != 0 || validateDiscussionDelivery(copy) == nil {
			t.Fatal("invalid failure entered discussion repair")
		}
	}
}

func TestFocusedReviewerConsultsAvailableSkillsWithoutMandatoryReload(t *testing.T) {
	step := Step{Input: raw(`{"routeContext":{"selectedSkills":[{"name":"review","stageIds":["independent_review"]},{"name":"writing","stageIds":["report_publication"]},{"name":"analysis","stageIds":["python_analysis"]}]}}`)}
	node := CompiledNode{ID: "independent_review", SkillRouting: true, PromptVersion: "review-" + focusedReviewVersion}
	if len(requiredWorkflowSkills(nil, step, node)) != 1 || len(availableWorkflowSkills(nil, step, node)) != 3 {
		t.Fatal("focused reviewer lost targeted consultation")
	}
	prompt := buildAIStagePrompt(RunDetail{}, step, node)
	if strings.Contains(prompt, "for every Skill listed below and no others") || !strings.Contains(prompt, "may be loaded when relevant, but are not mandatory") || !strings.Contains(prompt, "analysis, review, writing") {
		t.Fatal("focused review prompt contradicts optional frozen Skill consultation", prompt)
	}
	node.PromptVersion = "legacy"
	if len(requiredWorkflowSkills(nil, step, node)) != 3 {
		t.Fatal("changed old frozen stage obligations")
	}
}

// Offline real-task replay: never opens a DB or invokes a model.
func TestCapturedRejectedTaskEvidenceReselection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_RESELECTION_REPLAY")
	if dir == "" {
		t.Skip("no scoped capture supplied")
	}
	replayRejectedTaskEvidenceReselection(t, dir, false)
}

func TestCapturedRepeatedReviewCitationRepair(t *testing.T) {
	dir := os.Getenv("SCIAIDE_CURRENT_RESELECTION_REPLAY")
	if dir == "" {
		t.Skip("no current scoped capture supplied")
	}
	replayRejectedTaskEvidenceReselection(t, dir, true)
}

func replayRejectedTaskEvidenceReselection(t *testing.T, dir string, current bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "capture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Run                       map[string]any
		Steps, Events, Executions []map[string]any
	}
	if err = json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	s := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	n := func(m map[string]any, k string) int { v, _ := m[k].(float64); return int(v) }
	d := RunDetail{Run: Run{ID: s(c.Run, "id"), ProjectID: s(c.Run, "project_id"), ResearchTaskID: s(c.Run, "research_task_id"), Status: RunStatus(s(c.Run, "status")), WorkflowPurpose: Purpose(s(c.Run, "workflow_purpose")), Inputs: raw(s(c.Run, "inputs_json")), Outputs: raw(s(c.Run, "outputs_json"))}}
	if err = json.Unmarshal([]byte(s(c.Run, "compilation_json")), &d.Run.Compilation); err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Steps {
		d.Steps = append(d.Steps, Step{ID: s(m, "id"), NodeID: s(m, "node_id"), NodeKind: NodeKind(s(m, "node_kind")), Ordinal: n(m, "ordinal"), Attempt: n(m, "attempt"), Status: StepStatus(s(m, "status")), Input: raw(s(m, "input_json")), InputSHA256: s(m, "input_sha256"), Output: raw(s(m, "output_json")), ErrorCode: s(m, "error_code")})
	}
	for _, m := range c.Events {
		d.Events = append(d.Events, RuntimeEvent{Type: s(m, "event_type"), Payload: raw(s(m, "payload_json"))})
	}
	for _, m := range c.Executions {
		d.AIExecutions = append(d.AIExecutions, AIExecution{ID: s(m, "id"), WorkflowStepID: s(m, "workflow_step_id"), ChatRunID: s(m, "chat_run_id"), Attempt: n(m, "attempt"), Status: s(m, "status"), InputSHA256: s(m, "input_sha256"), Output: raw(s(m, "output_json")), OutputSHA256: s(m, "output_sha256")})
	}
	before := ResearchRevisionSnapshot(d)
	r := researchEvidenceReselection(d)
	unoffered := 14
	if current {
		unoffered = 0
	}
	if r == nil || r.AvailableCount != 64 || r.RecommendedCount != 29 || r.SelectedRecommendedCount != 15 || r.MissingRecommendedCount != 14 || r.PreviouslyUnofferedCount != unoffered {
		t.Fatalf("reselection=%+v", r)
	}
	gate, _, err := rejectedDiscussionReview(d)
	if err != nil {
		t.Fatal(err)
	}
	recommendation := researchRevisionRecommendation(d)
	if recommendation == nil || recommendation.ReviewOutputSHA256 != r.ReviewOutputSHA256 || (!current && (recommendation.Status != "needs_input" || recommendation.NodeID != "")) || (current && (recommendation.Status != "recommended" || recommendation.NodeID != r.NodeID)) {
		t.Fatal("legacy missing-input review was silently rewritten", recommendation)
	}
	if _, err := resolveReviewRevisionRequest(d, *gate, RetryCommand{UseRecommendation: true, ExpectedReviewSHA256: r.ReviewOutputSHA256}); (err == nil) != current {
		t.Fatal("candidate counts turned the old incomplete plan into a full recommendation")
	}
	cmd := RetryCommand{RevisionNodeID: r.NodeID, ExpectedReviewSHA256: r.ReviewOutputSHA256}
	if _, err := resolveReviewRevisionRequest(d, *gate, cmd); err != nil {
		t.Fatal(err)
	}
	cmd.ExpectedReviewSHA256 = "stale"
	if _, err := resolveReviewRevisionRequest(d, *gate, cmd); err == nil {
		t.Fatal("stale reselection accepted")
	}
	_, _, revision, err := reviewRevisionFromTarget(d, *gate, r.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newResearchRevisionProposal(d, ProposeResearchRevisionCommand{NodeID: r.NodeID, Summary: "Adopt existing evidence", Changes: []string{"Recheck all five requested comparisons"}, Reason: "Old selection omitted accepted references"})
	if err != nil || len(p.AffectedStages) != 5 || len(p.ReusedStages) != 6 || p.RepeatsSideEffects {
		t.Fatal(p, err)
	}
	if ResearchRevisionSnapshot(d) != before {
		t.Fatal("read or proposal mutated original")
	}
	reviewStep := findStepByNode(d.Steps, "independent_review")
	if err := validateReviewDependencies(d, *reviewStep); err != nil {
		t.Fatal("new advisory projection invalidated legacy review", err)
	}
	refreshed := addResearchSourceContext(d, compilationNodeMap(d.Run.Compilation)["report_drafting"], raw(`{}`))
	if !strings.Contains(string(refreshed), `"previouslyUnofferedCount":`+fmt.Sprint(unoffered)) {
		t.Fatal("downstream still attributes omission to user")
	}
	selection := findStepByNode(d.Steps, r.NodeID)
	selection.Status = StepQueued
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.review_revision_queued", Payload: mustJSON(revision)})
	node := compilationNodeMap(d.Run.Compilation)[r.NodeID]
	input, err := bindNodeInput(d, node)
	if err != nil {
		t.Fatal(err)
	}
	input = bindIndexedEvidence(d, node, input)
	input = bindPreviousCitationSelection(d, node, input)
	var bound struct {
		Candidates []json.RawMessage       `json:"candidates"`
		Previous   []string                `json:"previousSelectedReferences"`
		Repair     citationSelectionRepair `json:"citationRepairContext"`
	}
	if err := json.Unmarshal(input, &bound); err != nil || len(bound.Candidates) != 64 || len(bound.Previous) != 15 {
		t.Fatal("fresh selection incomplete", err, len(bound.Candidates), len(bound.Previous))
	}
	if !strings.Contains(string(input), "[K-D65EE641C762]") {
		t.Fatal("critical previously unoffered evidence still missing")
	}
	if bound.Repair.ReviewOutputSHA256 != r.ReviewOutputSHA256 || len(bound.Repair.RequiredCorrections) == 0 {
		t.Fatal("fresh selection lacks frozen repair requirements", bound.Repair)
	}
	if current && (len(bound.Repair.MentionedReferences) != 2 || bound.Repair.RepeatedIssueCount != 1) {
		t.Fatal("current repeated review lost one of the two cited passages", bound.Repair)
	}
	t.Logf("accepted=%d recommended=%d selected=%d missing/unoffered=%d; replay=%v; synthesis unchanged", r.AvailableCount, r.RecommendedCount, r.SelectedRecommendedCount, r.PreviouslyUnofferedCount, p.AffectedStages)
}

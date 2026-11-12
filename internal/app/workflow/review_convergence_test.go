package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReviewPartsAreLosslessAndRejectStaleOwnership(t *testing.T) {
	d, node, input := composedReportFixture(t)
	bound := bindReportComposition(d, node, input)
	root := decodeObject(bound)
	body := decodeObject(mustJSON(root["context"]))["markdown"].(string)
	if strings.Contains(body, screeningAppendixHeading) || !strings.Contains(body, "无法核实匹配") {
		t.Fatal("authored body not separated")
	}
	if _, _, err := reviewBasisAt(root, "/reportRecordAppendices/0"); err != nil {
		t.Fatal("record evidence inaccessible", err)
	}
	assembled, ok := reviewAssembledInput(bound)
	if !ok || !rawJSONEqual(mustJSON(decodeObject(assembled)["context"]), mustJSON(decodeObject(input)["context"])) {
		t.Fatal("not lossless")
	}
	if twice := bindReportComposition(d, node, bound); !rawJSONEqual(twice, bound) {
		t.Fatal("rebinding changed view")
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["reportRecordAppendices"].([]any)[0] = "forged appendix" },
		func(v map[string]any) { v["context"].(map[string]any)["markdown"] = "changed science" },
		func(v map[string]any) { v["reportComposition"].(map[string]any)["viewVersion"] = "unknown" },
		func(v map[string]any) { v["reportRecordAppendices"] = []string{} },
	} {
		v := decodeObject(bound)
		mutate(v)
		if validateReviewDependencies(d, Step{NodeID: node.ID, Input: mustJSON(v)}) == nil {
			t.Fatal("tampered separated report accepted")
		}
	}
	var restored RunDetail
	_ = json.Unmarshal(mustJSON(d), &restored)
	if err := validateReviewDependencies(restored, Step{NodeID: node.ID, Input: bound}); err != nil {
		t.Fatal("restart changed dependencies", err)
	}
	// Old snapshots retain the complete markdown and original exact contract.
	legacy := decodeObject(input)
	legacy["reportComposition"] = currentReportComposition(d, node, input)
	if err := validateReviewDependencies(d, Step{NodeID: node.ID, Input: mustJSON(legacy)}); err != nil {
		t.Fatal("old saved review invalidated", err)
	}
}

func TestClaimScopedAcceptanceAllowsUncertaintyNotFalseCompletion(t *testing.T) {
	in, out := acceptanceFixture(t)
	v := decodeObject(in)
	criteria, _ := researchAcceptanceCriteria(mustJSON(v["researchContract"]))
	v["requireEvidenceGapCheck"], v["evidenceAcceptancePolicy"] = true, scopedEvidenceAcceptanceVersion
	v["acceptanceCriteria"] = appendScopedEvidenceGapCriterion(criteria)
	v["evidenceScreening"] = map[string]any{"evidenceGaps": []evidenceGap{{Field: "effect", Status: "not_retrieved", Required: true, Query: "effect"}}}
	v["context"] = map[string]any{"markdown": "A complete plan, not an empirical effect estimate; effect remains unknown."}
	out["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "population provided"}, {"criterion-2", "met", "plan provided"}, {"criterion-3", "met", "design delivery makes no unsupported empirical claim"}}
	if err := validateResearchAcceptance(mustJSON(out), mustJSON(v)); err != nil {
		t.Fatal("uncertainty became an automatic veto", err)
	}
	out["acceptanceChecks"].([]ResearchAcceptanceCheck)[0].Status = "not_met"
	if validateResearchAcceptance(mustJSON(out), mustJSON(v)) == nil {
		t.Fatal("unmet promised work approved")
	}
	out["approved"], out["requiredCorrections"] = false, []string{"supply promised population definition"}
	if err := validateResearchAcceptance(mustJSON(out), mustJSON(v)); err != nil {
		t.Fatal("real correction lost", err)
	}
	out["acceptanceChecks"].([]ResearchAcceptanceCheck)[0].Status = "met"
	out["approved"], out["requiredCorrections"] = true, []string{}
	v["evidenceScreening"] = map[string]any{"evidenceGaps": []evidenceGap{{Field: "effect", Status: "not_discussed_in_checked_scope", Required: true, CheckedScope: []evidenceCheckedSource{{DocumentID: "unread", Locators: []string{"page:1"}}}, Assessment: "not discussed"}}}
	if validateResearchAcceptance(mustJSON(out), mustJSON(v)) == nil {
		t.Fatal("fabricated read scope approved")
	}
	v["evidenceAcceptancePolicy"] = "invented"
	if validateResearchAcceptance(mustJSON(out), mustJSON(v)) == nil {
		t.Fatal("unknown policy accepted")
	}
	delete(v, "evidenceAcceptancePolicy")
	v["acceptanceCriteria"] = appendEvidenceGapCriterion(criteria)
	v["evidenceScreening"] = map[string]any{"evidenceGaps": []evidenceGap{{Field: "effect", Status: "not_retrieved", Required: true, Query: "effect"}}}
	if validateResearchAcceptance(mustJSON(out), mustJSON(v)) == nil {
		t.Fatal("old frozen policy silently changed")
	}
}

func TestNewReviewUsesCurrentAssessmentNotGenerationChecklist(t *testing.T) {
	node := CompiledNode{ID: "independent_review", Kind: NodeAgentStage, PromptVersion: dynamicResearchReviewVersion, OutputSchema: trackedResearchReviewSchema(), Arguments: raw(`{"researchContract":{"successCriteria":["Describe evidence limits"]},"context":{"markdown":"Current result"},"evidenceScreening":{}}`)}
	d := RunDetail{Run: Run{Compilation: Compilation{Nodes: []CompiledNode{node}}}, Steps: []Step{{ID: "review", NodeID: node.ID, Status: StepQueued}}}
	in, err := bindNodeInput(d, node)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeObject(in)
	if root["evidenceAcceptancePolicy"] != scopedEvidenceAcceptanceVersion || root["reviewHistory"] == nil || root["_reviewCorrections"] != nil {
		t.Fatal("wrong review binding", string(in))
	}
	root["_reviewCorrections"] = map[string]any{"allKnownCorrections": []string{"old accusation"}}
	prompt := buildAIStagePrompt(d, Step{NodeID: node.ID, Input: mustJSON(root)}, node)
	if strings.Contains(prompt, "Apply every assigned correction") || !strings.Contains(prompt, "先独立核对当前正文与来源") {
		t.Fatal("review asked to implement old accusations")
	}
}

func TestRepeatedAdoptedEvidenceIgnoresRetryMetadataButNotNewQuotes(t *testing.T) {
	prior := raw(`{"evidenceGaps":[{"field":"intake","required":true,"status":"not_retrieved"}],"citationAssessments":[{"supportingQuote":"same actual source","sourceLevel":"abstract"}]}`)
	r := reviewRevision{ProducerNodeID: "evidence_screening", ProducerStepID: "s", NextProducerAttempt: 2, PriorSubject: prior, PriorSubjectSHA256: hashJSON(prior), IndependentReview: raw(`{}`), ReviewOutputSHA256: hashJSON(raw(`{}`))}
	d := RunDetail{Steps: []Step{{ID: "s", NodeID: "evidence_screening", Attempt: 3, Status: StepCompleted, Input: raw(`{"newRetryMetadata":true}`), Output: rawObject(map[string]any{"analysis": prior})}}, Events: []RuntimeEvent{{Type: "workflow.review_revision_queued", Payload: mustJSON(r)}}}
	if !unchangedAdoptedEvidenceRevision(d, "evidence_screening") || reviewRepairProgress(d, "evidence_screening") == "" {
		t.Fatal("same adopted results hidden by retry metadata")
	}
	d.Steps[0].Output = raw(strings.Replace(string(d.Steps[0].Output), "same actual source", "new actual source", 1))
	if unchangedAdoptedEvidenceRevision(d, "evidence_screening") {
		t.Fatal("new passage counted as unchanged")
	}
	d.Steps[0].Output = rawObject(map[string]any{"analysis": prior})
	for _, change := range []string{`"reference":"different-source",`, `"decision":"exclude",`} {
		d.Steps[0].Output = rawObject(map[string]any{"analysis": raw(strings.Replace(string(prior), `"supportingQuote":`, change+`"supportingQuote":`, 1))})
		if unchangedAdoptedEvidenceRevision(d, "evidence_screening") {
			t.Fatal("source/decision change hidden")
		}
	}
	d.Steps[0].Output = rawObject(map[string]any{"analysis": prior})
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.user_revision_queued"})
	if unchangedAdoptedEvidenceRevision(d, "evidence_screening") {
		t.Fatal("new user generation reuses old comparison")
	}
}

func TestSeparatedRecordHintDoesNotRewriteScientificBasis(t *testing.T) {
	d, node, input := composedReportFixture(t)
	v := decodeObject(bindReportComposition(d, node, input))
	quote := screeningAppendixHeading
	f := trackedReviewFinding{ID: "issue-1-1", Basis: []reviewBasis{{"/context/markdown", quote}}}
	before := mustJSON(f)
	err := reviewBasisDiagnostics([]trackedReviewFinding{f}, mustJSON(v))
	if err == nil || !strings.Contains(err.Error(), "/reportRecordAppendices/0") || !rawJSONEqual(before, mustJSON(f)) {
		t.Fatal("missing explicit relocation hint or rewritten judgment", err)
	}
	if separatedRecordQuoteHint(v, "/evidenceContext/0/quote", quote) != "" || separatedRecordQuoteHint(v, "/context/markdown", "程序管理的记录附录") != "" {
		t.Fatal("cross-source or ambiguous search")
	}
	delete(v, "reportComposition")
	if separatedRecordQuoteHint(v, "/context/markdown", quote) != "" {
		t.Fatal("unverified record searched")
	}
}

func TestReviewPartsPriorSubjectRetainsWholeReport(t *testing.T) {
	d, node, input := composedReportFixture(t)
	producer := &d.Steps[len(d.Steps)-1]
	producer.Ordinal = 0
	bound := bindReportComposition(d, node, input)
	fullReview := raw(`{"approved":false,"requiredCorrections":["Fix actual body error"]}`)
	core, _ := reviewCoreForGate(fullReview)
	d.Steps = append(d.Steps, Step{ID: "r", NodeID: node.ID, Ordinal: 1, Status: StepCompleted, Input: bound, Output: rawObject(map[string]any{"analysis": fullReview})}, Step{ID: "g", NodeID: "delivery_gate", Ordinal: 2, Status: StepFailed, ErrorCode: "RESEARCH_REVIEW_NOT_APPROVED", Input: rawObject(map[string]any{"subject": bound, "review": core})})
	d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, CompiledNode{ID: "delivery_gate", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.review.gate"}})
	d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, Edge{FromNode: producer.NodeID, FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"}, Edge{FromNode: node.ID, FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"})
	_, _, revision, err := reviewRevisionContext(d, d.Steps[len(d.Steps)-1])
	if err != nil || !rawJSONEqual(revision.PriorSubject, mustJSON(decodeObject(input)["context"])) {
		t.Fatal("revision baseline dropped program records", err)
	}
}

func TestReviewHistoryCanCloseFalseAccusationsWithoutHidingRealError(t *testing.T) {
	d, node, input := composedReportFixture(t)
	v := decodeObject(bindReportComposition(d, node, input))
	_, out := trackingFixture()
	old := out["reviewFindings"].([]trackedReviewFinding)[0]
	old.ID, old.Category, old.Summary = "issue-1-1", "methodIssues", "Remove allegedly copied records"
	closed := old
	closed.Status, closed.Origin, closed.CorrectionIndex = "withdrawn", "existing", -1
	closed.ChangeReason = "Current records are supplied separately by the host, not duplicate body text."
	closed.Basis = []reviewBasis{{"/reportRecordAppendices/0", "程序管理的记录附录"}}
	actual := out["reviewFindings"].([]trackedReviewFinding)[0]
	actual.ID, actual.Basis = "issue-2-1", []reviewBasis{{"/context/markdown", "无法核实匹配"}}
	v["reviewHistory"] = reviewHistory{IssuePrefix: "issue-2-", Findings: []trackedReviewFinding{old}}
	out["reviewFindings"] = []trackedReviewFinding{closed, actual}
	if err := validateTrackedReview(mustJSON(out), mustJSON(v)); err != nil {
		t.Fatal("cannot withdraw false accusation while retaining real finding", err)
	}
	out["approved"] = true
	if validateTrackedReview(mustJSON(out), mustJSON(v)) == nil {
		t.Fatal("unresolved true defect auto-approved")
	}
}

func TestCapturedReviewConvergenceSeparatesActualReport(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_CONVERGENCE_CAPTURE")
	if dir == "" {
		t.Skip("no scoped offline capture")
	}
	read := func(name string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(read("steps.json"), &rows); err != nil {
		t.Fatal(err)
	}
	var run map[string]json.RawMessage
	_ = json.Unmarshal(read("run.json"), &run)
	str := func(v json.RawMessage) string { var s string; _ = json.Unmarshal(v, &s); return s }
	num := func(v json.RawMessage) int { var n int; _ = json.Unmarshal(v, &n); return n }
	d := RunDetail{}
	_ = json.Unmarshal([]byte(str(run["compilation_json"])), &d.Run.Compilation)
	for _, r := range rows {
		d.Steps = append(d.Steps, Step{ID: str(r["id"]), NodeID: str(r["node_id"]), Ordinal: num(r["ordinal"]), Attempt: num(r["attempt"]), Status: StepStatus(str(r["status"])), Input: raw(str(r["input_json"])), InputSHA256: str(r["input_sha256"]), Output: raw(str(r["output_json"]))})
	}
	before := mustJSON(d)
	s := findStepByNode(d.Steps, "independent_review")
	node := compilationNodeMap(d.Run.Compilation)[s.NodeID]
	bound := bindReportComposition(d, node, s.Input)
	c := currentReportComposition(d, node, bound)
	if c == nil || c.ViewVersion != reportReviewPartsVersion || len(c.Sections) != 3 {
		t.Fatal("actual appendices unrecognized")
	}
	assembled, ok := reviewAssembledInput(bound)
	if !ok || !rawJSONEqual(mustJSON(decodeObject(assembled)["context"]), mustJSON(decodeObject(s.Input)["context"])) {
		t.Fatal("actual report changed")
	}
	body := decodeObject(mustJSON(decodeObject(bound)["context"]))["markdown"].(string)
	if strings.Contains(body, screeningAppendixHeading) || !strings.Contains(body, "90名2型糖尿病成年人") || !strings.Contains(body, "不能确认实际去重规则") {
		t.Fatal("body defect hidden or limitation lost")
	}
	if _, _, err := reviewBasisAt(decodeObject(bound), "/reportRecordAppendices/0"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, mustJSON(d)) {
		t.Fatal("captured state mutated")
	}
	root := decodeObject(bound)
	delete(root, "_reviewCorrections")
	criteria, err := researchAcceptanceCriteria(mustJSON(root["researchContract"]))
	if err != nil {
		t.Fatal(err)
	}
	root["acceptanceCriteria"] = appendScopedEvidenceGapCriterion(appendOriginalDeliveryCriterion(criteria))
	root["evidenceAcceptancePolicy"] = scopedEvidenceAcceptanceVersion
	derived := mustJSON(root)
	if err := os.MkdirAll(filepath.Join(dir, "derived"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "derived", "review-input.json"), derived, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual report restored exactly; 3 appendices separated; sample-size defect and explicit dedup limitation preserved; input bytes %d -> %d; no model verdict generated", len(s.Input), len(derived))
}

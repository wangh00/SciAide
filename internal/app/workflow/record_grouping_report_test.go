package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
)

func TestGroupingAppendixRecordsActualMethodWithoutGuessingLegacy(t *testing.T) {
	c := auditCandidate("c", "a", "b", "legacy")
	c.Records[0].Grouping = &research.RecordGrouping{Version: research.RecordGroupingVersion, CandidateID: "c", Action: "create_unmatched", ComparedAliases: []research.CandidateAlias{{Kind: "doi", Value: "10.1/a"}}}
	c.Records[1].Grouping = &research.RecordGrouping{Version: research.RecordGroupingVersion, CandidateID: "c", Action: "match_alias", Matches: []research.RecordGroupingMatch{{CandidateID: "c", Kind: "doi", Value: "10.1/a"}}}
	q, ok := auditQueryCandidates("q", []research.Candidate{c})
	if !ok || q.Grouping.Recorded != 2 || q.Grouping.Missing != 1 {
		t.Fatal(q)
	}
	input := mustJSON(map[string]any{"researchSourceContext": map[string]any{"retrieval": map[string]any{}}, "researchRecordAudit": researchRecordAudit{Version: recordAuditVersion, Status: "available", Queries: []queryRecordAudit{q}}})
	appendix, e := reportRetrievalAppendix(input)
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"本次检索实际题录归组方法", research.RecordGroupingRules, "精确标识匹配到唯一候选", "doi:10.1/a → c", "1 条没有此版本决定", "未保存可核决定"} {
		if !strings.Contains(appendix, want) {
			t.Fatal("missing", want)
		}
	}
	for j := range c.Records {
		c.Records[j].Grouping = nil
	}
	old, _ := auditQueryCandidates("old", []research.Candidate{c})
	if old.Grouping != nil {
		t.Fatal("invented old method")
	}
	var b strings.Builder
	writeRecordedGroupingMethod(&b, old)
	if b.Len() != 0 {
		t.Fatal("rewrote legacy appendix")
	}
}

func TestReviewHistoryPreservesPriorChecksAndRemedyWithoutLockingVerdict(t *testing.T) {
	input, out := acceptanceFixture(t)
	out["reviewFindings"], out["suggestions"], out["revisionPlan"] = []trackedReviewFinding{}, []string{}, []researchRevisionPlanItem{}
	full := mustJSON(out)
	node := CompiledNode{ID: "independent_review", OutputSchema: trackedResearchReviewSchema()}
	d := RunDetail{Steps: []Step{{ID: "s", NodeID: node.ID, Attempt: 1}}, AIExecutions: []AIExecution{{WorkflowStepID: "s", Attempt: 1, Status: "completed", InputSHA256: hashJSON(input), Output: full, OutputSHA256: hashJSON(full)}}}
	h, e := trackedReviewHistory(d, node)
	if e != nil || len(h.AcceptanceChecks) == 0 {
		t.Fatal(h, e)
	}
	var raw struct {
		Checks []ResearchAcceptanceCheck `json:"acceptanceChecks"`
	}
	_ = json.Unmarshal(full, &raw)
	if string(mustJSON(raw.Checks)) != string(mustJSON(h.AcceptanceChecks)) {
		t.Fatal("prior acceptance table lost")
	}
	stage := decodeObject(input)
	stage["reviewHistory"] = h
	// History is navigation only; do not let it become basis evidence.
	if _, _, e = reviewBasisAt(stage, "/reviewHistory/acceptanceChecks/0/basis"); e == nil {
		t.Fatal("history became evidence")
	}
	prompt := buildAIStagePrompt(d, Step{Input: mustJSON(stage)}, node)
	if !strings.Contains(prompt, reviewContinuityInstruction) {
		t.Fatal("continuity not supplied")
	}
}

func TestCapturedRepeatedDedupReviewRemedyIsNowHandedOff(t *testing.T) {
	dir := os.Getenv("SCIAIDE_DEDUP_REVIEW_CAPTURE")
	if dir == "" {
		t.Skip("read-only incident capture")
	}
	read := func(n string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, n))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var rows []struct {
		StepID     string `json:"workflow_step_id"`
		Attempt    int    `json:"attempt"`
		Status     string `json:"status"`
		Output     string `json:"output_json"`
		InputHash  string `json:"input_sha256"`
		OutputHash string `json:"output_sha256"`
		Schema     string `json:"output_schema_json"`
	}
	if e := json.Unmarshal(read("executions.json"), &rows); e != nil {
		t.Fatal(e)
	}
	var node CompiledNode
	d := RunDetail{}
	for _, r := range rows {
		if r.StepID == "0e89390b-8506-4d6e-ae05-1d20d21c09fb" && r.Attempt == 2 {
			node = CompiledNode{ID: "independent_review", OutputSchema: json.RawMessage(r.Schema)}
			d.Steps = []Step{{ID: r.StepID, NodeID: node.ID, Attempt: 2}}
			d.AIExecutions = []AIExecution{{WorkflowStepID: r.StepID, Attempt: r.Attempt, Status: r.Status, Output: json.RawMessage(r.Output), OutputSHA256: r.OutputHash, InputSHA256: r.InputHash}}
		}
	}
	h, e := trackedReviewHistory(d, node)
	if e != nil || len(h.AcceptanceChecks) != 9 || len(h.RequiredCorrections) != 1 || len(h.RevisionPlan) != 1 {
		t.Fatal(h, e)
	}
	if !strings.Contains(h.RequiredCorrections[0], "说明可核范围和缺口") {
		t.Fatal("lost previous alternative remedy")
	}
	input := decodeObject(read("independent_review-input_json.json"))
	input["reviewHistory"] = h
	before := read("independent_review-output_json.json")
	var out struct {
		Analysis map[string]any `json:"analysis"`
	}
	_ = json.Unmarshal(before, &out)
	if out.Analysis["approved"] != false {
		t.Fatal("wrong capture")
	}
	for _, f := range out.Analysis["reviewFindings"].([]any) {
		m := f.(map[string]any)
		if m["id"] == "issue-2-1" && m["status"] != "open" {
			t.Fatal("incident verdict changed")
		}
	}
	prompt := buildAIStagePrompt(d, Step{Input: mustJSON(input)}, node)
	if !strings.Contains(prompt, "prior remedy was insufficient") || !strings.Contains(prompt, "说明可核范围和缺口") {
		t.Fatal("same remedy cannot be compared")
	}
	if string(before) != string(read("independent_review-output_json.json")) {
		t.Fatal("capture modified")
	}
	if derived := os.Getenv("SCIAIDE_DEDUP_DERIVED"); derived != "" {
		// Simulate a NEW report/review input using this incident's real body
		// and freshly replayed query decisions. Never modify old snapshots.
		if replay := os.Getenv("SCIAIDE_GROUPING_CAPTURE"); replay != "" {
			b, e := os.ReadFile(filepath.Join(replay, "replayed-candidates.json"))
			if e != nil {
				t.Fatal(e)
			}
			var candidates []research.Candidate
			if e = json.Unmarshal(b, &candidates); e != nil {
				t.Fatal(e)
			}
			q, ok := auditQueryCandidates("replayed-query", candidates)
			if !ok {
				t.Fatal("invalid replay")
			}
			audit := researchRecordAudit{Version: recordAuditVersion, Status: "available", Queries: []queryRecordAudit{q}}
			input["researchRecordAudit"], input["researchRecordFacts"] = audit, compactResearchRecordFacts(audit)
			appendix, e := reportRetrievalAppendix(mustJSON(input))
			if e != nil {
				t.Fatal(e)
			}
			input["reportRecordAppendices"].([]any)[2] = appendix
			// The simulation changes records, so old composition hashes are not
			// claimed as proof of this new appendix. This is budget input only.
			delete(input, "reportComposition")
			prompt = buildAIStagePrompt(d, Step{Input: mustJSON(input)}, node)
		}
		if e := os.MkdirAll(derived, 0700); e != nil {
			t.Fatal(e)
		}
		for n, data := range map[string][]byte{"input.json": mustJSON(input), "prompt.txt": []byte(prompt), "schema.json": node.OutputSchema, "candidate.json": mustJSON(out.Analysis)} {
			if e := os.WriteFile(filepath.Join(derived, n), data, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	t.Log("Original 9 checks, prior remedy and report-only plan handed off; original rejection preserved, no model called.")
}

func TestCapturedSearchReplayProducesExactReviewableMethodAppendix(t *testing.T) {
	dir := os.Getenv("SCIAIDE_GROUPING_CAPTURE")
	if dir == "" {
		t.Skip("optional isolated search replay")
	}
	b, e := os.ReadFile(filepath.Join(dir, "replayed-candidates.json"))
	if e != nil {
		t.Fatal(e)
	}
	var candidates []research.Candidate
	if e = json.Unmarshal(b, &candidates); e != nil {
		t.Fatal(e)
	}
	q, ok := auditQueryCandidates("replayed-query", candidates)
	if !ok || q.Grouping == nil || q.Grouping.Recorded != 54 || q.Grouping.Missing != 0 {
		t.Fatal(q)
	}
	input := mustJSON(map[string]any{"researchSourceContext": map[string]any{"retrieval": map[string]any{}}, "researchRecordAudit": researchRecordAudit{Version: recordAuditVersion, Status: "available", Queries: []queryRecordAudit{q}}})
	appendix, e := reportRetrievalAppendix(input)
	if e != nil || strings.Count(appendix, "精确标识匹配到唯一候选") != 7 || !strings.Contains(appendix, "0 条没有此版本决定") {
		t.Fatal(e, appendix)
	}
	// Assemble into the normal stored report, then verify the host-managed
	// section and its method survive the same report/review contract.
	node := CompiledNode{ID: "report_drafting", PromptVersion: researchReportVersion, OutputSchema: dynamicReportSchema()}
	value, _, e := normalizeWorkflowAIStageSubmissionForInput(`{"markdown":"# Derived replay report\nBounded result only.","claimSummary":[],"methodSummary":"Recorded retrieval only.","limitations":[],"confidence":"low"}`, node, input)
	if e != nil {
		t.Fatal(e)
	}
	if e = validateWorkflowAIStageOutput(node, value, input); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "derived-search-report.json"), value, 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("54 real source records' new decisions rendered by production report assembly; report bytes=%d; no original report/verdict modified", len(value))
}

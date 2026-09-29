package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestEvidenceGapClassificationAndExactSource(t *testing.T) {
	in := raw(`{"candidates":[{"reference":"[K-A]","quote":"The authors did not report the dropout rate."}]}`)
	for _, tc := range []struct {
		name, gap         string
		sufficient, valid bool
	}{
		{"query required", `{"field":"rate","status":"not_retrieved","required":false}`, true, false},
		{"optional missing", `{"field":"rate","status":"not_retrieved","required":false,"query":"dropout rate"}`, true, true},
		{"core missing", `{"field":"rate","status":"not_retrieved","required":true,"query":"dropout rate"}`, true, false},
		{"core honestly limited", `{"field":"rate","status":"not_retrieved","required":true,"query":"dropout rate"}`, false, true},
		{"unreadable", `{"field":"figure","status":"unreadable","required":false}`, true, true},
		{"absence unsupported", `{"field":"rate","status":"explicitly_not_reported","required":false}`, true, false},
		{"absence exact", `{"field":"rate","status":"explicitly_not_reported","required":false,"reference":"[K-A]","supportingQuote":"did not report the dropout rate"}`, true, true},
		{"absence paraphrase", `{"field":"rate","status":"explicitly_not_reported","required":false,"reference":"[K-A]","supportingQuote":"No rate reported"}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := decodeObject(raw(`{"evidenceGaps":[` + tc.gap + `]}`))
			v["coverage"] = map[string]any{"sufficientForClaimedScope": tc.sufficient}
			if err := validateEvidenceGaps(mustJSON(v), in); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestSufficientEvidenceStillSupplementsMissingOptionalField(t *testing.T) {
	syncOutput := raw(`{"structured":{"documentIds":["only-selected"]}}`)
	d := RunDetail{Run: Run{ID: "run"}, Steps: []Step{
		{NodeID: "evidence_sync", Status: StepCompleted, Output: syncOutput},
		{ID: "search", NodeID: "evidence_search", Status: StepCompleted, Ordinal: 4, Input: raw(`{"limit":20}`), Output: raw(`{"citations":[]}`)},
	}}
	for _, output := range []json.RawMessage{
		raw(`{"coverage":{"sufficientForClaimedScope":true},"evidenceGaps":[{"field":"可接受性","status":"not_retrieved","required":false,"query":"acceptability dropping out dropout odds ratio"}]}`),
		raw(`{"coverage":{"sufficientForClaimedScope":true,"gaps":["可接受性仅有定性表述，无三种运动脱落率点估计。"]}}`),
	} {
		repo := &localContinuationFixture{}
		s := &RuntimeService{repository: repo, now: time.Now, newID: func() (string, error) { return "gap-event", nil }}
		step := Step{ID: "screen", Ordinal: 5, Attempt: 1}
		handled, err := s.supplementLocalEvidence(context.Background(), d, step, output)
		if err != nil || !handled || !repo.called {
			t.Fatal(handled, err)
		}
		var ev struct {
			Queries     []string `json:"queries"`
			MaterialKey string   `json:"materialKey"`
		}
		_ = json.Unmarshal(repo.event.Payload, &ev)
		if len(ev.Queries) != 1 || ev.Queries[0] != "acceptability dropping out dropout odds ratio" || ev.MaterialKey != hashJSON(syncOutput) {
			t.Fatal(ev)
		}
		follow := d
		follow.Events = []RuntimeEvent{repo.event}
		repo.called = false
		if handled, err = s.supplementLocalEvidence(context.Background(), follow, step, output); handled || err != nil || repo.called {
			t.Fatal("same gap loops", err)
		}
	}
}

func TestEvidenceGapDoesNotSearchUnreadableOrExplicitAbsent(t *testing.T) {
	if q := gapFollowupQueries([]evidenceGap{{Status: "unreadable", Query: "figure"}, {Status: "explicitly_not_reported", Query: "rate"}}, []string{"脱落率未报告"}, nil); len(q) != 0 {
		t.Fatal(q)
	}
}

func TestGapReviewCannotApproveUnresolvedCoreField(t *testing.T) {
	in, out := acceptanceFixture(t)
	v := decodeObject(in)
	criteria, _ := researchAcceptanceCriteria(mustJSON(v["researchContract"]))
	v["requireOriginalDelivery"] = true
	v["requireEvidenceGapCheck"] = true
	v["acceptanceCriteria"] = appendEvidenceGapCriterion(appendOriginalDeliveryCriterion(criteria))
	v["evidenceScreening"] = map[string]any{"evidenceGaps": []evidenceGap{{Field: "effect", Status: "not_retrieved", Required: true, Query: "effect"}}}
	out["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "source"}, {"criterion-2", "met", "source"}, {"criterion-3", "met", "source"}, {"criterion-4", "met", "only excerpts checked"}}
	if err := validateResearchAcceptance(mustJSON(out), mustJSON(v)); err == nil {
		t.Fatal("missing core field approved")
	}
	out["approved"] = false
	out["requiredCorrections"] = []string{"回到证据综合补查 effect"}
	out["acceptanceChecks"].([]ResearchAcceptanceCheck)[3].Status = "not_met"
	if err := validateResearchAcceptance(mustJSON(out), mustJSON(v)); err != nil {
		t.Fatal(err)
	}
	gate, err := reviewCoreForGate(mustJSON(out))
	if err != nil || strings.Contains(string(gate), "acceptanceChecks") {
		t.Fatal(err)
	}
}

func TestEvidenceGapSchemaAndInstructionsSurvivePhaseProjection(t *testing.T) {
	for _, phase := range []string{"batch", "coverage"} {
		n := selectedEvidencePhaseNode(CompiledNode{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}, raw(`{"_selectedEvidence":{"phase":"`+phase+`"}}`))
		if err := (tool.JSONSchemaValidator{}).ValidateSchema(n.OutputSchema); err != nil {
			t.Fatal(err)
		}
		if schemaDeclaresProperty(n.OutputSchema, "evidenceGaps") != (phase == "coverage") {
			t.Fatal(phase)
		}
	}
}

func TestReportAbsenceCannotBypassReviewByOmittingGapList(t *testing.T) {
	in, out := acceptanceFixture(t)
	v := decodeObject(in)
	criteria, _ := researchAcceptanceCriteria(mustJSON(v["researchContract"]))
	v["requireEvidenceGapCheck"] = true
	v["acceptanceCriteria"] = appendEvidenceGapCriterion(criteria)
	v["context"] = map[string]any{"markdown": "| 脱落率 | 未报告 |"}
	out["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "table"}, {"criterion-2", "met", "table"}, {"criterion-3", "met", "gaps empty"}}
	if err := validateResearchAcceptance(mustJSON(out), mustJSON(v)); err == nil {
		t.Fatal("absence escaped with no gap declaration")
	}
	out["approved"] = false
	out["requiredCorrections"] = []string{"补查脱落率，不将未找到写成未报告"}
	out["acceptanceChecks"].([]ResearchAcceptanceCheck)[2].Status = "not_met"
	if err := validateResearchAcceptance(mustJSON(out), mustJSON(v)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body     string
		expected int
	}{
		{"| 脱落率 | 尚未核验 |", 0},
		{"| 脱落率 | 原文未报告 [K-A] |", 0},
		{"| 其他指标 | 原文未报告 [K-A] |", 1},
		{"| 脱落率 | 原文未报告 [K-WRONG] |", 1},
		{"Dropout: not reported", 1},
	} {
		v["context"] = map[string]any{"markdown": tc.body}
		v["evidenceScreening"] = map[string]any{"evidenceGaps": []evidenceGap{{Field: "脱落率", Status: "explicitly_not_reported", Reference: "[K-A]", SupportingQuote: "dropout not reported"}}}
		v["evidenceContext"] = []tool.CitationRef{{Reference: "[K-A]", Quote: "In this trial, dropout not reported."}}
		if got := unsupportedReportAbsenceClaims(mustJSON(v)); len(got) != tc.expected {
			t.Fatal(tc.body, got)
		}
	}
	v["context"] = map[string]any{"markdown": "脱落率原文未报告 [K-A]"}
	out["approved"] = true
	out["requiredCorrections"] = []string{}
	out["acceptanceChecks"].([]ResearchAcceptanceCheck)[2].Status = "met"
	if err := validateResearchAcceptance(mustJSON(out), mustJSON(v)); err != nil {
		t.Fatal("valid exact-source absence rejected", err)
	}
	v["context"] = map[string]any{"markdown": "脱落率原文未报告 [K-A]"}
	v["evidenceContext"] = []tool.CitationRef{{Reference: "[K-A]", Quote: "Different quote"}}
	if len(unsupportedReportAbsenceClaims(mustJSON(v))) != 1 {
		t.Fatal("incorrect source quote accepted")
	}
}

func TestNewReviewBindsIndependentAbsenceGuard(t *testing.T) {
	contract := raw(`{"successCriteria":["整理指标表"]}`)
	review := CompiledNode{ID: "independent_review", Kind: NodeAgentStage, PromptVersion: dynamicResearchReviewVersion, OutputSchema: researchAcceptanceReviewSchema(), Arguments: mustJSON(map[string]any{
		"context": map[string]any{"markdown": "脱落率未报告"}, "researchContract": contract, "evidenceScreening": map[string]any{},
	})}
	d := RunDetail{Run: Run{Compilation: Compilation{Nodes: []CompiledNode{review}}}}
	bound, err := bindNodeInput(d, review)
	if err != nil {
		t.Fatal(err)
	}
	v := decodeObject(bound)
	if v["requireEvidenceGapCheck"] != true || v["requireOriginalDelivery"] != true || len(v["unsupportedAbsenceClaims"].([]any)) != 1 || len(v["acceptanceCriteria"].([]any)) != 3 {
		t.Fatal(string(bound))
	}
}

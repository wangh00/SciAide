package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"html"
	"strings"
	"testing"
)

func TestIdentityAwareScreeningSelectionAndPortableReadback(t *testing.T) {
	s, repo, reader, detail, node := literatureFixture(t, 3)
	for i := range reader.values {
		w := research.Work{SourceID: "fixture", SourceRecordID: fmt.Sprint(i), Title: "Undergraduate writing feedback and revision quality", Abstract: strings.Repeat("Students revised essays using structured feedback in this study. ", 5), Year: 2024, Authors: []research.Author{{Name: "Jane Doe"}}}
		reader.values[i].Records = []research.SourceRecord{{ID: fmt.Sprint(i), Work: w}}
		reader.values[i].Preferred = w
	}
	research.AnnotateRecordIdentities(reader.values)
	input, e := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	var in struct {
		Candidates []literatureCandidate `json:"candidates"`
	}
	json.Unmarshal(input, &in)
	ids := []string{}
	assessments := []literatureAssessment{}
	notes := []map[string]any{}
	for _, c := range in.Candidates {
		if c.RecordIdentity == nil || c.SourceSHA256Version != LiteratureCandidateDigestVersion {
			t.Fatal("lost identity input", c)
		}
		ids = append(ids, c.ID)
		assessments = append(assessments, literatureAssessment{CandidateID: c.ID, Decision: "core", Relevance: "high", Reason: "Source grounded", ImportAction: "direct", Purpose: "Read and verify"})
		notes = append(notes, map[string]any{"candidateId": c.ID, "quotes": fixtureLiteratureNote(c).Quotes})
	}
	output := mustJSON(map[string]any{"phase": "batch", "summary": "Fixture screening", "candidateAssessments": assessments, "evidenceNotes": notes})
	phase := literaturePhaseNode(node, input)
	if e = (tool.JSONSchemaValidator{}).Validate(phase.OutputSchema, output); e != nil {
		t.Fatal(e)
	}
	if e = validateWorkflowAIStageOutput(phase, output, input); e != nil {
		t.Fatal(e)
	}
	execution := AIExecution{ID: "identity-screened", Status: "completed", Output: output}
	step := detail.Steps[1]
	step.Input = input
	if e = s.completeLiteratureScreening(context.Background(), detail, step, node, execution, output); e != nil {
		t.Fatal(e)
	}
	detail.Events = append(detail.Events, repo.event)
	detail.AIExecutions = append(detail.AIExecutions, execution)
	analysis := map[string]any{"recommendedCandidateIds": ids}
	if e = s.finalizeLiteratureEvidence(context.Background(), detail, literatureInput{State: literatureState(detail)}, &analysis); e != nil {
		t.Fatal(e)
	}
	kept := analysis["selectionCandidates"].([]literatureCandidate)
	if kept[0].SourceSHA256 == kept[1].SourceSHA256 {
		t.Fatal("same metadata conflated two source endpoints")
	}
	for i := range reader.values {
		reader.values[i].ID = "restored-" + reader.values[i].ID
		for j := range reader.values[i].RecordIdentity.Relations {
			reader.values[i].RecordIdentity.Relations[j].TargetCandidateID = "restored-" + reader.values[i].RecordIdentity.Relations[j].TargetCandidateID
		}
	}
	for i := range kept {
		kept[i].ID = "restored-" + kept[i].ID
		for j := range kept[i].RecordIdentity.Relations {
			kept[i].RecordIdentity.Relations[j].TargetCandidateID = "restored-" + kept[i].RecordIdentity.Relations[j].TargetCandidateID
		}
	}
	detail.Steps[2].Input = mustJSON(map[string]any{"candidates": kept})
	s.repository = &literatureReadRepository{detail: detail}
	value, e := s.ReadLiteratureCandidate(context.Background(), "project", "run", "review", kept[0].ID)
	if e != nil {
		t.Fatal("portable source readback failed", e)
	}
	if !strings.Contains(string(value), "Students revised essays") {
		t.Fatal("no original abstract")
	}
	reader.values[0].Records[0].Work.Abstract = "Changed actual source"
	reader.values[0].Preferred.Abstract = "Changed actual source"
	research.AnnotateRecordIdentities(reader.values)
	if _, e = s.ReadLiteratureCandidate(context.Background(), "project", "run", "review", kept[0].ID); e == nil {
		t.Fatal("changed source bypassed content identity")
	}
}
func TestIdentityAuditDoesNotSpendQuotaOnEmptyHintsOrHideConflict(t *testing.T) {
	values := []research.Candidate{}
	for i := 0; i < 40; i++ {
		c := auditCandidate(fmt.Sprintf("%03d", i), fmt.Sprintf("record-%d", i))
		c.RecordIdentity = &research.RecordIdentity{Version: research.RecordIdentityVersion}
		if i >= 20 {
			kind := "possible_same_work"
			if i == 39 {
				kind = "identifier_conflict"
			}
			c.RecordIdentity.Relations = []research.RecordRelationHint{{Kind: kind, TargetCandidateID: "other"}}
		}
		values = append(values, c)
	}
	q, ok := auditQueryCandidates("q", values)
	if !ok || q.Identity.RetainedCandidateDetails != 16 || q.Identity.OmittedCandidateDetails != 4 || q.Identity.IndependentStudyCountVerified {
		t.Fatal(q.Identity)
	}
	found := false
	for _, g := range q.Groups {
		if g.CandidateID == "039" {
			found = g.RecordIdentity != nil && g.RecordIdentity.Relations[0].Kind == "identifier_conflict"
		}
	}
	if !found {
		t.Fatal("conflict starved by empty or low-risk records")
	}
	var b strings.Builder
	writeRecordedGroupingMethod(&b, q)
	if !strings.Contains(html.UnescapeString(b.String()), "identifier_conflict") || !strings.Contains(b.String(), "全部待核实") {
		t.Fatal("hints lost from report", b.String())
	}
	compact := compactResearchRecordFacts(researchRecordAudit{Queries: []queryRecordAudit{q}})
	if compact.Queries[0].Identity == nil || compact.Queries[0].Groups != nil {
		t.Fatal("upstream facts not compact")
	}
	records := []literatureEvidenceRecord{}
	for _, c := range values {
		records = append(records, literatureEvidenceRecord{CandidateID: c.ID, RecordIdentity: c.RecordIdentity})
	}
	nav := mustJSON(literatureIdentityNavigation(records))
	if len(nav) > 24*1024 || !strings.Contains(string(nav), "identifier_conflict") || !strings.Contains(string(nav), `"truncated":true`) {
		t.Fatal("bounded multilevel navigation lost conflict", string(nav))
	}
}
func TestGroupingV1HistoryRetainsItsOwnMethod(t *testing.T) {
	c := auditCandidate("old", "r")
	c.Records[0].Grouping = &research.RecordGrouping{Version: "record-grouping-v1", CandidateID: "old", Action: "match_alias", ComparedAliases: []research.CandidateAlias{{Kind: "title_year", Value: "2024\told title"}}}
	q, ok := auditQueryCandidates("q", []research.Candidate{c})
	if !ok || q.Grouping.Version != "record-grouping-v1" || !strings.Contains(q.Grouping.Rules, "年份与规范化题名精确匹配") || strings.Contains(q.Grouping.Rules, "题名和年份不作为自动归并") {
		t.Fatal("old method relabeled", q)
	}
}

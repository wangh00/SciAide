package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
)

type recordAuditReader struct {
	values map[string][]research.Candidate
	err    error
	calls  []string
}

func (r *recordAuditReader) LiteratureCandidates(_ context.Context, project string, ids []string) ([]research.Candidate, error) {
	if project != "p" || len(ids) != 1 {
		return nil, fmt.Errorf("unexpected scope")
	}
	r.calls = append(r.calls, ids[0])
	return r.values[ids[0]], r.err
}
func auditCandidate(id string, records ...string) research.Candidate {
	c := research.Candidate{ID: id, ProjectID: "p"}
	for _, r := range records {
		c.Records = append(c.Records, research.SourceRecord{ID: r, ProjectID: "p", Work: research.Work{SourceID: "fixture", SourceRecordID: r, Title: "An observed bounded research record", Year: 2026}})
	}
	return c
}
func TestRecordAuditCountsAndStableSourceIdentity(t *testing.T) {
	values := []research.Candidate{auditCandidate("b", "r3"), auditCandidate("a", "r2", "r1")}
	a, ok := auditQueryCandidates("q", values)
	if !ok || a.SourceRecords != 3 || a.Candidates != 2 || a.GroupedRecords != 1 || a.Groups[0].CandidateID != "a" || a.Groups[0].Records[0].ID != "r1" {
		t.Fatalf("audit=%+v", a)
	}
	values[0], values[1] = values[1], values[0]
	values[0].Records[0], values[0].Records[1] = values[0].Records[1], values[0].Records[0]
	b, _ := auditQueryCandidates("q", values)
	if !rawJSONEqual(mustJSON(a), mustJSON(b)) {
		t.Fatal("ordering changed frozen identity")
	}
	for _, v := range [][]research.Candidate{{auditCandidate("a", "r"), auditCandidate("a", "s")}, {auditCandidate("a", "r"), auditCandidate("b", "r")}, {auditCandidate("a")}, {auditCandidate("", "r")}, {auditCandidate("a", "")}} {
		if _, ok := auditQueryCandidates("q", v); ok {
			t.Fatal("ambiguous records counted", v)
		}
	}
}
func TestRecordAuditBindingUsesIssuedQueriesAndFrozenReport(t *testing.T) {
	d := RunDetail{Run: Run{ProjectID: "p"}, Steps: []Step{{NodeID: "literature_discovery", Status: StepCompleted, Output: raw(`{"structured":{"queryIds":["q2","q1","q1"]}}`)}}}
	r := &recordAuditReader{values: map[string][]research.Candidate{"q1": {auditCandidate("a", "r1", "r2")}, "q2": {auditCandidate("a", "r1")}}}
	s := &RuntimeService{literature: r}
	n := CompiledNode{ID: "report_drafting", PromptVersion: researchReportVersion}
	before := mustJSON(researchSourceContext(d))
	for _, id := range []string{"evidence_screening", "method_selection"} {
		original := raw(`{"documentIds":["d"],"candidates":[]}`)
		got := s.bindResearchRecordAudit(context.Background(), d, CompiledNode{ID: id}, original)
		if selectedEvidenceSourceKey(d, original) != selectedEvidenceSourceKey(d, got) || len(got) > 4096 || !strings.Contains(string(got), `"sourceRecordCount":2`) || strings.Contains(string(got), `"recordId"`) {
			t.Fatal("upstream facts missing, oversized or changed checkpoint", string(got))
		}
		r.calls = nil
		r.err = fmt.Errorf("temporary metadata read failure")
		unavailable := s.bindResearchRecordAudit(context.Background(), d, CompiledNode{ID: id}, original)
		if selectedEvidenceSourceKey(d, got) != selectedEvidenceSourceKey(d, unavailable) || !strings.Contains(string(unavailable), `"status":"incomplete"`) {
			t.Fatal("transient audit read reset completed batches")
		}
		r.calls = nil
		r.err = nil
	}
	input := s.bindResearchRecordAudit(context.Background(), d, n, raw(`{}`))
	var v struct {
		Audit researchRecordAudit `json:"researchRecordAudit"`
	}
	_ = json.Unmarshal(input, &v)
	if v.Audit.Status != "available" || len(v.Audit.Queries) != 2 || v.Audit.Queries[0].SourceRecords != 2 || v.Audit.Queries[1].SourceRecords != 1 || strings.Join(r.calls, ",") != "q1,q2" {
		t.Fatal(string(input), r.calls)
	}
	if !rawJSONEqual(before, mustJSON(researchSourceContext(d))) {
		t.Fatal("legacy source context changed")
	}
	d.Steps = append(d.Steps, Step{NodeID: "report_drafting", Status: StepCompleted, Input: input})
	r.err = fmt.Errorf("reader unavailable")
	review := CompiledNode{ID: "independent_review", OutputSchema: independentReviewSchema()}
	bound := s.bindResearchRecordAudit(context.Background(), d, review, raw(`{}`))
	if !rawJSONEqual(bound, input) || len(r.calls) != 2 {
		t.Fatal("review refreshed report identity")
	}
	if err := validateReviewDependencies(d, Step{Input: bound}); err != nil {
		t.Fatal(err)
	}
	bad := decodeObject(bound)
	bad["researchRecordAudit"] = map[string]any{"status": "different"}
	if err := validateReviewDependencies(d, Step{Input: mustJSON(bad)}); err == nil {
		t.Fatal("different report/review audit accepted")
	}
	for _, state := range []string{"unavailable", "incomplete", "foreign candidate", "foreign record"} {
		t.Run(state, func(t *testing.T) {
			r.err = nil
			r.values["q1"] = []research.Candidate{auditCandidate("a", "r1")}
			s.literature = r
			switch state {
			case "unavailable":
				s.literature = nil
			case "incomplete":
				r.err = fmt.Errorf("offline")
			case "foreign candidate":
				r.values["q1"][0].ProjectID = "other"
			case "foreign record":
				r.values["q1"][0].Records[0].ProjectID = "other"
			}
			got := s.bindResearchRecordAudit(context.Background(), d, n, raw(`{}`))
			_ = json.Unmarshal(got, &v)
			if v.Audit.Status == "available" {
				t.Fatal("missing/foreign records treated as available", string(got))
			}
		})
	}
}
func TestRecordAuditAppendixDoesNotRewriteLegacyInput(t *testing.T) {
	old := raw(`{"reportAssembly":"host-report-assembly-v1","researchSourceContext":{"retrieval":{"queries":["bounded query"],"sources":[],"candidateCount":2}}}`)
	before, _ := reportRetrievalAppendix(old)
	q, _ := auditQueryCandidates("q", []research.Candidate{auditCandidate("a", "r1", "r2"), auditCandidate("b", "r3")})
	v := decodeObject(old)
	v["researchRecordAudit"] = researchRecordAudit{Version: recordAuditVersion, Status: "available", Queries: []queryRecordAudit{q}, Interpretation: "题录归并不是独立试验去重"}
	a, err := reportRetrievalAppendix(mustJSON(v))
	if err != nil || !strings.Contains(a, "3 条保存的来源记录归属到 2 个候选") || !strings.Contains(a, "fixture:r1") || !strings.Contains(a, "不代表独立试验数量") {
		t.Fatal(a, err)
	}
	after, _ := reportRetrievalAppendix(old)
	if before != after || !strings.Contains(before, "该字段不提供去重规则") {
		t.Fatal("old frozen appendix changed")
	}
}
func TestCapturedRecordAuditReplay(t *testing.T) {
	path := os.Getenv("SCIAIDE_RECORD_AUDIT_FIXTURE")
	if path == "" {
		t.Skip("optional task-limited readonly capture")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var values []research.Candidate
	if err = json.Unmarshal(b, &values); err != nil {
		t.Fatal(err)
	}
	a, ok := auditQueryCandidates("bedcbc11-cb05-4651-9331-3c5b5db41e36", values)
	if !ok || a.SourceRecords != 80 || a.Candidates != 61 || a.GroupedRecords != 19 {
		t.Fatalf("unexpected captured audit: %+v", a)
	}
	groups := 0
	for _, g := range a.Groups {
		if len(g.Records) > 1 {
			groups++
		}
	}
	if groups != 17 {
		t.Fatal(groups)
	}
	t.Logf("captured query: %d records -> %d candidates; %d multi-source groups; audit JSON %d bytes (not independent trials)", a.SourceRecords, a.Candidates, groups, len(mustJSON(a)))
}

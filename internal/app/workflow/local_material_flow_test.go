package workflow

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"testing"
	"time"
)

type localContinuationFixture struct {
	RuntimeRepository
	called bool
	event  RuntimeEvent
}

func (f *localContinuationFixture) QueueLocalEvidenceSearch(_ context.Context, _, _, _ string, _ int, _ time.Time, event RuntimeEvent) error {
	f.called = true
	f.event = event
	return nil
}
func TestLocalSupplementIsBoundedAndDoesNotReimport(t *testing.T) {
	repo := &localContinuationFixture{}
	s := &RuntimeService{repository: repo, now: time.Now, newID: func() (string, error) { return "event", nil }}
	d := RunDetail{Run: Run{ID: "run"}, Steps: []Step{{ID: "search", NodeID: "evidence_search", Status: StepCompleted, Ordinal: 4, Input: raw(`{"limit":20}`), Output: raw(`{"citations":[]}`)}}}
	step := Step{ID: "screen", Ordinal: 5, Attempt: 1}
	out := raw(`{"coverage":{"sufficientForClaimedScope":false},"supplementalQueries":["walking effect credible interval"]}`)
	handled, err := s.supplementLocalEvidence(context.Background(), d, step, out)
	if err != nil || !handled || !repo.called {
		t.Fatal(handled, err)
	}
	d.Events = []RuntimeEvent{{Type: "workflow.local_evidence_requested", Payload: raw(`{"queries":["walking effect credible interval"]}`)}}
	repo.called = false
	handled, err = s.supplementLocalEvidence(context.Background(), d, step, out)
	if err != nil || handled || repo.called {
		t.Fatal("duplicate queries rerun", err)
	}
	d.Events = append(d.Events, d.Events[0])
	handled, err = s.supplementLocalEvidence(context.Background(), d, step, raw(`{"supplementalQueries":["new third round"]}`))
	if err != nil || handled {
		t.Fatal("unbounded retry", err)
	}
	args := map[string]any{"query": "topic", "documentIds": []string{"selected"}, "queries": []string{"results", "methods", "limitations"}}
	bindSupplementaryEvidenceQueries(d, args)
	b, _ := json.Marshal(args)
	if !json.Valid(b) {
		t.Fatal(string(b))
	}
	if args["documentIds"].([]string)[0] != "selected" {
		t.Fatal("scope broadened")
	}
}
func TestInsufficientEvidenceCannotPassOriginalDeliveryCriterion(t *testing.T) {
	input, review := acceptanceFixture(t)
	v := decodeObject(input)
	criteria, _ := researchAcceptanceCriteria(mustJSON(v["researchContract"]))
	v["requireOriginalDelivery"] = true
	v["acceptanceCriteria"] = appendOriginalDeliveryCriterion(criteria)
	v["evidenceScreening"] = map[string]any{"coverage": map[string]any{"sufficientForClaimedScope": false}}
	review["acceptanceChecks"] = []ResearchAcceptanceCheck{{"criterion-1", "met", "object"}, {"criterion-2", "met", "method"}, {"criterion-3", "met", "accepted limited evidence"}}
	if validateResearchAcceptance(mustJSON(review), mustJSON(v)) == nil {
		t.Fatal("limited evidence passed original task")
	}
	review["approved"] = false
	review["requiredCorrections"] = []string{"补查所选材料的结果"}
	review["acceptanceChecks"].([]ResearchAcceptanceCheck)[2].Status = "not_met"
	if err := validateResearchAcceptance(mustJSON(review), mustJSON(v)); err != nil {
		t.Fatal(err)
	}
}
func TestMaterialSourceContextOverridesEarlyAssumptions(t *testing.T) {
	d := RunDetail{Steps: []Step{{NodeID: "evidence_sync", Status: StepCompleted, Output: raw(`{"structured":{"documentIds":["doc"],"documents":[{"status":"ready"}]}}`)}, {NodeID: "evidence_search", Status: StepCompleted, Output: raw(`{"structured":{"totalMatches":20}}`)}}}
	v := researchSourceContext(d)
	if v["indexedMaterials"] == nil || v["retrievedEvidence"] == nil {
		t.Fatal(v)
	}
}

func TestSingleSelectedFullTextUsesScopeCoverageNotTwoPaperQuota(t *testing.T) {
	c := tool.CitationRef{ID: "a", Reference: "[K-AAAAAAAAAAAA]", DocumentID: "paper", SourceName: "paper.pdf", MIMEType: "application/pdf", Quote: "Exact result"}
	input := map[string]any{"selectedMaterialOnly": true, "candidates": []tool.CitationRef{c}, "screening": map[string]any{"recommendedReferences": []string{c.Reference}, "citationAssessments": []map[string]any{{"reference": c.Reference, "sourceLevel": "full_text"}}, "coverage": map[string]any{"strength": "adequate", "sufficientForClaimedScope": true}}}
	audit := auditCitationSelection(mustJSON(input), []tool.CitationRef{c}, false)
	if audit.RequiresLimitedAcceptance || !audit.HostMinimumCoverageMet {
		t.Fatal(audit)
	}
	input["selectedMaterialOnly"] = false
	if !auditCitationSelection(mustJSON(input), []tool.CitationRef{c}, false).RequiresLimitedAcceptance {
		t.Fatal("legacy scope relaxed")
	}
}

type reuseRepositoryFixture struct {
	RuntimeRepository
	output json.RawMessage
}

func (r *reuseRepositoryFixture) CompleteStep(_ context.Context, _, _ string, output json.RawMessage, _ int, _ bool, _ json.RawMessage, _ time.Time, event RuntimeEvent) error {
	r.output = output
	return nil
}

type reuseMaterialFixture struct{ missing bool }

func (f reuseMaterialFixture) ReferenceMaterials(context.Context, string, string, []string) ([]attachment.Attachment, error) {
	if f.missing {
		return nil, nil
	}
	return []attachment.Attachment{{ID: "chosen", Status: attachment.StatusReady}}, nil
}
func TestReuseExplicitLocalMaterialsAndNeverAutoAcceptNewCandidates(t *testing.T) {
	repo := &reuseRepositoryFixture{}
	s := &RuntimeService{repository: repo, materialLoader: reuseMaterialFixture{}, projects: fixedProjectLoader{project.Project{ID: "p", WorkspacePath: t.TempDir()}}, now: time.Now, newID: func() (string, error) { return "reuse", nil }}
	node := CompiledNode{ID: "candidate_review", Kind: NodeCandidateSelection}
	d := RunDetail{Run: Run{ID: "run", ProjectID: "p", Inputs: raw(`{}`), Compilation: Compilation{Nodes: []CompiledNode{node, {ID: "next", Kind: NodeHumanConfirmation}}}}, Steps: []Step{{ID: "step", NodeID: "candidate_review", Ordinal: 0}, {ID: "next", NodeID: "next", Ordinal: 1}}}
	d.Run.InputsSHA256 = hashJSON(d.Run.Inputs)
	encoded, _ := canonicalJSON(d.Run.Compilation)
	d.Run.CompilationSHA256 = hashBytes(encoded)
	d.Run.Compilation.CompilationSHA256 = d.Run.CompilationSHA256
	step := d.Steps[0]
	step.Input = raw(`{"reuseSelectedMaterials":true,"referenceMaterials":[{"attachmentId":"chosen"}],"candidates":[]}`)
	step.InputSHA256 = hashJSON(step.Input)
	handled, err := s.reuseSelectedMaterials(context.Background(), d, step, node)
	if err != nil || !handled || len(repo.output) == 0 {
		t.Fatal(handled, err)
	}
	s.materialLoader = reuseMaterialFixture{missing: true}
	if _, err := s.reuseSelectedMaterials(context.Background(), d, step, node); err == nil {
		t.Fatal("missing attachment accepted")
	}
	step.Input = raw(`{"reuseSelectedMaterials":true,"referenceMaterials":[{"attachmentId":"chosen"}],"candidates":[{"id":"new"}]}`)
	step.InputSHA256 = hashJSON(step.Input)
	if handled, err := s.reuseSelectedMaterials(context.Background(), d, step, node); handled || err != nil {
		t.Fatal("new candidate silently approved", err)
	}
}

func TestLocalEvidenceRetainsEarlierRoundsAndRejectsTamperedSnapshot(t *testing.T) {
	c1 := tool.CitationRef{ChunkID: "round1", Quote: "one", QuoteSHA256: "q1", DocumentID: "doc", IndexVersionID: "v"}
	c2 := tool.CitationRef{ChunkID: "round2", Quote: "two", QuoteSHA256: "q2", DocumentID: "doc", IndexVersionID: "v"}
	old := mustJSON(map[string]any{"citations": []tool.CitationRef{c1}})
	d := RunDetail{Events: []RuntimeEvent{{Type: "workflow.local_evidence_requested", Payload: mustJSON(map[string]any{"previousSearchOutput": old, "previousSearchHash": hashJSON(old), "queries": []string{"one", "two", "three", "four"}})}}}
	result, err := mergeLocalEvidenceResults(d, Step{NodeID: "evidence_search"}, tool.Result{Citations: []tool.CitationRef{c2}, Structured: raw(`{"documentCoverage":[{"documentId":"doc"}]}`)})
	if err != nil || len(result.Citations) != 2 || result.Citations[0].ChunkID != "round1" {
		t.Fatal(result, err)
	}
	second := mustJSON(map[string]any{"citations": result.Citations})
	d.Events = append(d.Events, RuntimeEvent{Type: "workflow.local_evidence_requested", Payload: mustJSON(map[string]any{"previousSearchOutput": second, "previousSearchHash": hashJSON(second)})})
	third := tool.CitationRef{ChunkID: "round3", Quote: "three", QuoteSHA256: "q3", DocumentID: "doc", IndexVersionID: "v"}
	result, err = mergeLocalEvidenceResults(d, Step{NodeID: "evidence_search"}, tool.Result{Citations: []tool.CitationRef{third}, Structured: raw(`{"documentCoverage":[{"documentId":"doc"}]}`)})
	if err != nil || len(result.Citations) != 3 {
		t.Fatal("second continuation lost prior evidence", err)
	}
	input := mustJSON(map[string]any{"documentIds": []string{"doc"}, "candidates": result.Citations})
	prepared, err := prepareSelectedEvidence(d, input)
	if err != nil {
		t.Fatal(err)
	}
	var candidates struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	_ = json.Unmarshal(prepared, &candidates)
	if len(candidates.Candidates) != 3 {
		t.Fatal("final screening lost round1 or round2 evidence")
	}
	args := map[string]any{"queries": []string{"results", "methods", "limitations"}}
	bindSupplementaryEvidenceQueries(d, args)
	var queries []string
	_ = json.Unmarshal(mustJSON(args["queries"]), &queries)
	if len(queries) != 7 {
		t.Fatal("follow-up silently dropped", queries)
	}
	d.Events[0].Payload = mustJSON(map[string]any{"previousSearchOutput": old, "previousSearchHash": "bad"})
	if _, err := mergeLocalEvidenceResults(d, Step{NodeID: "evidence_search"}, tool.Result{}); err == nil {
		t.Fatal("tampered frozen citation accepted")
	}
}

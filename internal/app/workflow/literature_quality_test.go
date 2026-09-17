package workflow

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/research"
	"testing"
)

func TestLiteratureCoreRequiresAbstract(t *testing.T) {
	output := raw(`{"candidateAssessments":[{"candidateId":"a","decision":"core"}]}`)
	if validateLiteratureCore(output, raw(`{"_literature":{"phase":"batch"},"candidates":[{"id":"a"}]}`)) == nil {
		t.Fatal("metadata-only core accepted")
	}
	if err := validateLiteratureCore(output, raw(`{"_literature":{"phase":"coverage"},"candidates":[{"id":"a","abstract":"Source abstract"}]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestLiteratureExcludedRecheckMustUseIssuedIDs(t *testing.T) {
	input := raw(`{"_literature":{"phase":"coverage"},"candidates":[],"excludedAssessments":[{"candidateId":"excluded"}]}`)
	if err := validateLiteratureCore(raw(`{"recheckCandidateIds":["excluded"]}`), input); err != nil {
		t.Fatal(err)
	}
	if err := validateLiteratureCore(raw(`{"recheckCandidateIds":["invented"]}`), input); err == nil {
		t.Fatal("invented recheck ID accepted")
	}
	s, repo, _, detail, node := literatureFixture(t, 0)
	step := detail.Steps[1]
	step.Input = input
	if err := s.completeLiteratureScreening(context.Background(), detail, step, node, AIExecution{ID: "coverage"}, raw(`{"recheckCandidateIds":["excluded"]}`)); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 1 || repo.rewind != "" {
		t.Fatal("recheck should repeat screening without new search")
	}
}

func TestLiteraturePrefilterOnlyExplicitPeerReview(t *testing.T) {
	s, _, reader, detail, node := literatureFixture(t, 0)
	reader.values = []research.Candidate{
		{ID: "review", Preferred: research.Work{Title: "Review of an article", WorkType: "peer-review"}},
		{ID: "uncertain", Preferred: research.Work{Title: "Review of fasting", WorkType: "journal-article"}},
	}
	input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Candidates []literatureCandidate `json:"candidates"`
		Literature literatureInput       `json:"_literature"`
	}
	if err := json.Unmarshal(input, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Candidates) != 1 || decoded.Candidates[0].ID != "uncertain" || decoded.Literature.AutomaticExcluded != 1 {
		t.Fatal(string(input))
	}
}

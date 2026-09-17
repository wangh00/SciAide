package workflow

import (
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/research"
	"testing"
)

func TestFailedLiteraturePageIsRetriedOnceAndSurvivesRestart(t *testing.T) {
	state := literatureCheckpoint{}
	updateLiteraturePages(&state, []research.SourceSearch{{SourceID: "pubmed", EffectiveQuery: "trial", Status: research.SearchOK, LimitReached: true}})
	result := literatureScreening{}
	result.Coverage.Sufficient = true
	if !scheduleLiteratureSearch(&state, result, 0) {
		t.Fatal("missing next page")
	}
	failure := research.SourceSearch{SourceID: "pubmed", EffectiveQuery: "trial", Offset: 20, Status: research.SearchFailed, Retryable: true}
	updateLiteraturePages(&state, []research.SourceSearch{failure})
	encoded, _ := json.Marshal(state)
	state = literatureCheckpoint{}
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	if !scheduleLiteratureSearch(&state, result, 0) || state.NextOffset != 20 || state.Round != 0 {
		t.Fatal("lost failed page", state)
	}
	updateLiteraturePages(&state, []research.SourceSearch{failure})
	if scheduleLiteratureSearch(&state, result, 0) || !literaturePagesIncomplete(state) {
		t.Fatal("unbounded retry or false completion", state)
	}
}

func TestLiteratureSourceFailureDoesNotTriggerExpansion(t *testing.T) {
	state := literatureCheckpoint{SourceFailures: true}
	result := literatureScreening{}
	result.Coverage.Sufficient = true
	if scheduleLiteratureSearch(&state, result, 0) {
		t.Fatal("failure triggered search")
	}
}
func TestLiteraturePagesRemainIndependent(t *testing.T) {
	state := literatureCheckpoint{}
	updateLiteraturePages(&state, []research.SourceSearch{
		{SourceID: "pubmed", EffectiveQuery: "first", Status: research.SearchOK, LimitReached: true},
		{SourceID: "openalex", EffectiveQuery: "second", Status: research.SearchOK, LimitReached: true},
	})
	result := literatureScreening{SupplementalQueries: []string{"new"}, Recommendation: "expand_search"}
	if !scheduleLiteratureSearch(&state, result, 0) || state.NextSources[0] != "pubmed" || state.Round != 0 {
		t.Fatal(state)
	}
	updateLiteraturePages(&state, []research.SourceSearch{{SourceID: "pubmed", EffectiveQuery: "first", Offset: 20, Status: research.SearchOK, LimitReached: true}})
	if !scheduleLiteratureSearch(&state, result, 0) || state.NextSources[0] != "openalex" {
		t.Fatal("starved other page", state)
	}
	state.NoGrowth = 20
	state.Pages = nil
	state.PageLedger = nil
	if !scheduleLiteratureSearch(&state, result, 0) || state.Round != 1 {
		t.Fatal("count-based premature stop", state)
	}
}

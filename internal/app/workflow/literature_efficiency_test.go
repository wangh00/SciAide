package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestStableLiteratureGroupsKeepUnchangedMembersAfterInsertion(t *testing.T) {
	records := []literatureEvidenceRecord{}
	for i := 0; i < 162; i++ {
		records = append(records, literatureEvidenceRecord{CandidateID: fmt.Sprintf("candidate-%04d", i), Title: strings.Repeat("source ", 220), Assessment: literatureAssessment{Decision: "support"}})
	}
	groups, membership := stableLiteratureGroups(records, nil)
	encoded := mustJSON(membership)
	var restored [][]string
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	updated := append([]literatureEvidenceRecord(nil), records...)
	updated[80].Title = "Changed evidence"
	for i := 0; i < 12; i++ {
		updated = append([]literatureEvidenceRecord{{CandidateID: fmt.Sprintf("a-new-%d", i), Title: "New evidence"}}, updated...)
	}
	next, nextMembership := stableLiteratureGroups(updated, restored)
	unchanged := 0
	for i, group := range groups {
		if reflect.DeepEqual(group, next[i]) {
			unchanged++
		}
		if !reflect.DeepEqual(membership[i], nextMembership[i]) {
			t.Fatal("insertion shifted an existing group")
		}
	}
	if unchanged != len(groups)-1 || len(next) != len(groups)+1 {
		t.Fatalf("updated groups=%d original=%d unchanged=%d", len(next), len(groups), unchanged)
	}
	seen := map[string]bool{}
	for _, group := range next {
		for _, record := range group {
			if seen[record.CandidateID] {
				t.Fatal("duplicate record", record.CandidateID)
			}
			seen[record.CandidateID] = true
		}
	}
	if len(seen) != 174 {
		t.Fatal("lost records", len(seen))
	}
}

func TestLiteratureNoGrowthPageDoesNotRequireCoverage(t *testing.T) {
	s, repo, _, detail, node := literatureFixture(t, 0)
	state := literatureCheckpoint{QueryIDs: []string{"q1"}, Pages: []literaturePage{{Query: "original", Source: "pubmed", Offset: 20}}, FailedSearches: map[string]bool{"unavailable": true}, SourceFailures: true}
	detail.Events = append(detail.Events, RuntimeEvent{Type: literatureCheckpointEvent, Payload: mustJSON(map[string]any{"discoveryId": "discovery", "state": state})})
	input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{"researchContext":"preserved"}`))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Literature literatureInput `json:"_literature"`
	}
	json.Unmarshal(input, &in)
	if in.Literature.Phase != "selection" || in.Literature.State.PageRequests != 0 || len(in.Literature.State.Pages) != 0 {
		t.Fatal("first-page boundary triggered paging", string(input))
	}
	if repo.continuations != 0 || len(detail.AIExecutions) != 0 {
		t.Fatal("preparation mutated state")
	}
	// The checkpoint still bounds retries even after a service restart.
	state.PageRequests = 30
	detail.Events = append(detail.Events, RuntimeEvent{Type: literatureCheckpointEvent, Payload: mustJSON(map[string]any{"discoveryId": "discovery", "state": state})})
	input, err = s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(input, &in)
	if in.Literature.Phase != "selection" || !in.Literature.State.SourceFailures || literaturePagesIncomplete(in.Literature.State) {
		t.Fatal("budget incorrectly declared retrieval complete")
	}
}

func TestLiteraturePromptDropsOnlyHostBookkeeping(t *testing.T) {
	input := mustJSON(map[string]any{
		"_literature":       literatureInput{Phase: "batch", Total: 2, Pending: 1, Fingerprints: map[string]string{"a": "hash"}, State: literatureCheckpoint{QueryIDs: []string{"private-query"}, PageLedger: map[string]int{"private-ledger": 20}, RecheckedSources: map[string]string{"a": "hash"}}},
		"candidates":        []literatureCandidate{{ID: "a", Title: "Full title", Abstract: strings.Repeat("Exact original evidence. ", 500)}},
		"researchContext":   map[string]any{"scope": "frozen", "number": json.Number("9007199254740993")},
		"readbackQuestions": []literatureRecheck{{CandidateID: "a", Question: "Exact question"}},
	})
	before := string(input)
	projected := literatureModelInput(input)
	var source, model map[string]json.RawMessage
	json.Unmarshal(input, &source)
	json.Unmarshal(projected, &model)
	for _, key := range []string{"researchContext", "readbackQuestions"} {
		if !rawJSONEqual(source[key], model[key]) {
			t.Fatal("projection changed scientific inputs", key)
		}
	}
	var original []literatureCandidate
	var projectedCandidates []struct {
		ID               string
		Title            string
		AbstractSegments []literatureSegment
	}
	json.Unmarshal(source["candidates"], &original)
	json.Unmarshal(model["candidates"], &projectedCandidates)
	var restored strings.Builder
	for _, segment := range projectedCandidates[0].AbstractSegments {
		restored.WriteString(segment.Text)
	}
	if restored.String() != original[0].Abstract || projectedCandidates[0].ID != original[0].ID || projectedCandidates[0].Title != original[0].Title {
		t.Fatal("source segment projection lost evidence")
	}
	if string(input) != before || strings.Contains(string(projected), "private-ledger") || strings.Contains(string(projected), "private-query") || !strings.Contains(string(projected), "alreadyRecheckedCandidateIds") {
		t.Fatal("bookkeeping projection damaged audit or retained irrelevant state")
	}
}

func TestLiteratureRechecksRequireQuestionAndDoNotRepeatUnchangedSource(t *testing.T) {
	result := literatureScreening{RecheckCandidateIDs: []string{"a"}, Rechecks: []literatureRecheck{{CandidateID: "a", Question: "Does the abstract identify the control group?"}}}
	offered := map[string]bool{"a": true}
	info := literatureInput{Fingerprints: map[string]string{"a": "current"}}
	if err := validateLiteratureRechecks(result, info, offered); err != nil {
		t.Fatal(err)
	}
	info.State.RecheckedSources = map[string]string{"a": "current"}
	if validateLiteratureRechecks(result, info, offered) == nil {
		t.Fatal("repeated same-source reread accepted")
	}
	info.Fingerprints["a"] = "updated"
	if err := validateLiteratureRechecks(result, info, offered); err != nil {
		t.Fatal("new source evidence could not be checked", err)
	}
	result.Rechecks = nil
	if validateLiteratureRechecks(result, info, offered) == nil {
		t.Fatal("unfocused reread accepted")
	}
}

func TestLiteratureAbstractQuoteDoesNotRequireCopyingTitle(t *testing.T) {
	node, input, text := literatureQuoteFixture(t)
	output := decodeObject(raw(text))
	note := output["evidenceNotes"].([]any)[0].(map[string]any)
	note["quotes"] = note["quotes"].([]any)[1:]
	if err := validateWorkflowAIStageOutput(node, mustJSON(output), input); err != nil {
		t.Fatal(err)
	}
	note["quotes"] = []any{}
	if validateWorkflowAIStageOutput(node, mustJSON(output), input) == nil {
		t.Fatal("evidence without source quote accepted")
	}
}

func TestLiteratureCoverageKeyIgnoresQueueProgressButTracksEvidence(t *testing.T) {
	info := literatureInput{Phase: "coverage", State: literatureCheckpoint{QueryIDs: []string{"q1"}, Pages: []literaturePage{{Query: "query", Source: "pubmed", Offset: 20}}}}
	key := func(info literatureInput, evidence string) string {
		t.Helper()
		value, err := literatureCoverageInput(map[string]any{"researchContext": "frozen", "evidenceRecords": evidence}, info)
		if err != nil {
			t.Fatal(err)
		}
		var in struct {
			Literature literatureInput `json:"_literature"`
		}
		json.Unmarshal(value, &in)
		return in.Literature.CoverageKey
	}
	baseline := key(info, "same evidence")
	info.State.QueryIDs = append(info.State.QueryIDs, "q2")
	info.State.PageRequests++
	info.State.Round++
	if key(info, "same evidence") != baseline {
		t.Fatal("unchanged science invalidated by bookkeeping")
	}
	if key(info, "new evidence") == baseline {
		t.Fatal("changed evidence reused")
	}
	info.State.SourceFailures = true
	if key(info, "same evidence") == baseline {
		t.Fatal("source status change reused")
	}
	info.State.SourceFailures = false
	info.State.Pages = nil
	if key(info, "same evidence") == baseline {
		t.Fatal("retrieval completion change reused")
	}
}

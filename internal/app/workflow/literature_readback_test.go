package workflow

import (
	"context"
	"encoding/json"
	"testing"
)

type literatureReadRepository struct {
	RuntimeRepository
	detail RunDetail
}

func (r *literatureReadRepository) GetRun(_ context.Context, projectID, runID string) (RunDetail, error) {
	return r.detail, nil
}

func TestLiteratureSelectionReadbackChecksFrozenContent(t *testing.T) {
	s, _, reader, detail, node := literatureFixture(t, 1)
	c := literatureCandidates(reader.values)[0]
	c.SourceSHA256 = hashJSON(mustJSON(c))
	c.Abstract = ""
	detail.Steps[2].Input = mustJSON(map[string]any{"candidates": []literatureCandidate{c}})
	detail.Events = []RuntimeEvent{{Type: literatureCheckpointEvent, Payload: mustJSON(map[string]any{"discoveryId": "discovery", "state": literatureCheckpoint{QueryIDs: []string{"q1"}}})}}
	repo := &literatureReadRepository{detail: detail}
	s.repository = repo
	value, err := s.ReadLiteratureCandidate(context.Background(), "project", "run", "review", "0")
	if err != nil {
		t.Fatal(err)
	}
	var got literatureCandidate
	json.Unmarshal(value, &got)
	if got.Abstract != "Complete abstract" {
		t.Fatal("source not read")
	}
	if _, err := s.ReadLiteratureCandidate(context.Background(), "project", "run", "review", "invented"); err == nil {
		t.Fatal("unknown candidate allowed")
	}
	reader.values[0].Preferred.Abstract = "Modified content"
	if _, err := s.ReadLiteratureCandidate(context.Background(), "project", "run", "review", "0"); err == nil {
		t.Fatal("changed source substituted")
	}
	if err := validateAIStageProtocol(node); err != nil {
		t.Fatal(err)
	}
}

func TestLiteratureSearchRetainsYearScopeForSupplementAndPages(t *testing.T) {
	s, _, _, detail, _ := literatureFixture(t, 0)
	detail.Steps = append(detail.Steps, Step{NodeID: "literature_query_expansion", Status: StepCompleted, Output: raw(`{"analysis":{"publicationYears":{"from":2020,"to":2025}}}`)})
	detail.Events = []RuntimeEvent{{Type: literatureCheckpointEvent, Payload: mustJSON(map[string]any{"discoveryId": "discovery", "state": literatureCheckpoint{Phase: "search", NextQueries: []string{"new"}, NextSources: []string{"pubmed"}, NextOffset: 20}})}}
	input, err := s.prepareLiteratureInput(context.Background(), detail, CompiledNode{ID: "literature_discovery"}, raw(`{"query":"original","publicationYears":{"from":2022,"to":2024}}`))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Years  struct{ From, To int } `json:"publicationYears"`
		Offset int                    `json:"offset"`
	}
	json.Unmarshal(input, &in)
	if in.Years.From != 2020 || in.Years.To != 2025 || in.Offset != 20 {
		t.Fatal(string(input))
	}
}

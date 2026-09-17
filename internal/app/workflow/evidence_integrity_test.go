package workflow

import (
	"encoding/json"
	"testing"
)

func TestOverviewRejectsWrongChunkAndMetadataEvidence(t *testing.T) {
	cases := []struct {
		quote, support string
		valid          bool
	}{{"Exact result P = 0.11.", "P = 0.11", true}, {"Author name and title", "P = 0.11", false}, {"## Bibliographic metadata\nAuthors: A", "Authors: A", false}}
	for _, tc := range cases {
		input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []map[string]any{{"reference": "[K-AAA]", "documentId": "doc", "quote": tc.quote}}})
		output := mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{"[K-AAA]"}, "citationAssessments": []map[string]any{{"reference": "[K-AAA]", "decision": "core", "supportingQuote": tc.support}}})
		if (validateSelectedEvidence(output, input) == nil) != tc.valid {
			t.Fatal(tc)
		}
	}
}

func TestResearchSourceContextPreservesRetrievalAndBibliography(t *testing.T) {
	detail := RunDetail{Steps: []Step{{NodeID: "literature_discovery", Status: StepCompleted, Output: raw(`{"structured":{"queries":["topic"],"candidateCount":74,"sources":[{"sourceId":"pubmed","count":20}]}}`)}, {NodeID: "candidate_screening", Status: StepCompleted, Output: raw(`{"candidates":[{"id":"a","doi":"10.1/test","venue":"Journal","authors":[{"name":"A"}]}]}`)}}}
	out := addResearchSourceContext(detail, CompiledNode{ID: "independent_review"}, raw(`{}`))
	var obj map[string]any
	json.Unmarshal(out, &obj)
	provenance := obj["researchSourceContext"].(map[string]any)
	if provenance["retrieval"] == nil || provenance["bibliography"] == nil {
		t.Fatal(string(out))
	}
}

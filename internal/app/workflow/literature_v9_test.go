package workflow

import (
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/tool"
	"testing"
)

func TestEvidenceOverviewFitsFortyThreeDocumentsWithoutBatch(t *testing.T) {
	docs := []string{}
	citations := []tool.CitationRef{}
	for i := 0; i < 43; i++ {
		id := string(rune('A' + i))
		docs = append(docs, id)
		citations = append(citations, tool.CitationRef{DocumentID: id, Reference: "K-" + id, Quote: "Original source excerpt"})
	}
	input, err := prepareSelectedEvidence(RunDetail{}, mustJSON(map[string]any{"documentIds": docs, "candidates": citations}))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	json.Unmarshal(input, &in)
	if !in.Info.Direct || in.Info.Phase != "coverage" || len(in.Info.Documents) != 43 {
		t.Fatal(string(input))
	}
	node := selectedEvidencePhaseNode(CompiledNode{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}, input)
	if !schemaDeclaresProperty(node.OutputSchema, "documentAnalyses") {
		t.Fatal("missing document coverage contract")
	}
	if _, ok := automaticEvidenceCitations(raw(`{"screening":{"recommendation":"proceed_limited","coverage":{"sufficientForClaimedScope":false}}}`)); ok {
		t.Fatal("limited evidence auto-confirmed")
	}
}

func TestOverviewOnlyAssessesRecommendedCitations(t *testing.T) {
	input := raw(`{"_selectedEvidence":{"phase":"coverage"},"candidates":[{"reference":"[K-AAA]","documentId":"a","quote":"Exact source result."},{"reference":"[K-BBB]","documentId":"b"}]}`)
	output := raw(`{"recommendation":"proceed","recommendedReferences":["[K-AAA]"],"citationAssessments":[{"reference":"[K-AAA]","decision":"core","supportingQuote":"Exact source result."}]}`)
	if err := validateSelectedEvidence(output, input); err != nil {
		t.Fatal(err)
	}
	bad := raw(`{"summary":"unsupported [K-CCC]","recommendation":"proceed","recommendedReferences":["[K-AAA]"],"citationAssessments":[{"reference":"[K-AAA]","decision":"core"}]}`)
	if err := validateSelectedEvidence(bad, input); err == nil {
		t.Fatal("unissued marker accepted")
	}
}

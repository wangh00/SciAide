package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestSupportingQuoteTypography(t *testing.T) {
	for _, tc := range []struct {
		source, quote string
		valid         bool
	}{
		{"nighttime‐specific smartphone use", "nighttime-specific smartphone use", true},
		{"nighttime‑specific use", "nighttime-specific use", true},
		{"中文：nighttime‐specific 结果。", "nighttime-specific 结果。", true},
		{"Reported\nsevere\u00a0sleep disturbance.", "Reported severe sleep disturbance.", true},
		{"OR 1.41", "OR 1.59", false},
		{"effect −0.3", "effect -0.3", false},
		{"effect ‐0.3", "effect -0.3", false},
		{"10–20 participants", "10-20 participants", false},
		{"No benefit was observed.", "Benefit was observed.", false},
		{"first result; other text; last result", "first result last result", false},
		{"nighttime‐specific; nighttime‑specific", "nighttime-specific", false},
	} {
		q, ok := exactSupportingQuote(tc.source, tc.quote)
		if ok != tc.valid {
			t.Fatalf("%+v got=%q ok=%v", tc, q, ok)
		}
		if ok && !strings.Contains(tc.source, q) {
			t.Fatalf("returned text is not exact source: %q", q)
		}
	}
}

func TestCanonicalQuoteWithReissuedMarker(t *testing.T) {
	source := "Observed nighttime‐specific use."
	ref := tool.CitationRef{Reference: "[K-ORIGINAL]", IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: citation.QuoteSHA256(source), Quote: source}
	compilation := Compilation{Nodes: []CompiledNode{{ID: "search", Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, {ID: "evidence_screening"}}, Edges: []Edge{{FromNode: "search", FromPort: "citations", ToNode: "evidence_screening", ToPort: "candidates"}}}
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []tool.CitationRef{ref}})
	step := Step{NodeID: "evidence_screening", Ordinal: 1, Input: input}
	detail := RunDetail{Run: Run{Compilation: compilation}, Steps: []Step{{NodeID: "search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": []tool.CitationRef{ref}})}}}
	marker := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	output := mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{marker}, "citationAssessments": []map[string]any{{"reference": marker, "decision": "support", "supportingQuote": "Observed nighttime-specific use."}}})
	got := canonicalCitationSubmission(output, detail, step, compilation.Nodes[1], "chat")
	if err := validateSelectedEvidence(got, input); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), marker) || !strings.Contains(string(got), source) {
		t.Fatalf("marker/quote was not restored: %s", got)
	}
}

// Optional read-only replay; private research records are never checked in.
func TestSupportingQuoteLocalReplay(t *testing.T) {
	dir := os.Getenv("SCIAIDE_QUOTE_REPLAY_DIR")
	if dir == "" {
		t.Skip("no local replay fixture supplied")
	}
	input, err := os.ReadFile(filepath.Join(dir, "step-input.json"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(filepath.Join(dir, "execution-output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		t.Fatal(err)
	}
	value := json.RawMessage(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(string(output)), "```json"), "```")))
	value = restoreWorkflowCitationMarkers(value, in.Candidates, os.Getenv("SCIAIDE_QUOTE_REPLAY_CHAT_RUN"))
	if err := validateSelectedEvidence(value, input); err == nil {
		t.Fatal("fixture must reproduce original failure")
	} else {
		t.Logf("before: %v", err)
	}
	value = canonicalSupportingQuotes(value, input)
	if err := validateSelectedEvidence(value, input); err != nil {
		t.Fatal(err)
	}
	t.Log("original failing submission now passes evidence validation")
}

func TestCanonicalSubmissionRestoresExactSourceQuote(t *testing.T) {
	source := "Four unique profiles for nighttime‐specific smartphone use were identified."
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []map[string]any{{"reference": "[K-AAA]", "quote": source}}})
	output := mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{"[K-AAA]"}, "citationAssessments": []map[string]any{{"reference": "[K-AAA]", "decision": "support", "supportingQuote": "Four unique profiles for nighttime-specific smartphone use were identified."}}})
	canonical := canonicalCitationSubmission(output, RunDetail{}, Step{Input: input}, CompiledNode{ID: "evidence_screening"}, "")
	if err := validateSelectedEvidence(canonical, input); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Assessments []struct {
			Quote string `json:"supportingQuote"`
		} `json:"citationAssessments"`
	}
	json.Unmarshal(canonical, &got)
	if got.Assessments[0].Quote != source {
		t.Fatalf("did not restore source: %s", canonical)
	}
	if string(canonicalSupportingQuotes(canonical, input)) != string(canonical) {
		t.Fatal("canonicalization is not idempotent")
	}
}

func TestCanonicalQuotePreservesBoundaries(t *testing.T) {
	output := json.RawMessage(`{"count":9007199254740993,"citationAssessments":[{"reference":"[K-A]","decision":"support","supportingQuote":"nighttime-specific use"}]}`)
	for _, candidates := range []any{
		[]map[string]any{{"reference": "[K-B]", "quote": "nighttime‐specific use"}},
		[]map[string]any{{"reference": "[K-A]", "quote": "nighttime‐specific use"}, {"reference": "[K-A]", "quote": "different source"}},
		[]map[string]any{{"reference": "[K-A]", "quote": "nighttime‐specific" + strings.Repeat(" ", 701) + "use"}},
	} {
		input := mustJSON(map[string]any{"candidates": candidates})
		if got := canonicalSupportingQuotes(output, input); string(got) != string(output) {
			t.Fatalf("must not repair wrong/ambiguous/oversized quote: %s", got)
		}
	}
	input := mustJSON(map[string]any{"candidates": []map[string]any{{"reference": "[K-A]", "quote": "nighttime‐specific use"}}})
	got := canonicalSupportingQuotes(output, input)
	if !strings.Contains(string(got), "9007199254740993") || !strings.Contains(string(got), "nighttime‐specific use") {
		t.Fatalf("lost exact numeric value or failed repair: %s", got)
	}
}

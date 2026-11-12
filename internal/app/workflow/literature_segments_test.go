package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLiteratureSegmentsPreserveEverySourceCharacter(t *testing.T) {
	for _, text := range []string{"", "Exact evidence.", strings.Repeat("Unicode \u22123.42; P < 0.001. \u6d4b\u8bd5\n", 100), strings.Repeat("x", 1001), strings.Repeat("A.", 260), " $L^3$-based " + strings.Repeat("regularity ", 100)} {
		segments := literatureSourceSegments(text, "abstract")
		var restored strings.Builder
		for _, segment := range segments {
			if len([]rune(segment.Text)) > 400 || len(segment.Text) == 0 {
				t.Fatal("unbounded or empty segment")
			}
			restored.WriteString(segment.Text)
		}
		if restored.String() != text {
			t.Fatal("source lost characters")
		}
	}
}

func TestLiteratureSegmentReferencesExpandAndRejectWrongBindings(t *testing.T) {
	node, input, text := literatureQuoteFixture(t)
	obj := decodeObject(raw(text))
	note := obj["evidenceNotes"].([]any)[0].(map[string]any)
	note["quotes"] = []any{map[string]any{"field": "abstract", "segmentId": "a0001"}}
	text = string(mustJSON(obj))
	value, changes, err := normalizeWorkflowAIStageSubmissionForInput(text, node, input)
	if err != nil || len(changes) != 1 || changes[0].Rule != "literature_quote_source_segment" {
		t.Fatal(changes, err)
	}
	if !strings.Contains(string(value), "Observed change was \u22125.23 kg.") || strings.Contains(text, "Observed change") {
		t.Fatal("not resolved from original source")
	}
	if err := validateWorkflowAIStageOutput(node, value, input); err != nil {
		t.Fatal(err)
	}
	again, next, err := normalizeWorkflowAIStageSubmissionForInput(string(value), node, input)
	if err != nil || len(next) != 0 || !rawJSONEqual(again, value) {
		t.Fatal("not idempotent", err)
	}
	for _, bad := range []map[string]any{
		{"field": "abstract", "segmentId": "a9999"},
		{"field": "title", "segmentId": "a0001"},
		{"field": "abstract", "segmentId": "a0001", "quote": "Changed quantity 5.23 kg"},
	} {
		note["quotes"] = []any{bad}
		if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(obj)), node, input); err == nil {
			t.Fatal("invalid reference accepted", bad)
		}
	}
	var accepted literatureScreening
	json.Unmarshal(value, &accepted)
	accepted.EvidenceNotes[0].Quotes[0].Quote = "Altered"
	if validateWorkflowAIStageOutput(node, mustJSON(accepted), input) == nil {
		t.Fatal("tampered resolved quotation accepted")
	}
}

func TestLiteratureReportsMultipleQuoteErrorsTogether(t *testing.T) {
	node, input, text := literatureQuoteFixture(t)
	obj := decodeObject(raw(text))
	note := obj["evidenceNotes"].([]any)[0].(map[string]any)
	note["quotes"] = []any{map[string]any{"field": "title", "quote": "Invented title"}, map[string]any{"field": "abstract", "quote": "Changed quantity"}}
	err := validateWorkflowAIStageOutput(node, mustJSON(obj), input)
	if err == nil || !strings.Contains(err.Error(), "quotes[0]") || !strings.Contains(err.Error(), "quotes[1]") {
		t.Fatal("only one repair defect was exposed", err)
	}
}

func TestLiteratureQuoteDiagnosticsDoNotHideCellsBeyondEight(t *testing.T) {
	_, input, text := literatureQuoteFixture(t)
	var in struct {
		Literature literatureInput       `json:"_literature"`
		Candidates []literatureCandidate `json:"candidates"`
	}
	_ = json.Unmarshal(input, &in)
	var out literatureScreening
	_ = json.Unmarshal([]byte(text), &out)
	c, a, n := in.Candidates[0], out.CandidateAssessments[0], out.EvidenceNotes[0]
	in.Candidates = nil
	out.CandidateAssessments = nil
	out.EvidenceNotes = nil
	for i := 0; i < 12; i++ {
		id := strings.Repeat("c", i+1)
		c.ID, a.CandidateID, n.CandidateID = id, id, id
		n.Quotes = []literatureQuote{{Field: "abstract", Quote: "not in original"}}
		in.Candidates = append(in.Candidates, c)
		out.CandidateAssessments = append(out.CandidateAssessments, a)
		out.EvidenceNotes = append(out.EvidenceNotes, n)
	}
	e := validateLiteratureHierarchy(mustJSON(map[string]any{"phase": "batch", "summary": out.Summary, "candidateAssessments": out.CandidateAssessments, "evidenceNotes": out.EvidenceNotes}), mustJSON(in))
	if e == nil || !strings.Contains(e.Error(), "evidenceNotes[11].quotes[0]") || strings.Contains(e.Error(), "first 8") {
		t.Fatal("hidden diagnostic", e)
	}
}

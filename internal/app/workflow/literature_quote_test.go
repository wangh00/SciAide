package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestLiteratureQuoteSourceSpanHyphenBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, source, quote, want string
	}{
		{"observed title", "Correction to \u201cA meta\u2010analysis comparing time\u2010restricted eating\u201d", "Correction to \u201cA meta\u2010analysis comparing time-restricted eating\u201d", "Correction to \u201cA meta\u2010analysis comparing time\u2010restricted eating\u201d"},
		{"observed substring", "alternate day fasting, the 5:2 diet, and time\u2010restricted eating for weight loss", "alternate day fasting, the 5:2 diet, and time-restricted eating for weight loss", "alternate day fasting, the 5:2 diet, and time\u2010restricted eating for weight loss"},
		{"nonbreaking hyphen", "time\u2011restricted eating", "time-restricted eating", "time\u2011restricted eating"},
		{"inverse glyph", "time-restricted eating", "time\u2010restricted eating", "time-restricted eating"},
		{"utf8 offsets", "\u7814\u7a76: time\u2010restricted eating", "time-restricted", "time\u2010restricted"},
		{"exact repeated", "time-restricted; time-restricted", "time-restricted", "time-restricted"},
		{"ambiguous location", "time\u2010restricted; time\u2011restricted", "time-restricted", ""},
		{"no negative number rewrite", "change \u22125.23 kg", "change -5.23 kg", ""},
		{"no lost minus", "change -5.23 kg", "change 5.23 kg", ""},
		{"no value rewrite", "change 5.23 kg", "change 5.32 kg", ""},
		{"no range rewrite", "2020\u20132025", "2020-2025", ""},
		{"no numeric hyphen rewrite", "2020\u20102025", "2020-2025", ""},
		{"no word minus rewrite", "A\u2212B", "A-B", ""},
		{"no word dash rewrite", "A\u2013B", "A-B", ""},
		{"no terminal glyph rewrite", "word\u2010", "word-", ""},
		{"no whitespace rewrite", "time\u2010restricted eating", "time-restricted  eating", ""},
		{"no case rewrite", "Time\u2010restricted eating", "time-restricted eating", ""},
		{"no deleted negation", "did not reduce weight", "did reduce weight", ""},
		{"empty quote", "real evidence", "", ""},
		{"empty source", "", "real evidence", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, matched := literatureQuoteSourceSpan(tt.source, tt.quote)
			if matched != (tt.want != "") || got != tt.want {
				t.Fatalf("span=%q matched=%v want=%q", got, matched, tt.want)
			}
			if matched && !strings.Contains(tt.source, got) {
				t.Fatal("accepted quote is not an exact original source span")
			}
		})
	}
}

func literatureQuoteFixture(t *testing.T) (CompiledNode, json.RawMessage, string) {
	t.Helper()
	_, _, _, _, node := literatureFixture(t, 0)
	c := literatureCandidate{ID: "candidate", Title: "Correction to time\u2010restricted eating", Abstract: "Observed change was \u22125.23 kg."}
	input := mustJSON(map[string]any{"_literature": literatureInput{Phase: "batch"}, "candidates": []literatureCandidate{c}})
	output := screenFixture(input, false)
	return literaturePhaseNode(node, input), input, strings.ReplaceAll(string(output), "time\u2010restricted", "time-restricted")
}

func TestLiteratureQuoteNormalizationRestoresSourceWithoutChangingEvidence(t *testing.T) {
	node, input, text := literatureQuoteFixture(t)
	beforeInput, beforeText := string(input), text
	value, changes, err := normalizeWorkflowAIStageSubmissionForInput(text, node, input)
	if err != nil || len(changes) != 1 || changes[0].Path != "$.evidenceNotes[0].quotes[0].quote" || changes[0].Rule != "literature_quote_source_hyphen" {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	if len(changes[0].BeforeSHA256) != 64 || len(changes[0].AfterSHA256) != 64 || changes[0].BeforeSHA256 == changes[0].AfterSHA256 {
		t.Fatal("missing normalization audit hashes")
	}
	if string(input) != beforeInput || text != beforeText || !strings.Contains(string(value), "time\u2010restricted") {
		t.Fatal("raw input/output changed or source glyph was not restored")
	}
	if err := validateWorkflowAIStageOutput(node, value, input); err != nil {
		t.Fatal(err)
	}
	var original, accepted literatureScreening
	_ = json.Unmarshal([]byte(text), &original)
	_ = json.Unmarshal(value, &accepted)
	original.EvidenceNotes[0].Quotes[0].Quote = accepted.EvidenceNotes[0].Quotes[0].Quote
	if !reflect.DeepEqual(original, accepted) {
		t.Fatal("normalization changed fields beyond the source quote")
	}
	again, nextChanges, err := normalizeWorkflowAIStageSubmissionForInput(string(value), node, input)
	if err != nil || len(nextChanges) != 0 || !rawJSONEqual(value, again) {
		t.Fatal("normalization is not idempotent", err)
	}
	if validateWorkflowAIStageOutput(node, raw(text), input) == nil {
		t.Fatal("raw nonverbatim quote bypassed strict final validation")
	}
}

func TestLiteratureQuoteNormalizationDoesNotHideInvalidSubmissions(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"wrong candidate", func(note map[string]any) { note["candidateId"] = "other" }},
		{"wrong field", func(note map[string]any) { note["quotes"].([]any)[0].(map[string]any)["field"] = "abstract" }},
		{"altered quantity", func(note map[string]any) {
			note["quotes"].([]any)[1].(map[string]any)["quote"] = "Observed change was 5.23 kg."
		}},
		{"invented quote", func(note map[string]any) { note["quotes"].([]any)[0].(map[string]any)["quote"] = "Improved survival" }},
		{"unknown field", func(note map[string]any) { note["invented"] = "must not be discarded" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node, input, text := literatureQuoteFixture(t)
			output := decodeObject(raw(text))
			tt.edit(output["evidenceNotes"].([]any)[0].(map[string]any))
			value, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(output)), node, input)
			if err == nil && validateWorkflowAIStageOutput(node, value, input) == nil {
				t.Fatal("invalid submission accepted")
			}
		})
	}
	// Even when called independently of the schema preflight, the normalizer
	// must preserve unknown fields and integers rather than reserializing a DTO.
	_, input, text := literatureQuoteFixture(t)
	text = strings.Replace(text, `"phase":"batch"`, `"preciseInteger":9007199254740993,"phase":"batch"`, 1)
	normalized, _, err := normalizeLiteratureQuotes(raw(text), input)
	if err != nil || !strings.Contains(string(normalized), `"preciseInteger":9007199254740993`) {
		t.Fatal("normalization discarded or rounded an unrelated field", err)
	}
}

func TestValidateResearchSubmissionNormalizesLiteratureQuotesReadOnly(t *testing.T) {
	node, input, text := literatureQuoteFixture(t)
	r := &submissionValidationRepository{detail: RunDetail{
		Run:   Run{ID: "run", ProjectID: "project", ConversationID: "conversation", Status: RunRunning, Inputs: raw(`{}`), InputsSHA256: hashJSON(raw(`{}`)), Compilation: Compilation{Nodes: []CompiledNode{node}}},
		Steps: []Step{{ID: "step", NodeID: node.ID, Status: StepRunning, Input: input, InputSHA256: hashJSON(input)}},
	}}
	compilation, _ := canonicalJSON(r.detail.Run.Compilation)
	r.detail.Run.CompilationSHA256 = hashBytes(compilation)
	r.detail.Run.Compilation.CompilationSHA256 = r.detail.Run.CompilationSHA256
	s := &RuntimeService{repository: r}
	before := mustJSON(r.detail)
	value, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", "step", text)
	if err != nil || !strings.Contains(string(value), "time\u2010restricted") {
		t.Fatal("precommit rejected equivalent source quote", err)
	}
	if !rawJSONEqual(before, mustJSON(r.detail)) {
		t.Fatal("precommit rewrote frozen state")
	}
	if err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, value); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(node, value, input); err != nil {
		t.Fatal(err)
	}
}

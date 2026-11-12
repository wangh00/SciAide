package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestCitationQuoteNavigationIsExactOfferedSameDocument(t *testing.T) {
	source := tool.CitationRef{Reference: "[K-A]", DocumentID: "doc", IndexVersionID: "v1", ChunkID: "a", QuoteSHA256: "a", Quote: "Result is 1;"}
	match := source
	match.Reference = "[K-B]"
	match.ChunkID = "b"
	match.QuoteSHA256 = "b"
	match.Quote = "Result is 1; P = 0.68."
	foreign := match
	foreign.Reference = "[K-C]"
	foreign.DocumentID = "other"
	stale := match
	stale.Reference = "[K-D]"
	stale.IndexVersionID = "v0"
	approx := match
	approx.Reference = "[K-E]"
	approx.Quote = "Result is 1; P=0.68."
	refs := []tool.CitationRef{source, match, foreign, stale, approx, match}
	issue := citationQuoteIssue(source, match.Quote, refs)
	if !reflect.DeepEqual(issue.SameDocumentQuoteMatches, []string{"[K-B]"}) || issue.Reference != source.Reference {
		t.Fatal("navigation widened source scope", issue)
	}
	visible := modelVisibleQuoteError(&issue, refs, "chat")
	if visible.Reference != citation.KnowledgeReference("chat", "v1", "a", "a") || !reflect.DeepEqual(visible.SameDocumentQuoteMatches, []string{citation.KnowledgeReference("chat", "v1", "b", "b")}) {
		t.Fatal("wrong chat marker mapping", visible)
	}
	visible.SameDocumentQuoteMatches[0] = "mutated"
	if issue.SameDocumentQuoteMatches[0] != "[K-B]" {
		t.Fatal("model projection mutated frozen diagnostic")
	}
	source.DocumentID = ""
	if len(citationQuoteIssue(source, match.Quote, refs).SameDocumentQuoteMatches) != 0 {
		t.Fatal("missing identity admitted source")
	}
}

func TestCapturedCitationSpanNavigationFullValidationAndResume(t *testing.T) {
	dir := os.Getenv("SCIAIDE_CITATION_SPAN_CAPTURE")
	if dir == "" {
		t.Skip("no isolated incident capture")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var d RunDetail
	if err := json.Unmarshal(read("derived/detail.json"), &d); err != nil {
		t.Fatal(err)
	}
	// Preserve raw captured JSON, including whitespace used by source hashes.
	for _, name := range []string{"workflow_steps.json", "workflow_ai_executions.json", "workflow_events.json"} {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(read(name), &rows); err != nil {
			t.Fatal(err)
		}
		str := func(v json.RawMessage) string { var s string; _ = json.Unmarshal(v, &s); return s }
		for _, row := range rows {
			id := str(row["id"])
			for i := range d.Steps {
				if d.Steps[i].ID == id {
					d.Steps[i].Input = raw(str(row["input_json"]))
					d.Steps[i].Output = raw(str(row["output_json"]))
				}
			}
			for i := range d.AIExecutions {
				if d.AIExecutions[i].ID == id {
					d.AIExecutions[i].Output = raw(str(row["output_json"]))
				}
			}
			for i := range d.Events {
				if d.Events[i].ID == id {
					d.Events[i].Payload = raw(str(row["payload_json"]))
				}
			}
		}
	}
	before := mustJSON(d)
	step := *findStepByNode(d.Steps, "evidence_screening")
	node := compilationNodeMap(d.Run.Compilation)[step.NodeID]
	phase := selectedEvidencePhaseNode(node, step.Input)
	var turns []struct {
		Text string `json:"draft_text"`
	}
	_ = json.Unmarshal(read("model_turn_journal.json"), &turns)
	bad, _, err := normalizeWorkflowAIStageSubmissionForInput(turns[0].Text, phase, step.Input)
	if err != nil {
		t.Fatal(err)
	}
	chat := "75018abf-c63d-49a5-a432-dfccf20368b2"
	bad = canonicalCitationSubmission(bad, d, step, phase, chat)
	err = validateWorkflowAIStageOutput(phase, bad, step.Input)
	var quote *CitationQuoteError
	if !errors.As(err, &quote) || quote.Reference != "[K-8F599FE9845D]" || !reflect.DeepEqual(quote.SameDocumentQuoteMatches, []string{"[K-8799F2F92F64]"}) {
		t.Fatal("exact cross-block navigation missing", err)
	}
	visible := modelVisibleQuoteError(quote, workflowCitationSeeds(d, step), chat)
	if visible.Reference != "[K-7BB516B2D96A]" || visible.SameDocumentQuoteMatches[0] == quote.SameDocumentQuoteMatches[0] {
		t.Fatal("frozen marker leaked into correction")
	}
	// Derived full result explicitly rebinds every use of this citation. Never
	// do this in production: the model must choose and resubmit the assessment.
	good := json.RawMessage(strings.ReplaceAll(string(bad), quote.Reference, quote.SameDocumentQuoteMatches[0]))
	if err := validateWorkflowAIStageOutput(phase, good, step.Input); err != nil {
		t.Fatal("complete rebind failed original business validation", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "derived", "full-source-rebind.json"), good, 0600); err != nil {
		t.Fatal(err)
	}
	// A quote-only replacement from the other block still fails.
	if !strings.Contains(string(bad), quote.SupportingQuote) {
		t.Fatal("original quote absent")
	}
	if err := validateWorkflowAIStageOutput(phase, bad, step.Input); err == nil {
		t.Fatal("host silently rebound source")
	}
	retry := d
	retry.Steps = append([]Step{}, d.Steps...)
	current := findStepByNode(retry.Steps, step.NodeID)
	current.Status = StepQueued
	current.Input = raw(`{}`)
	current.InputSHA256 = ""
	bound, err := bindNodeInput(retry, node)
	if err != nil {
		t.Fatal(err)
	}
	bound, err = prepareSelectedEvidence(retry, bindIndexedEvidence(retry, node, bound))
	if err != nil {
		t.Fatal(err)
	}
	var resumed struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(bound, &resumed)
	if resumed.Info.Phase != "coverage" || len(resumed.Info.Analyses) != 29 {
		t.Fatalf("batches repeated: phase=%s analyses=%d", resumed.Info.Phase, len(resumed.Info.Analyses))
	}
	if !reflect.DeepEqual(before, mustJSON(d)) {
		t.Fatal("captured state mutated")
	}
	t.Log("original mismatch rejected; exact offered-source navigation and explicit full-result rebind pass; retry preserves 29 completed analyses")
}

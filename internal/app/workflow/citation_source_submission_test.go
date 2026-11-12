package workflow

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCitationFirstSubmissionProtocolIsFrozenAndPhaseScoped(t *testing.T) {
	input := raw(`{"candidates":[],"_selectedEvidence":{"phase":"coverage"}}`)
	base := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}
	for _, modern := range []bool{false, true} {
		node := base
		if modern {
			node.OutputSchema = citationSourceCanonicalSchema(node.OutputSchema)
		}
		node = selectedEvidencePhaseNode(node, input)
		prompt := buildAIStagePrompt(RunDetail{}, Step{Input: input, InputSHA256: hashJSON(input)}, node)
		version := reportExecutionPromptVersion(node, prompt)
		if IsCitationSourceProtocol(version) != modern {
			t.Fatal("protocol upgraded legacy or failed new route", version)
		}
		if modern && (strings.Contains(prompt, "End the response with exactly one fenced") || !strings.Contains(prompt, `"quoteSelection"`) || strings.Contains(prompt, "supportingQuote必须逐字摘录")) {
			t.Fatal("conflicting first submission instructions")
		}
		node.PromptVersion = version
		if reportExecutionPromptVersion(node, prompt) != version || len(version) > 128 {
			t.Fatal("execution identity not stable")
		}
	}
	node := base
	node.OutputSchema = citationSourceCanonicalSchema(node.OutputSchema)
	node = selectedEvidencePhaseNode(node, raw(`{"_selectedEvidence":{"phase":"batch"}}`))
	if usesCitationSourceProtocol(node) || IsCitationSourceProtocol(reportExecutionPromptVersion(node, node.Prompt)) {
		t.Fatal("document-only batch acquired a quotation protocol")
	}
	for _, v := range []string{"", "x|citation-source-v1|planner-action-v1", "X|citation-source-v1", strings.Repeat("a", 128) + "|" + CitationSourceProtocolVersion} {
		if IsCitationSourceProtocol(v) {
			t.Fatal("invalid version accepted", v)
		}
	}
}

func TestCitationFirstSubmissionProjectionPreservesScientificFields(t *testing.T) {
	schema := citationSourceCanonicalSchema(selectedEvidenceSchema())
	wire := CitationSourceSubmissionSchema(schema)
	if len(wire) == 0 || !strings.Contains(string(wire), `"quoteSelection"`) {
		t.Fatal("wrong wire schema")
	}
	original := decodeObject(schema)
	projected := decodeObject(wire)
	a := original["properties"].(map[string]any)
	b := projected["properties"].(map[string]any)
	delete(a, "citationAssessments")
	delete(b, "citationAssessments")
	if !reflect.DeepEqual(a, b) {
		t.Fatal("science fields changed")
	}
}

func TestCitationFirstSubmissionResolverRejectsAmbiguityAndNeverChoosesDecision(t *testing.T) {
	schema := raw(`{"type":"object","additionalProperties":false,"required":["citationAssessments","summary"],"properties":{"summary":{"type":"string"},"citationAssessments":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["reference","decision","reason","supportingQuote"],"properties":{"reference":{"type":"string"},"decision":{"type":"string","enum":["support","exclude"]},"reason":{"type":"string"},"supportingQuote":{"type":"string","maxLength":700}}}}}}`)
	calls := 0
	resolve := func(ref string, selection CitationSourceSelection) (string, error) {
		calls++
		return "誊写🙂 &amp; 原文", nil
	}
	good := `{"summary":"模型的科学结论不改","citationAssessments":[{"reference":"[K-A]","decision":"support","reason":"原理由不改","quoteSelection":{"sourceSegmentIds":["q_id"]}}]}`
	value, err := ResolveCitationSourceSubmission(good, schema, resolve)
	if err != nil || calls != 1 || !strings.Contains(string(value), "誊写🙂") || !strings.Contains(string(value), "原理由不改") || strings.Contains(string(value), "quoteSelection") {
		t.Fatal(string(value), err)
	}
	var exact struct {
		Rows []struct {
			Quote string `json:"supportingQuote"`
		} `json:"citationAssessments"`
	}
	if json.Unmarshal(value, &exact) != nil || len(exact.Rows) != 1 || exact.Rows[0].Quote != "誊写🙂 &amp; 原文" {
		t.Fatal("copied source bytes changed", string(value))
	}
	excluded := strings.Replace(strings.Replace(good, `"support"`, `"exclude"`, 1), `["q_id"]`, `[]`, 1)
	calls = 0
	value, err = ResolveCitationSourceSubmission(excluded, schema, resolve)
	if err != nil || calls != 0 || !strings.Contains(string(value), `"supportingQuote":""`) {
		t.Fatal("explicit exclude changed", string(value), err)
	}
	for _, bad := range []string{
		strings.Replace(good, `["q_id"]`, `[]`, 1),
		strings.Replace(good, `"quoteSelection":`, `"supportingQuote":"guess","quoteSelection":`, 1),
		strings.Replace(good, `"decision":"support"`, `"decision":"exclude","decision":"support"`, 1),
		strings.Replace(good, `"summary":`, `"summary":"first","summary":`, 1),
	} {
		if v, err := ResolveCitationSourceSubmission(bad, schema, resolve); err == nil {
			t.Fatal("ambiguous/native-invalid payload accepted", string(v))
		}
	}
}

func TestCitationFirstSubmissionPreservesSelectedWhitespaceAndLiteralMarkers(t *testing.T) {
	node := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion, OutputSchema: citationSourceCanonicalSchema(selectedEvidenceSchema())}
	output := raw(`{"citationAssessments":[{"reference":"[K-A]","decision":"support","supportingQuote":" \nResult. \t"}]}`)
	step := Step{Input: raw(`{"candidates":[{"reference":"[K-A]","quote":" \nResult. \t"}]}`)}
	getQuote := func(value json.RawMessage) string {
		var v struct {
			Rows []struct {
				Quote string `json:"supportingQuote"`
			} `json:"citationAssessments"`
		}
		if json.Unmarshal(value, &v) != nil || len(v.Rows) != 1 {
			t.Fatal(string(value))
		}
		return v.Rows[0].Quote
	}
	if got := getQuote(canonicalCitationSubmission(output, RunDetail{}, step, node, "")); got != " \nResult. \t" {
		t.Fatal("new selected boundary was normalized", got)
	}
	node.OutputSchema = selectedEvidenceSchema()
	if got := getQuote(canonicalCitationSubmission(output, RunDetail{}, step, node, "")); got != "Result." {
		t.Fatal("legacy normalization changed", got)
	}
	node.OutputSchema = citationSourceCanonicalSchema(selectedEvidenceSchema())
	ref := tool.CitationRef{IndexVersionID: "idx", ChunkID: "chunk", QuoteSHA256: citation.QuoteSHA256("data")}
	ref.Reference = citation.KnowledgeReference("workflow", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	current := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	original := mustJSON(map[string]any{"citationAssessments": []any{map[string]any{"reference": current, "decision": "support", "supportingQuote": " source text " + current + " ", "reason": "Unchanged judgement"}}})
	restored := restoreCitationSubmissionMarkers(original, []tool.CitationRef{ref}, "chat", node)
	if getQuote(restored) != " source text "+current+" " {
		t.Fatal("literal source marker rewritten", string(restored))
	}
	var result struct {
		Rows []struct{ Reference string } `json:"citationAssessments"`
	}
	_ = json.Unmarshal(restored, &result)
	if result.Rows[0].Reference != ref.Reference {
		t.Fatal("mechanical reference alias not restored", string(restored))
	}
}

func TestCitationFirstSubmissionCommittedQuoteSurvivesReplayAndDependency(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	node.OutputSchema = citationSourceCanonicalSchema(node.OutputSchema)
	d.Run.Compilation.Nodes[0] = node
	b := calls.calls[0].Result.Citations[0]
	b.ChunkID = "other"
	b.Quote = "Other source."
	b.QuoteSHA256 = citation.QuoteSHA256(b.Quote)
	b.Reference = citation.KnowledgeReference(e.ChatRunID, b.IndexVersionID, b.ChunkID, b.QuoteSHA256)
	literal := citation.KnowledgeReference(d.Run.ID, b.IndexVersionID, b.ChunkID, b.QuoteSHA256)
	a := calls.calls[0].Result.Citations[0]
	a.Quote = " \nVerified literal " + literal + " source. \t"
	a.QuoteSHA256 = citation.QuoteSHA256(a.Quote)
	a.Reference = citation.KnowledgeReference(e.ChatRunID, a.IndexVersionID, a.ChunkID, a.QuoteSHA256)
	a.SourceEnd = len([]rune(a.Quote))
	calls.calls[0].Result.Citations = []tool.CitationRef{a, b}
	step.Input = raw(`{"documentIds":["doc"],"candidates":[],"_selectedEvidence":{"direct":true,"phase":"coverage","documents":["doc"]}}`)
	step.InputSHA256 = hashJSON(step.Input)
	e.InputSHA256, e.OutputText = step.InputSHA256, indexedSynthesis(a)
	d.Steps[2] = step
	repo := &indexedAcceptedFixture{}
	runtime := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if err := runtime.completeAIExecution(t.Context(), d, step, node, e, true); err != nil || repo.failure != "" {
		t.Fatal("complete new source failed", err, repo.failure)
	}
	frozen := repo.execution
	quote := func(v json.RawMessage) string {
		var x struct {
			Rows []struct {
				Quote string `json:"supportingQuote"`
			} `json:"citationAssessments"`
		}
		_ = json.Unmarshal(v, &x)
		if len(x.Rows) != 1 {
			t.Fatal(string(v))
		}
		return x.Rows[0].Quote
	}
	if quote(frozen.Output) != a.Quote {
		t.Fatal("commit rewrote selected original", string(frozen.Output))
	}
	d.AIExecutions = []AIExecution{frozen}
	if err := runtime.completeAIExecution(context.Background(), d, step, node, frozen, false); err != nil || repo.failure != "" || repo.execution.OutputSHA256 != frozen.OutputSHA256 {
		t.Fatal("replay changed source or output hash", err, repo.failure)
	}
	d.Steps[2].Status, d.Steps[2].Output = StepCompleted, repo.output
	value, err := resolveWorkflowEdgeValue(d, Edge{FromNode: node.ID, FromPort: "analysis", ToNode: "method_selection", ToPort: "evidenceScreening"})
	if err != nil || quote(value) != a.Quote {
		t.Fatal("downstream input rewrote selected range", err, string(value))
	}
}

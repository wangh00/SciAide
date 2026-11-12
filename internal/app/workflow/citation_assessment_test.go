package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func citationAssessmentFixture() (json.RawMessage, json.RawMessage, []tool.CitationRef) {
	refs := []tool.CitationRef{}
	for _, id := range []string{"A", "B", "C", "D", "E"} {
		quote := "Exact continuous fixture evidence " + id + "."
		ref := tool.CitationRef{Kind: citation.KindKnowledgeChunk, Reference: "[K-WORKFLOW-" + id + "]", ProjectID: "project", IndexVersionID: "index", ChunkID: id, DocumentID: "doc-" + id, AttachmentID: "attachment-" + id, SourceName: "paper.pdf", Quote: quote, QuoteSHA256: citation.QuoteSHA256(quote)}
		refs = append(refs, ref)
	}
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": refs})
	output := mustJSON(map[string]any{
		"summary": "Bounded synthetic observation", "recommendedReferences": []string{refs[0].Reference, refs[1].Reference, refs[2].Reference, refs[3].Reference, refs[4].Reference},
		"citationAssessments": []map[string]any{{"reference": refs[0].Reference, "decision": "support", "sourceLevel": "abstract", "reason": "Synthetic observation only", "supportingQuote": "Invented fixture quote."}},
		"coverage":            map[string]any{"strength": "limited", "sufficientForClaimedScope": true, "independentStudyEstimate": 0, "directPopulationEvidence": false, "fullTextEvidenceAvailable": false, "gaps": []string{}},
		"recommendedScope":    "Synthetic fixture only", "recommendation": "proceed_limited", "limitations": []string{"Not scientific validation"},
	})
	return input, output, refs
}

func TestEvidenceOverviewReportsMissingAssessmentsTogetherWithQuotes(t *testing.T) {
	input, output, refs := citationAssessmentFixture()
	err := validateSelectedEvidence(output, input)
	if err == nil {
		t.Fatal("missing assessments and invented quote were accepted")
	}
	for _, ref := range refs {
		if !strings.Contains(err.Error(), ref.Reference) {
			t.Fatal("correction omitted a known citation defect", ref.Reference, err)
		}
	}
	if !strings.Contains(err.Error(), "recommendedReferences[1]") || !strings.Contains(err.Error(), "citationAssessments") || !strings.Contains(err.Error(), refs[0].Quote) {
		t.Fatal("correction lacks paths and exact quotation evidence", err)
	}
	var quote *CitationQuoteError
	var quotes *CitationQuoteErrors
	if errors.As(err, &quote) || errors.As(err, &quotes) {
		t.Fatal("a mixed result error advertised a quotation-only patch")
	}
	// Correcting the quote alone must still identify every missing assessment.
	quoteOnly := decodeObject(output)
	quoteOnly["citationAssessments"].([]any)[0].(map[string]any)["supportingQuote"] = refs[0].Quote
	err = validateSelectedEvidence(mustJSON(quoteOnly), input)
	for _, ref := range refs[1:] {
		if err == nil || !strings.Contains(err.Error(), ref.Reference) {
			t.Fatal("quote-only correction hid a missing decision", err)
		}
	}
	// Host never chooses an assessment or silently deletes a recommendation.
	corrected := decodeObject(output)
	assessments := []map[string]any{}
	for _, ref := range refs {
		assessments = append(assessments, map[string]any{"reference": ref.Reference, "decision": "support", "sourceLevel": "abstract", "reason": "Fixture evidence", "supportingQuote": ref.Quote})
	}
	corrected["citationAssessments"] = assessments
	if err := validateSelectedEvidence(mustJSON(corrected), input); err != nil {
		t.Fatal("complete model-supplied correction rejected", err)
	}
}

func TestLiveAssessmentDiagnosticUsesChatMarkersWithoutChangingFrozenState(t *testing.T) {
	input, output, refs := citationAssessmentFixture()
	node := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema(), OutputSchemaSHA256: hashJSON(selectedEvidenceSchema())}
	compilation := Compilation{Nodes: []CompiledNode{{ID: "search", Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, node}, Edges: []Edge{{FromNode: "search", FromPort: "citations", ToNode: node.ID, ToPort: "candidates"}}}
	encoded, _ := canonicalJSON(compilation)
	compilation.CompilationSHA256 = hashBytes(encoded)
	step := Step{ID: "step", NodeID: node.ID, Ordinal: 1, Attempt: 1, Status: StepRunning, Input: input, InputSHA256: hashJSON(input)}
	detail := RunDetail{Run: Run{ID: "run", ProjectID: "project", ConversationID: "conversation", Status: RunRunning, CurrentStep: 1, Inputs: raw(`{}`), InputsSHA256: hashJSON(raw(`{}`)), Compilation: compilation, CompilationSHA256: compilation.CompilationSHA256}, Steps: []Step{{NodeID: "search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": refs})}, step}, AIExecutions: []AIExecution{{WorkflowStepID: "step", Attempt: 1, InputSHA256: step.InputSHA256, ChatRunID: "chat"}}}
	repo := &submissionValidationRepository{detail: detail}
	service := &RuntimeService{repository: repo}
	text := string(output)
	for _, ref := range refs {
		text = strings.ReplaceAll(text, ref.Reference, citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256))
	}
	before := mustJSON(repo.detail)
	_, err := service.ValidateResearchSubmission(context.Background(), "conversation", "run", "step", text)
	if err == nil {
		t.Fatal("invalid candidate was accepted")
	}
	for _, ref := range refs {
		marker := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
		if !strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), ref.Reference) {
			t.Fatal("feedback does not use every model-visible marker", err)
		}
	}
	if !rawJSONEqual(before, mustJSON(repo.detail)) {
		t.Fatal("preflight changed the original candidate or frozen state")
	}
}

func TestEvidenceOverviewSeparatesMissingDuplicateAndExcludedAssessments(t *testing.T) {
	input, output, refs := citationAssessmentFixture()
	for _, tc := range []struct {
		name    string
		mutate  func(map[string]any)
		problem string
	}{
		{"duplicate assessment", func(v map[string]any) {
			v["citationAssessments"] = []map[string]any{
				{"reference": refs[0].Reference, "decision": "support", "supportingQuote": refs[0].Quote},
				{"reference": refs[0].Reference, "decision": "exclude", "supportingQuote": ""},
			}
		}, "duplicate citation assessment"},
		{"excluded recommendation", func(v map[string]any) {
			v["citationAssessments"] = []map[string]any{{"reference": refs[0].Reference, "decision": "exclude", "supportingQuote": ""}}
		}, "has an exclude decision"},
		{"duplicate recommendation", func(v map[string]any) {
			v["recommendedReferences"] = []string{refs[0].Reference, refs[0].Reference, refs[1].Reference}
			v["citationAssessments"] = []map[string]any{{"reference": refs[0].Reference, "decision": "support", "supportingQuote": refs[0].Quote}}
		}, "duplicate recommended citation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := decodeObject(output)
			tc.mutate(v)
			before := mustJSON(v)
			err := validateSelectedEvidence(before, input)
			if err == nil || !strings.Contains(err.Error(), tc.problem) || !strings.Contains(err.Error(), refs[0].Reference) || !strings.Contains(err.Error(), refs[1].Reference) {
				t.Fatal("conflicting decisions or other missing assessments were hidden", err)
			}
			if !rawJSONEqual(before, mustJSON(v)) {
				t.Fatal("validation silently repaired a scientific decision")
			}
		})
	}
}

func TestEvidenceOverviewKeepsSourceLevelErrorsSeparateFromPureQuoteRepair(t *testing.T) {
	input, output, refs := citationAssessmentFixture()
	ref := refs[0]
	ref.SourceName = "paper-metadata.md"
	ref.Quote = "## Bibliographic metadata\nTitle: Fixture\n## Abstract\nExact reported result."
	input = mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []tool.CitationRef{ref}})
	for _, tc := range []struct {
		name, level, quote, problem string
		quoteOnly                   bool
	}{
		{"wrong quote and wrong source level", "full_text", "Invented quotation.", "cannot be classified as full text", false},
		{"bibliographic quote", "abstract", "Title: Fixture", "supporting quote is bibliographic", false},
		{"pure quote repair", "abstract", "Invented quotation.", "supportingQuote", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := decodeObject(output)
			v["recommendedReferences"] = []string{ref.Reference}
			v["citationAssessments"] = []map[string]any{{"reference": ref.Reference, "decision": "support", "sourceLevel": tc.level, "supportingQuote": tc.quote}}
			err := validateSelectedEvidence(mustJSON(v), input)
			if err == nil || !strings.Contains(err.Error(), tc.problem) {
				t.Fatal("source level or exact source quotation check was weakened", err)
			}
			var quote *CitationQuoteError
			if errors.As(err, &quote) != tc.quoteOnly {
				t.Fatal("only a pure quotation error may offer the existing local patch", err)
			}
		})
	}
	// Same-document membership is not a substitute for a source-specific
	// assessment: a different indexed block may contain different evidence.
	sameDoc := refs[1]
	sameDoc.DocumentID = refs[0].DocumentID
	input = mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []tool.CitationRef{refs[0], sameDoc}})
	v := decodeObject(output)
	v["recommendedReferences"] = []string{sameDoc.Reference}
	v["citationAssessments"] = []map[string]any{{"reference": refs[0].Reference, "decision": "support", "sourceLevel": "abstract", "supportingQuote": refs[0].Quote}}
	if err := validateSelectedEvidence(mustJSON(v), input); err == nil || !strings.Contains(err.Error(), sameDoc.Reference) || !strings.Contains(err.Error(), "no corresponding citationAssessments") {
		t.Fatal("another block's assessment satisfied this exact marker", err)
	}
}

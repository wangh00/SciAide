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

func TestLiveQuoteDiagnosticUsesChatReferenceWithoutChangingSnapshots(t *testing.T) {
	ref := tool.CitationRef{Reference: "[K-WORKFLOW]", IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: citation.QuoteSHA256("Exact evidence."), Quote: "Exact evidence."}
	node := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion, OutputSchema: json.RawMessage(`{"type":"object","properties":{}}`), OutputSchemaSHA256: "schema"}
	compilation := Compilation{Nodes: []CompiledNode{{ID: "search", Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, node}, Edges: []Edge{{FromNode: "search", FromPort: "citations", ToNode: node.ID, ToPort: "candidates"}}}
	encoded, _ := canonicalJSON(compilation)
	compilation.CompilationSHA256 = hashBytes(encoded)
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []tool.CitationRef{ref}})
	step := Step{ID: "step", NodeID: node.ID, Ordinal: 1, Attempt: 1, Status: StepRunning, Input: input, InputSHA256: hashJSON(input)}
	detail := RunDetail{Run: Run{ID: "run", ProjectID: "project", ConversationID: "conversation", Status: RunRunning, CurrentStep: 1, Inputs: raw(`{}`), InputsSHA256: hashJSON(raw(`{}`)), Compilation: compilation, CompilationSHA256: compilation.CompilationSHA256}, Steps: []Step{{NodeID: "search", Status: StepCompleted, Output: mustJSON(map[string]any{"citations": []tool.CitationRef{ref}})}, step}, AIExecutions: []AIExecution{{WorkflowStepID: "step", Attempt: 1, InputSHA256: step.InputSHA256, ChatRunID: "chat"}}}
	repo := &submissionValidationRepository{detail: detail}
	service := &RuntimeService{repository: repo}
	marker := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	text := string(mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{marker}, "citationAssessments": []map[string]any{{"reference": marker, "decision": "support", "supportingQuote": "Invented evidence."}}}))
	before, _ := json.Marshal(repo.detail)
	_, err := service.ValidateResearchSubmission(context.Background(), "conversation", "run", "step", text)
	var issue *CitationQuoteError
	if !errors.As(err, &issue) || issue.Reference != marker || issue.CandidateQuote != ref.Quote {
		t.Fatalf("%v", err)
	}
	if _, err := service.ValidateResearchSubmission(context.Background(), "conversation", "run", "step", strings.Replace(text, "Invented evidence.", ref.Quote, 1)); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(repo.detail)
	if string(before) != string(after) {
		t.Fatal("mutated frozen data")
	}
}

func TestQuoteErrorPreservesExcerptAndUsesModelMarker(t *testing.T) {
	// Reproduce the real failure: the indexed excerpt starts inside a word.
	source := "tions had higher drop-out rates than antidepressant interventions (risk ratio 1.31; 95% CI 1.09 to 1.57). Heterogeneity in the network was moderate (τ 2 =0.03; I 2 =46%)."
	ref := tool.CitationRef{Reference: "[K-D1265F722EEE]", IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: citation.QuoteSHA256(source), Quote: source}
	input := mustJSON(map[string]any{"_selectedEvidence": map[string]any{"phase": "coverage"}, "candidates": []tool.CitationRef{ref}})
	makeOutput := func(q string) []byte {
		return mustJSON(map[string]any{"recommendation": "proceed", "recommendedReferences": []string{ref.Reference}, "citationAssessments": []map[string]any{{"reference": ref.Reference, "decision": "support", "sourceLevel": "abstract", "supportingQuote": q}}})
	}
	bad := "Exercise interven" + source
	err := validateSelectedEvidence(canonicalSupportingQuotes(makeOutput(bad), input), input)
	var issue *CitationQuoteError
	if !errors.As(err, &issue) || issue.CandidateQuote != source || issue.SupportingQuote != bad {
		t.Fatalf("%v", err)
	}
	visible := modelVisibleQuoteError(issue, []tool.CitationRef{ref}, "chat")
	marker := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	if visible.Reference != marker || strings.Contains(visible.Error(), ref.Reference) || issue.Reference != ref.Reference {
		t.Fatal("wrong marker or mutated original")
	}
	good := "Heterogeneity in the network was moderate (τ 2 =0.03; I 2 =46%)."
	if err := validateSelectedEvidence(makeOutput(good), input); err != nil {
		t.Fatal(err)
	}
	if err := validateSelectedEvidence(makeOutput(strings.Replace(good, "0.03", "0.04", 1)), input); err == nil {
		t.Fatal("changed numeric evidence accepted")
	}
}

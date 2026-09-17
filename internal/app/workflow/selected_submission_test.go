package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestSelectedEvidenceSubmissionUsesBatchSchema(t *testing.T) {
	s, r, _ := submissionValidationFixture(t)
	step := &r.detail.Steps[4]
	step.NodeID = "evidence_screening"
	step.Input = raw(`{"_selectedEvidence":{"phase":"batch","documents":["doc"]},"candidates":[]}`)
	step.InputSHA256 = hashJSON(step.Input)
	n := CompiledNode{ID: "evidence_screening", Kind: NodeAIAnalysis, PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}
	n.OutputSchemaSHA256 = hashJSON(n.OutputSchema)
	r.detail.Run.Compilation.Nodes = []CompiledNode{n}
	r.detail.Run.Compilation.CompilationSHA256 = ""
	encoded, err := canonicalJSON(r.detail.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	r.detail.Run.CompilationSHA256 = hashBytes(encoded)
	r.detail.Run.Compilation.CompilationSHA256 = r.detail.Run.CompilationSHA256
	text := `{"documentAnalyses":[{"documentId":"doc","finding":"unknown","applicability":"unknown","limitations":"no excerpts","references":[]}]}`
	if _, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, text); err != nil {
		t.Fatal(err)
	}
}

func TestSelectedEvidenceSubmissionRestoresSeededMarkersBeforeBusinessValidation(t *testing.T) {
	s, r, _ := submissionValidationFixture(t)
	step := &r.detail.Steps[4]
	step.NodeID = "evidence_screening"
	ref := tool.CitationRef{Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "doc", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "paper-metadata.md", Locator: "lines:1-4", Quote: "Verified result.", QuoteSHA256: citation.QuoteSHA256("Verified result.")}
	ref.Reference = citation.KnowledgeReference("run", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	step.Input = rawObject(map[string]any{"_selectedEvidence": map[string]any{"phase": "batch", "documents": []string{"doc"}}, "candidates": []tool.CitationRef{ref}})
	step.InputSHA256 = hashJSON(step.Input)
	n := CompiledNode{ID: "evidence_screening", Kind: NodeAIAnalysis, PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}
	n.OutputSchemaSHA256 = hashJSON(n.OutputSchema)
	r.detail.Run.Compilation.Nodes = []CompiledNode{{ID: "search", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}}, n}
	r.detail.Run.Compilation.Edges = []Edge{{FromNode: "search", FromPort: "citations", ToNode: n.ID, ToPort: "candidates"}}
	r.detail.Steps[0] = Step{NodeID: "search", Status: StepCompleted, Output: rawObject(map[string]any{"citations": []tool.CitationRef{ref}})}
	r.detail.AIExecutions = []AIExecution{{WorkflowStepID: step.ID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, ChatRunID: "chat"}}
	r.detail.Run.Compilation.CompilationSHA256 = ""
	encoded, err := canonicalJSON(r.detail.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	r.detail.Run.CompilationSHA256 = hashBytes(encoded)
	r.detail.Run.Compilation.CompilationSHA256 = r.detail.Run.CompilationSHA256
	chatRef := citation.KnowledgeReference("chat", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	value, _ := json.Marshal(map[string]any{"documentAnalyses": []any{map[string]any{"documentId": "doc", "finding": "Verified result.", "limitations": "Abstract only.", "references": []string{chatRef}}}})
	accepted, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, string(value))
	if err != nil || !strings.Contains(string(accepted), ref.Reference) || strings.Contains(string(accepted), chatRef) {
		t.Fatalf("valid seeded submission rejected: %s %v", accepted, err)
	}
	if _, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, strings.ReplaceAll(string(value), chatRef, "[K-FFFFFFFFFFFF]")); err == nil {
		t.Fatal("invented marker accepted")
	}
}

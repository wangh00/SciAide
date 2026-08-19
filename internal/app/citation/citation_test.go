package citation

import (
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestResolveAcceptsOnlyExactRunBoundEvidence(t *testing.T) {
	runID, messageID := "run-a", "assistant-a"
	ref := tool.CitationRef{
		ID: "attachment-a", Kind: KindKnowledgeChunk, ProjectID: "project-a",
		IndexVersionID: "index-a", DocumentID: "document-a", AttachmentID: "attachment-a", ChunkID: "chunk-a",
		SourceName: "paper.pdf", MIMEType: "application/pdf", Locator: "page:7", Title: "Results",
		Quote: "The intervention reduced the measured outcome.", SourceStart: 40, SourceEnd: 96,
	}
	ref.QuoteSHA256 = QuoteSHA256(ref.Quote)
	ref.Reference = KnowledgeReference(runID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	reference := ref.Reference
	calls := []tool.Call{
		{ID: "call-a", RunID: runID, ToolName: KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}},
		{ID: "call-b", RunID: runID, ToolName: KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}},
		{ID: "other-run", RunID: "run-b", ToolName: KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}},
	}
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	values := Resolve(runID, messageID, "Supported "+reference+" and repeated "+reference+"; fabricated [K-000000000000].", calls, at)
	if len(values) != 1 {
		t.Fatalf("resolved citations = %#v", values)
	}
	value := values[0]
	if value.Reference != reference || value.Ordinal != 0 || value.ToolCallID != "call-b" || value.QuoteSHA256 != ref.QuoteSHA256 || value.Locator != "page:7" {
		t.Fatalf("resolved citation = %#v", value)
	}
}

func TestResolveRejectsChangedOrAmbiguousEvidence(t *testing.T) {
	runID := "run-a"
	base := tool.CitationRef{Kind: KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk-a", SourceName: "paper.pdf", Locator: "page:1", Quote: "evidence"}
	base.QuoteSHA256 = QuoteSHA256(base.Quote)
	base.Reference = KnowledgeReference(runID, base.IndexVersionID, base.ChunkID, base.QuoteSHA256)
	reference := base.Reference
	changed := base
	changed.Locator = "page:2"
	calls := []tool.Call{
		{ID: "first", RunID: runID, ToolName: KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{base}}},
		{ID: "changed", RunID: runID, ToolName: KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{changed}}},
	}
	if values := Resolve(runID, "message", reference, calls, time.Now()); len(values) != 0 {
		t.Fatalf("ambiguous evidence was accepted: %#v", values)
	}
	changed = base
	changed.Quote = "a different bounded excerpt from the same chunk"
	changed.QuoteSHA256 = QuoteSHA256(changed.Quote)
	changed.Reference = KnowledgeReference(runID, changed.IndexVersionID, changed.ChunkID, changed.QuoteSHA256)
	if changed.Reference == reference {
		t.Fatal("different bounded excerpts reused the same reference")
	}
	values := Resolve(runID, "message", reference+" and "+changed.Reference, []tool.Call{
		{ID: "first", RunID: runID, ToolName: KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{base}}},
		{ID: "later", RunID: runID, ToolName: KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{changed}}},
	}, time.Now())
	if len(values) != 2 || values[0].Quote != base.Quote || values[1].Quote != changed.Quote || values[1].ToolCallID != "later" {
		t.Fatalf("distinct excerpts were not preserved: %#v", values)
	}
	base.QuoteSHA256 = QuoteSHA256("different")
	if values := Resolve(runID, "message", reference, []tool.Call{{ID: "bad", RunID: runID, ToolName: KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{base}}}}, time.Now()); len(values) != 0 {
		t.Fatalf("changed quote was accepted: %#v", values)
	}
}

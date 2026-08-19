package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/document"
)

type documentFixture struct {
	attachment attachment.Attachment
	parsed     document.Parsed
}

type knowledgeFixture struct{ result knowledge.SearchResult }

func (f knowledgeFixture) SearchWithOptions(context.Context, string, knowledge.SearchOptions) (knowledge.SearchResult, error) {
	return f.result, nil
}

func (f documentFixture) List(context.Context, string) ([]attachment.Attachment, error) {
	return []attachment.Attachment{f.attachment}, nil
}
func (f documentFixture) Parsed(context.Context, string, string) (attachment.Attachment, document.Parsed, error) {
	return f.attachment, f.parsed, nil
}

func TestDocumentToolsExposeBoundedContentAndCitations(t *testing.T) {
	fixture := documentFixture{
		attachment: attachment.Attachment{ID: "paper", ProjectID: "project", OriginalName: "paper.pdf", MIMEType: "application/pdf", Format: document.FormatPDF, Status: attachment.StatusReady},
		parsed: document.Parsed{SchemaVersion: document.SchemaVersion, Format: document.FormatPDF, Units: []document.Unit{
			{Index: 1, Kind: "page", Locator: "page:1", Content: "background evidence"},
			{Index: 2, Kind: "page", Locator: "page:2", Content: "alpha treatment produced result 42"},
		}},
	}
	read, err := NewReadDocument(fixture).Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"attachmentId":"paper","locator":"page:2","maxChars":20}`)})
	if err != nil || read.Status != tool.ResultSuccess || len(read.Citations) != 1 || read.Citations[0].Locator != "page:2" || !read.Truncated {
		t.Fatalf("read = %#v, %v", read, err)
	}
	searched, err := NewSearchDocument(fixture).Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"attachmentId":"paper","query":"result 42"}`)})
	if err != nil || len(searched.Citations) != 1 || searched.Citations[0].Locator != "page:2" {
		t.Fatalf("search = %#v, %v", searched, err)
	}
	inspected, err := NewInspectDocument(fixture).Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"attachmentId":"paper"}`)})
	if err != nil || inspected.Status != tool.ResultSuccess || len(inspected.Artifacts) != 1 {
		t.Fatalf("inspect = %#v, %v", inspected, err)
	}
}

func TestDocumentToolsExposeSectionTitlesWithoutDuplicatePageCitations(t *testing.T) {
	fixture := documentFixture{
		attachment: attachment.Attachment{ID: "paper", ProjectID: "project", OriginalName: "paper.pdf", MIMEType: "application/pdf", Format: document.FormatPDF, Status: attachment.StatusReady},
		parsed: document.Parsed{SchemaVersion: document.SchemaVersion, Format: document.FormatPDF, Units: []document.Unit{
			{Index: 1, Kind: "section", Locator: "page:2", Title: "Methods", Content: "alpha method"},
			{Index: 2, Kind: "section", Locator: "page:2", Title: "Results", Content: "alpha result"},
		}},
	}
	read, err := NewReadDocument(fixture).Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"attachmentId":"paper","locator":"page:2"}`)})
	if err != nil || len(read.Citations) != 1 || !strings.Contains(read.Text, "page:2 | Methods") || !strings.Contains(read.Text, "page:2 | Results") {
		t.Fatalf("section read = %#v, %v", read, err)
	}
	searched, err := NewSearchDocument(fixture).Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"attachmentId":"paper","query":"alpha"}`)})
	if err != nil || len(searched.Citations) != 1 || !strings.Contains(searched.Text, "page:2 | Methods") || !strings.Contains(searched.Text, "page:2 | Results") {
		t.Fatalf("section search = %#v, %v", searched, err)
	}
}

func TestKnowledgeSearchExposesCrossDocumentCitations(t *testing.T) {
	fixture := knowledgeFixture{result: knowledge.SearchResult{
		Query: "replication", TotalMatches: 2, Status: knowledge.ProjectStatus{Documents: 2, Ready: 2},
		Matches: []knowledge.Match{
			{ChunkID: "chunk-a", IndexVersionID: "index-v3", DocumentID: "document-a", AttachmentID: "paper-a", Name: "a.pdf", MIMEType: "application/pdf", Format: document.FormatPDF, Locator: "page:2", Snippet: "first replication"},
			{ChunkID: "chunk-b", IndexVersionID: "index-v3", DocumentID: "document-b", AttachmentID: "paper-b", Name: "b.pdf", MIMEType: "application/pdf", Format: document.FormatPDF, Locator: "page:8", Snippet: "second replication"},
		},
	}}
	result, err := NewSearchKnowledge(fixture).Invoke(context.Background(), tool.Invocation{RunID: "run", ProjectID: "project", Arguments: json.RawMessage(`{"query":"replication","limit":2}`)})
	if err != nil || len(result.Citations) != 2 || len(result.Artifacts) != 2 || result.Citations[1].Locator != "page:8" || result.Citations[0].Kind != citation.KindKnowledgeChunk || result.Citations[0].Reference == "" {
		t.Fatalf("knowledge search = %#v, %v", result, err)
	}
	if strings.Contains(string(result.Structured), "first replication") || !strings.Contains(result.Text, "first replication") || !strings.Contains(result.Text, result.Citations[0].Reference) {
		t.Fatalf("knowledge snippets were duplicated or omitted: text=%q structured=%s", result.Text, result.Structured)
	}
}

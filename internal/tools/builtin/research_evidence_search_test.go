package builtin

import (
	"context"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/tool"
	"strings"
	"testing"
)

type verifiedEvidenceFixture struct {
	perDocumentFixture
	task string
}

func (f *verifiedEvidenceFixture) SearchResearchEvidence(_ context.Context, project, task string, o knowledge.SearchOptions) (knowledge.SearchResult, error) {
	f.task = task
	return knowledge.SearchResult{Matches: []knowledge.Match{{IndexVersionID: "v", DocumentID: o.DocumentIDs[0], AttachmentID: "a", ChunkID: "c", Name: "study-metadata.md", Snippet: strings.Repeat("Original source. ", 80) + "P = 0.11", SourceStart: 0, SourceEnd: 1500}}}, nil
}
func TestResearchEvidenceToolKeepsCompleteQuoteAndTaskScope(t *testing.T) {
	f := &verifiedEvidenceFixture{}
	v, err := NewSearchKnowledge(f).Invoke(context.Background(), tool.Invocation{RunID: "run", ProjectID: "p", ResearchTaskID: "task", Arguments: []byte(`{"query":"comparison","documentIds":["doc"],"perDocument":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if f.task != "task" || len(v.Citations) != 1 {
		t.Fatal(f.task, v)
	}
	c := v.Citations[0]
	if !strings.HasSuffix(c.Quote, "P = 0.11") || len(c.Quote) < 900 || c.QuoteSHA256 != citation.QuoteSHA256(c.Quote) || c.Reference != citation.KnowledgeReference("run", "v", "c", c.QuoteSHA256) {
		t.Fatal("quote shortened or incorrectly signed", c)
	}
}

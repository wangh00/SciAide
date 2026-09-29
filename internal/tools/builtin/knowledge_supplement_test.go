package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/tool"
	"strings"
	"testing"
)

type diverseEvidenceFixture struct {
	queries []string
	limits  []int
	ids     []string
}

func (f *diverseEvidenceFixture) SearchWithOptions(context.Context, string, knowledge.SearchOptions) (knowledge.SearchResult, error) {
	panic("research evidence must use verified reader")
}
func (f *diverseEvidenceFixture) SearchResearchEvidence(_ context.Context, _, task string, o knowledge.SearchOptions) (knowledge.SearchResult, error) {
	if task != "task" {
		panic("lost task scope")
	}
	f.queries = append(f.queries, o.Query)
	f.limits = append(f.limits, o.Limit)
	f.ids = append(f.ids, o.DocumentIDs...)
	matches := []knowledge.Match{}
	for n := 0; n < o.Limit; n++ {
		key := fmt.Sprintf("%s-%d", o.Query, n)
		matches = append(matches, knowledge.Match{ChunkID: key, DocumentID: o.DocumentIDs[0], AttachmentID: "a", IndexVersionID: "v", Name: "paper.pdf", Snippet: key + " exact full source text"})
	}
	return knowledge.SearchResult{Matches: matches, TotalMatches: o.Limit}, nil
}
func TestComplementaryQueriesShareBudgetAndPreserveExactQuotes(t *testing.T) {
	f := &diverseEvidenceFixture{}
	input := json.RawMessage(`{"query":"topic","queries":["results","methods","results"],"perDocument":true,"documentIds":["selected"],"limit":20}`)
	def, _ := NewSearchKnowledge(f).Definition(context.Background())
	if err := (tool.JSONSchemaValidator{}).Validate(def.InputSchema, input); err != nil {
		t.Fatal(err)
	}
	result, err := NewSearchKnowledge(f).Invoke(context.Background(), tool.Invocation{RunID: "run", ProjectID: "p", ResearchTaskID: "task", Arguments: input})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Citations) != 20 || len(f.queries) != 3 || !result.Truncated {
		t.Fatal(len(result.Citations), f.queries, result.Truncated)
	}
	for n, q := range []string{"topic", "results", "methods"} {
		if !strings.HasPrefix(result.Citations[n].Quote, q+"-0") {
			t.Fatal("one query crowded out complementary evidence")
		}
	}
	for _, c := range result.Citations {
		if c.DocumentID != "selected" || !strings.HasSuffix(c.Quote, "exact full source text") {
			t.Fatal(c)
		}
	}
}
func TestComplementaryRetrievalBoundsGlobalCitationBudget(t *testing.T) {
	f := &diverseEvidenceFixture{}
	ids := []string{}
	for n := 0; n < 100; n++ {
		ids = append(ids, fmt.Sprint(n))
	}
	args, _ := json.Marshal(map[string]any{"query": "topic", "queries": []string{"methods"}, "perDocument": true, "documentIds": ids, "limit": 20})
	result, err := NewSearchKnowledge(f).Invoke(context.Background(), tool.Invocation{ResearchTaskID: "task", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Citations) > 240 {
		t.Fatal("unbounded citations", len(result.Citations))
	}
	for _, n := range f.limits {
		if n != 2 {
			t.Fatal(n)
		}
	}
}

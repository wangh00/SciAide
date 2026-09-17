package builtin

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/tool"
	"testing"
)

type perDocumentFixture struct{ ids []string }

func (f *perDocumentFixture) SearchWithOptions(_ context.Context, _ string, o knowledge.SearchOptions) (knowledge.SearchResult, error) {
	f.ids = append(f.ids, o.DocumentIDs...)
	if len(o.DocumentIDs) != 1 || (o.Limit != 3 && o.Limit != 2) {
		panic("not per-document retrieval")
	}
	return knowledge.SearchResult{}, nil
}
func TestKnowledgePerDocumentRecordsEmptyDocuments(t *testing.T) {
	f := &perDocumentFixture{}
	v, err := NewSearchKnowledge(f).Invoke(context.Background(), tool.Invocation{Arguments: json.RawMessage(`{"query":"topic","perDocument":true,"documentIds":["a","b","a"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Coverage []struct {
			ID    string `json:"documentId"`
			Count int    `json:"excerptCount"`
		} `json:"documentCoverage"`
	}
	json.Unmarshal(v.Structured, &out)
	if len(f.ids) != 2 || len(out.Coverage) != 2 || out.Coverage[1].ID != "b" || out.Coverage[1].Count != 0 {
		t.Fatal(string(v.Structured), f.ids)
	}
}

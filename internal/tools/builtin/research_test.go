package builtin

import (
	"context"
	"encoding/json"
	"testing"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type fixtureResearchService struct{}

func (fixtureResearchService) Catalog() []appresearch.Source {
	return []appresearch.Source{{ID: "openalex", Name: "OpenAlex", Description: "fixture"}}
}
func (fixtureResearchService) Search(context.Context, appresearch.SearchCommand) (appresearch.SearchResult, error) {
	return appresearch.SearchResult{Query: "q", Works: []appresearch.Work{{SourceID: "openalex", SourceRecordID: "W1", Title: "Paper", Abstract: "Evidence", RawSnapshot: json.RawMessage(`{"secret":"source payload"}`)}}, Sources: []appresearch.SourceSearch{{SourceID: "openalex", Status: appresearch.SearchOK, Count: 1}}, Partial: false}, nil
}
func (fixtureResearchService) Fetch(context.Context, appresearch.FetchCommand) (appresearch.Work, error) {
	return appresearch.Work{SourceID: "openalex", SourceRecordID: "W1", Title: "Paper", RawSnapshot: json.RawMessage(`{"large":true}`)}, nil
}

func TestResearchToolsUseFixedSurfaceAndNeverReturnTrustedCitations(t *testing.T) {
	ctx := context.Background()
	values := []tool.Tool{NewResearchCatalog(fixtureResearchService{}), NewResearchSearch(fixtureResearchService{}), NewResearchFetch(fixtureResearchService{})}
	wantNames := []string{ResearchCatalogName, ResearchSearchName, ResearchFetchName}
	arguments := []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"query":"q","sourceIds":["openalex"]}`), json.RawMessage(`{"sourceId":"openalex","recordId":"W1"}`)}
	for index, value := range values {
		definition, err := value.Definition(ctx)
		if err != nil || definition.QualifiedName != wantNames[index] {
			t.Fatalf("definition = %#v, %v", definition, err)
		}
		result, err := value.Invoke(ctx, tool.Invocation{RunID: "run", ProjectID: "project", Arguments: arguments[index]})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Citations) != 0 || len(result.Artifacts) != 0 {
			t.Fatalf("online result acquired trusted refs: %#v", result)
		}
		if string(result.Structured) == "" || json.Valid(result.Structured) == false {
			t.Fatalf("structured result = %q", result.Structured)
		}
		if string(result.Structured) != "" && string(result.Structured) != `{"sources":[{"id":"openalex","name":"OpenAlex","domain":"","description":"fixture","homepage":"","host":"","keyFree":false,"fullText":false}]}` && string(result.Structured) != `{"query":"q","works":[{"sourceId":"openalex","sourceRecordId":"W1","title":"Paper","abstract":"Evidence","authors":null,"identifiers":{},"openAccess":false}],"sources":[{"sourceId":"openalex","status":"ok","count":1,"retryable":false}],"partial":false}` && string(result.Structured) != `{"work":{"sourceId":"openalex","sourceRecordId":"W1","title":"Paper","authors":null,"identifiers":{},"openAccess":false}}` {
			var payload map[string]any
			if err := json.Unmarshal(result.Structured, &payload); err != nil {
				t.Fatal(err)
			}
		}
		if string(result.Structured) != "" && containsJSONKey(result.Structured, "rawSnapshot") {
			t.Fatalf("raw source payload leaked through tool: %s", result.Structured)
		}
	}
}

func containsJSONKey(value []byte, key string) bool {
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return false
	}
	encoded, _ := json.Marshal(decoded)
	return string(encoded) != "" && stringContains(string(encoded), `"`+key+`"`)
}

func stringContains(value, needle string) bool {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

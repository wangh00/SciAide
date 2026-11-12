package research

import (
	"context"
	"testing"
)

type projectedConnector struct {
	fixtureConnector
	query *string
}

func (c projectedConnector) Search(_ context.Context, o SearchOptions) ([]Work, error) {
	*c.query = o.Query
	return c.works, nil
}
func TestProviderProjectionPreservesLogicalQuery(t *testing.T) {
	got := ""
	service, err := NewService([]Connector{projectedConnector{fixtureConnector: fixtureConnector{source: testSource("crossref")}, query: &got}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), SearchCommand{Query: `("A" OR "B") AND "C"`, ProviderQueries: map[string]string{"crossref": "A versus C"}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got != "A versus C" || result.Query != `("A" OR "B") AND "C"` || result.Sources[0].ProviderQuery != got || result.Sources[0].EffectiveQuery != got {
		t.Fatal(result, got)
	}
	if status := result.Sources[0]; status.InputQuery != got || status.QueryOrigin != "planned_provider_query" || status.QueryMode != "ranked_bibliographic" {
		t.Fatal("planned origin must not conceal the endpoint's actual syntax", status)
	}
}

package research

import (
	"context"
	"strings"
	"testing"
)

func TestArXivStatusSeparatesLogicalAndProviderQuery(t *testing.T) {
	service, err := NewService([]Connector{fixtureConnector{source: testSource("arxiv")}})
	if err != nil {
		t.Fatal(err)
	}
	query := `intermittent fasting AND obesity`
	result, err := service.Search(context.Background(), SearchCommand{Query: query, SourceIDs: []string{"arxiv"}, Offset: 20})
	if err != nil {
		t.Fatal(err)
	}
	status := result.Sources[0]
	want, err := ArXivQuery(query)
	if err != nil || status.EffectiveQuery != query || status.ProviderQuery != want || status.Offset != 20 || status.QueryMode != "arxiv_boolean" {
		t.Fatal(status, err)
	}
}

func TestArXivQueryPreservesBooleanConceptsAndFields(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{`intermittent fasting AND (calorie restriction OR energy restriction)`, `((all:intermittent AND all:fasting) AND ((all:calorie AND all:restriction) OR (all:energy AND all:restriction)))`},
		{`"intermittent fasting" AND (obesity OR overweight)`, `(all:"intermittent fasting" AND (all:obesity OR all:overweight))`},
		{`ti:(fasting OR "time-restricted eating") ANDNOT cat:cs.AI`, `((ti:fasting OR ti:"time-restricted eating") ANDNOT cat:cs.AI)`},
		{`fasting AND NOT (mice OR rats)`, `(all:fasting ANDNOT (all:mice OR all:rats))`},
		{`5:2 AND weight loss`, `((all:"5:2" AND all:weight) AND all:loss)`},
		{`all:electron`, `all:electron`},
		{`submittedDate:[202001010000 TO 202512312359] AND ti:fasting`, `(submittedDate:[202001010000 TO 202512312359] AND ti:fasting)`},
	} {
		got, err := ArXivQuery(tt.input)
		if err != nil || got != tt.want {
			t.Fatalf("%s: %s %v", tt.input, got, err)
		}
		again, err := ArXivQuery(got)
		if err != nil || again != got {
			t.Fatalf("projection changed on paging: %s %v", again, err)
		}
	}
}

func TestArXivQueryRejectsMalformedInsteadOfBroadening(t *testing.T) {
	for _, query := range []string{`fasting AND`, `(fasting OR obesity`, `fasting )`, `"unclosed`, `NOT fasting`, strings.Repeat("(", 70) + "fasting" + strings.Repeat(")", 70)} {
		if _, err := ArXivQuery(query); err == nil {
			t.Fatal("accepted invalid expression", query)
		}
	}
}

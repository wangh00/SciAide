package research

import (
	"strings"
	"testing"
)

func TestRankedBibliographicQueryPreservesConcepts(t *testing.T) {
	got := RankedBibliographicQuery(`("time-restricted eating" OR "5:2 diet") AND ("calorie restriction" OR "energy restriction")`)
	if strings.Contains(got, "AND") || strings.Contains(got, "OR") || !strings.Contains(got, "5:2 diet") || !strings.Contains(got, "time-restricted eating") {
		t.Fatal(got)
	}
}

func TestRankedQueryDoesNotPromoteExcludedTerms(t *testing.T) {
	for _, query := range []string{`fasting NOT cancer`, `fasting NOT (cancer OR (diabetes AND children))`, `fasting AND NOT "cancer treatment"`} {
		if got := RankedBibliographicQuery(query); got != "fasting" {
			t.Fatal(got)
		}
	}
}

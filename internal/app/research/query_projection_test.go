package research

import (
	"strings"
	"testing"
)

func TestRankedBibliographicQueryPreservesConcepts(t *testing.T) {
	projection, err := RankedBibliographicQuery(`("time-restricted eating" OR "5:2 diet") AND ("calorie restriction" OR "energy restriction")`)
	if err != nil {
		t.Fatal(err)
	}
	got := projection.Query
	if strings.Contains(got, "AND") || strings.Contains(got, "OR") || !strings.Contains(got, "5:2 diet") || !strings.Contains(got, "time-restricted eating") {
		t.Fatal(got)
	}
}

func TestRankedQueryDoesNotPromoteExcludedTerms(t *testing.T) {
	for _, query := range []string{`fasting NOT cancer`, `fasting NOT (cancer OR (diabetes AND children))`, `fasting AND NOT "cancer treatment"`} {
		if got, err := RankedBibliographicQuery(query); err != nil || got.Query != "fasting" {
			t.Fatal(got, err)
		}
	}
}

func TestRankedProjectionRegressionBaseline(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{`"AI feedback"[Title/Abstract] AND writing`, "AI feedback writing"},
		{`writing NOT animal-based`, "writing"},
		{`writing NOT cancer[Title/Abstract]`, "writing"},
		{`C++ tutoring student feedback`, "C++ tutoring student feedback"},
		{`5:2 diet AND student writing`, "5:2 diet student writing"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			if got, err := RankedBibliographicQuery(tc.query); err != nil || got.Query != tc.want {
				t.Fatalf("got %+v (%v); want %q", got, err, tc.want)
			}
		})
	}
}

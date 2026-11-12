package research

import (
	"context"
	"strings"
	"testing"
)

func TestSearchRecordContractPreservesIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		works        []Work
		ids          []string
	}{
		{"long-id-is-not-truncated", "alpha", []Work{{SourceRecordID: strings.Repeat("x", MaxRecordIDRunes) + "suffix", Title: "Other paper"}}, nil},
		{"wrong-source-is-not-relabelled", "alpha", []Work{{SourceID: "beta", SourceRecordID: "1", Title: "Other paper"}}, nil},
		{"unfetchable-openalex", "openalex", []Work{{SourceRecordID: "A123", Title: "Not a work"}}, nil},
		{"unfetchable-semantic", "semantic-scholar", []Work{{SourceRecordID: "paper?other", Title: "Other paper"}}, nil},
		{"unfetchable-arxiv", "arxiv", []Work{{SourceRecordID: "2401.01234?other", Title: "Other paper"}}, nil},
		{"unfetchable-pubmed", "pubmed", []Work{{SourceRecordID: "not-a-pmid", Title: "Other paper"}}, nil},
		{"canonical-same-source-dedup", "openalex", []Work{{SourceRecordID: "w123", Title: "First title"}, {SourceRecordID: "W123", Title: "Second title"}}, []string{"w123"}},
		{"opaque-case-remains-distinct", "alpha", []Work{{SourceRecordID: "Case", Title: "First title"}, {SourceRecordID: "case", Title: "Second title"}}, []string{"Case", "case"}},
		{"semantic-case-remains-distinct", "semantic-scholar", []Work{{SourceRecordID: "Case", Title: "First title"}, {SourceRecordID: "case", Title: "Second title"}}, []string{"Case", "case"}},
		{"valid-mixed-page", "alpha", []Work{{SourceID: "beta", SourceRecordID: "1", Title: "Wrong source"}, {SourceRecordID: "KeepID", Title: "Original title", Abstract: "Original abstract", Year: 2025}}, []string{"KeepID"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewService([]Connector{fixtureConnector{source: testSource(tc.source), works: tc.works}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Search(context.Background(), SearchCommand{Query: "writing feedback", SourceIDs: []string{tc.source}, Limit: 5})
			if err != nil || len(got.Works) != len(tc.ids) {
				t.Fatal("record identity changed or wrong records accepted", got, err)
			}
			if len(tc.ids) == 0 {
				if !got.Partial || got.Sources[0].ErrorCode != FailureInvalidData || got.Sources[0].Retryable {
					t.Fatal("invalid identities became success/empty", got)
				}
				return
			}
			for i, id := range tc.ids {
				if got.Works[i].SourceRecordID != id {
					t.Fatal("identity was rewritten", got.Works[i])
				}
				for _, original := range tc.works {
					if original.SourceRecordID == id {
						if got.Works[i].Title != original.Title || got.Works[i].Abstract != original.Abstract || got.Works[i].Year != original.Year {
							t.Fatal("first valid record fields changed", got.Works[i])
						}
						break
					}
				}
			}
			if got.Partial {
				t.Fatal("valid page became partial", got)
			}
		})
	}
}

func TestCanonicalArXivIdentityBoundary(t *testing.T) {
	for _, length := range []int{101, MaxRecordIDRunes, MaxRecordIDRunes + 1} {
		id := strings.Repeat("x", length)
		want := id
		if length > MaxRecordIDRunes {
			want = ""
		}
		for _, input := range []string{id, "https://arxiv.org/abs/" + id + "v1"} {
			if CanonicalSourceRecordID("arxiv", input) != want || NormalizeArXiv(input) != want {
				t.Errorf("canonical/identifier mismatch at ID length %d (input length %d)", length, len(input))
			}
		}
		if IsUsableSourceRecord("arxiv", Work{SourceRecordID: id, Title: "Original title"}) != (want != "") {
			t.Errorf("record boundary mismatch at length %d", length)
		}
	}
}

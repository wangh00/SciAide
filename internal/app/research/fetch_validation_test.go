package research

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFetchRejectsIdentityChangeBeforeNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, requested string
		work            Work
		valid           bool
	}{
		{"same-record", "Wanted", Work{SourceRecordID: "Wanted", Title: "Original title", Abstract: "Original abstract", Year: 2025}, true},
		{"different-record", "Wanted", Work{SourceRecordID: "Other", Title: "Other title"}, false},
		{"opaque-case-is-significant", "Wanted", Work{SourceRecordID: "wanted", Title: "Other title"}, false},
		{"different-source", "Wanted", Work{SourceID: "beta", SourceRecordID: "Wanted", Title: "Other title"}, false},
		{"missing-identity", "Wanted", Work{Title: "Other title"}, false},
		{"missing-title", "Wanted", Work{SourceRecordID: "Wanted", Title: " \t"}, false},
		{"truncation-cannot-create-match", strings.Repeat("x", MaxRecordIDRunes), Work{SourceRecordID: strings.Repeat("x", MaxRecordIDRunes) + "suffix", Title: "Other title"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, err := NewService([]Connector{fixtureConnector{source: testSource("alpha"), works: []Work{tc.work}}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := service.Fetch(context.Background(), FetchCommand{SourceID: "alpha", RecordID: tc.requested})
			if tc.valid {
				if err != nil || got.SourceRecordID != tc.requested || got.Title != tc.work.Title || got.Abstract != tc.work.Abstract || got.Year != tc.work.Year {
					t.Fatal("valid record changed", got, err)
				}
				return
			}
			var sourceErr *SourceError
			if !errors.As(err, &sourceErr) || sourceErr.Code != FailureInvalidData || sourceErr.Retryable || got.SourceRecordID != "" {
				t.Fatal("wrong or invalid record escaped Fetch", got, err)
			}
		})
	}
}

func TestEnrichmentUsesFetchCanonicalIdentityWithoutReplacingSearch(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-work-case", true: "doi-conflict"}[conflict], func(t *testing.T) {
			original := Work{SourceRecordID: "w123", Title: "Original search title", Identifiers: Identifiers{DOI: "10.5555/original"}}
			fetched := Work{SourceRecordID: "W123", Title: "Detail title", Abstract: "Recovered abstract.", Identifiers: original.Identifiers}
			if conflict {
				fetched.Identifiers.DOI = "10.5555/other"
			}
			fixture := &enrichmentFixture{fixtureConnector: fixtureConnector{source: testSource("openalex"), works: []Work{original}}, fetched: fetched}
			service, err := NewService([]Connector{fixture})
			if err != nil {
				t.Fatal(err)
			}
			got, err := service.Search(context.Background(), SearchCommand{Query: "writing feedback", EnrichMetadata: true})
			if err != nil || len(got.Works) != 1 || fixture.calls != 1 {
				t.Fatal(got, err)
			}
			work := got.Works[0]
			if work.SourceRecordID != original.SourceRecordID || work.Title != original.Title || work.Identifiers.DOI != original.Identifiers.DOI {
				t.Fatal("enrichment replaced search identity", work)
			}
			if conflict {
				if work.MetadataFetchStatus != "identity_mismatch" || work.Abstract != "" {
					t.Fatal("DOI conflict was accepted", work)
				}
			} else if work.MetadataFetchStatus != "enriched" || work.Abstract != fetched.Abstract {
				t.Fatal("equivalent fetched identity was rejected", work)
			}
		})
	}
}

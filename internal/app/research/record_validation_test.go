package research

import (
	"context"
	"testing"
)

func TestSearchNonemptyUnusableRecordsAreInvalid(t *testing.T) {
	valid := Work{SourceRecordID: "KeepID", Title: "Original writing study", Abstract: "Original abstract", Year: 2025}
	cases := []struct {
		name   string
		works  []Work
		limit  int
		status SourceSearchStatus
	}{
		{"missing-identity", []Work{{Title: "Writing study"}}, 5, SearchFailed},
		{"missing-title", []Work{{SourceRecordID: "1", Title: " \t"}}, 5, SearchFailed},
		{"split-fields", []Work{{Title: "Writing study"}, {SourceRecordID: "1"}}, 5, SearchFailed},
		{"valid-only-outside-limit", []Work{{SourceRecordID: "1"}, valid}, 1, SearchFailed},
		{"true-empty", []Work{}, 5, SearchEmpty},
		{"valid-mixed-page", []Work{{SourceRecordID: "1"}, valid, {Title: "No identity"}}, 5, SearchOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, err := NewService([]Connector{fixtureConnector{source: testSource("alpha"), works: tc.works}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Search(context.Background(), SearchCommand{Query: "writing", SourceIDs: []string{"alpha"}, Limit: tc.limit})
			if err != nil || len(result.Sources) != 1 || result.Sources[0].Status != tc.status || result.Partial != (tc.status == SearchFailed) {
				t.Fatal("unusable records conflated with no source results", result, err)
			}
			if tc.status == SearchFailed && (result.Sources[0].ErrorCode != FailureInvalidData || result.Sources[0].Retryable || result.Sources[0].LimitReached) {
				t.Fatal("wrong failure or invalid page advertised for further paging", result.Sources[0])
			}
			if tc.status == SearchOK && (len(result.Works) != 1 || result.Works[0].SourceRecordID != valid.SourceRecordID || result.Works[0].Title != valid.Title || result.Works[0].Abstract != valid.Abstract || result.Works[0].Year != valid.Year) {
				t.Fatal("valid mixed-page record lost or changed", result.Works)
			}
		})
	}
}

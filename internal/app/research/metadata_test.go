package research

import (
	"context"
	"testing"
)

type enrichmentFixture struct {
	fixtureConnector
	fetched    Work
	calls      int
	fetchError error
}

func (f *enrichmentFixture) Fetch(context.Context, string) (Work, error) {
	f.calls++
	return f.fetched, f.fetchError
}
func TestMetadataEnrichmentPreservesSearchAndRejectsWrongIdentity(t *testing.T) {
	f := &enrichmentFixture{fixtureConnector: fixtureConnector{source: testSource("alpha"), works: []Work{{SourceRecordID: "a", Title: "Original title"}}}, fetched: Work{SourceRecordID: "a", Title: "Detail title", Abstract: "Full abstract"}}
	s, err := NewService([]Connector{f})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(context.Background(), SearchCommand{Query: "query", EnrichMetadata: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 || got.Works[0].Title != "Original title" || got.Works[0].Abstract != "Full abstract" || got.Works[0].MetadataFetchStatus != "enriched" {
		t.Fatal(got)
	}
	f.fetched.SourceRecordID = "other"
	got, err = s.Search(context.Background(), SearchCommand{Query: "query", EnrichMetadata: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Works[0].Abstract != "" || got.Works[0].MetadataFetchStatus != "identity_mismatch" {
		t.Fatal(got)
	}
	f.fetchError = &SourceError{Code: FailureRateLimited, Message: "429", Retryable: true}
	got, err = s.Search(context.Background(), SearchCommand{Query: "query", EnrichMetadata: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Partial || got.Works[0].MetadataFetchStatus != "failed" || len(got.Works) != 1 {
		t.Fatal(got)
	}
}

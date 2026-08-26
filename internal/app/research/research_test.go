package research

import (
	"context"
	"errors"
	"testing"
)

type fixtureConnector struct {
	source Source
	works  []Work
	err    error
}

func (c fixtureConnector) Source() Source { return c.source }
func (c fixtureConnector) Search(context.Context, SearchOptions) ([]Work, error) {
	return c.works, c.err
}
func (c fixtureConnector) Fetch(context.Context, string) (Work, error) {
	if c.err != nil {
		return Work{}, c.err
	}
	return c.works[0], nil
}

func testSource(id string) Source {
	return Source{ID: id, Name: id, Domain: "literature", Description: "fixture source", Homepage: "https://" + id + ".example", Host: id + ".example", KeyFree: true}
}

func TestServicePreservesPartialFailuresAndNormalizesRecords(t *testing.T) {
	service, err := NewService([]Connector{
		fixtureConnector{source: testSource("alpha"), works: []Work{{SourceRecordID: "A", Title: "  A   title ", Identifiers: Identifiers{DOI: "https://doi.org/10.1/ABC"}}}},
		fixtureConnector{source: testSource("beta"), err: &SourceError{SourceID: "beta", Code: FailureRateLimited, Message: "HTTP 429", Retryable: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), SearchCommand{Query: "test", SourceIDs: []string{"alpha", "beta"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial || len(result.Works) != 1 || result.Works[0].Title != "A title" || result.Works[0].Identifiers.DOI != "10.1/abc" {
		t.Fatalf("result = %#v", result)
	}
	if result.Sources[1].Status != SearchFailed || result.Sources[1].ErrorCode != FailureRateLimited || !result.Sources[1].Retryable {
		t.Fatalf("source status = %#v", result.Sources[1])
	}
}

func TestServiceRejectsUnknownSourceAndCancellationIsTerminal(t *testing.T) {
	service, err := NewService([]Connector{fixtureConnector{source: testSource("alpha"), works: []Work{{SourceRecordID: "A", Title: "A"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(context.Background(), SearchCommand{Query: "test", SourceIDs: []string{"missing"}}); err == nil {
		t.Fatal("unknown source was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Search(ctx, SearchCommand{Query: "test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestIdentifierNormalizationIsConservative(t *testing.T) {
	if got := NormalizeDOI(" DOI:10.1000/ABC "); got != "10.1000/abc" {
		t.Fatalf("DOI = %q", got)
	}
	if got := NormalizeDOI("not a doi"); got != "" {
		t.Fatalf("invalid DOI = %q", got)
	}
	if got := NormalizeArXiv("https://arxiv.org/abs/2401.01234v3"); got != "2401.01234" {
		t.Fatalf("arXiv = %q", got)
	}
}

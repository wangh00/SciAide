package connectors

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"

	research "github.com/wangh00/SciAide/internal/app/research"
)

func TestSourceSearchErrorEnvelopeIsFailureNotEmpty(t *testing.T) {
	cases := []struct {
		id, nullResults, empty string
		connector              func(*Client, string) research.Connector
	}{
		{"crossref", `{"message":{"items":null}}`, `{"message":{"items":[]}}`, func(c *Client, base string) research.Connector { return newCrossref(c, base) }},
		{"semantic-scholar", `{"data":null}`, `{"data":[]}`, func(c *Client, base string) research.Connector { return newSemanticScholar(c, base, 0) }},
		{"openalex", `{"results":null}`, `{"results":[]}`, func(c *Client, base string) research.Connector { return newOpenAlex(c, base) }},
		{"europepmc", `{"resultList":{"result":null}}`, `{"resultList":{"result":[]}}`, func(c *Client, base string) research.Connector { return newEuropePMC(c, base) }},
		{"pubmed", `{"esearchresult":{"idlist":null}}`, `{"esearchresult":{"idlist":[]}}`, func(c *Client, base string) research.Connector { return newPubMed(c, base, 0) }},
		{"eric", `{"response":{"numFound":0,"docs":null}}`, `{"response":{"numFound":0,"docs":[]}}`, func(c *Client, base string) research.Connector { return newERIC(c, base, 0) }},
		{"core", `{"totalHits":0,"results":null}`, `{"totalHits":0,"results":[]}`, func(c *Client, base string) research.Connector { return newCORE(c, base, 0) }},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			for name, broken := range map[string]string{
				"error-object": `{"error":"controlled source failure"}`,
				"empty-object": `{}`,
				"null-root":    `null`,
				"null-results": tc.nullResults,
			} {
				t.Run(name, func(t *testing.T) {
					var calls atomic.Int32
					server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						if calls.Add(1) == 1 {
							fmt.Fprint(w, broken)
							return
						}
						fmt.Fprint(w, tc.empty)
					})
					defer server.Close()
					service, err := research.NewService([]research.Connector{tc.connector(client, server.URL)})
					if err != nil {
						t.Fatal(err)
					}
					command := research.SearchCommand{Query: "peer feedback writing", SourceIDs: []string{tc.id}, Limit: 5}
					first, err := service.Search(context.Background(), command)
					if err != nil || !first.Partial || len(first.Works) != 0 || first.Sources[0].Status != research.SearchFailed || first.Sources[0].ErrorCode != research.FailureInvalidData || first.Sources[0].Retryable || calls.Load() != 1 {
						t.Fatalf("invalid success envelope must fail, not become empty or retry: result=%+v HTTP=%d err=%v", first, calls.Load(), err)
					}
					second, err := service.Search(context.Background(), command)
					if err != nil || second.Partial || len(second.Works) != 0 || second.Sources[0].Status != research.SearchEmpty || calls.Load() != 2 {
						t.Fatalf("explicit retry did not reach valid empty response: result=%+v HTTP=%d err=%v", second, calls.Load(), err)
					}
					third, err := service.Search(context.Background(), command)
					if err != nil || third.Partial || third.Sources[0].Status != research.SearchEmpty || calls.Load() != 2 {
						t.Fatalf("valid empty response was not cached: result=%+v HTTP=%d err=%v", third, calls.Load(), err)
					}
				})
			}
		})
	}
}

func TestEuropePMCErrorEnvelopeCannotEndCursorTraversal(t *testing.T) {
	var calls atomic.Int32
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"error":"controlled cursor page failure"}`)
	})
	defer server.Close()
	service, err := research.NewService([]research.Connector{newEuropePMC(client, server.URL)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(context.Background(), research.SearchCommand{Query: "writing", SourceIDs: []string{"europepmc"}, Limit: 5, Offset: 5})
	if err != nil || !result.Partial || result.Sources[0].Status != research.SearchFailed || result.Sources[0].ErrorCode != research.FailureInvalidData || calls.Load() != 1 {
		t.Fatal("missing intermediate page became a valid end of results", result, calls.Load(), err)
	}
}

func TestEuropePMCFetchErrorEnvelopeIsNotNotFound(t *testing.T) {
	var calls atomic.Int32
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"error":"controlled metadata failure"}`)
			return
		}
		fmt.Fprint(w, `{"resultList":{"result":[]}}`)
	})
	defer server.Close()
	connector := newEuropePMC(client, server.URL)
	for index, want := range []research.FailureCode{research.FailureInvalidData, research.FailureNotFound, research.FailureNotFound} {
		_, err := connector.Fetch(context.Background(), "MED/1")
		var sourceErr *research.SourceError
		if !errors.As(err, &sourceErr) || sourceErr.Code != want || sourceErr.Retryable {
			t.Fatalf("call %d: invalid response and real absence conflated: %v", index+1, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("error envelope was cached, valid empty response was not cached or an automatic retry occurred", calls.Load())
	}
}

func TestCachedSourceEnvelopeIsRevalidatedWithoutAutomaticRequest(t *testing.T) {
	var calls atomic.Int32
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"message":{"items":[]}}`)
	})
	defer server.Close()
	target := server.URL + "?" + (url.Values{"query.bibliographic": {"writing"}, "rows": {"5"}, "sort": {"relevance"}}).Encode()
	client.storeCache(target, http.StatusOK, http.Header{}, []byte(`{"error":"controlled cached source failure"}`))
	connector := newCrossref(client, server.URL)
	options := research.SearchOptions{Query: "writing", Limit: 5}
	_, err := connector.Search(context.Background(), options)
	var sourceErr *research.SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureInvalidData || calls.Load() != 0 {
		t.Fatal("bad cached envelope was accepted or retried automatically", err, calls.Load())
	}
	if _, exists := client.cached(target); exists {
		t.Fatal("rejected source envelope remained cached")
	}
	for i := 0; i < 2; i++ {
		if works, err := connector.Search(context.Background(), options); err != nil || len(works) != 0 || calls.Load() != 1 {
			t.Fatal("explicit retry or valid empty cache failed", works, err, calls.Load())
		}
	}
}

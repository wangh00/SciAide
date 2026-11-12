package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	research "github.com/wangh00/SciAide/internal/app/research"
)

func TestFetchIdentityCheckedBeforeCache(t *testing.T) {
	cases := []struct {
		source, requested, canonical, other string
		connector                           func(*Client, string) research.Connector
	}{
		{"crossref", "https://doi.org/10.5555/TARGET", "10.5555/target", "10.5555/other", func(c *Client, b string) research.Connector { return newCrossref(c, b) }},
		{"semantic-scholar", "TargetPaper", "TargetPaper", "OtherPaper", func(c *Client, b string) research.Connector { return newSemanticScholar(c, b, 0) }},
		{"openalex", "https://openalex.org/W123", "W123", "W999", func(c *Client, b string) research.Connector { return newOpenAlex(c, b) }},
		{"europepmc", "MED/123", "MED/123", "MED/999", func(c *Client, b string) research.Connector { return newEuropePMC(c, b) }},
		{"eric", "ej1234567", "EJ1234567", "EJ9999999", func(c *Client, b string) research.Connector { return newERIC(c, b, 0) }},
		{"core", "000123", "123", "999", func(c *Client, b string) research.Connector { return newCORE(c, b, 0) }},
		{"pubmed", "000123", "123", "999", func(c *Client, b string) research.Connector { return newPubMed(c, b, 0) }},
		{"arxiv", "https://arxiv.org/abs/2401.01234v2", "2401.01234", "2401.99999", func(c *Client, b string) research.Connector { return newArXiv(c, b, 0) }},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			t.Run("http-not-found", func(t *testing.T) {
				var calls atomic.Int32
				server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					http.Error(w, "not found", http.StatusNotFound)
				})
				defer server.Close()
				client.attempts = 3
				connector := tc.connector(client, server.URL+"/records")
				for attempt := int32(1); attempt <= 2; attempt++ {
					_, err := connector.Fetch(context.Background(), tc.requested)
					var sourceErr *research.SourceError
					if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureNotFound || sourceErr.Retryable || calls.Load() != attempt {
						t.Fatal("HTTP not_found classification or explicit retry changed", err, calls.Load())
					}
				}
			})
			if empty, exists := map[string]string{
				"eric":      `{"response":{"numFound":0,"docs":[]}}`,
				"europepmc": `{"resultList":{"result":[]}}`,
				"pubmed":    `<PubmedArticleSet></PubmedArticleSet>`,
				"arxiv":     `<feed xmlns="http://www.w3.org/2005/Atom"></feed>`,
			}[tc.source]; exists {
				t.Run("empty-exact-lookup", func(t *testing.T) {
					var calls atomic.Int32
					server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						fmt.Fprint(w, empty)
					})
					defer server.Close()
					connector := tc.connector(client, server.URL+"/records")
					for attempt := 0; attempt < 2; attempt++ {
						_, err := connector.Fetch(context.Background(), tc.requested)
						var sourceErr *research.SourceError
						if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureNotFound || sourceErr.Retryable || calls.Load() != 1 {
							t.Fatal("valid empty exact lookup changed classification or negative caching", err, calls.Load())
						}
					}
				})
			}
			bad := map[string]string{
				"different-record": fetchIdentityBody(t, tc.source, tc.other, "Original title"),
				"missing-identity": fetchIdentityBody(t, tc.source, "", "Original title"),
				"missing-title":    fetchIdentityBody(t, tc.source, tc.canonical, " \t"),
				"malformed":        `{`,
				"cached-mismatch":  fetchIdentityBody(t, tc.source, tc.other, "Original title"),
			}
			if tc.source == "europepmc" {
				var page map[string]any
				if err := json.Unmarshal([]byte(fetchIdentityBody(t, tc.source, tc.canonical, "Original title")), &page); err != nil {
					t.Fatal(err)
				}
				list := page["resultList"].(map[string]any)
				list["result"] = append([]any{map[string]any{"id": "", "title": "Unrelated invalid row"}}, list["result"].([]any)...)
				encoded, err := json.Marshal(page)
				if err != nil {
					t.Fatal(err)
				}
				bad["multiple-raw-records"] = string(encoded)
			}
			if tc.source == "arxiv" || tc.source == "pubmed" {
				valid := fetchIdentityBody(t, tc.source, tc.canonical, "Original title")
				bad["second-root"] = valid + fetchIdentityBody(t, tc.source, tc.other, "Other title")
				bad["trailing-non-xml"] = valid + "\nnot XML"
				bad["leading-non-xml"] = "not XML\n" + valid
				bad["non-xml-whitespace"] = valid + "\u00a0"
				bad["trailing-bom"] = valid + "\ufeff"
				bad["double-bom"] = "\ufeff\ufeff" + valid
				for name, document := range map[string]string{
					"legal-xml-tail":          valid + "\n<!-- audit --><?fixture audit?>\n",
					"leading-utf8-bom":        "\ufeff" + valid,
					"leading-bom-declaration": "\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>" + valid,
				} {
					t.Run(name, func(t *testing.T) {
						var calls atomic.Int32
						server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
							calls.Add(1)
							fmt.Fprint(w, document)
						})
						defer server.Close()
						connector := tc.connector(client, server.URL+"/records")
						for attempt := 0; attempt < 2; attempt++ {
							got, err := connector.Fetch(context.Background(), tc.requested)
							if err != nil || got.SourceRecordID != tc.canonical || calls.Load() != 1 {
								t.Fatal("legal trailing XML/cached value rejected", got, err, calls.Load())
							}
						}
					})
				}
			}
			if tc.source != "arxiv" && tc.source != "pubmed" {
				var value map[string]any
				if err := json.Unmarshal([]byte(bad["different-record"]), &value); err != nil {
					t.Fatal(err)
				}
				value["fetchRecordID"], value["FetchRecordID"] = tc.other, tc.other
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				bad["source-cannot-override-request"] = string(encoded)
			}
			for name, body := range bad {
				t.Run(name, func(t *testing.T) {
					var calls atomic.Int32
					targets := make(chan string, 4)
					server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
						targets <- "http://" + r.Host + r.URL.RequestURI()
						if calls.Add(1) == 1 {
							fmt.Fprint(w, body)
							return
						}
						fmt.Fprint(w, fetchIdentityBody(t, tc.source, tc.canonical, "Original title"))
					})
					defer server.Close()
					client.attempts = 3 // Invalid identity must not use transport retries.
					// A nonempty base path keeps the original cache key identical to
					// the on-wire RequestURI (HTTP inserts '/' for an empty path).
					service, err := research.NewService([]research.Connector{tc.connector(client, server.URL+"/records")})
					if err != nil {
						t.Fatal(err)
					}
					command := research.FetchCommand{SourceID: tc.source, RecordID: tc.requested}
					got, err := service.Fetch(context.Background(), command)
					var sourceErr *research.SourceError
					if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureInvalidData || sourceErr.Retryable || got.SourceRecordID != "" || calls.Load() != 1 {
						t.Fatal("wrong/invalid fetch accepted or retried", got, err, calls.Load())
					}
					key := <-targets
					if _, exists := client.cached(key); exists {
						t.Fatal("rejected fetch entered response cache")
					}
					if name == "cached-mismatch" {
						// Seed a legacy cache entry at the original request URL; reading
						// it must fail/evict without silently dispatching another HTTP.
						client.storeCache(key, http.StatusOK, http.Header{}, []byte(body))
						got, err = service.Fetch(context.Background(), command)
						if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureInvalidData || calls.Load() != 1 || got.SourceRecordID != "" {
							t.Fatal("cached mismatch accepted or automatically refetched", got, err, calls.Load())
						}
						if _, exists := client.cached(key); exists {
							t.Fatal("legacy wrong-record cache not evicted")
						}
					}
					wantCalls := int32(2)
					for attempt := 0; attempt < 2; attempt++ {
						got, err := service.Fetch(context.Background(), command)
						if err != nil || got.SourceRecordID != tc.canonical || got.Title != "Original title" || got.Abstract != "Original abstract." || got.Year != 2025 || calls.Load() != wantCalls {
							t.Fatal("explicit recovery/cache or valid fields changed", got, err, calls.Load())
						}
					}
				})
			}
		})
	}
}

func fetchIdentityBody(t *testing.T, source, id, title string) string {
	t.Helper()
	var value any
	switch source {
	case "crossref":
		value = map[string]any{"message": map[string]any{"DOI": id, "title": []string{title}, "abstract": "Original abstract.", "issued": map[string]any{"date-parts": [][]int{{2025}}}}}
	case "semantic-scholar":
		value = map[string]any{"paperId": id, "title": title, "abstract": "Original abstract.", "year": 2025}
	case "openalex":
		if id != "" {
			id = "https://openalex.org/" + id
		}
		value = map[string]any{"id": id, "display_name": title, "publication_year": 2025, "abstract_inverted_index": map[string][]int{"Original": {0}, "abstract.": {1}}}
	case "europepmc":
		namespace, record, _ := strings.Cut(id, "/")
		value = map[string]any{"resultList": map[string]any{"result": []any{map[string]any{"id": record, "source": namespace, "title": title, "abstractText": "Original abstract.", "pubYear": "2025"}}}}
	case "eric":
		value = map[string]any{"response": map[string]any{"numFound": 1, "docs": []any{map[string]any{"id": id, "title": title, "description": "Original abstract.", "publicationdateyear": 2025}}}}
	case "core":
		number, _ := strconv.Atoi(id)
		value = map[string]any{"id": number, "title": title, "abstract": "Original abstract.", "yearPublished": 2025}
	case "pubmed":
		return fmt.Sprintf(`<PubmedArticleSet><PubmedArticle><MedlineCitation><PMID>%s</PMID><Article><ArticleTitle>%s</ArticleTitle><Abstract><AbstractText>Original abstract.</AbstractText></Abstract><Journal><JournalIssue><PubDate><Year>2025</Year></PubDate></JournalIssue></Journal></Article></MedlineCitation></PubmedArticle></PubmedArticleSet>`, id, title)
	case "arxiv":
		if id != "" {
			id = "https://arxiv.org/abs/" + id + "v3"
		}
		return fmt.Sprintf(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>%s</id><title>%s</title><summary>Original abstract.</summary><published>2025-01-01</published></entry></feed>`, id, title)
	default:
		t.Fatal("unexpected source", source)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

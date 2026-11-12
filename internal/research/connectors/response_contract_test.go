package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	research "github.com/wangh00/SciAide/internal/app/research"
)

// Transport-only fixtures explicitly validate JSON syntax, not a provider's
// business schema. Production destinations have their own required validator.
type syntaxFixtureResponse struct {
	OK     bool `json:"ok"`
	Public bool `json:"public"`
}

func (*syntaxFixtureResponse) validateSourceResponse() error { return nil }

func TestCacheRequiresExplicitResponseValidator(t *testing.T) {
	var calls atomic.Int32
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"ok":true}`) })
	defer server.Close()
	options := requestOptions{SourceID: "fixture", Host: endpointHost(server.URL), Cache: true}
	for _, legacy := range []bool{false, true} {
		if legacy {
			client.storeCache(server.URL, http.StatusOK, http.Header{}, []byte(`{"ok":true}`))
		}
		_, _, err := client.get(context.Background(), server.URL, options)
		var failure *research.SourceError
		if !errors.As(err, &failure) || failure.Code != research.FailureInvalidData || failure.Retryable || calls.Load() != 0 {
			t.Fatal("unvalidated request reached cache/network", err, calls.Load())
		}
	}
}

func TestUnifiedResponseAdmissionContract(t *testing.T) {
	type check struct {
		name, source, broken, breakOn string
		limit, offset, first, total   int
		good                          func(*http.Request) string
		connector                     func(*Client, string) research.Connector
		ids                           []string
	}
	one := func(body string) func(*http.Request) string { return func(*http.Request) string { return body } }
	pub := func(c *Client, b string) research.Connector { return newPubMed(c, b, 0) }
	arxiv := func(c *Client, b string) research.Connector { return newArXiv(c, b, 0) }
	epmc := func(c *Client, b string) research.Connector { return newEuropePMC(c, b) }
	var cases []check
	for name, ids := range map[string]string{"empty-id": `[""]`, "non-digit": `["abc"]`, "null-id": `[null]`, "duplicate": `["1","1"]`, "canonical-duplicate": `["0001","1"]`} {
		cases = append(cases, check{name: "pubmed-ids/" + name, source: "pubmed", broken: `{"esearchresult":{"idlist":` + ids + `}}`, breakOn: "esearch", first: 1, total: 3, connector: pub, ids: []string{"1"}, good: func(r *http.Request) string {
			if strings.Contains(r.URL.Path, "esearch") {
				return `{"esearchresult":{"idlist":["1"]}}`
			}
			return contractPubMedBatch(t, "1")
		}})
	}
	full := contractPubMedBatch(t, "2", "1")
	for name, bad := range map[string]string{
		"malformed": `<PubmedArticleSet>`, "wrong-root": `<error/>`, "missing": contractPubMedBatch(t, "1"),
		"extra": contractPubMedBatch(t, "1", "2", "3"), "duplicate": contractPubMedBatch(t, "1", "1", "2"),
		"empty": `<PubmedArticleSet/>`, "second-root": full + `<error/>`, "trailing-text": full + `bad`,
		"missing-title": strings.ReplaceAll(full, "Original title", " "),
	} {
		cases = append(cases, check{name: "pubmed-batch/" + name, source: "pubmed", broken: bad, breakOn: "efetch", first: 2, total: 3, connector: pub, ids: []string{"1", "2"}, good: func(r *http.Request) string {
			if strings.Contains(r.URL.Path, "esearch") {
				return `{"esearchresult":{"idlist":["1","2"]}}`
			}
			return full
		}})
	}
	for name, body := range map[string]string{
		"missing-id":     fetchIdentityBody(t, "arxiv", "", "Original title"),
		"missing-title":  fetchIdentityBody(t, "arxiv", "2401.01234", " "),
		"unfetchable-id": fetchIdentityBody(t, "arxiv", "2401.01234?other", "Original title"),
		"long-id":        fetchIdentityBody(t, "arxiv", strings.Repeat("x", research.MaxRecordIDRunes+1), "Original title"),
	} {
		cases = append(cases, check{name: "arxiv/" + name, source: "arxiv", broken: body, first: 1, total: 2, connector: arxiv, ids: []string{"2401.01234"}, good: one(fetchIdentityBody(t, "arxiv", "2401.01234", "Original title"))})
	}
	for name, next := range map[string]string{"missing": "", "not-advancing": "*"} {
		page := recordPageJSON(t, "europepmc", []any{map[string]any{"id": "1", "source": "MED", "title": "Original title"}})
		var value map[string]any
		if err := json.Unmarshal([]byte(page), &value); err != nil {
			t.Fatal(err)
		}
		value["nextCursorMark"] = next
		bad, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, check{name: "cursor/" + name, source: "europepmc", broken: string(bad), limit: 5, offset: 5, first: 1, total: 3, connector: epmc, ids: []string{"MED/2"}, good: func(r *http.Request) string {
			if r.URL.Query().Get("cursorMark") == "*" {
				return `{"nextCursorMark":"next","resultList":{"result":[{"id":"1","source":"MED","title":"Original title"}]}}`
			}
			return `{"resultList":{"result":[{"id":"2","source":"MED","title":"Original title"}]}}`
		}})
	}
	for _, source := range []string{"crossref", "semantic-scholar", "openalex", "europepmc"} {
		id, field := "KeepPaper", "paperId"
		factory := func(c *Client, b string) research.Connector { return newSemanticScholar(c, b, 0) }
		switch source {
		case "crossref":
			id, field, factory = "10.5555/target", "DOI", func(c *Client, b string) research.Connector { return newCrossref(c, b) }
		case "openalex":
			id, field, factory = "W123", "id", func(c *Client, b string) research.Connector { return newOpenAlex(c, b) }
		case "europepmc":
			id, field, factory = "123", "id", epmc
		}
		record := map[string]any{field: id, "title": "Original title"}
		if source == "crossref" {
			record["title"] = []string{"Original title"}
		}
		if source == "europepmc" {
			record["source"] = "MED"
		}
		good := recordPageJSON(t, source, []any{record})
		badRecord := cloneRecordFields(record)
		badRecord[field] = strings.Repeat("x", research.MaxRecordIDRunes+1)
		if source == "crossref" {
			badRecord[field] = "10.5555/" + badRecord[field].(string)
		}
		if source == "openalex" {
			badRecord[field] = "W" + badRecord[field].(string)
		}
		expected := id
		if source == "europepmc" {
			expected = "MED/" + id
		}
		cases = append(cases, check{name: source + "/long-id", source: source, broken: recordPageJSON(t, source, []any{badRecord}), first: 1, total: 2, connector: factory, good: one(good), ids: []string{expected}})
		if source == "crossref" || source == "openalex" {
			var object map[string]any
			if err := json.Unmarshal([]byte(good), &object); err != nil {
				t.Fatal(err)
			}
			if source == "crossref" {
				object["status"] = "error"
			} else {
				object["error"] = "declared failure"
			}
			encoded, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			cases = append(cases, check{name: source + "/declared-failure", source: source, broken: string(encoded), first: 1, total: 2, connector: factory, good: one(good), ids: []string{expected}})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls, brokenCalls atomic.Int32
			badTargets := make(chan string, 1)
			server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if strings.Contains(r.URL.Path, tc.breakOn) && brokenCalls.Add(1) == 1 {
					badTargets <- "http://" + r.Host + r.URL.RequestURI()
					fmt.Fprint(w, tc.broken)
					return
				}
				fmt.Fprint(w, tc.good(r))
			})
			defer server.Close()
			client.attempts = 3
			service, err := research.NewService([]research.Connector{tc.connector(client, server.URL+"/records")})
			if err != nil {
				t.Fatal(err)
			}
			limit := tc.limit
			if limit == 0 {
				limit = 5
			}
			command := research.SearchCommand{Query: "writing feedback", SourceIDs: []string{tc.source}, Limit: limit, Offset: tc.offset}
			fail := func() {
				t.Helper()
				got, err := service.Search(context.Background(), command)
				if err != nil || !got.Partial || len(got.Works) != 0 || got.Sources[0].ErrorCode != research.FailureInvalidData || got.Sources[0].Retryable || calls.Load() != int32(tc.first) {
					t.Fatal("contract violation accepted, hidden or retried", got, err, calls.Load())
				}
			}
			fail()
			key := <-badTargets
			if _, exists := client.cached(key); exists {
				t.Fatal("failed semantic response admitted to cache")
			}
			client.storeCache(key, http.StatusOK, http.Header{}, []byte(tc.broken))
			fail()
			if _, exists := client.cached(key); exists {
				t.Fatal("legacy invalid cache not evicted")
			}
			for attempt := 0; attempt < 2; attempt++ {
				got, err := service.Search(context.Background(), command)
				if err != nil || got.Partial || len(got.Works) != len(tc.ids) || calls.Load() != int32(tc.total) {
					t.Fatal("explicit recovery/cache failed", got, err, calls.Load())
				}
				for i, id := range tc.ids {
					if got.Works[i].SourceRecordID != id || got.Works[i].Title != "Original title" {
						t.Fatal("record identity/order/title changed", got.Works)
					}
				}
			}
		})
	}
}

func contractPubMedBatch(t *testing.T, ids ...string) string {
	t.Helper()
	var articles strings.Builder
	for _, id := range ids {
		body := fetchIdentityBody(t, "pubmed", id, "Original title")
		articles.WriteString(strings.TrimSuffix(strings.TrimPrefix(body, "<PubmedArticleSet>"), "</PubmedArticleSet>"))
	}
	return "<PubmedArticleSet>" + articles.String() + "</PubmedArticleSet>"
}

func TestResponseContractCompatibility(t *testing.T) {
	t.Run("pubmed-canonical-batch-order", func(t *testing.T) {
		var calls atomic.Int32
		server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if strings.Contains(r.URL.Path, "esearch") {
				fmt.Fprint(w, `{"esearchresult":{"idlist":["0001","2"]}}`)
				return
			}
			fmt.Fprint(w, contractPubMedBatch(t, "2", "1"))
		})
		defer server.Close()
		s, err := research.NewService([]research.Connector{newPubMed(client, server.URL+"/records", 0)})
		if err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			got, err := s.Search(context.Background(), research.SearchCommand{Query: "writing", SourceIDs: []string{"pubmed"}, Limit: 5})
			if err != nil || got.Partial || len(got.Works) != 2 || got.Works[0].SourceRecordID != "1" || got.Works[1].SourceRecordID != "2" || calls.Load() != 2 {
				t.Fatal("canonical batch/order/cache changed", got, err, calls.Load())
			}
		}
	})
	t.Run("arxiv-long-identity-is-lossless", func(t *testing.T) {
		for _, length := range []int{101, research.MaxRecordIDRunes} {
			t.Run(fmt.Sprint(length), func(t *testing.T) {
				id := strings.Repeat("x", length)
				var calls atomic.Int32
				server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					fmt.Fprint(w, fetchIdentityBody(t, "arxiv", id, "Original title"))
				})
				defer server.Close()
				s, err := research.NewService([]research.Connector{newArXiv(client, server.URL+"/records", 0)})
				if err != nil {
					t.Fatal(err)
				}
				for attempt := 0; attempt < 2; attempt++ {
					got, err := s.Search(context.Background(), research.SearchCommand{Query: "writing", SourceIDs: []string{"arxiv"}, Limit: 5})
					if err != nil || got.Partial || len(got.Works) != 1 || got.Works[0].SourceRecordID != id || got.Works[0].Identifiers.ArXiv != id || calls.Load() != 1 {
						t.Fatal("source identity truncated/rejected", got, err, calls.Load())
					}
				}
				got, err := s.Fetch(context.Background(), research.FetchCommand{SourceID: "arxiv", RecordID: id})
				if err != nil || got.SourceRecordID != id || got.Identifiers.ArXiv != id || calls.Load() != 2 {
					t.Fatal("lossless ID cannot be fetched", got, err, calls.Load())
				}
			})
		}
	})
	t.Run("arxiv-window-and-mixed-page", func(t *testing.T) {
		var calls atomic.Int32
		bad := strings.TrimSuffix(fetchIdentityBody(t, "arxiv", "2401.99999", " "), "</feed>") + strings.TrimPrefix(fetchIdentityBody(t, "arxiv", "2401.01234", "Original title"), `<feed xmlns="http://www.w3.org/2005/Atom">`)
		good := strings.TrimSuffix(fetchIdentityBody(t, "arxiv", "", "Invalid row"), "</feed>") + strings.TrimPrefix(fetchIdentityBody(t, "arxiv", "2401.01234", "Original title"), `<feed xmlns="http://www.w3.org/2005/Atom">`)
		server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				fmt.Fprint(w, bad)
			} else {
				fmt.Fprint(w, good)
			}
		})
		defer server.Close()
		s, err := research.NewService([]research.Connector{newArXiv(client, server.URL+"/records", 0)})
		if err != nil {
			t.Fatal(err)
		}
		command := research.SearchCommand{Query: "writing", SourceIDs: []string{"arxiv"}, Limit: 1}
		first, err := s.Search(context.Background(), command)
		if err != nil || !first.Partial || len(first.Works) != 0 || calls.Load() != 1 {
			t.Fatal("out-of-window item admitted bad page", first, err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			got, err := s.Search(context.Background(), command)
			if err != nil || got.Partial || len(got.Works) != 1 || got.Works[0].SourceRecordID != "2401.01234" || calls.Load() != 2 {
				t.Fatal("invalid raw row consumed mixed-page window", got, err, calls.Load())
			}
		}
	})
	t.Run("cursor-validation-is-request-specific", func(t *testing.T) {
		var calls atomic.Int32
		server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
			n := calls.Add(1)
			if n == 1 {
				fmt.Fprint(w, `{"resultList":{"result":[{"id":"1","source":"MED","title":"Original title"}]}}`)
				return
			}
			if r.URL.Query().Get("cursorMark") == "*" {
				fmt.Fprint(w, `{"nextCursorMark":"next","resultList":{"result":[{"id":"1","source":"MED","title":"Original title"}]}}`)
				return
			}
			fmt.Fprint(w, `{"resultList":{"result":[{"id":"2","source":"MED","title":"Original title"}]}}`)
		})
		defer server.Close()
		s, err := research.NewService([]research.Connector{newEuropePMC(client, server.URL+"/records")})
		if err != nil {
			t.Fatal(err)
		}
		cmd := research.SearchCommand{Query: "writing", SourceIDs: []string{"europepmc"}, Limit: 5}
		first, err := s.Search(context.Background(), cmd)
		if err != nil || first.Partial || len(first.Works) != 1 {
			t.Fatal("single-page response required an unused cursor", first, err)
		}
		cmd.Offset = 5
		second, err := s.Search(context.Background(), cmd)
		if err != nil || !second.Partial || len(second.Works) != 0 || calls.Load() != 1 {
			t.Fatal("cached page bypassed new request cursor requirement", second, err, calls.Load())
		}
		for attempt := 0; attempt < 2; attempt++ {
			got, err := s.Search(context.Background(), cmd)
			if err != nil || got.Partial || len(got.Works) != 1 || got.Works[0].SourceRecordID != "MED/2" || calls.Load() != 3 {
				t.Fatal("explicit pagination recovery/cache failed", got, err, calls.Load())
			}
		}
	})
}

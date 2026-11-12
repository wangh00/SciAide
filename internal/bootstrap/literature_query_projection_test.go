package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/research/connectors"
)

// Real provider adapters over local HTTP, then the production facade and SQLite.
// No model, public API, task recovery or scientific gate approval is involved.
func TestSourceQueryProjectionHTTPAndSQLiteRestart(t *testing.T) {
	const logical = `("AI feedback" OR Grammarly) AND writing NOT animal-based`
	const crossrefInput = `"ChatGPT feedback"[Title/Abstract] AND writing NOT school-aged`
	const ericInput = `(descriptor:"Feedback (Response)" AND educationlevel:"Higher Education")`
	const arxivInput = `ti:(writing OR feedback) ANDNOT cat:physics.ed-ph`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var field, want, body string
		switch r.Header.Get("X-Fixture-Source") {
		case "api.crossref.org":
			field, want = "query.bibliographic", "ChatGPT feedback writing"
			body = `{"message":{"items":[{"DOI":"10.5555/query-fixture","title":["Controlled AI feedback study"],"type":"journal-article","published-print":{"date-parts":[[2025]]}}]}}`
		case "api.semanticscholar.org":
			field, want, body = "query", "AI feedback Grammarly writing", `{"data":[]}`
		case "api.ies.ed.gov":
			field, want, body = "search", ericInput, `{"response":{"numFound":0,"docs":[]}}`
		case "export.arxiv.org", "arxiv.org":
			field, want, body = "search_query", `((ti:writing OR ti:feedback) ANDNOT cat:physics.ed-ph)`, `<feed xmlns="http://www.w3.org/2005/Atom"></feed>`
		default:
			t.Error("fixture escaped the selected sources", r.URL, r.Header.Get("X-Fixture-Source"))
			http.Error(w, "unexpected source", 400)
			return
		}
		if got := r.URL.Query().Get(field); got != want {
			t.Errorf("wire query for %s: got %q want %q", field, got, want)
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := &routingFixtureTransport{endpoint: endpoint}
	options := Options{RootDir: t.TempDir(), ResearchConnectors: connectors.DefaultWithTransport(transport)}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if app != nil {
			app.Close()
		}
	}()
	project, err := app.ProjectFacade.CreateProject(struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		WorkspacePath string `json:"workspacePath"`
	}{Name: "Query projection controlled fixture"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.ResearchFacade.Search(research.DiscoverySearchCommand{
		ProjectID: project.ID, Query: logical, Limit: 5,
		SourceIDs:       []string{"crossref", "semantic-scholar", "eric", "arxiv"},
		ProviderQueries: map[string]string{"crossref": crossrefInput, "eric": ericInput, "arxiv": arxivInput},
	})
	if err != nil || transport.calls.Load() != 4 {
		t.Fatal(err, transport.calls.Load())
	}
	if result.Query.Text != logical || result.Query.Partial || result.Page.Total != 1 {
		t.Fatal(result)
	}
	for _, source := range result.Query.Sources {
		if source.QueryProjectionVersion != "source-query-v1" || source.ProviderQuery == "" {
			t.Fatal(source)
		}
		if source.SourceID == "crossref" && (source.InputQuery != crossrefInput || source.QueryMode != "ranked_bibliographic" || len(source.QueryLimitations) != 4) {
			t.Fatal(source)
		}
		if source.SourceID == "eric" && (source.ProviderQuery != ericInput || len(source.QueryLimitations) != 0) {
			t.Fatal("native dialect was rewritten", source)
		}
		if source.SourceID == "arxiv" && (source.ProviderQuery == arxivInput || source.QueryMode != "arxiv_boolean") {
			t.Fatal("compiled arxiv query was hidden", source)
		}
	}
	invalid, err := app.ResearchFacade.Search(research.DiscoverySearchCommand{
		ProjectID: project.ID, Query: "writing", Limit: 5, SourceIDs: []string{"crossref", "semantic-scholar"},
		ProviderQueries: map[string]string{"crossref": "NOT (animals OR children)", "semantic-scholar": "writing AND"},
		Years:           research.PublicationYears{From: 2023, To: 2026},
	})
	if err != nil || transport.calls.Load() != 4 || !invalid.Query.Partial {
		t.Fatal(err, invalid, transport.calls.Load())
	}
	for _, source := range invalid.Query.Sources {
		if source.ErrorCode != research.FailureInvalidQuery || source.Retryable || source.ProviderQuery != "" || source.YearFilterApplied {
			t.Fatal(source)
		}
	}
	before := map[string]string{}
	for _, query := range []research.Query{result.Query, invalid.Query} {
		b, e := json.Marshal(query.Sources)
		if e != nil {
			t.Fatal(e)
		}
		before[query.ID] = string(b)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app = nil
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := app.ResearchFacade.ListQueries(project.ID)
	if err != nil || len(restored) != 2 {
		t.Fatal(err, restored)
	}
	for _, query := range restored {
		b, e := json.Marshal(query.Sources)
		if e != nil || string(b) != before[query.ID] {
			t.Fatal("restart changed frozen source query audit", e, string(b))
		}
	}
	if transport.calls.Load() != 4 {
		t.Fatal("restart retried a query", transport.calls.Load())
	}
	t.Log("4 controlled source HTTP calls, 0 extra calls for invalid input/restart, 0 model calls; source audit unchanged after SQLite restart")
}

func TestArXivInvalidPlannedQueryDoesNotDispatch(t *testing.T) {
	transport := &routingFixtureTransport{}
	service, err := research.NewService(connectors.DefaultWithTransport(transport))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(t.Context(), research.SearchCommand{Query: "writing", SourceIDs: []string{"arxiv"}, ProviderQueries: map[string]string{"arxiv": "ti:(writing"}})
	if err != nil || transport.calls.Load() != 0 || len(result.Sources) != 1 || result.Sources[0].ErrorCode != research.FailureInvalidQuery || strings.TrimSpace(result.Sources[0].ProviderQuery) != "" {
		t.Fatal(err, result, transport.calls.Load())
	}
}

func TestNativeYearQueryHTTPMatchesPersistableAudit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		years    research.PublicationYears
		from, to int
	}{
		{"closed", research.PublicationYears{From: 2023, To: 2026}, 2023, 2026},
		{"from-only", research.PublicationYears{From: 2023}, 2023, 3000},
		{"to-only", research.PublicationYears{To: 2026}, 1000, 2026},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := make(chan struct{ source, query string }, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var source, field, body, want string
				switch r.Header.Get("X-Fixture-Source") {
				case "api.ies.ed.gov":
					source, field, body = "eric", "search", `{"response":{"numFound":0,"docs":[]}}`
					want = fmt.Sprintf("(writing) AND publicationdateyear:[%d TO %d]", tc.from, tc.to)
				case "api.core.ac.uk":
					source, field, body = "core", "q", `{"totalHits":0,"results":[]}`
					want = fmt.Sprintf("(writing) AND yearPublished>=%d AND yearPublished<=%d", tc.from, tc.to)
				case "www.ebi.ac.uk":
					source, field, body = "europepmc", "query", `{"hitCount":0,"resultList":{"result":[]}}`
					want = fmt.Sprintf("(writing) AND FIRST_PDATE:[%d-01-01 TO %d-12-31]", tc.from, tc.to)
				default:
					t.Error("unexpected year-query source", r.Header.Get("X-Fixture-Source"))
					http.Error(w, "unexpected source", 400)
					return
				}
				got := r.URL.Query().Get(field)
				if got != want {
					t.Errorf("%s wire query: got %q want %q", source, got, want)
				}
				wire <- struct{ source, query string }{source, got}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			transport := &routingFixtureTransport{endpoint: endpoint}
			service, err := research.NewService(connectors.DefaultWithTransport(transport))
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Search(t.Context(), research.SearchCommand{Query: "writing", SourceIDs: []string{"eric", "core", "europepmc"}, Years: tc.years})
			if err != nil || result.Partial || transport.calls.Load() != 3 {
				t.Fatal(err, result, transport.calls.Load())
			}
			seen := map[string]string{}
			for range 3 {
				select {
				case call := <-wire:
					seen[call.source] = call.query
				default:
					t.Fatal("missing expected HTTP query")
				}
			}
			for _, source := range result.Sources {
				if source.ProviderQuery != seen[source.SourceID] || source.InputQuery != "writing" || source.EffectiveQuery != "writing" || !source.YearFilterApplied {
					t.Fatal("wire/audit diverged", source, seen)
				}
			}
		})
	}
}

type queryOpaqueTransport struct{ calls int }

func (t *queryOpaqueTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, fmt.Errorf("unexpected opaque transport invocation")
}

func TestPreparedSourceQueryIsNotEvidenceOfHTTPDispatch(t *testing.T) {
	transport := &queryOpaqueTransport{}
	service, err := research.NewService(connectors.DefaultWithTransport(transport))
	if err != nil {
		t.Fatal(err)
	}
	ctx, budget, err := research.WithSourceHTTPBudget(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Search(ctx, research.SearchCommand{Query: "writing", SourceIDs: []string{"eric"}, Years: research.PublicationYears{From: 2023, To: 2026}})
	if err != nil || !result.Partial || transport.calls != 0 {
		t.Fatal(err, result, transport.calls)
	}
	used, denied := budget.Usage()
	if used != 0 || denied != 0 {
		t.Fatal("unexpected HTTP use", used, denied)
	}
	source := result.Sources[0]
	if source.Status != research.SearchFailed || source.ErrorCode != research.FailureInvalidData || source.ProviderQuery != "(writing) AND publicationdateyear:[2023 TO 2026]" || !source.YearFilterApplied {
		t.Fatal("failed to retain compiled-only audit", source)
	}
	t.Log("compiled expression/filter retained with failed status; 0 HTTP. Prepared values are not a dispatch receipt.")
}

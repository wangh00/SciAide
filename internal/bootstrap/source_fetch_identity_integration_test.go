package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/research/connectors"
)

func TestFetchIdentityPreservesSearchAndSQLiteHistory(t *testing.T) {
	var searchCalls, fetchCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Source") != "api.crossref.org" {
			t.Error("unexpected source host", r.Header.Get("X-Fixture-Source"))
			http.Error(w, "unexpected host", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/works":
			searchCalls.Add(1)
			fmt.Fprint(w, `{"message":{"items":[{"DOI":"10.5555/fetch-bound","title":["Original search title"],"issued":{"date-parts":[[2025]]}}]}}`)
		case "/works/10.5555/fetch-bound":
			if fetchCalls.Add(1) == 1 {
				fmt.Fprint(w, `{"message":{"DOI":"10.5555/wrong","title":["Wrong paper"],"abstract":"WRONG record must not be saved"}}`)
				return
			}
			fmt.Fprint(w, `{"message":{"DOI":"10.5555/fetch-bound","title":["Detail title"],"abstract":"Recovered original abstract."}}`)
		default:
			t.Error("unexpected path", r.URL.Path)
			http.NotFound(w, r)
		}
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
	}{Name: "Controlled exact fetch identity"})
	if err != nil {
		t.Fatal(err)
	}
	command := research.DiscoverySearchCommand{ProjectID: project.ID, Query: "peer feedback writing", SourceIDs: []string{"crossref"}, Limit: 5, EnrichMetadata: true}
	first, err := app.ResearchFacade.Search(command)
	if err != nil || first.Page.Total != 1 || first.Query.Partial || searchCalls.Load() != 1 || fetchCalls.Load() != 1 {
		t.Fatal("wrong identity discarded search or retried automatically", first, err)
	}
	persisted, err := app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: project.ID})
	if err != nil || len(persisted.Items) != 1 || len(persisted.Items[0].Records) != 1 {
		t.Fatal(persisted, err)
	}
	original := persisted.Items[0].Records[0].Work
	if original.SourceRecordID != "10.5555/fetch-bound" || original.Title != "Original search title" || original.Abstract != "" || original.MetadataFetchStatus != "identity_mismatch" || strings.Contains(string(original.RawSnapshot), "WRONG") {
		t.Fatal("wrong fetch contaminated persisted search record", original)
	}
	frozen := map[string]string{}
	for attempt := 0; attempt < 3; attempt++ {
		result := first
		if attempt != 0 {
			result, err = app.ResearchFacade.Search(command)
			if err != nil || result.Page.Total != 1 || result.Query.Partial || searchCalls.Load() != 1 || fetchCalls.Load() != 2 {
				t.Fatal("explicit recovery or valid cache failed", result, err, searchCalls.Load(), fetchCalls.Load())
			}
			work := result.Page.Items[0].Records[0].Work
			if work.SourceRecordID != original.SourceRecordID || work.Title != original.Title || work.Abstract != "Recovered original abstract." || work.MetadataFetchStatus != "enriched" || strings.Contains(string(work.RawSnapshot), "WRONG") {
				t.Fatal("recovery changed search identity/title or kept wrong payload", work)
			}
		}
		encoded, err := json.Marshal(result.Query.Sources)
		if err != nil {
			t.Fatal(err)
		}
		if _, duplicate := frozen[result.Query.ID]; duplicate {
			t.Fatal("explicit searches overwrote history")
		}
		frozen[result.Query.ID] = string(encoded)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app = nil
	app, err = New(options) // Same process, same connectors; reopen temporary SQLite.
	if err != nil {
		t.Fatal(err)
	}
	queries, err := app.ResearchFacade.ListQueries(project.ID)
	if err != nil || len(queries) != 3 {
		t.Fatal(queries, err)
	}
	for _, q := range queries {
		encoded, err := json.Marshal(q.Sources)
		if expected, exists := frozen[q.ID]; err != nil || !exists || string(encoded) != expected || q.Partial {
			t.Fatal("reopening changed frozen query/source history", q.ID, err)
		}
	}
	if searchCalls.Load() != 1 || fetchCalls.Load() != 2 || transport.calls.Load() != 3 {
		t.Fatal("reopening dispatched source requests", searchCalls.Load(), fetchCalls.Load(), transport.calls.Load())
	}
	t.Log("1 search + 2 explicit fetch HTTP requests, no wrong record persisted, 3 query histories preserved across same-process SQLite reopen; 0 public/model requests")
}

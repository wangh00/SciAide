package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/research/connectors"
)

func TestERICCountFailureExplicitRetryAndSQLiteHistory(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Source") != "api.ies.ed.gov" || r.URL.Path != "/eric/" || r.URL.Query().Get("search") != "peer feedback writing" || r.URL.Query().Get("start") != "0" {
			t.Error("unexpected source or query", r.URL, r.Header.Get("X-Fixture-Source"))
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"response":{"numFound":1,"docs":[]}}`)
			return
		}
		fmt.Fprint(w, `{"response":{"numFound":1,"docs":[{"id":"EJ1234567","title":"Recovered writing study","description":"Original abstract.","publicationdateyear":2025}]}}`)
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
	}{Name: "Controlled ERIC count cache"})
	if err != nil {
		t.Fatal(err)
	}
	command := research.DiscoverySearchCommand{ProjectID: project.ID, Query: "peer feedback writing", SourceIDs: []string{"eric"}, Limit: 5}
	type snapshot struct {
		sources string
		partial bool
	}
	frozen := map[string]snapshot{}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := app.ResearchFacade.Search(command)
		if err != nil || len(result.Query.Sources) != 1 {
			t.Fatal(result, err)
		}
		if attempt == 0 {
			if !result.Query.Partial || result.Page.Total != 0 || result.Query.Sources[0].Status != research.SearchFailed || result.Query.Sources[0].ErrorCode != research.FailureInvalidData || result.Query.Sources[0].Retryable || calls.Load() != 1 {
				t.Fatal("count failure was hidden or automatically retried", result, calls.Load())
			}
		} else {
			if result.Query.Partial || result.Page.Total != 1 || result.Query.Sources[0].Status != research.SearchOK || calls.Load() != 2 {
				t.Fatal("explicit recovery/cache failed", result, calls.Load())
			}
			work := result.Page.Items[0].Records[0].Work
			if work.SourceRecordID != "EJ1234567" || work.Title != "Recovered writing study" || work.Abstract != "Original abstract." || work.Year != 2025 {
				t.Fatal("recovered source record changed", work)
			}
		}
		encoded, err := json.Marshal(result.Query.Sources)
		if err != nil {
			t.Fatal(err)
		}
		if _, duplicate := frozen[result.Query.ID]; duplicate {
			t.Fatal("explicit search overwrote query history")
		}
		frozen[result.Query.ID] = snapshot{string(encoded), result.Query.Partial}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app = nil
	app, err = New(options) // Same process and connectors, reopening temporary SQLite.
	if err != nil {
		t.Fatal(err)
	}
	queries, err := app.ResearchFacade.ListQueries(project.ID)
	if err != nil || len(queries) != 3 {
		t.Fatal(queries, err)
	}
	for _, query := range queries {
		encoded, err := json.Marshal(query.Sources)
		prior, exists := frozen[query.ID]
		if err != nil || !exists || prior.sources != string(encoded) || prior.partial != query.Partial {
			t.Fatal("reopening changed frozen failure/success history", query.ID, err)
		}
	}
	if calls.Load() != 2 || transport.calls.Load() != 2 {
		t.Fatal("reopening automatically requested source", calls.Load(), transport.calls.Load())
	}
	t.Log("3 explicit searches / 2 local HTTP; failure and success histories preserved on same-process SQLite reopen; 0 public/model/PDF requests")
}

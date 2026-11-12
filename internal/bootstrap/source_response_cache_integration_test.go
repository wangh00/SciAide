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

// The facade, real connector/client and temporary SQLite stay on the production
// path. The HTTP server only controls source bytes; no model or public API runs.
func TestSourceResponseCacheExplicitRetryAndSQLiteRestart(t *testing.T) {
	testSourceResponseCacheExplicitRetryAndSQLiteRestart(t, `{"message":{"items":[`)
}

func TestSourceErrorEnvelopeExplicitRetryAndSQLiteRestart(t *testing.T) {
	testSourceResponseCacheExplicitRetryAndSQLiteRestart(t, `{"error":"controlled source error without a result envelope"}`)
}

func TestSourceUnusableRecordsExplicitRetryAndSQLiteRestart(t *testing.T) {
	testSourceResponseCacheExplicitRetryAndSQLiteRestart(t, `{"message":{"items":[{"DOI":"10.5555/no-title","title":[" "]}]}}`)
}

func testSourceResponseCacheExplicitRetryAndSQLiteRestart(t *testing.T, broken string) {
	t.Helper()
	var serverCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Source") != "api.crossref.org" || r.URL.Path != "/works" || r.URL.Query().Get("query.bibliographic") != "peer feedback writing" {
			t.Error("unexpected source request", r.URL, r.Header.Get("X-Fixture-Source"))
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if serverCalls.Add(1) == 1 {
			fmt.Fprint(w, broken)
			return
		}
		fmt.Fprint(w, `{"message":{"items":[{"DOI":"10.5555/source-cache-recovery","title":["Controlled writing feedback result"],"issued":{"date-parts":[[2025]]}}]}}`)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := &routingFixtureTransport{endpoint: endpoint}
	root := t.TempDir()
	options := Options{RootDir: root, ResearchConnectors: connectors.DefaultWithTransport(transport)}
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
	}{Name: "Controlled source cache recovery"})
	if err != nil {
		t.Fatal(err)
	}
	command := research.DiscoverySearchCommand{ProjectID: project.ID, Query: "peer feedback writing", SourceIDs: []string{"crossref"}, Limit: 5}
	first, err := app.ResearchFacade.Search(command)
	if err != nil || !first.Query.Partial || first.Page.Total != 0 || len(first.Query.Sources) != 1 || first.Query.Sources[0].ErrorCode != research.FailureInvalidData || first.Query.Sources[0].Retryable || serverCalls.Load() != 1 {
		t.Fatal("malformed source was accepted or retried automatically", err, first, serverCalls.Load())
	}
	second, err := app.ResearchFacade.Search(command)
	if err != nil || second.Query.Partial || second.Page.Total != 1 || second.Query.ID == first.Query.ID || serverCalls.Load() != 2 {
		t.Fatal("explicit same-query retry reused rejected bytes or rewrote failure history", err, second, serverCalls.Load())
	}
	third, err := app.ResearchFacade.Search(command)
	if err != nil || third.Query.Partial || third.Page.Total != 1 || serverCalls.Load() != 2 || transport.calls.Load() != 2 {
		t.Fatal("valid response was not reused", err, third, serverCalls.Load(), transport.calls.Load())
	}
	type snapshot struct {
		sources string
		partial bool
	}
	frozen := map[string]snapshot{}
	for _, query := range []research.Query{first.Query, second.Query, third.Query} {
		encoded, err := json.Marshal(query.Sources)
		if err != nil {
			t.Fatal(err)
		}
		frozen[query.ID] = snapshot{string(encoded), query.Partial}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app = nil
	// Reuse the original connectors so reopening cannot mask the cache behavior.
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := app.ResearchFacade.ListQueries(project.ID)
	if err != nil || len(queries) != 3 {
		t.Fatal("restart lost source result history", err, len(queries))
	}
	for _, query := range queries {
		encoded, err := json.Marshal(query.Sources)
		prior, exists := frozen[query.ID]
		if err != nil || !exists || prior.sources != string(encoded) || prior.partial != query.Partial {
			t.Fatal("restart changed frozen source status", err)
		}
	}
	if serverCalls.Load() != 2 || transport.calls.Load() != 2 {
		t.Fatal("restart automatically resumed source requests")
	}
	t.Log("2 local HTTP requests across 3 explicit searches; failed and successful snapshots unchanged after SQLite restart; 0 public API/model requests")
}

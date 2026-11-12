package bootstrap

import (
	"context"
	"encoding/json"
	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/research/connectors"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRecordHintsV1RealAdaptersHTTPScopedImportAndRestart(t *testing.T) {
	ctx := context.Background()
	title := "Automated writing feedback and undergraduate revision quality"
	abstract := strings.Repeat("In this controlled study students revised essays using structured feedback. ", 6)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Fixture-Source") {
		case "api.ies.ed.gov":
			json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"numFound": 1, "docs": []any{map[string]any{"id": "EJ1234567", "title": title, "description": abstract, "author": []string{"Jane Doe"}, "publicationdateyear": 2024, "e_fulltextauth": 0}}}})
		case "api.core.ac.uk":
			json.NewEncoder(w).Encode(map[string]any{"totalHits": 3, "results": []any{
				map[string]any{"id": 1, "title": title, "abstract": abstract, "authors": []string{"Doe, Jane"}, "yearPublished": 2024, "documentType": "research article"},
				map[string]any{"id": 2, "title": `Supplemental Files for "` + title + `"`, "abstract": "Supplementary files for the named article.", "authors": []string{"Doe, Jane"}, "yearPublished": 2024, "documentType": "project"},
				map[string]any{"id": 3, "title": title, "abstract": abstract, "authors": []string{"Different Author"}, "yearPublished": 2024},
			}})
		default:
			t.Error("unexpected source", r.Header.Get("X-Fixture-Source"))
			http.Error(w, "unexpected", 400)
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := &routingFixtureTransport{endpoint: endpoint}
	options := Options{RootDir: t.TempDir(), ResearchConnectors: connectors.DefaultWithTransport(transport)}
	app, e := New(options)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if app != nil {
			app.Close()
		}
	}()
	p, e := app.ProjectFacade.CreateProject(struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		WorkspacePath string `json:"workspacePath"`
	}{Name: "Controlled record hints"})
	if e != nil {
		t.Fatal(e)
	}
	taskID := "controlled-record-hints"
	if e = sqlite.NewResearchTaskRepository(app.store.DB()).Upsert(ctx, researchtask.UpsertCommand{ID: taskID, ProjectID: p.ID, Title: "Controlled task", OriginKind: researchtask.OriginAI, At: time.Now().UTC()}); e != nil {
		t.Fatal(e)
	}
	result, e := app.ResearchFacade.Search(research.DiscoverySearchCommand{ProjectID: p.ID, ResearchTaskID: taskID, Query: "writing feedback", SourceIDs: []string{"eric", "core"}, Limit: 20})
	if e != nil || transport.calls.Load() != 2 {
		t.Fatal(e, transport.calls.Load())
	}
	page, e := app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: p.ID, QueryID: result.Query.ID, ResearchTaskID: taskID, Limit: 100})
	if e != nil || page.Total != 4 {
		t.Fatal("title-only records merged", e, page.Total)
	}
	hashes := map[string]string{}
	var selected research.Candidate
	for _, c := range page.Items {
		key := c.Records[0].Work.SourceID + ":" + c.Records[0].Work.SourceRecordID
		hashes[key] = research.RecordIdentityContentSHA256(c.RecordIdentity)
		switch key {
		case "eric:EJ1234567":
			selected = c
			if c.RecordIdentity.Scopes[0].RelationKinds["possible_same_work"] != 1 || c.RecordIdentity.Scopes[0].RelationKinds["possible_companion"] != 1 {
				t.Fatal(c.RecordIdentity)
			}
		case "core:3":
			if len(c.RecordIdentity.Relations) != 0 {
				t.Fatal("different author falsely related")
			}
		case "core:2":
			if len(c.RecordIdentity.Roles) != 1 || c.RecordIdentity.Roles[0].Kind != "companion_material" {
				t.Fatal("missed supplement")
			}
		}
		if c.ReviewStatus != research.ReviewPending || c.ImportStatus != research.ImportNotImported {
			t.Fatal("metadata auto-approved/imported")
		}
	}
	if _, e = app.ResearchFacade.UpdateReview(research.ReviewCommand{ProjectID: p.ID, ResearchTaskID: taskID, CandidateID: selected.ID, Status: research.ReviewIncluded}); e != nil {
		t.Fatal(e)
	}
	imported, e := app.ResearchFacade.ImportCandidate(research.ImportCandidateCommand{ProjectID: p.ID, ResearchTaskID: taskID, CandidateID: selected.ID, Mode: research.MaterializeMetadata})
	if e != nil || imported.Candidate.ImportKind != research.ImportMetadataAbstract {
		t.Fatal(e, imported)
	}
	if e = app.Close(); e != nil {
		t.Fatal(e)
	}
	app = nil
	app, e = New(options)
	if e != nil {
		t.Fatal(e)
	}
	page, e = app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: p.ID, QueryID: result.Query.ID, ResearchTaskID: taskID, Limit: 100})
	if e != nil || page.Total != 4 {
		t.Fatal(e, page.Total)
	}
	for _, c := range page.Items {
		key := c.Records[0].Work.SourceID + ":" + c.Records[0].Work.SourceRecordID
		if hashes[key] != research.RecordIdentityContentSHA256(c.RecordIdentity) {
			t.Fatal("identity drift", key)
		}
	}
	scoped, e := sqlite.NewResearchRepository(app.store.DB()).CandidateForTask(ctx, p.ID, selected.ID, taskID)
	if e != nil || scoped.RecordIdentity == nil || scoped.ImportKind != research.ImportMetadataAbstract {
		t.Fatal("task import lost identity", e)
	}
	if transport.calls.Load() != 2 {
		t.Fatal("import/restart refetched source")
	}
	t.Log("2 local HTTP, 0 models; 4 candidates preserved, task metadata import and identity hashes survived restart")
}

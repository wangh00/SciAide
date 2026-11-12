package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/research/connectors"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

type ericFixtureTransport struct {
	endpoint  *url.URL
	transport http.RoundTripper
	calls     atomic.Int32
}

func (t *ericFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "api.ies.ed.gov" {
		return nil, fmt.Errorf("unexpected external source in controlled fixture: %s", request.URL.Host)
	}
	t.calls.Add(1)
	clone := request.Clone(request.Context())
	target := *request.URL
	target.Scheme = t.endpoint.Scheme
	target.Host = t.endpoint.Host
	clone.URL = &target
	clone.Host = t.endpoint.Host
	response, err := t.transport.RoundTrip(clone)
	if response != nil {
		response.Request = request
	}
	return response, err
}

func TestERICRealAdapterMetadataIndexAndRestartPreserveSourceAttributes(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/eric/" || !strings.Contains(r.URL.Query().Get("fields"), "educationlevel") {
			t.Error(r.URL.String())
		}
		w.Write([]byte(`{"response":{"numFound":1,"docs":[{"id":"EJ1234567","title":"Controlled education feedback study","description":"The controlled ERIC result is epsilon forty two.","author":["Doe, Jane"],"publicationdateyear":2025,"subject":["Writing Feedback"],"educationlevel":["Higher Education"],"peerreviewed":"T","source":"Fixture Education Journal","sourceid":"v1 n1 p1-2","e_fulltextauth":0,"url":"https://doi.org/10.5555/eric-fixture"}]}}`))
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := &ericFixtureTransport{endpoint: endpoint, transport: http.DefaultTransport}
	root := t.TempDir()
	options := Options{RootDir: root, ResearchConnectors: connectors.DefaultWithTransport(transport)}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if app != nil {
			_ = app.Close()
		}
	}()
	project, err := app.ProjectFacade.CreateProject(struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		WorkspacePath string `json:"workspacePath"`
	}{Name: "ERIC controlled integration"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.ResearchFacade.Search(research.DiscoverySearchCommand{ProjectID: project.ID, Query: "writing feedback", SourceIDs: []string{"eric"}, Limit: 5, Years: research.PublicationYears{From: 2023, To: 2026}, EnrichMetadata: true})
	if err != nil || transport.calls.Load() != 1 {
		t.Fatal(err, transport.calls.Load())
	}
	page, err := app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: project.ID, QueryID: result.Query.ID, Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err, page)
	}
	candidate := page.Items[0]
	if candidate.Preferred.SourceMetadata == nil || len(candidate.Preferred.SourceMetadata.EducationLevels) != 1 || candidate.Preferred.Identifiers.DOI != "10.5555/eric-fixture" {
		t.Fatal("metadata disappeared in SQLite", candidate)
	}
	// Search alone has not issued trusted knowledge evidence.
	before, err := app.knowledge.Search(ctx, project.ID, "epsilon forty two", 5)
	if err != nil || len(before.Matches) != 0 {
		t.Fatal("online discovery became trusted evidence", err, before)
	}
	candidate, err = app.ResearchFacade.UpdateReview(research.ReviewCommand{ProjectID: project.ID, CandidateID: candidate.ID, Status: research.ReviewIncluded, Note: "controlled integration selection"})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := app.ResearchFacade.ImportCandidate(research.ImportCandidateCommand{ProjectID: project.ID, CandidateID: candidate.ID, Mode: research.MaterializeMetadata})
	if err != nil || imported.Candidate.ImportKind != research.ImportMetadataAbstract {
		t.Fatal(err, imported)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		documents, err := app.KnowledgeFacade.ListDocuments(project.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(documents) == 1 && documents[0].Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("index did not become ready", documents)
		}
		time.Sleep(20 * time.Millisecond)
	}
	evidence, err := app.ResearchFacade.SearchEvidence(project.ID, candidate.ID, "epsilon forty two")
	if err != nil || len(evidence) == 0 || evidence[0].Reference.AttachmentID != imported.Attachment.ID {
		t.Fatal(err, evidence)
	}
	saved, err := app.ResearchFacade.SaveEvidence(research.SaveEvidenceCommand{ProjectID: project.ID, CandidateID: candidate.ID, Field: research.EvidenceFinding, Content: "The controlled ERIC result is epsilon forty two.", Provenance: research.EvidenceModel, ReviewStatus: research.EvidencePending, Reference: &evidence[0].Reference})
	if err != nil || saved.EvidenceLevel != research.EvidenceMetadataAbstract || saved.ReviewStatus != research.EvidencePending {
		t.Fatal("metadata promoted to approved/full-text evidence", err, saved)
	}
	// Replaying the import must reuse the same material, not issue a new source call.
	replay, err := app.ResearchFacade.ImportCandidate(research.ImportCandidateCommand{ProjectID: project.ID, CandidateID: candidate.ID, Mode: research.MaterializeMetadata})
	if err != nil || replay.Candidate.AttachmentID != imported.Attachment.ID || transport.calls.Load() != 1 {
		t.Fatal(err, replay)
	}
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app = nil
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	repository := sqlite.NewResearchRepository(app.store.DB())
	restored, err := repository.GetCandidate(ctx, project.ID, candidate.ID)
	if err != nil || restored.Preferred.SourceMetadata == nil || restored.Preferred.SourceMetadata.EducationLevels[0] != "Higher Education" || restored.AttachmentID != imported.Attachment.ID {
		t.Fatal("restart lost source metadata or import", err, restored)
	}
	entries, err := app.ResearchFacade.ListEvidence(project.ID, candidate.ID)
	if err != nil || len(entries) != 1 || entries[0].ReviewStatus != research.EvidencePending || entries[0].EvidenceLevel != research.EvidenceMetadataAbstract {
		t.Fatal(err, entries)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("restart contacted source", transport.calls.Load())
	}
}

package bootstrap

import (
	research "github.com/wangh00/SciAide/internal/app/research"
	"os"
	"testing"
	"time"
)

// Explicit opt-in: public HTTP + a new temporary SQLite/workspace only. No
// model/profile is selected, no existing task is resumed and no report approved.
func TestERICLivePublicSourceToIndexedPDF(t *testing.T) {
	if os.Getenv("SCIAIDE_LITERATURE_SOURCE_LIVE") != "1" {
		t.Skip("public network source probe is opt-in")
	}
	app, err := New(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	project, err := app.ProjectFacade.CreateProject(struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		WorkspacePath string `json:"workspacePath"`
	}{Name: "ERIC public source integration"})
	if err != nil {
		t.Fatal(err)
	}
	found, err := app.ResearchFacade.Search(research.DiscoverySearchCommand{ProjectID: project.ID, Query: "id:EJ1426704", SourceIDs: []string{"eric"}, Limit: 1, Years: research.PublicationYears{From: 2024, To: 2024}})
	if err != nil || found.Query.Partial {
		t.Fatal("live ERIC search", err, found.Query.Sources)
	}
	page, err := app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: project.ID, QueryID: found.Query.ID, Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err, page)
	}
	candidate := page.Items[0]
	if candidate.Preferred.SourceRecordID != "EJ1426704" || candidate.Preferred.PDFURL != "https://files.eric.ed.gov/fulltext/EJ1426704.pdf" || candidate.Preferred.SourceMetadata == nil {
		t.Fatal("live source identity/fields", candidate.Preferred.SourceRecordID)
	}
	if _, err = app.ResearchFacade.UpdateReview(research.ReviewCommand{ProjectID: project.ID, CandidateID: candidate.ID, Status: research.ReviewIncluded, Note: "engineering PDF access test only, not scientific approval"}); err != nil {
		t.Fatal(err)
	}
	imported, err := app.ResearchFacade.ImportCandidate(research.ImportCandidateCommand{ProjectID: project.ID, CandidateID: candidate.ID, Mode: research.MaterializeFullText})
	if err != nil || imported.Candidate.ImportKind != research.ImportFullText {
		t.Fatal("live full-text materialization", err, imported.Candidate.ImportKind)
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		documents, err := app.KnowledgeFacade.ListDocuments(project.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(documents) == 1 && documents[0].Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("public PDF was not indexed", documents)
		}
		time.Sleep(50 * time.Millisecond)
	}
	evidence, err := app.ResearchFacade.SearchEvidence(project.ID, candidate.ID, "ChatGPT")
	if err != nil || len(evidence) == 0 || evidence[0].Reference.AttachmentID != imported.Attachment.ID {
		t.Fatal("public PDF citation search", err, len(evidence))
	}
	t.Logf("live ERIC record=%s importKind=%s pdfSha256=%s indexed=true candidateEvidenceMatches=%d; modelRequests=0; no scientific approval", candidate.Preferred.SourceRecordID, imported.Candidate.ImportKind, imported.Attachment.SHA256, len(evidence))
}

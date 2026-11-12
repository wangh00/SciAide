package materializer

import (
	"context"
	research "github.com/wangh00/SciAide/internal/app/research"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestERICMaterialScopeAndControlledPDFDownload(t *testing.T) {
	production := New()
	if !production.allowedTarget("eric", "https://files.eric.ed.gov/fulltext/EJ1234567.pdf") {
		t.Fatal("ERIC PDF host missing")
	}
	for _, target := range []string{"https://files.eric.ed.gov.evil.test/fulltext/EJ1234567.pdf", "http://files.eric.ed.gov/fulltext/EJ1234567.pdf", "https://127.0.0.1/paper.pdf", "https://core.ac.uk/download/123.pdf"} {
		if production.allowedTarget("eric", target) {
			t.Fatal("expanded authority", target)
		}
	}
	if production.allowedTarget("openalex", "https://files.eric.ed.gov/fulltext/EJ1234567.pdf") {
		t.Fatal("OpenAlex inherited ERIC-specific authorization")
	}
	if production.allowedTarget("core", "https://core.ac.uk/download/123.pdf") {
		t.Fatal("CORE PDF access enabled")
	}
	pdf := []byte("%PDF-1.4\ncontrolled ERIC fixture\n%%EOF\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(pdf)
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	service := newTestService(server.Client(), "eric", parsed.Hostname())
	project := createMaterializerProject(t)
	peer := true
	work := research.Work{SourceID: "eric", SourceRecordID: "EJ1234567", Title: "ERIC fixture", Abstract: "epsilon forty two", PDFURL: server.URL + "/fulltext/EJ1234567.pdf", OpenAccess: true, SourceMetadata: &research.SourceMetadata{EducationLevels: []string{"Higher Education"}, PeerReviewed: &peer}}
	candidate := research.Candidate{ID: "eric-candidate", ProjectID: project.ID, ReviewStatus: research.ReviewIncluded, Preferred: work, Records: []research.SourceRecord{{Work: work}}}
	value, err := service.Materialize(context.Background(), project, candidate, research.MaterializeFullText)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Cleanup(value)
	data, err := os.ReadFile(value.Path)
	if err != nil || string(data) != string(pdf) || value.Kind != research.ImportFullText {
		t.Fatal(value, err)
	}
	markdown := metadataMarkdown(candidate)
	if !strings.Contains(markdown, "Higher Education") || !strings.Contains(markdown, "peerReviewed") || !strings.Contains(markdown, "not the publication full text") || !strings.Contains(markdown, "not study-quality approval") {
		t.Fatal(markdown)
	}
}

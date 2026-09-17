package materializer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

func newTestService(client *http.Client, sourceID, host string) *Service {
	return &Service{client: client, allowHTTP: true, maxPDFBytes: defaultMaxPDFBytes, allowedHosts: map[string]map[string]struct{}{sourceID: {strings.ToLower(host): {}}}}
}

func createMaterializerProject(t *testing.T) project.Project {
	t.Helper()
	root := t.TempDir()
	service := project.NewService(&materializerProjectRepository{}, filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	value, err := service.Create(context.Background(), "Materializer", "")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type materializerProjectRepository struct{ value project.Project }

func (r *materializerProjectRepository) Create(_ context.Context, value project.Project) error {
	r.value = value
	return nil
}
func (r *materializerProjectRepository) Get(_ context.Context, _ string) (project.Project, error) {
	return r.value, nil
}
func (r *materializerProjectRepository) List(context.Context) ([]project.Project, error) {
	return []project.Project{r.value}, nil
}
func (r *materializerProjectRepository) UpdateWorkspace(_ context.Context, _ string, path, kind string, _ time.Time) error {
	r.value.WorkspacePath, r.value.WorkspaceKind = path, kind
	return nil
}
func (r *materializerProjectRepository) Delete(context.Context, string) error { return nil }

func TestMaterializeDownloadsValidatedPDF(t *testing.T) {
	pdf := []byte("%PDF-1.4\nfixture\n%%EOF\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/pdf")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(pdf)
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	service := newTestService(server.Client(), "arxiv", parsed.Hostname())
	selected := createMaterializerProject(t)
	candidate := appresearch.Candidate{ID: "candidate-1234567890", ProjectID: selected.ID, ReviewStatus: appresearch.ReviewIncluded, Preferred: appresearch.Work{Title: "Fixture"}, Records: []appresearch.SourceRecord{{Work: appresearch.Work{SourceID: "arxiv", SourceRecordID: "2401.1", Title: "Fixture", PDFURL: server.URL + "/paper.pdf", OpenAccess: true}}}}
	value, err := service.Materialize(context.Background(), selected, candidate, appresearch.MaterializeAuto)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Cleanup(value)
	contents, err := os.ReadFile(value.Path)
	if err != nil || string(contents) != string(pdf) || value.Kind != appresearch.ImportFullText || value.Name != "research-candidate123.pdf" {
		t.Fatalf("materialized = %#v, contents=%q, err=%v", value, contents, err)
	}
	digest := sha256.Sum256(pdf)
	if value.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("sha256 = %s", value.SHA256)
	}
}

func TestMaterializeRejectsFakeAndOversizedPDF(t *testing.T) {
	selected := createMaterializerProject(t)
	for _, fixture := range []struct {
		name string
		body string
		mime string
		max  int64
		want string
	}{
		{name: "fake", body: "<html>not pdf</html>", mime: "application/pdf", max: 100, want: "not a PDF"},
		{name: "mime", body: "%PDF-fixture", mime: "text/html", max: 100, want: "unexpected MIME"},
		{name: "large", body: "%PDF-0123456789", mime: "application/pdf", max: 6, want: "size limit"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", fixture.mime)
				_, _ = io.WriteString(writer, fixture.body)
			}))
			defer server.Close()
			parsed, _ := url.Parse(server.URL)
			service := newTestService(server.Client(), "arxiv", parsed.Hostname())
			service.maxPDFBytes = fixture.max
			candidate := appresearch.Candidate{ID: "candidate", ProjectID: selected.ID, Preferred: appresearch.Work{Title: "Fixture"}, Records: []appresearch.SourceRecord{{Work: appresearch.Work{SourceID: "arxiv", PDFURL: server.URL + "/paper.pdf", OpenAccess: true}}}}
			_, err := service.Materialize(context.Background(), selected, candidate, appresearch.MaterializeFullText)
			if err == nil || !strings.Contains(err.Error(), fixture.want) {
				t.Fatalf("error = %v, want %q", err, fixture.want)
			}
		})
	}
}

func TestMaterializeMetadataDisclosesEvidenceLevel(t *testing.T) {
	selected := createMaterializerProject(t)
	service := New()
	candidate := appresearch.Candidate{ID: "metadata", ProjectID: selected.ID, Preferred: appresearch.Work{Title: "Metadata paper", Abstract: "An online abstract.", Authors: []appresearch.Author{{Name: "Ada"}}, Year: 2025, Identifiers: appresearch.Identifiers{DOI: "10.1/test"}}, Records: []appresearch.SourceRecord{{Work: appresearch.Work{SourceID: "crossref", SourceRecordID: "10.1/test", LandingURL: "https://doi.org/10.1/test"}}}}
	value, err := service.Materialize(context.Background(), selected, candidate, appresearch.MaterializeAuto)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Cleanup(value)
	contents, err := os.ReadFile(value.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{"not the publication full text", "Metadata paper", "An online abstract.", "crossref", "10.1/test"} {
		if !strings.Contains(text, required) {
			t.Fatalf("metadata file lacks %q:\n%s", required, text)
		}
	}
	if value.Kind != appresearch.ImportMetadataAbstract || !strings.HasSuffix(value.Name, "-metadata.md") {
		t.Fatalf("materialized = %#v", value)
	}
}

func TestAllowedTargetAndRedirectStayOnFixedHost(t *testing.T) {
	service := newTestService(&http.Client{}, "arxiv", "arxiv.org")
	if !service.allowedTarget("arxiv", "https://arxiv.org/pdf/1") || service.allowedTarget("arxiv", "https://example.com/paper.pdf") || service.allowedTarget("crossref", "https://arxiv.org/pdf/1") {
		t.Fatal("fixed source host boundary is incorrect")
	}
	first, _ := http.NewRequest(http.MethodGet, "https://arxiv.org/pdf/1", nil)
	same, _ := http.NewRequest(http.MethodGet, "https://arxiv.org/pdf/2", nil)
	other, _ := http.NewRequest(http.MethodGet, "https://example.com/pdf/2", nil)
	if err := fixedHostRedirect(same, []*http.Request{first}); err != nil {
		t.Fatal(err)
	}
	if err := fixedHostRedirect(other, []*http.Request{first}); err == nil {
		t.Fatal("cross-host redirect was accepted")
	}
}

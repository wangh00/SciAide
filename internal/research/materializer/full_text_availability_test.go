package materializer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	research "github.com/wangh00/SciAide/internal/app/research"
)

func TestFullTextTargetsTrustedResolution(t *testing.T) {
	s := New()
	for _, tc := range []struct {
		source, pdf, pmcid string
		want               int
	}{
		{"pubmed", "", "PMC10611992", 1},
		{"pubmed", "", "PMC123/../../evil", 0},
		{"openalex", "https://www.nature.com/articles/example.pdf", "", 1},
		{"crossref", "https://jamanetwork.com/example.pdf", "", 1},
		{"openalex", "https://www.nature.com.evil.test/example.pdf", "", 0},
		{"openalex", "http://www.nature.com/example.pdf", "", 0},
	} {
		c := research.Candidate{Records: []research.SourceRecord{{Work: research.Work{SourceID: tc.source, PDFURL: tc.pdf, OpenAccess: true, Identifiers: research.Identifiers{PMCID: tc.pmcid}}}}}
		if got := len(s.fullTextTargets(c)); got != tc.want {
			t.Fatalf("%+v targets=%d", tc, got)
		}
	}
}

func TestFullTextRateLimitCoolsDownHost(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(429) }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	s := newTestService(server.Client(), "arxiv", u.Hostname())
	p := createMaterializerProject(t)
	for _, path := range []string{"/one.pdf", "/two.pdf"} {
		c := research.Candidate{ID: "candidate", ProjectID: p.ID, Records: []research.SourceRecord{{Work: research.Work{SourceID: "arxiv", PDFURL: server.URL + path, OpenAccess: true}}}}
		_, err := s.Materialize(context.Background(), p, c, research.MaterializeFullText)
		var unavailable *FullTextUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("err=%v", err)
		}
	}
	if requests != 1 {
		t.Fatalf("requests=%d", requests)
	}
	s.downloadMu.Lock()
	defer s.downloadMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := s.downloadPDF(ctx, p, research.Candidate{}, research.SourceRecord{}, server.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait cancellation=%v", err)
	}
}

func TestFullTextFallbackOnlyForUnavailable(t *testing.T) {
	for _, status := range []int{404, 200} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/pdf")
			if r.URL.Path == "/first" {
				w.WriteHeader(status)
				w.Write([]byte("not pdf"))
				return
			}
			w.Write([]byte("%PDF-1.4\nfixture\n%%EOF\n"))
		}))
		u, _ := url.Parse(server.URL)
		s := newTestService(server.Client(), "arxiv", u.Hostname())
		p := createMaterializerProject(t)
		c := research.Candidate{ID: "candidate", ProjectID: p.ID}
		for _, path := range []string{"/first", "/second"} {
			c.Records = append(c.Records, research.SourceRecord{ID: path, Work: research.Work{SourceID: "arxiv", PDFURL: server.URL + path, OpenAccess: true}})
		}
		v, err := s.Materialize(context.Background(), p, c, research.MaterializeFullText)
		s.Cleanup(v)
		server.Close()
		if status == 404 && (err != nil || calls != 2) {
			t.Fatalf("fallback calls=%d err=%v", calls, err)
		}
		if status == 200 && (err == nil || calls != 1) {
			t.Fatalf("invalid PDF bypass calls=%d err=%v", calls, err)
		}
	}
}

package materializer

import (
	"context"
	"errors"
	research "github.com/wangh00/SciAide/internal/app/research"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFullTextBudgetAndParentCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	p := createMaterializerProject(t)
	c := research.Candidate{ID: "paper", ProjectID: p.ID, Records: []research.SourceRecord{{Work: research.Work{SourceID: "arxiv", OpenAccess: true, PDFURL: server.URL + "/one", PDFURLs: []string{server.URL + "/two"}}}}}
	for _, mode := range []research.MaterializeMode{research.MaterializeAuto, research.MaterializeFullText} {
		s := newTestService(server.Client(), "arxiv", u.Hostname())
		s.fullTextTimeout = 30 * time.Millisecond
		start := time.Now()
		v, err := s.Materialize(context.Background(), p, c, mode)
		defer s.Cleanup(v)
		if time.Since(start) > time.Second {
			t.Fatal("budget not enforced")
		}
		if mode == research.MaterializeAuto && (err != nil || !strings.Contains(v.Warning, "per-paper budget")) {
			t.Fatalf("auto=%+v %v", v, err)
		}
		if mode == research.MaterializeFullText && err == nil {
			t.Fatal("strict full text accepted timeout")
		}
	}
	s := newTestService(server.Client(), "arxiv", u.Hostname())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := s.Materialize(ctx, p, c, research.MaterializeAuto)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("parent cancellation hidden: %v", err)
	}
}

func TestFullTextRetainsAllUnavailableReasons(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/one" {
			w.WriteHeader(404)
		} else {
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	s := newTestService(server.Client(), "arxiv", u.Hostname())
	p := createMaterializerProject(t)
	c := research.Candidate{ID: "paper", ProjectID: p.ID, Records: []research.SourceRecord{{Work: research.Work{SourceID: "arxiv", OpenAccess: true, PDFURL: server.URL + "/one", PDFURLs: []string{server.URL + "/two"}}}}}
	v, err := s.Materialize(context.Background(), p, c, research.MaterializeAuto)
	defer s.Cleanup(v)
	if err != nil || !strings.Contains(v.Warning, "404") || !strings.Contains(v.Warning, "403") {
		t.Fatalf("lost reasons: %+v %v", v, err)
	}
}

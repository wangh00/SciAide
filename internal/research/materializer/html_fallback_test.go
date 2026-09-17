package materializer

import (
	"context"
	research "github.com/wangh00/SciAide/internal/app/research"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHTMLFallbackAndMultipleLocations(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/pdf" {
			w.Header().Set("Content-Type", "application/pdf")
			w.Write([]byte("%PDF-1.4\nfixture\n%%EOF\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>challenge</html>"))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	p := createMaterializerProject(t)
	c := research.Candidate{ID: "candidate", ProjectID: p.ID, Records: []research.SourceRecord{{Work: research.Work{SourceID: "arxiv", OpenAccess: true, PDFURL: server.URL + "/html"}}}}
	s := newTestService(server.Client(), "arxiv", u.Hostname())
	v, err := s.Materialize(context.Background(), p, c, research.MaterializeAuto)
	if err != nil || v.Kind != research.ImportMetadataAbstract || v.Warning == "" {
		t.Fatalf("fallback=%+v %v", v, err)
	}
	s.Cleanup(v)
	c.Records[0].Work.PDFURLs = []string{server.URL + "/pdf"}
	v, err = s.Materialize(context.Background(), p, c, research.MaterializeFullText)
	if err != nil || v.Kind != research.ImportFullText || calls != 2 {
		t.Fatalf("copies calls=%d result=%+v err=%v", calls, v, err)
	}
	s.Cleanup(v)
}

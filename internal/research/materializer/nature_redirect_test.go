package materializer

import (
	"context"
	research "github.com/wangh00/SciAide/internal/app/research"
	"net/http"
	"net/url"
	"os"
	"testing"
)

func TestNatureRedirectBoundary(t *testing.T) {
	original := "https://www.nature.com/articles/s41366-019-0339-7.pdf"
	first, _ := http.NewRequest("GET", original, nil)
	for _, tc := range []struct {
		target string
		ok     bool
	}{
		{"https://idp.nature.com/authorize?client_id=grover&response_type=cookie&redirect_uri=" + url.QueryEscape(original), true},
		{"https://idp.nature.com/transit?code=fixture&redirect_uri=" + url.QueryEscape(original), true},
		{original, true},
		{"https://idp.nature.com/transit?redirect_uri=https://evil.test", false},
		{"https://idp.nature.com.evil.test/authorize", false},
		{"http://idp.nature.com/transit", false},
		{"https://www.nature.com/articles/other.pdf", false},
	} {
		r, _ := http.NewRequest("GET", tc.target, nil)
		if err := fixedHostRedirect(r, []*http.Request{first}); (err == nil) != tc.ok {
			t.Fatalf("%s err=%v", tc.target, err)
		}
	}
}

func TestNatureLiveDownload(t *testing.T) {
	if os.Getenv("SCIAIDE_TEST_NATURE_LIVE") != "1" {
		t.Skip("explicit live probe only")
	}
	p := createMaterializerProject(t)
	s := New()
	c := research.Candidate{ID: "nature-probe", ProjectID: p.ID, Records: []research.SourceRecord{{Work: research.Work{SourceID: "crossref", OpenAccess: true, PDFURL: "https://www.nature.com/articles/s41366-019-0339-7.pdf"}}}}
	v, err := s.Materialize(context.Background(), p, c, research.MaterializeFullText)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Cleanup(v)
	info, err := os.Stat(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("validated PDF bytes=%d sha256=%s", info.Size(), v.SHA256)
}

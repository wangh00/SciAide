package materializer

import (
	"context"
	"errors"
	"github.com/wangh00/SciAide/internal/httpua"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

func TestAbstractFirstAndRecoverableFullTextFailure(t *testing.T) {
	for _, test := range []struct {
		name, abstract, body   string
		status                 int
		mode                   appresearch.MaterializeMode
		wantRequests           int
		wantError, wantWarning bool
	}{
		{name: "abstract skips network", abstract: "Verified source abstract", status: 429, mode: appresearch.MaterializeAuto},
		{name: "auto falls back on 429", status: 429, mode: appresearch.MaterializeAuto, wantRequests: 1, wantWarning: true},
		{name: "explicit full text stays strict", status: 429, mode: appresearch.MaterializeFullText, wantRequests: 1, wantError: true},
		{name: "fake PDF not downgraded", status: 200, body: "<html>not PDF</html>", mode: appresearch.MaterializeAuto, wantRequests: 1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				host, _, _ := net.SplitHostPort(r.Host)
				if r.UserAgent() != httpua.ForHost(host) {
					t.Errorf("UA=%s", r.UserAgent())
				}
				w.Header().Set("Content-Type", "application/pdf")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			parsed, _ := url.Parse(server.URL)
			s := newTestService(server.Client(), "europepmc", parsed.Hostname())
			p := createMaterializerProject(t)
			c := appresearch.Candidate{ID: "candidate", ProjectID: p.ID, Preferred: appresearch.Work{Title: "Source paper"}, Records: []appresearch.SourceRecord{{Work: appresearch.Work{Title: "Source paper", Abstract: test.abstract, SourceID: "europepmc", PDFURL: server.URL + "/paper.pdf", OpenAccess: true}}}}
			v, err := s.Materialize(context.Background(), p, c, test.mode)
			defer s.Cleanup(v)
			if (err != nil) != test.wantError || requests != test.wantRequests {
				t.Fatalf("requests=%d result=%+v err=%v", requests, v, err)
			}
			if err == nil {
				if v.Kind != appresearch.ImportMetadataAbstract || (v.Warning != "") != test.wantWarning {
					t.Fatalf("result=%+v", v)
				}
				contents, err := os.ReadFile(v.Path)
				if err != nil || !strings.Contains(string(contents), test.abstract) || !strings.Contains(string(contents), "not the publication full text") {
					t.Fatalf("metadata=%s err=%v", contents, err)
				}
			}
		})
	}
}

func TestCanceledAutoImportDoesNotWriteMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v, err := New().Materialize(ctx, createMaterializerProject(t), appresearch.Candidate{Preferred: appresearch.Work{Abstract: "abstract"}}, appresearch.MaterializeAuto)
	if !errors.Is(err, context.Canceled) || v.Path != "" {
		t.Fatalf("result=%+v err=%v", v, err)
	}
}

type timeoutTransport struct{}

func (timeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, os.ErrDeadlineExceeded
}

func TestAutoImportTimeoutFallsBackButExplicitFullTextDoesNot(t *testing.T) {
	p := createMaterializerProject(t)
	s := newTestService(&http.Client{Transport: timeoutTransport{}}, "europepmc", "europepmc.org")
	c := appresearch.Candidate{ID: "candidate", ProjectID: p.ID, Preferred: appresearch.Work{Title: "Paper"}, Records: []appresearch.SourceRecord{{Work: appresearch.Work{SourceID: "europepmc", PDFURL: "https://europepmc.org/paper.pdf", OpenAccess: true}}}}
	v, err := s.Materialize(context.Background(), p, c, appresearch.MaterializeAuto)
	defer s.Cleanup(v)
	if err != nil || v.Kind != appresearch.ImportMetadataAbstract || !strings.Contains(v.Warning, "timed out") {
		t.Fatalf("timeout fallback=%+v err=%v", v, err)
	}
	if _, err := s.Materialize(context.Background(), p, c, appresearch.MaterializeFullText); err == nil {
		t.Fatal("explicit full text silently fell back")
	}
}

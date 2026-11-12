package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/researchsources"
)

func TestERICNativeFieldsYearsAndFullTextFlags(t *testing.T) {
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("search") != "(ChatGPT AND writing) AND publicationdateyear:[2023 TO 2026]" || q.Get("start") != "5" || q.Get("rows") != "5" || !strings.Contains(q.Get("fields"), "e_fulltextauth,educationlevel") {
			t.Errorf("query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/plain;charset=utf-8")
		w.Write([]byte(`{"response":{"numFound":2,"docs":[{"id":"EJ1234567","title":"Students&apos; feedback","description":"<p>p<.005 supports no invented inference</p>","author":["Doe, Jane"],"subject":["Writing"],"educationlevel":["Higher Education"],"publicationtype":["Journal Articles"],"source":"Education","sourceid":"v1 n2 p3-4","url":"https://dx.doi.org/10.1234/ABC","publicationdateyear":2024,"e_fulltextauth":1,"peerreviewed":"T"},{"id":"ED123456","title":"Metadata only","e_fulltextauth":0}]}}`))
	})
	defer server.Close()
	works, err := newERIC(client, server.URL, 0).Search(context.Background(), research.SearchOptions{Query: "ChatGPT AND writing", Limit: 5, Offset: 5, Years: research.PublicationYears{From: 2023, To: 2026}})
	if err != nil || len(works) != 2 {
		t.Fatal(works, err)
	}
	w := works[0]
	if w.Title != "Students' feedback" || !strings.Contains(w.Abstract, "p<.005") || len(w.Authors) != 1 || w.Authors[0].Name != "Doe, Jane" || w.Identifiers.DOI != "10.1234/abc" {
		t.Fatalf("normalization: %#v", w)
	}
	if w.PDFURL != "https://files.eric.ed.gov/fulltext/EJ1234567.pdf" || !w.OpenAccess || works[1].PDFURL != "" || works[1].OpenAccess {
		t.Fatal("full-text authorization flag ignored", works)
	}
	if w.SourceMetadata == nil || len(w.SourceMetadata.EducationLevels) != 1 || w.SourceMetadata.PeerReviewed == nil || !*w.SourceMetadata.PeerReviewed || w.SourceMetadata.SourceCitation != "v1 n2 p3-4" {
		t.Fatal(w.SourceMetadata)
	}
	var original ericRecord
	if json.Unmarshal(w.RawSnapshot, &original) != nil || original.Title != "Students&apos; feedback" {
		t.Fatal("source snapshot not retained")
	}
}

func TestERICFetchRejectsQueryInjectionAndIdentityMismatch(t *testing.T) {
	var calls atomic.Int32
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("search") != "id:EJ1234567" {
			t.Error("not an exact-id lookup")
		}
		w.Write([]byte(`{"response":{"numFound":1,"docs":[{"id":"EJ9999999","title":"Other"}]}}`))
	})
	defer server.Close()
	c := newERIC(client, server.URL, 0)
	if _, err := c.Fetch(context.Background(), "EJ1234567 OR *:*"); err == nil || calls.Load() != 0 {
		t.Fatal("invalid id reached network")
	}
	if _, err := c.Fetch(context.Background(), "EJ1234567"); err == nil || calls.Load() != 1 {
		t.Fatal("identity mismatch accepted")
	}
	for _, url := range []string{"https://doi.org.evil.test/10.1234/abc", "https://publisher.test/10.1234/abc", "https://user@doi.org/10.1234/abc"} {
		if doiFromERICURL(url) != "" {
			t.Fatal("unverified DOI host accepted", url)
		}
	}
}

func TestNewSourceEnvelopesDistinguishEmptyAndInvalid(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"eric-empty", `{"response":{"numFound":0,"docs":[]}}`, true},
		{"eric-missing", `{"response":{"numFound":0}}`, false},
		{"eric-bad", `{"version":"1"}`, false},
		{"eric-count-empty", `{"response":{"numFound":1,"docs":[]}}`, false},
		{"eric-count-zero", `{"response":{"numFound":0,"docs":[{"id":"EJ1234567","title":"x"}]}}`, false},
		{"core-empty", `{"totalHits":0,"results":[]}`, true},
		{"core-missing", `{"totalHits":0}`, false},
		{"core-bad", `{"results":[]}`, false},
		{"core-count-empty", `{"totalHits":1,"results":[]}`, false},
		{"core-count-zero", `{"totalHits":0,"results":[{"id":1,"title":"x"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) })
			defer server.Close()
			var err error
			if strings.HasPrefix(tc.name, "eric") {
				_, err = newERIC(client, server.URL, 0).Search(context.Background(), research.SearchOptions{Query: "x", Limit: 5})
			} else {
				_, err = newCORE(client, server.URL, 0).Search(context.Background(), research.SearchOptions{Query: "x", Limit: 5})
			}
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestCOREMetadataCannotBecomeFullTextAndNeedsExplicitSelection(t *testing.T) {
	var calls atomic.Int32
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/search/works/" {
			t.Error("metadata refetch amplification", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "(writing) AND yearPublished>=2023 AND yearPublished<=2026" {
			t.Error(got)
		}
		w.Write([]byte(`{"totalHits":1,"results":[{"id":123,"title":"Writing","fullText":"Not available for public API users.","downloadUrl":"https://core.ac.uk/download/789.pdf","abstract":"","authors":[{"name":"Jane Doe"},"John Doe"],"language":{"code":"en"},"doi":"10.1234/ABC","yearPublished":2024}]}`))
	})
	defer server.Close()
	service, err := research.NewService([]research.Connector{newCORE(client, server.URL, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Search(context.Background(), research.SearchCommand{Query: "writing"}); err != nil || calls.Load() != 0 {
		t.Fatal("CORE used by default", err, calls.Load())
	}
	result, err := service.Search(context.Background(), research.SearchCommand{Query: "writing", SourceIDs: []string{"core"}, EnrichMetadata: true, Years: research.PublicationYears{From: 2023, To: 2026}})
	if err != nil || len(result.Works) != 1 || calls.Load() != 1 {
		t.Fatal(err, result, calls.Load())
	}
	w := result.Works[0]
	if w.Abstract != "" || w.PDFURL != "" || len(w.PDFURLs) > 0 || w.OpenAccess || strings.Contains(string(w.RawSnapshot), "Not available") {
		t.Fatal("access notice leaked into evidence", w)
	}
	if w.Identifiers.DOI != "10.1234/abc" || len(w.Authors) != 2 || w.Language != "en" || !result.Sources[0].YearFilterApplied {
		t.Fatal(w, result.Sources)
	}
	catalog := service.Catalog()
	catalog[0].Capabilities.ExplicitSelection = false
	if _, err = service.Search(context.Background(), research.SearchCommand{Query: "writing"}); err != nil || calls.Load() != 1 {
		t.Fatal("catalog mutation changed default scope", err)
	}
}

func TestSourceCatalogMatchesConnectorAssembly(t *testing.T) {
	sources := Default()
	if len(sources) != len(researchsources.IDs()) {
		t.Fatal("catalog/connector drift")
	}
	for _, c := range sources {
		spec, ok := researchsources.Lookup(c.Source().ID)
		if !ok || c.Source().Host != endpointHost(spec.Endpoint) {
			t.Fatal(c.Source())
		}
	}
}

func TestSourceRateWindowAppliesAcrossRequestsWithoutBurstOrSleep(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	}))
	defer server.Close()
	c := newTestClient(server.Client())
	options := requestOptions{SourceID: "core", Host: endpointHost(server.URL)}
	for _, query := range []string{"?q=one", "?q=two"} {
		start := time.Now()
		_, _, err := c.get(context.Background(), server.URL+query, options)
		var source *research.SourceError
		if !errors.As(err, &source) || source.Code != research.FailureRateLimited || time.Since(start) > time.Second {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("rate limit burst", calls.Load())
	}
}

func TestSourceAccessDeniedIsNotEmptyOrRetryable(t *testing.T) {
	var calls atomic.Int32
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(403) })
	defer server.Close()
	_, err := newCORE(client, server.URL, 0).Search(context.Background(), research.SearchOptions{Query: "x", Limit: 5})
	var source *research.SourceError
	if !errors.As(err, &source) || source.Code != research.FailureAccessDenied || source.Retryable || calls.Load() != 1 {
		t.Fatal(err)
	}
}

func TestMissingRetryWindowStillPausesSeparateQueries(t *testing.T) {
	var calls atomic.Int32
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(429) })
	defer server.Close()
	options := requestOptions{SourceID: "core", Host: endpointHost(server.URL)}
	for _, q := range []string{"?q=one", "?q=two"} {
		_, _, err := client.get(context.Background(), server.URL+q, options)
		if err == nil {
			t.Fatal("rate limit accepted")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("repeated HTTP after unknown recovery window", calls.Load())
	}
}
func TestProductionSourceRedirectCannotExpandHTTPSPort(t *testing.T) {
	c := NewClient()
	first, _ := http.NewRequest("GET", "https://api.ies.ed.gov/eric/", nil)
	other, _ := http.NewRequest("GET", "https://api.ies.ed.gov:8443/eric/", nil)
	if c.http.CheckRedirect(other, []*http.Request{first}) == nil {
		t.Fatal("port expansion")
	}
	canonical, _ := http.NewRequest("GET", "https://api.ies.ed.gov:443/eric/", nil)
	if err := c.http.CheckRedirect(canonical, []*http.Request{first}); err != nil {
		t.Fatal(err)
	}
	_, _, err := c.get(context.Background(), other.URL.String(), requestOptions{SourceID: "eric", Host: "api.ies.ed.gov"})
	if err == nil {
		t.Fatal("initial port expansion")
	}
}

type sourceBrokenBody struct{}

func (sourceBrokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (sourceBrokenBody) Close() error             { return nil }

type sourceRateTransport struct{ calls atomic.Int32 }

func (t *sourceRateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"60"}}, Body: sourceBrokenBody{}, Request: r}, nil
}
func TestRateHeadersSurviveBrokenErrorBody(t *testing.T) {
	transport := &sourceRateTransport{}
	client := NewClient()
	client.http.Transport = transport
	options := requestOptions{SourceID: "core", Host: "api.core.ac.uk"}
	for _, q := range []string{"one", "two"} {
		_, _, err := client.get(context.Background(), "https://api.core.ac.uk/v3/search/works/?q="+q, options)
		var source *research.SourceError
		if !errors.As(err, &source) || source.Code != research.FailureRateLimited {
			t.Fatal("HTTP rate status hidden by body failure", err)
		}
	}
	if transport.calls.Load() != 1 {
		t.Fatal("server retry window lost", transport.calls.Load())
	}
}

type sourceBrokenServerTransport struct{ calls atomic.Int32 }

func (t *sourceBrokenServerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return &http.Response{StatusCode: 500, Header: http.Header{}, Body: sourceBrokenBody{}, Request: r}, nil
}
func TestBrokenServerBodyDoesNotAddHTTPRetries(t *testing.T) {
	transport := &sourceBrokenServerTransport{}
	client := NewClient()
	client.http.Transport = transport
	_, _, err := client.get(context.Background(), "https://api.core.ac.uk/v3/search/works/?q=one", requestOptions{SourceID: "core", Host: "api.core.ac.uk"})
	var source *research.SourceError
	if !errors.As(err, &source) || source.Code != research.FailureInvalidData || source.Retryable || transport.calls.Load() != 1 {
		t.Fatal("unreadable 500 response gained retries", err, transport.calls.Load())
	}
}

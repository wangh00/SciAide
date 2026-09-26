package websearch

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestPriorityQuotaFallbackAndSecretIsolation(t *testing.T) {
	s := New(secretstore.NewMemory())
	ctx := context.Background()
	for _, c := range []SaveCommand{{"firecrawl", true, 1, "secret-one"}, {"brave", true, 2, "secret-two"}} {
		if e := s.Save(ctx, c); e != nil {
			t.Fatal(e)
		}
	}
	calls := []string{}
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Hostname())
		switch r.URL.Hostname() {
		case "api.firecrawl.dev":
			if r.Header.Get("Authorization") != "Bearer secret-one" {
				t.Fatal("missing auth")
			}
			return reply(402, `{"error":"quota"}`), nil
		case "api.search.brave.com":
			if r.Header.Get("X-Subscription-Token") != "secret-two" || r.Header.Get("Authorization") != "" {
				t.Fatal("credential crossed providers")
			}
			return reply(200, `{"web":{"results":[{"title":"Docs","url":"https://example.org/docs","description":"text"}]}}`), nil
		}
		t.Fatal("unexpected fallback")
		return nil, nil
	})
	r, e := s.Search(ctx, "query", 3)
	if e != nil || r.Provider != "brave" || len(r.Attempts) != 2 || r.Attempts[0].Status != "quota_exhausted" {
		t.Fatalf("%+v %v", r, e)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "secret-") {
		t.Fatal("secret leaked")
	}
	_, e = s.Search(ctx, "query", 3)
	if e != nil || len(calls) != 3 {
		t.Fatalf("cooldown ignored: %v %v", calls, e)
	}
	channels, _ := s.List(ctx)
	b, _ = json.Marshal(channels)
	if strings.Contains(string(b), "secret-") {
		t.Fatal("configuration leaked secret")
	}
	if e = s.Save(ctx, SaveCommand{"brave", true, 1, ""}); e != nil {
		t.Fatal(e)
	}
	v, _ := s.read(ctx, "brave")
	if v.Key != "secret-two" {
		t.Fatal("blank key replaced saved key")
	}
}
func TestDDGDefaultChallengeAndCancellation(t *testing.T) {
	s := New(secretstore.NewMemory())
	calls := 0
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Hostname() != "html.duckduckgo.com" || r.Header.Get("Authorization") != "" {
			t.Fatal("wrong default")
		}
		return reply(202, `<form id="challenge-form"></form>`), nil
	})
	r, e := s.Search(context.Background(), "hello", 3)
	if e != nil || r.Status != "unavailable" || r.Attempts[0].Status != "challenge" {
		t.Fatalf("%+v %v", r, e)
	}
	_, _ = s.Search(context.Background(), "another", 3)
	if calls != 1 {
		t.Fatal("challenge retried")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = s.Search(ctx, "hello", 3); e == nil {
		t.Fatal("cancel ignored")
	}
}
func TestEmptyDoesNotFallbackAndProviderParsers(t *testing.T) {
	cases := map[string]string{"firecrawl": `{"success":true,"data":{"web":[]}}`, "brave": `{"web":{"results":[]}}`, "tavily": `{"results":[]}`, "exa": `{"results":[]}`}
	for id, body := range cases {
		t.Run(id, func(t *testing.T) {
			s := New(secretstore.NewMemory())
			if e := s.Save(context.Background(), SaveCommand{id, true, 1, "key"}); e != nil {
				t.Fatal(e)
			}
			calls := 0
			s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return reply(200, body), nil })
			r, e := s.Search(context.Background(), "hello", 3)
			if e != nil || r.Status != "empty" || calls != 1 {
				t.Fatalf("%+v %v", r, e)
			}
		})
	}
	for _, body := range []string{`{}`, `{"error":"bad"}`, `{"results":null}`} {
		if _, e := parseAPI("tavily", []byte(body)); e == nil {
			t.Fatal("malformed response accepted")
		}
	}
}
func TestDDGParsingAndPublicNetworkBoundary(t *testing.T) {
	rows, e := parseDDG([]byte(`<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2F">Example</a><a class="result__snippet">Useful <b>text</b></a>`))
	if e != nil || len(rows) != 1 || rows[0].URL != "https://example.org/" || rows[0].Snippet != "Useful text" {
		t.Fatalf("%+v %v", rows, e)
	}
	if _, e = parseDDG([]byte(`<html>unexpected page</html>`)); e == nil {
		t.Fatal("unknown page accepted")
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "::1", "::ffff:127.0.0.1", "fc00::1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Fatalf("unsafe IP %s", ip)
		}
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public rejected")
	}
	for _, u := range []string{"file:///test", "https://user:key@example.org", "http://example.org:8080"} {
		if _, e := validateURL(u); e == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
	if retryAfter("120", time.Second) != 2*time.Minute {
		t.Fatal("Retry-After ignored")
	}
	if _, e := Open(context.Background(), "http://127.0.0.1/", ""); e == nil {
		t.Fatal("loopback fetched")
	}
}
func TestProviderFailureDoesNotExposeBody(t *testing.T) {
	s := New(secretstore.NewMemory())
	_ = s.Save(context.Background(), SaveCommand{"tavily", true, 1, "private-key"})
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.tavily.com" {
			return reply(429, `{"message":"private-key"}`), nil
		}
		return reply(202, `<form id="challenge-form">`), nil
	})
	r, e := s.Search(context.Background(), "query", 3)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "private-key") || r.Attempts[0].Status != "rate_limited" {
		t.Fatalf("%s", b)
	}
}

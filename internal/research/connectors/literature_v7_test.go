package connectors

import (
	"context"
	research "github.com/wangh00/SciAide/internal/app/research"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRateLimitDoesNotBurstRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(429) }))
	defer server.Close()
	c := newTestClient(server.Client())
	_, _, err := c.get(context.Background(), server.URL, requestOptions{SourceID: "fixture", Host: endpointHost(server.URL)})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v requests=%d", err, calls.Load())
	}
}

func TestEuropePMCRejectsVersionOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"version":"6.9"}`)) }))
	defer server.Close()
	c := newEuropePMC(newTestClient(server.Client()), server.URL)
	if _, err := c.Search(context.Background(), research.SearchOptions{Query: "topic", Limit: 20}); err == nil {
		t.Fatal("invalid response accepted as empty search")
	}
}

func TestOpenAlexNativeRankingAndWildcardMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sort") != "relevance_score:desc" || r.URL.Query().Get("search.exact") != "obes*" || r.URL.Query().Has("search") {
			t.Error(r.URL.String())
		}
		w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	c := newOpenAlex(newTestClient(server.Client()), server.URL)
	if _, err := c.Search(context.Background(), research.SearchOptions{Query: "obes*", Limit: 20}); err != nil {
		t.Fatal(err)
	}
}

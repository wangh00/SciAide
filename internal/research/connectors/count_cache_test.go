package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	research "github.com/wangh00/SciAide/internal/app/research"
)

func TestSearchCountContradictionsDoNotPoisonCache(t *testing.T) {
	for _, source := range []string{"eric", "core"} {
		t.Run(source, func(t *testing.T) {
			valid := map[string]any{"id": 123, "title": "Original title", "abstract": "Original abstract.", "yearPublished": 2025}
			id := "123"
			if source == "eric" {
				id = "EJ1234567"
				valid = map[string]any{"id": id, "title": "Original title", "description": "Original abstract.", "publicationdateyear": 2025}
			}
			for _, tc := range []struct {
				name          string
				offset, total int
				records       []any
			}{
				{"empty-before-total", 0, 1, []any{}},
				{"empty-interior-page", 5, 6, []any{}},
				{"records-with-zero-total", 0, 0, []any{valid}},
				{"source-cannot-override-offset", 0, 1, []any{}},
				{"legacy-count-cache", 0, 1, []any{}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					broken := countCachePage(t, source, tc.total, tc.records)
					if tc.name == "source-cannot-override-offset" {
						var object map[string]any
						if err := json.Unmarshal([]byte(broken), &object); err != nil {
							t.Fatal(err)
						}
						object["searchOffset"], object["SearchOffset"] = 1000, 1000
						encoded, err := json.Marshal(object)
						if err != nil {
							t.Fatal(err)
						}
						broken = string(encoded)
					}
					var calls atomic.Int32
					targets := make(chan string, 3)
					server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
						targets <- "http://" + r.Host + r.URL.RequestURI()
						key := "offset"
						if source == "eric" {
							key = "start"
						}
						if r.URL.Query().Get(key) != strconv.Itoa(tc.offset) {
							t.Error("host offset changed", r.URL.RawQuery)
						}
						if calls.Add(1) == 1 {
							fmt.Fprint(w, broken)
							return
						}
						fmt.Fprint(w, countCachePage(t, source, tc.offset+1, []any{valid}))
					})
					defer server.Close()
					client.attempts = 3
					connector := countCacheConnector(source, client, server.URL+"/records")
					service, err := research.NewService([]research.Connector{connector})
					if err != nil {
						t.Fatal(err)
					}
					command := research.SearchCommand{Query: "peer feedback writing", SourceIDs: []string{source}, Offset: tc.offset, Limit: 5}
					assertCountFailure := func() {
						t.Helper()
						got, err := service.Search(context.Background(), command)
						if err != nil || !got.Partial || len(got.Works) != 0 || got.Sources[0].Status != research.SearchFailed || got.Sources[0].ErrorCode != research.FailureInvalidData || got.Sources[0].Retryable || !strings.Contains(got.Sources[0].Message, "count contradicts") || calls.Load() != 1 {
							t.Fatal("count contradiction changed category or retried", got, err, calls.Load())
						}
						if got.Sources[0].LimitReached {
							t.Fatal("failed source claimed complete result limit")
						}
					}
					assertCountFailure()
					key := <-targets
					if _, exists := client.cached(key); exists {
						t.Fatal("count-contradictory page was cached before rejection")
					}
					if tc.name == "legacy-count-cache" {
						client.storeCache(key, http.StatusOK, http.Header{}, []byte(broken))
						assertCountFailure()
						if _, exists := client.cached(key); exists {
							t.Fatal("legacy contradictory cached page was not evicted")
						}
					}
					for attempt := 0; attempt < 2; attempt++ {
						got, err := service.Search(context.Background(), command)
						if err != nil || got.Partial || len(got.Works) != 1 || got.Sources[0].Status != research.SearchOK || calls.Load() != 2 {
							t.Fatal("explicit same-query recovery/cache failed", got, err, calls.Load())
						}
						work := got.Works[0]
						if work.SourceRecordID != id || work.Title != "Original title" || work.Abstract != "Original abstract." || work.Year != 2025 {
							t.Fatal("valid recovery record changed", work)
						}
					}
				})
			}
			for _, tc := range []struct {
				name          string
				offset, total int
			}{
				{"true-empty", 0, 0},
				{"at-end-of-results", 5, 5},
				{"beyond-end-of-results", 10, 5},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var calls atomic.Int32
					server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						fmt.Fprint(w, countCachePage(t, source, tc.total, []any{}))
					})
					defer server.Close()
					service, err := research.NewService([]research.Connector{countCacheConnector(source, client, server.URL+"/records")})
					if err != nil {
						t.Fatal(err)
					}
					for attempt := 0; attempt < 2; attempt++ {
						got, err := service.Search(context.Background(), research.SearchCommand{Query: "writing", SourceIDs: []string{source}, Offset: tc.offset, Limit: 5})
						if err != nil || got.Partial || len(got.Works) != 0 || got.Sources[0].Status != research.SearchEmpty || calls.Load() != 1 {
							t.Fatal("legitimate empty/end page rejected or not cached", got, err, calls.Load())
						}
					}
				})
			}
		})
	}
}

func countCacheConnector(source string, client *Client, base string) research.Connector {
	if source == "eric" {
		return newERIC(client, base, 0)
	}
	return newCORE(client, base, 0)
}

func countCachePage(t *testing.T, source string, total int, records []any) string {
	t.Helper()
	var value any = map[string]any{"totalHits": total, "results": records}
	if source == "eric" {
		value = map[string]any{"response": map[string]any{"numFound": total, "docs": records}}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

package connectors

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	research "github.com/wangh00/SciAide/internal/app/research"
)

func TestSourceMalformedJSONCannotPoisonSameQueryCache(t *testing.T) {
	for name, broken := range map[string]string{
		"truncated":      `{"message":{"items":[`,
		"trailing-value": `{"message":{"items":[]}} {"error":"broken"}`,
		"wrong-type":     `{"message":{"items":"not an array"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					fmt.Fprint(w, broken)
					return
				}
				fmt.Fprint(w, `{"message":{"items":[{"DOI":"10.5555/cache-recovery","title":["Writing feedback cache recovery"]}]}}`)
			})
			defer server.Close()
			service, err := research.NewService([]research.Connector{newCrossref(client, server.URL)})
			if err != nil {
				t.Fatal(err)
			}
			command := research.SearchCommand{Query: "peer feedback writing", SourceIDs: []string{"crossref"}, Limit: 5}
			first, err := service.Search(context.Background(), command)
			if err != nil || !first.Partial || len(first.Works) != 0 || first.Sources[0].ErrorCode != research.FailureInvalidData || first.Sources[0].Retryable || calls.Load() != 1 {
				t.Fatalf("first malformed response must fail without automatic retry: result=%+v calls=%d err=%v", first, calls.Load(), err)
			}
			second, err := service.Search(context.Background(), command)
			if err != nil || second.Partial || len(second.Works) != 1 || second.Sources[0].Status != research.SearchOK || calls.Load() != 2 {
				t.Fatalf("explicit same-query retry did not reach recovered upstream: result=%+v calls=%d err=%v", second, calls.Load(), err)
			}
			third, err := service.Search(context.Background(), command)
			if err != nil || third.Partial || len(third.Works) != 1 || calls.Load() != 2 {
				t.Fatalf("valid response cache was disabled: result=%+v calls=%d err=%v", third, calls.Load(), err)
			}
		})
	}
}

func TestClientRejectsPreexistingMalformedJSONCacheWithoutAutoRetry(t *testing.T) {
	var calls atomic.Int32
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"ok":true}`)
	})
	defer server.Close()
	client.attempts = 3
	client.storeCache(server.URL, http.StatusOK, http.Header{}, []byte(`{"ok":`))
	options := requestOptions{SourceID: "fixture", Host: endpointHost(server.URL), Cache: true}
	var value syntaxFixtureResponse
	err := client.getJSON(context.Background(), server.URL, options, &value)
	var sourceErr *research.SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureInvalidData || sourceErr.Retryable || calls.Load() != 0 {
		t.Fatalf("bad cached JSON must fail without automatic network retry: err=%v calls=%d", err, calls.Load())
	}
	if _, exists := client.cached(server.URL); exists {
		t.Fatal("rejected cached response survived validation")
	}
	if err := client.getJSON(context.Background(), server.URL, options, &value); err != nil || !value.OK || calls.Load() != 1 {
		t.Fatalf("explicit call could not reach recovered source: err=%v calls=%d", err, calls.Load())
	}
	value.OK = false
	if err := client.getJSON(context.Background(), server.URL, options, &value); err != nil || !value.OK || calls.Load() != 1 {
		t.Fatalf("valid cache no longer works: err=%v calls=%d", err, calls.Load())
	}
}

func TestClientRejectedCacheCannotEvictDifferentNewerResponse(t *testing.T) {
	client := newTestClient(nil)
	const key = "https://fixture.test/search"
	rejected := []byte(`{"ok":`)
	accepted := []byte(`{"ok":true}`)
	client.storeCache(key, http.StatusOK, http.Header{}, rejected)
	stale, _ := client.cached(key)
	client.storeCache(key, http.StatusOK, http.Header{}, accepted)
	client.discardCache(key, stale.body)
	current, exists := client.cached(key)
	if !exists || string(current.body) != string(accepted) {
		t.Fatal("stale rejection removed a different response")
	}
}

func TestClientRejectedJSONCachePreservesSourceHTTPBudget(t *testing.T) {
	client := NewClient()
	transport := &requestBudgetFixture{status: http.StatusOK}
	client.http.Transport = transport
	const target = "https://fixture.test/search"
	client.storeCache(target, http.StatusOK, http.Header{}, []byte(`{"public":`))
	ctx, budget, err := research.WithSourceHTTPBudget(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	options := requestOptions{SourceID: "fixture", Host: "fixture.test", Cache: true}
	var value syntaxFixtureResponse
	var sourceErr *research.SourceError
	err = client.getJSON(ctx, target, options, &value)
	if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureInvalidData {
		t.Fatal("bad cache must retain its decode failure", err)
	}
	if used, denied := budget.Usage(); used != 0 || denied != 0 || transport.calls != 0 {
		t.Fatal("bad-cache failure issued or charged an automatic request", used, denied, transport.calls)
	}
	if err := client.getJSON(ctx, target, options, &value); err != nil || !value.Public {
		t.Fatal("explicit retry did not use the available request", err)
	}
	if err := client.getJSON(ctx, target, options, &value); err != nil {
		t.Fatal("valid cache should work without another budget charge", err)
	}
	if used, denied := budget.Usage(); used != 1 || denied != 0 || transport.calls != 1 {
		t.Fatal("explicit request/cache-hit accounting changed", used, denied, transport.calls)
	}
	err = client.getJSON(ctx, target+"?fresh=1", options, &value)
	if !errors.As(err, &sourceErr) || sourceErr.Code != research.FailureRequestBudget || sourceErr.Retryable {
		t.Fatal("fresh request escaped the exhausted source budget", err)
	}
	if used, denied := budget.Usage(); used != 1 || denied != 1 || transport.calls != 1 {
		t.Fatal("denied request reached transport", used, denied, transport.calls)
	}
}

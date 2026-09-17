package connectors

import (
	"context"
	"net/http"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
)

func TestArXivSendsAllConceptsAndKeepsPageOffset(t *testing.T) {
	requests := 0
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		want := `((all:intermittent AND all:fasting) AND (all:obesity OR all:overweight))`
		if r.URL.Query().Get("search_query") != want || r.URL.Query().Get("start") != "20" {
			t.Error("incorrect provider query", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"></feed>`))
	})
	defer server.Close()
	c := newArXiv(client, server.URL, 0)
	_, err := c.Search(context.Background(), research.SearchOptions{Query: "intermittent fasting AND (obesity OR overweight)", Limit: 20, Offset: 20})
	if err != nil || requests != 1 {
		t.Fatal(err, requests)
	}
	_, err = c.Search(context.Background(), research.SearchOptions{Query: "fasting AND", Limit: 20})
	if err == nil || requests != 1 {
		t.Fatal("invalid query was sent as a broad search", err, requests)
	}
}

package connectors

import (
	"net/http"
	"testing"
)

func TestSourceCredentialDestination(t *testing.T) {
	t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "fixture-secret")
	for _, tc := range []struct {
		url, source string
		want        bool
	}{
		{"https://api.semanticscholar.org/graph/v1/paper/search", "semantic-scholar", true},
		{"http://api.semanticscholar.org/graph/v1/paper/search", "semantic-scholar", false},
		{"https://api.semanticscholar.org.evil.test/search", "semantic-scholar", false},
		{"https://api.semanticscholar.org/search", "openalex", false},
	} {
		r, _ := http.NewRequest("GET", tc.url, nil)
		applySourceCredential(r, tc.source)
		if (r.Header.Get("x-api-key") == "fixture-secret") != tc.want {
			t.Fatal(tc)
		}
		if r.URL.String() != tc.url {
			t.Fatal("credential entered URL")
		}
	}
}

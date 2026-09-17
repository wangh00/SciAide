package httpua

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPoolAndStableHostSelection(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		host := fmt.Sprintf("api%d.example.org", i)
		ua := ForHost(host)
		seen[ua] = true
		if ua != ForHost(strings.ToUpper(host)+".") {
			t.Fatal("host selection unstable")
		}
	}
	if len(seen) != 3 {
		t.Fatalf("pool coverage=%d", len(seen))
	}
	for _, token := range []string{"Firefox/", "Chrome/", "Edg/"} {
		found := false
		for ua := range seen {
			if strings.Contains(ua, token) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s", token)
		}
	}
}

func TestApplyOverridesOnlyUserAgent(t *testing.T) {
	r, _ := http.NewRequest("GET", "https://api.example.org/test", nil)
	r.Header["user-agent"] = []string{"old"}
	r.Header.Set("User-Agent", "custom")
	r.Header.Set("Authorization", "Bearer fixture")
	Apply(r)
	if r.UserAgent() != ForHost("api.example.org") || r.Header.Get("Authorization") != "Bearer fixture" {
		t.Fatal("incorrect headers")
	}
	if _, ok := r.Header["user-agent"]; ok {
		t.Fatal("duplicate UA")
	}
}

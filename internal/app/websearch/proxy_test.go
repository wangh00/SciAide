package websearch

import (
	"context"
	"github.com/wangh00/SciAide/internal/network"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestProxyPersistenceAndPageRouting(t *testing.T) {
	ctx := context.Background()
	secrets := secretstore.NewMemory()
	s := New(secrets)
	policy, _ := network.New(ctx, secrets)
	old := network.Current()
	network.Activate(policy)
	defer network.Activate(old)
	// A stale legacy record must never override the visible application policy.
	if err := secrets.Put(ctx, "websearch/proxy", []byte(`{"enabled":true,"url":"http://127.0.0.1:1"}`)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Host != "example.org" {
			t.Errorf("target=%s", r.URL)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected credentials")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><title>Test</title><body>proxy page</body></html>`))
	}))
	defer proxy.Close()
	cfg := policy.Get()
	cfg.Global = network.Proxy{Mode: "custom", URL: proxy.URL}
	if e := policy.Save(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	reloaded, err := network.New(ctx, secrets)
	if err != nil {
		t.Fatal(err)
	}
	network.Activate(reloaded)
	policy = reloaded
	s = New(secrets)
	p, e := s.Open(ctx, "http://example.org/", "")
	if e != nil || p.Title != "Test" || calls != 1 {
		t.Fatalf("%+v %v calls=%d", p, e, calls)
	}
	if _, e = s.Open(ctx, "http://127.0.0.1/", ""); e == nil {
		t.Fatal("private target accepted")
	}
	if calls != 1 {
		t.Fatal("private target reached proxy")
	}
	for _, raw := range []string{"file:///x", "http://user:pass@localhost:8080", "http://localhost:8080/path"} {
		if e := network.ValidateProxy(network.Proxy{Mode: "custom", URL: raw}, false); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	u, _ := url.Parse("http://169.254.169.254/")
	if validateProxyTarget(u) == nil {
		t.Fatal("metadata accepted")
	}
	cfg = policy.Get()
	cfg.Modules["search"] = network.Proxy{Mode: "direct"}
	if e = policy.Save(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	u, e = s.proxyURL(ctx)
	if e != nil || u != nil {
		t.Fatal("disable ignored")
	}
}

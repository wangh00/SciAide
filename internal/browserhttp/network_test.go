package browserhttp

import (
	"context"
	"github.com/wangh00/SciAide/internal/network"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApplicationProxyAndPinnedUA(t *testing.T) {
	ctx := context.Background()
	s, _ := network.New(ctx, secretstore.NewMemory())
	old := network.Current()
	network.Activate(s)
	defer network.Activate(old)
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.UserAgent() != "Browser/session-UA" {
			t.Errorf("UA overwritten: %s", r.UserAgent())
		}
		w.Write([]byte("proxied"))
	}))
	defer proxy.Close()
	cfg := s.Get()
	cfg.Global = network.Proxy{Mode: "custom", URL: proxy.URL}
	if e := s.Save(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	tr := NewScoped(http.DefaultTransport.(*http.Transport).Clone(), "research")
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: WithSessionUA(tr, "Browser/session-UA")}
	r, e := client.Get("http://paper.invalid/pdf")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if calls != 1 {
		t.Fatal(calls)
	}
	proxy.Close()
	if r, e = client.Get("http://paper.invalid/pdf"); e == nil {
		r.Body.Close()
		t.Fatal("failed proxy silently bypassed")
	}
}

func TestExistingClientSwitchesProxyAfterSave(t *testing.T) {
	ctx := context.Background()
	s, _ := network.New(ctx, secretstore.NewMemory())
	old := network.Current()
	network.Activate(s)
	defer network.Activate(old)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("first")) }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("second")) }))
	defer second.Close()
	tr := NewScoped(http.DefaultTransport.(*http.Transport).Clone(), "research")
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	for _, target := range []struct{ url, want string }{{first.URL, "first"}, {second.URL, "second"}} {
		cfg := s.Get()
		cfg.Global = network.Proxy{Mode: "custom", URL: target.url}
		if err := s.Save(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		res, err := client.Get("http://paper.invalid/test")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || string(body) != target.want {
			t.Fatalf("proxy not refreshed: %s %v", body, err)
		}
	}
}

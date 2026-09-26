package network

import (
	"context"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"net/http"
	"strings"
	"testing"
)

func TestPrecedencePersistenceAndEnvironment(t *testing.T) {
	ctx := context.Background()
	secrets := secretstore.NewMemory()
	s, e := New(ctx, secrets)
	if e != nil {
		t.Fatal(e)
	}
	old := Current()
	Activate(s)
	defer Activate(old)
	c := s.Get()
	c.Global = Proxy{Mode: "custom", URL: "http://127.0.0.1:7890"}
	c.Modules["model"] = Proxy{Mode: "direct"}
	c.Modules["research"] = Proxy{Mode: "custom", URL: "socks5://127.0.0.1:1080"}
	if e = s.Save(ctx, c); e != nil {
		t.Fatal(e)
	}
	if s.Save(ctx, c) == nil {
		t.Fatal("stale save accepted")
	}
	for m, want := range map[string]string{"model": "", "research": "socks5://127.0.0.1:1080", "search": "http://127.0.0.1:7890"} {
		p, _ := s.Resolve(m)
		if p.URL != want {
			t.Fatalf("%s %+v", m, p)
		}
	}
	restart, e := New(ctx, secrets)
	if e != nil || restart.Get().Revision != 1 {
		t.Fatal(e)
	}
	r, _ := http.NewRequest("GET", "http://localhost:8000", nil)
	u, e := ProxyFunc("research")(r)
	if e != nil || u != nil {
		t.Fatal("loopback proxied")
	}
	env := Environment([]string{"PATH=x", "HTTP_PROXY=http://wrong", "ALL_PROXY=socks5://wrong", "no_proxy=*"}, "search")
	all := strings.Join(env, "\n")
	if strings.Contains(all, "wrong") || !strings.Contains(all, "HTTP_PROXY=http://127.0.0.1:7890") || strings.Contains(all, "no_proxy=*") {
		t.Fatal(all)
	}
	direct := strings.Join(PinnedEnvironment(env, Proxy{Mode: "direct"}), "\n")
	if strings.Contains(direct, "7890") || !strings.Contains(direct, "NO_PROXY=*") {
		t.Fatal(direct)
	}
}
func TestInvalidProxy(t *testing.T) {
	for _, u := range []string{"file:///tmp", "http://user:secret@host:80", "http://host/path", "http://host/?q=x", "http://"} {
		if ValidateProxy(Proxy{Mode: "custom", URL: u}, true) == nil {
			t.Fatal(u)
		}
	}
}

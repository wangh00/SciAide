package websearch

import (
	"context"
	"net"
	"os"
	"testing"
)

func TestFakeIPRevalidation(t *testing.T) {
	for _, tc := range []struct {
		host, ip, fallback string
		ok, called         bool
	}{
		{"example.org", "198.18.2.226", "104.16.10.1", true, true},
		{"example.org", "198.19.0.1", "104.16.10.1", true, true},
		{"example.org", "104.16.10.1", "", true, false},
		{"198.18.2.226", "198.18.2.226", "104.16.10.1", false, false},
		{"example.org", "127.0.0.1", "104.16.10.1", false, false},
		{"example.org", "10.0.0.1", "104.16.10.1", false, false},
		{"example.org", "198.18.0.1", "127.0.0.1", false, true},
		{"example.org", "198.18.0.1", "198.18.0.2", false, true},
	} {
		called := false
		_, err := validatedPageIPs(context.Background(), tc.host, []net.IPAddr{{IP: net.ParseIP(tc.ip)}}, func(context.Context, string) ([]net.IPAddr, error) {
			called = true
			return []net.IPAddr{{IP: net.ParseIP(tc.fallback)}}, nil
		})
		if (err == nil) != tc.ok || called != tc.called {
			t.Errorf("%+v: called=%v err=%v", tc, called, err)
		}
	}
}

func TestTUNDocumentationLive(t *testing.T) {
	if os.Getenv("SCIAIDE_TEST_TUN") != "1" {
		t.Skip("opt-in external network verification")
	}
	p, err := Open(context.Background(), "https://curl-cffi.readthedocs.io/en/latest/", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Text) == 0 {
		t.Fatal("empty document")
	}
	t.Logf("url=%s title=%s chars=%d", p.URL, p.Title, len([]rune(p.Text)))
}

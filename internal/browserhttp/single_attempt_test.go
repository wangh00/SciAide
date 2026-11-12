package browserhttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/network"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
)

type singleAttemptTestAdapter struct{ *Transport }

func (t singleAttemptTestAdapter) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.RoundTripSingleAttempt(r)
}

func TestSingleAttemptDoesNotReplayDroppedRequestOrNegotiateHTTP2(t *testing.T) {
	var warm, drop atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 || (r.TLS.NegotiatedProtocol != "" && r.TLS.NegotiatedProtocol != "http/1.1") {
			t.Errorf("unexpected protocol %s/%s", r.Proto, r.TLS.NegotiatedProtocol)
		}
		if r.URL.Path == "/drop" {
			drop.Add(1)
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				c.Close()
			}
			return
		}
		warm.Add(1)
		fmt.Fprint(w, "warm")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	tr := NewScoped(&http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, "research")
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: singleAttemptTestAdapter{tr}, Timeout: 3 * time.Second}
	r, err := client.Get(server.URL + "/warm")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r, err = client.Get(server.URL + "/drop"); err == nil {
		r.Body.Close()
		t.Fatal("dropped response unexpectedly succeeded")
	}
	if warm.Load() != 1 || drop.Load() != 1 {
		t.Fatal("hidden request replay", warm.Load(), drop.Load())
	}
}

func TestSingleAttemptProxyRefreshCONNECTAndCertificateValidation(t *testing.T) {
	var received atomic.Int32
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.ProtoMajor != 1 {
			t.Errorf("negotiated %s", r.Proto)
		}
		fmt.Fprint(w, "source")
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	defer origin.Close()
	var first, second atomic.Int32
	proxy := func(count *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "CONNECT" || r.Host != "example.com:443" {
				http.Error(w, "unexpected proxy request", 400)
				return
			}
			count.Add(1)
			up, err := net.DialTimeout("tcp", origin.Listener.Addr().String(), time.Second)
			if err != nil {
				http.Error(w, "connect failed", 502)
				return
			}
			down, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				up.Close()
				return
			}
			fmt.Fprint(down, "HTTP/1.1 200 Connection Established\r\n\r\n")
			go func() { defer up.Close(); io.Copy(up, down) }()
			io.Copy(down, up)
			down.Close()
		}))
	}
	one, two := proxy(&first), proxy(&second)
	defer one.Close()
	defer two.Close()
	settings, _ := network.New(context.Background(), secretstore.NewMemory())
	old := network.Current()
	network.Activate(settings)
	defer network.Activate(old)
	pool := x509.NewCertPool()
	pool.AddCert(origin.Certificate())
	base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("direct dial forbidden")
	}}
	tr := NewScoped(base, "research")
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: singleAttemptTestAdapter{tr}, Timeout: 3 * time.Second}
	for _, url := range []string{one.URL, two.URL} {
		cfg := settings.Get()
		cfg.Global = network.Proxy{Mode: "custom", URL: url}
		if err := settings.Save(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		r, err := client.Get("https://example.com/search")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	if first.Load() != 1 || second.Load() != 1 || received.Load() != 2 {
		t.Fatal(first.Load(), second.Load(), received.Load())
	}
	untrusted := NewScoped(&http.Transport{}, "research")
	defer untrusted.CloseIdleConnections()
	bad := &http.Client{Transport: singleAttemptTestAdapter{untrusted}, Timeout: 3 * time.Second}
	if r, err := bad.Get("https://example.com/search"); err == nil {
		r.Body.Close()
		t.Fatal("untrusted source certificate accepted")
	}
	if received.Load() != 2 {
		t.Fatal("untrusted handshake reached source application")
	}
}

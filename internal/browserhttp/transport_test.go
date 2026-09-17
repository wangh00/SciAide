package browserhttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProfilesNegotiateHTTP2AndVerifyCertificates(t *testing.T) {
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	s.EnableHTTP2 = true
	s.StartTLS()
	defer s.Close()
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	for profile := 0; profile < 3; profile++ {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.Proxy = nil
		base.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "example.com"}
		base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, s.Listener.Addr().String())
		}
		// The fixture certificate includes 127.0.0.1; use its URL and select the profile directly.
		tr := New(base)
		defer tr.CloseIdleConnections()
		c := &http.Client{Transport: tr.profiles[profile], Timeout: 5 * time.Second}
		r, err := c.Get(s.URL)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.ProtoMajor != 2 {
			t.Fatalf("profile %d protocol=%s", profile, r.Proto)
		}
		base.TLSClientConfig = &tls.Config{}
		bad := New(base)
		defer bad.CloseIdleConnections()
		c.Transport = bad.profiles[profile]
		if r, err = c.Get(s.URL); err == nil {
			r.Body.Close()
			t.Fatal("untrusted certificate accepted")
		}
	}
}

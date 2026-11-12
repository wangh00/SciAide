package modelutil_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/modelutil"
)

func TestModelTLSConstructorUsesCountableSingleAttempt(t *testing.T) {
	var received atomic.Int32
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.ProtoMajor != 1 {
			t.Error("model constructor negotiated HTTP2")
			return
		}
		io.Copy(io.Discard, r.Body)
		conn, _, e := w.(http.Hijacker).Hijack()
		if e == nil {
			conn.Close()
		}
	}))
	s.EnableHTTP2 = true
	s.StartTLS()
	defer s.Close()
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	prior := http.DefaultTransport
	fixtureBase := prior.(*http.Transport).Clone()
	fixtureBase.TLSClientConfig = &tls.Config{RootCAs: pool}
	http.DefaultTransport = fixtureBase
	defer func() { http.DefaultTransport = prior; fixtureBase.CloseIdleConnections() }()
	var admissions atomic.Int32
	c := modelutil.WithRequestGate(modelutil.NewStreamingHTTPClient(time.Second), func(context.Context) error { admissions.Add(1); return nil })
	defer c.CloseIdleConnections()
	for i, method := range []string{"POST", "GET"} {
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader(`{"fixture":true}`)
		}
		req, _ := http.NewRequest(method, s.URL, body)
		resp, e := c.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		if e == nil {
			t.Fatal("broken response treated as success")
		}
		if admissions.Load() != int32(i+1) || received.Load() != int32(i+1) {
			t.Fatalf("hidden TLS request replay: admissions=%d received=%d", admissions.Load(), received.Load())
		}
	}
}

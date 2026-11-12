package browserhttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestSetupClaimByAnotherRequestClearsDeadline(t *testing.T) {
	m := newConnectionSetup(30 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner := &setupRequest{manager: m, ctx: ctx}
	left, right := net.Pipe()
	defer right.Close()
	conn, e := m.dial(func(context.Context, string, string) (net.Conn, error) { return left, nil })(context.WithValue(ctx, setupKey{}, owner), "tcp", "fixture")
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	// B acquires A's socket; clearing it is transport-wide, not owner-specific.
	m.finish(conn)
	cancel()
	owner.cancelPending()
	time.Sleep(50 * time.Millisecond)
	done := make(chan error, 1)
	go func() { _, e := conn.Write([]byte("ok")); done <- e }()
	right.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 2)
	if _, e = io.ReadFull(right, b); e != nil {
		t.Fatal("claimed connection was closed or retained setup deadline", e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
func TestCancelledLateDialCannotEscapeSetup(t *testing.T) {
	m := newConnectionSetup(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	owner := &setupRequest{manager: m, ctx: ctx}
	left, right := net.Pipe()
	defer right.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := m.dial(func(context.Context, string, string) (net.Conn, error) { close(started); <-release; return left, nil })(context.WithValue(ctx, setupKey{}, owner), "tcp", "fixture")
		done <- e
	}()
	<-started
	cancel()
	owner.cancelPending()
	close(release)
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal("late dial lost cancellation", e)
	}
	right.SetReadDeadline(time.Now().Add(time.Second))
	if _, e := right.Read(make([]byte, 1)); e != io.EOF {
		t.Fatal("late socket leaked", e)
	}
}

func tlsProxy(t *testing.T, target string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" {
			w.WriteHeader(400)
			return
		}
		up, e := net.DialTimeout("tcp", target, time.Second)
		if e != nil {
			w.WriteHeader(502)
			return
		}
		defer up.Close()
		down, buf, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		defer down.Close()
		buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buf.Flush()
		go io.Copy(up, buf)
		io.Copy(down, up)
	}))
	return s
}
func TestIPv6FingerprintDirectAndProxyVerification(t *testing.T) {
	ln, e := net.Listen("tcp6", "[::1]:0")
	if e != nil {
		t.Skip("IPv6 loopback unavailable")
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	s.Listener.Close()
	s.Listener = ln
	s.EnableHTTP2 = true
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	defer s.Close()
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	proxy := tlsProxy(t, ln.Addr().String())
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	for _, viaProxy := range []bool{false, true} {
		for _, mode := range []string{"trusted", "wrong-ip", "untrusted"} {
			name := "direct/" + mode
			if viaProxy {
				name = "proxy/" + mode
			}
			t.Run(name, func(t *testing.T) {
				host := "::1"
				if mode == "wrong-ip" {
					host = "::2"
				}
				target := net.JoinHostPort(host, port)
				base := http.DefaultTransport.(*http.Transport).Clone()
				base.Proxy = nil
				base.TLSClientConfig = &tls.Config{RootCAs: roots}
				if mode == "untrusted" {
					base.TLSClientConfig = &tls.Config{RootCAs: x509.NewCertPool()}
				}
				if viaProxy {
					base.Proxy = func(*http.Request) (*url.URL, error) { return pu, nil }
				}
				base.DialContext = func(ctx context.Context, n, addr string) (net.Conn, error) {
					if addr == target {
						return (&net.Dialer{}).DialContext(ctx, "tcp6", ln.Addr().String())
					}
					if viaProxy && addr == pu.Host {
						return (&net.Dialer{}).DialContext(ctx, "tcp", pu.Host)
					}
					return nil, errors.New("unexpected fixture address")
				}
				tr := NewFixed(base)
				defer tr.CloseIdleConnections()
				c := &http.Client{Transport: tr, Timeout: 2 * time.Second}
				resp, e := c.Get("https://" + target)
				if resp != nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				if mode == "trusted" {
					if e != nil {
						t.Fatal("trusted IPv6 failed", e)
					}
				} else if e == nil {
					t.Fatal("certificate validation bypassed")
				}
			})
		}
	}
}
func TestSetupDeadlineDoesNotCutHealthyTLSHTTP2Body(t *testing.T) {
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		time.Sleep(120 * time.Millisecond)
		io.WriteString(w, "data: healthy\n\n")
	}))
	s.EnableHTTP2 = true
	s.StartTLS()
	defer s.Close()
	base := s.Client().Transport.(*http.Transport).Clone()
	base.TLSHandshakeTimeout = 80 * time.Millisecond
	base.ResponseHeaderTimeout = 80 * time.Millisecond
	for _, single := range []bool{false, true} {
		t.Run(map[bool]string{false: "browser-h2", true: "single-h1"}[single], func(t *testing.T) {
			tr := newFixed(base, single)
			defer tr.CloseIdleConnections()
			c := &http.Client{Transport: tr, Timeout: time.Second}
			r, e := c.Get(s.URL)
			if e != nil {
				t.Fatal(e)
			}
			b, e := io.ReadAll(r.Body)
			r.Body.Close()
			if e != nil || string(b) != "data: healthy\n\n" {
				t.Fatal("setup timeout cut healthy body", e)
			}
			if single && r.ProtoMajor != 1 {
				t.Fatal("single-attempt path negotiated HTTP2")
			}
			if !single && r.ProtoMajor != 2 {
				t.Fatal("browser path lost HTTP2")
			}
		})
	}
}

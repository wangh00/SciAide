package modelutil_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/modelutil"
	"github.com/wangh00/SciAide/internal/network"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
)

func stalledModelProxy(t *testing.T, scheme string) <-chan net.Conn {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ln.Close() })
	ready := make(chan net.Conn, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			return
		}
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if scheme == "socks5" {
			b := make([]byte, 3)
			_, e = io.ReadFull(c, b)
		} else {
			br := bufio.NewReader(c)
			for {
				var line string
				line, e = br.ReadString('\n')
				if e != nil || line == "\r\n" {
					break
				}
			}
		}
		if e != nil {
			c.Close()
			return
		}
		c.SetReadDeadline(time.Time{})
		ready <- c
	}()
	service, e := network.New(context.Background(), secretstore.NewMemory())
	if e != nil {
		t.Fatal(e)
	}
	cfg := service.Get()
	cfg.Modules["model"] = network.Proxy{Mode: "custom", URL: scheme + "://" + ln.Addr().String()}
	if e = service.Save(context.Background(), cfg); e != nil {
		t.Fatal(e)
	}
	old := network.Current()
	network.Activate(service)
	t.Cleanup(func() { network.Activate(old) })
	return ready
}
func receiveProxy(t *testing.T, ch <-chan net.Conn) net.Conn {
	t.Helper()
	select {
	case c := <-ch:
		t.Cleanup(func() { c.Close() })
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("local proxy not reached")
		return nil
	}
}
func assertProxyClosed(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(time.Second))
	_, e := c.Read(make([]byte, 1))
	if e == nil {
		t.Fatal("proxy remained writable after setup ended")
	}
	var timeout net.Error
	if errors.As(e, &timeout) && timeout.Timeout() {
		t.Fatal("abandoned proxy connection not reclaimed")
	}
}
func TestModelProxySetupBoundAndCancellation(t *testing.T) {
	for _, scheme := range []string{"http", "socks5"} {
		for _, cancelEarly := range []bool{false, true} {
			name := scheme + "/timeout"
			if cancelEarly {
				name = scheme + "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				ready := stalledModelProxy(t, scheme)
				wait := 80 * time.Millisecond
				if cancelEarly {
					wait = 3 * time.Second
				}
				client := modelutil.NewStreamingHTTPClient(wait)
				client = modelutil.WithRequestGate(client, func(context.Context) error { return nil })
				defer client.CloseIdleConnections()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				req, _ := http.NewRequestWithContext(ctx, "POST", "https://proxy-fixture.invalid/v1/responses", strings.NewReader(`{}`))
				done := make(chan error, 1)
				go func() {
					r, e := client.Do(req)
					if r != nil {
						r.Body.Close()
					}
					done <- e
				}()
				peer := receiveProxy(t, ready)
				if cancelEarly {
					// CloseIdleConnections must not terminate an active setup.
					client.CloseIdleConnections()
					select {
					case e := <-done:
						t.Fatalf("idle cleanup interrupted active setup: %v", e)
					case <-time.After(30 * time.Millisecond):
					}
					cancel()
				}
				select {
				case e := <-done:
					if e == nil {
						t.Fatal("stalled proxy succeeded")
					}
					if cancelEarly && !errors.Is(e, context.Canceled) {
						t.Fatalf("cancel identity lost: %v", e)
					}
					if !cancelEarly {
						var ne net.Error
						if !errors.As(e, &ne) || !ne.Timeout() {
							t.Fatalf("setup timeout identity lost: %v", e)
						}
					}
				case <-time.After(time.Second):
					t.Fatal("model setup exceeded configured bound")
				}
				assertProxyClosed(t, peer)
			})
		}
	}
}

type cleanupTransport struct{ calls atomic.Int32 }

func (t *cleanupTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("fixture")
}
func (t *cleanupTransport) CloseIdleConnections() { t.calls.Add(1) }
func TestRequestGateForwardsIdleCleanup(t *testing.T) {
	base := &cleanupTransport{}
	client := modelutil.WithRequestGate(&http.Client{Transport: base}, func(context.Context) error { return nil })
	client.CloseIdleConnections()
	if base.calls.Load() != 1 {
		t.Fatal("gate swallowed cleanup")
	}
}

func TestModelSingleAttemptAndRedirectAdmission(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			var received, admitted atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				if r.ProtoMajor != 1 {
					t.Error("model request must use HTTP/1.1")
				}
				io.Copy(io.Discard, r.Body)
				c, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					c.Close()
				}
			}))
			defer server.Close()
			denied := errors.New("fixture admission exhausted")
			client := modelutil.WithRequestGate(modelutil.NewStreamingHTTPClient(time.Second), func(context.Context) error {
				if admitted.Add(1) > 1 {
					return denied
				}
				return nil
			})
			defer client.CloseIdleConnections()
			req, _ := http.NewRequest(method, server.URL, strings.NewReader("fixture"))
			if method == "GET" {
				req.Body = nil
				req.GetBody = nil
				req.ContentLength = 0
			}
			if resp, e := client.Do(req); e == nil {
				resp.Body.Close()
				t.Fatal("broken response treated as success")
			}
			if admitted.Load() != 1 || received.Load() != 1 {
				t.Fatalf("hidden replay: gate=%d received=%d", admitted.Load(), received.Load())
			}
			req, _ = http.NewRequest(method, server.URL, strings.NewReader("fixture"))
			_, e := client.Do(req)
			if !errors.Is(e, denied) || received.Load() != 1 {
				t.Fatal("explicit second attempt bypassed gate", e)
			}
		})
	}
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var received, admitted atomic.Int32
			var sourcePort atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				_, port, _ := net.SplitHostPort(r.RemoteAddr)
				if r.URL.Path == "/start" {
					sourcePort.Store(port)
					w.Header().Set("Location", "/finish")
					w.WriteHeader(status)
					return
				}
				if port == sourcePort.Load() {
					t.Error("model request reused a connection")
				}
				b, _ := io.ReadAll(r.Body)
				if status == 307 || status == 308 {
					if r.Method != "POST" || string(b) != "fixture" {
						t.Error("redirect lost POST body")
					}
				} else if r.Method != "GET" || len(b) != 0 {
					t.Error("redirect method semantics changed")
				}
				io.WriteString(w, "ok")
			}))
			defer server.Close()
			client := modelutil.WithRequestGate(modelutil.NewStreamingHTTPClient(time.Second), func(context.Context) error { admitted.Add(1); return nil })
			defer client.CloseIdleConnections()
			resp, e := client.Post(server.URL+"/start", "application/json", strings.NewReader("fixture"))
			if e != nil {
				t.Fatal(e)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if admitted.Load() != 2 || received.Load() != 2 {
				t.Fatal("redirect bypassed admission")
			}
		})
	}
}

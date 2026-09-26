// Package browserhttp provides browser TLS and HTTP/2 profiles without changing
// application retry, redirect, timeout, or response streaming policy.
package browserhttp

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/imroc/req/v3"
	"github.com/wangh00/SciAide/internal/httpua"
	"github.com/wangh00/SciAide/internal/network"
)

type Transport struct {
	profiles [3]*req.Transport
	mu       sync.Mutex
	base     *http.Transport
	module   string
	revision uint64
}

func New(base *http.Transport) *Transport { return NewScoped(base, "") }
func pinnedBase(base *http.Transport, module string, p network.Proxy) *http.Transport {
	base = base.Clone()
	base.Proxy = nil
	if p.Mode == "custom" {
		u, _ := url.Parse(p.URL)
		base.Proxy = func(r *http.Request) (*url.URL, error) {
			if network.IsLoopback(r.URL.Hostname()) {
				return nil, nil
			}
			return u, nil
		}
		oldDial := base.DialContext
		if oldDial != nil {
			base.DialContext = func(ctx context.Context, n, addr string) (net.Conn, error) {
				proxyHost := u.Host
				if u.Port() == "" {
					port := "80"
					if u.Scheme == "https" {
						port = "443"
					} else if strings.HasPrefix(u.Scheme, "socks") {
						port = "1080"
					}
					proxyHost = net.JoinHostPort(u.Hostname(), port)
				}
				if strings.EqualFold(addr, proxyHost) {
					return (&net.Dialer{}).DialContext(ctx, n, addr)
				}
				return oldDial(ctx, n, addr)
			}
		}
	}
	return base
}
func NewScoped(base *http.Transport, module string) *Transport {
	p, revision := network.Resolve(module)
	t := NewFixed(pinnedBase(base, module, p))
	t.base = base.Clone()
	t.module = module
	t.revision = revision
	return t
}

// NewFixed preserves an explicitly resolved proxy for an entire browser-bound request.
func NewFixed(base *http.Transport) *Transport {
	t := &Transport{}
	for i := range t.profiles {
		c := req.C()
		switch i {
		case 1:
			c.ImpersonateFirefox()
		case 2:
			c.ImpersonateChrome().SetTLSFingerprintEdge()
		default:
			c.ImpersonateChrome()
		}
		p := c.GetTransport()
		p.Proxy = base.Proxy
		p.DialContext = base.DialContext
		p.TLSHandshakeTimeout = base.TLSHandshakeTimeout
		p.ResponseHeaderTimeout = base.ResponseHeaderTimeout
		p.IdleConnTimeout = base.IdleConnTimeout
		p.MaxIdleConns = base.MaxIdleConns
		p.MaxIdleConnsPerHost = base.MaxIdleConnsPerHost
		p.MaxConnsPerHost = base.MaxConnsPerHost
		p.DisableCompression = base.DisableCompression
		if base.TLSClientConfig != nil {
			c.SetTLSClientConfig(base.TLSClientConfig.Clone())
		}
		t.profiles[i] = p
	}
	return t
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	index := httpua.Index(c.URL.Hostname())
	if ua, ok := c.Context().Value(sessionUAKey{}).(string); ok {
		c.Header.Set("User-Agent", ua)
		index = 0
	} else {
		httpua.Apply(c)
	}
	t.mu.Lock()
	if t.base != nil {
		p, revision := network.Resolve(t.module)
		if revision != t.revision {
			previous := t.profiles
			t.profiles = NewFixed(pinnedBase(t.base, t.module, p)).profiles
			t.revision = revision
			for _, tr := range previous {
				tr.CloseIdleConnections()
			}
		}
	}
	profile := t.profiles[index]
	t.mu.Unlock()
	return profile.RoundTrip(c)
}

func (t *Transport) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.profiles {
		p.CloseIdleConnections()
	}
}

type sessionUAKey struct{}
type sessionTransport struct {
	base http.RoundTripper
	ua   string
}

func WithSessionUA(base http.RoundTripper, ua string) http.RoundTripper {
	return sessionTransport{base, ua}
}
func (t sessionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(context.WithValue(r.Context(), sessionUAKey{}, t.ua))
	r.Header.Set("User-Agent", t.ua)
	return t.base.RoundTrip(r)
}

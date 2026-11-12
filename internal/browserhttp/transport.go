// Package browserhttp provides browser profiles and explicitly single-attempt
// standard transports with bounded connection establishment, not body deadlines.
package browserhttp

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/imroc/req/v3"
	"github.com/wangh00/SciAide/internal/httpua"
	"github.com/wangh00/SciAide/internal/network"
)

type Transport struct {
	profiles [3]*req.Transport
	standard *http.Transport
	setup    *connectionSetup
	single   *http.Transport
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
					return (&net.Dialer{Timeout: setupTimeout(base), KeepAlive: 30 * time.Second}).DialContext(ctx, n, addr)
				}
				return oldDial(ctx, n, addr)
			}
		}
	}
	return base
}
func NewScoped(base *http.Transport, module string) *Transport {
	return newScoped(base, module, false)
}

// NewSingleAttemptScoped uses standard TLS + HTTP/1.1 on fresh connections.
// No implicit connection-reuse/HTTP2 request replay may bypass host admission.
// Proxy selection, establishment bounds and live config refresh are shared.
func NewSingleAttemptScoped(base *http.Transport, module string) *Transport {
	return newScoped(base, module, true)
}
func newScoped(base *http.Transport, module string, standard bool) *Transport {
	p, revision := network.Resolve(module)
	t := newFixed(pinnedBase(base, module, p), standard)
	t.base = base.Clone()
	t.module = module
	t.revision = revision
	return t
}

// NewFixed preserves an explicitly resolved proxy for an entire browser-bound request.
func NewFixed(base *http.Transport) *Transport {
	return newFixed(base, false)
}
func newFixed(base *http.Transport, standard bool) *Transport {
	base = base.Clone()
	setup := newConnectionSetup(setupTimeout(base))
	// Lifecycle ownership is request-scoped only on the non-reusing path.
	// Browser pools may hand an in-flight dial to a different request before
	// GotConn; do not cancel those shared dials with the originating request.
	singleBase := base.Clone()
	singleBase.DialContext = setup.dial(base.DialContext)
	t := &Transport{single: newSingleAttemptTransport(singleBase), setup: setup}
	if standard {
		t.standard = t.single
		return t
	}
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
		// req/v3 strips a final colon segment even when its input is already
		// an unported IPv6 literal. Adapt only that case; never skip verification.
		handshake := p.TLSHandshakeContext
		p.TLSHandshakeContext = func(ctx context.Context, addr string, plain net.Conn) (net.Conn, *tls.ConnectionState, error) {
			host := addr
			if h, _, err := net.SplitHostPort(addr); err == nil {
				host = h
			}
			if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
				host = host[1 : len(host)-1]
			}
			if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() {
				addr = ip.WithZone("").String() + ":0"
			}
			return handshake(ctx, addr, plain)
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
	t.refreshLocked()
	var profile http.RoundTripper = t.profiles[index]
	if t.standard != nil {
		profile = t.standard
	}
	setup := t.setup
	single := t.standard != nil
	t.mu.Unlock()
	if single {
		return setup.roundTrip(c, profile)
	}
	return profile.RoundTrip(c)
}

func (t *Transport) refreshLocked() {
	if t.base != nil {
		p, revision := network.Resolve(t.module)
		if revision != t.revision {
			previous := t.profiles
			replacement := newFixed(pinnedBase(t.base, t.module, p), t.standard != nil)
			t.profiles = replacement.profiles
			if t.standard != nil {
				t.standard.CloseIdleConnections()
			}
			t.standard = replacement.standard
			t.setup = replacement.setup
			t.single.CloseIdleConnections()
			t.single = replacement.single
			t.revision = revision
			for _, tr := range previous {
				if tr != nil {
					tr.CloseIdleConnections()
				}
			}
		}
	}
}

func (t *Transport) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.profiles {
		if p != nil {
			p.CloseIdleConnections()
		}
	}
	if t.standard != nil {
		t.standard.CloseIdleConnections()
	}
	t.single.CloseIdleConnections()
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

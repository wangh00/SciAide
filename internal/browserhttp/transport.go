// Package browserhttp provides browser TLS and HTTP/2 profiles without changing
// application retry, redirect, timeout, or response streaming policy.
package browserhttp

import (
	"net/http"

	"github.com/imroc/req/v3"
	"github.com/wangh00/SciAide/internal/httpua"
)

type Transport struct{ profiles [3]*req.Transport }

func New(base *http.Transport) *Transport {
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
	httpua.Apply(c)
	return t.profiles[httpua.Index(c.URL.Hostname())].RoundTrip(c)
}

func (t *Transport) CloseIdleConnections() {
	for _, p := range t.profiles {
		p.CloseIdleConnections()
	}
}

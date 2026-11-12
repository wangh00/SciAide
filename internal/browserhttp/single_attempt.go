package browserhttp

import (
	"crypto/tls"
	"net/http"
)

// Bounded source APIs deliberately use standard TLS + HTTP/1.1, not a browser
// fingerprint. A fresh connection prevents hidden GET replays below RoundTrip.
// Do not Clone registered alternate protocols or custom TLS dialers into this
// transport. Certificate policy, proxy resolution and scoped dialing are kept.
func newSingleAttemptTransport(base *http.Transport) *http.Transport {
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	tlsConfig := &tls.Config{}
	if base.TLSClientConfig != nil {
		tlsConfig = base.TLSClientConfig.Clone()
	}
	tlsConfig.NextProtos = []string{"http/1.1"}
	return &http.Transport{
		Proxy: base.Proxy, DialContext: base.DialContext,
		TLSClientConfig: tlsConfig, TLSHandshakeTimeout: base.TLSHandshakeTimeout,
		ResponseHeaderTimeout: base.ResponseHeaderTimeout, ExpectContinueTimeout: base.ExpectContinueTimeout,
		MaxResponseHeaderBytes: base.MaxResponseHeaderBytes, DisableCompression: base.DisableCompression,
		MaxConnsPerHost: base.MaxConnsPerHost, ProxyConnectHeader: base.ProxyConnectHeader.Clone(),
		GetProxyConnectHeader: base.GetProxyConnectHeader,
		DisableKeepAlives:     true, ForceAttemptHTTP2: false, Protocols: protocols,
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}

func (t *Transport) RoundTripSingleAttempt(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	c.Header = r.Header.Clone()
	t.mu.Lock()
	t.refreshLocked()
	transport := t.single
	setup := t.setup
	t.mu.Unlock()
	return setup.roundTrip(c, transport)
}

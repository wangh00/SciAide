package websearch

import (
	"context"
	"github.com/wangh00/SciAide/internal/network"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Search and page reads share the application's single search-module policy.
// Legacy websearch/proxy records are deliberately not consulted.
func (s *Service) proxyURL(ctx context.Context) (*url.URL, error) {
	return network.URL("search")
}

// An explicitly configured proxy resolves public hostnames remotely. The proxy
// is a trusted network boundary; literal/private target addresses remain denied.
func validateProxyTarget(u *url.URL) error {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return errNonPublicAddress
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return errNonPublicAddress
	}
	return nil
}
func (s *Service) Open(ctx context.Context, raw, find string) (Page, error) {
	u, e := s.proxyURL(ctx)
	if e != nil {
		return Page{}, e
	}
	return openPage(ctx, raw, find, u)
}
func proxyTransport(proxy *url.URL) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	if proxy != nil {
		base.Proxy = http.ProxyURL(proxy)
	}
	return base
}

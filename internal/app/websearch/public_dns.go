package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wangh00/SciAide/internal/httpua"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

var errFakeIPResolution = errors.New("检测到 TUN/Fake-IP，但公网 DNS 核验未成功；请在设置的网络与代理中配置代理软件的 HTTP/SOCKS 端口，无需关闭 TUN")

func fakeIP(ip net.IP) bool {
	_, block, _ := net.ParseCIDR("198.18.0.0/15")
	return block.Contains(ip)
}

// Never connect to a Fake-IP directly or exempt it from SSRF protection.
// Re-resolve domain names through certificate-verified, IP-pinned DoH, then
// dial only validated public addresses. The original HTTPS host/SNI is retained.
func validatedPageIPs(ctx context.Context, host string, ips []net.IPAddr, resolve func(context.Context, string) ([]net.IPAddr, error)) ([]net.IPAddr, error) {
	needsFallback := false
	for _, ip := range ips {
		if fakeIP(ip.IP) && net.ParseIP(host) == nil {
			needsFallback = true
			continue
		}
		if !publicIP(ip.IP) {
			return nil, errNonPublicAddress
		}
	}
	if needsFallback {
		var err error
		ips, err = resolve(ctx, host)
		if err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, errNonPublicAddress
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, errNonPublicAddress
		}
	}
	return ips, nil
}

func resolvePublicDNS(ctx context.Context, host string) ([]net.IPAddr, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, server := range []struct{ endpoint, address string }{
		{"https://cloudflare-dns.com/dns-query", "1.1.1.1:443"},
		{"https://dns.google/resolve", "8.8.8.8:443"},
	} {
		ips, err := queryPublicDNS(ctx, server.endpoint, server.address, host)
		if err == nil {
			return ips, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errFakeIPResolution
}

func queryPublicDNS(ctx context.Context, endpoint, address, host string) ([]net.IPAddr, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
	}, TLSHandshakeTimeout: 3 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?name="+url.QueryEscape(host)+"&type=A", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	httpua.Apply(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DNS status %d", resp.StatusCode)
	}
	var body struct {
		Status int
		Answer []struct {
			Type int
			Data string
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != 0 {
		return nil, fmt.Errorf("DNS response status %d", body.Status)
	}
	var ips []net.IPAddr
	for _, a := range body.Answer {
		if a.Type != 1 && a.Type != 28 {
			continue
		}
		ip := net.ParseIP(a.Data)
		if ip == nil || !publicIP(ip) {
			return nil, errNonPublicAddress
		}
		ips = append(ips, net.IPAddr{IP: ip})
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("DNS returned no public addresses")
	}
	return ips, nil
}

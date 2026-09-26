package websearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/browserhttp"
	"github.com/wangh00/SciAide/internal/httpua"
	"golang.org/x/net/html"
)

type Page struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	Links       []Item `json:"links"`
	Truncated   bool   `json:"truncated"`
	RetrievedAt string `json:"retrievedAt"`
}

var errNonPublicAddress = errors.New("网页域名解析到非公网地址；请检查 DNS 或代理的 Fake-IP 设置")

func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "2002::/16"} {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
func validateURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || (u.Port() != "" && u.Port() != "443" && u.Port() != "80") {
		return nil, fmt.Errorf("仅支持公开 HTTP/HTTPS 网页")
	}
	return u, nil
}
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("主机地址不可用")
	}
	ips, err = validatedPageIPs(ctx, host, ips, resolvePublicDNS)
	if err != nil {
		return nil, err
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}
func Open(ctx context.Context, raw, find string) (Page, error) {
	return openPage(ctx, raw, find, nil)
}
func openPage(ctx context.Context, raw, find string, proxy *url.URL) (Page, error) {
	p := Page{Links: []Item{}}
	u, err := validateURL(raw)
	if err != nil {
		return p, err
	}
	if err := validateProxyTarget(u); err != nil {
		return p, err
	}
	tr := proxyTransport(proxy)
	if proxy == nil {
		tr.DialContext = publicDial
	} else if err := validateProxyTarget(u); err != nil {
		return p, err
	}
	transport := browserhttp.NewFixed(tr)
	defer transport.CloseIdleConnections()
	c := &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("网页重定向过多")
		}
		_, e := validateURL(req.URL.String())
		if e == nil {
			e = validateProxyTarget(req.URL)
		}
		httpua.Apply(req)
		return e
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return p, err
	}
	httpua.Apply(req)
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return p, ctx.Err()
		}
		if errors.Is(err, errNonPublicAddress) {
			return p, errNonPublicAddress
		}
		if errors.Is(err, errFakeIPResolution) {
			return p, errFakeIPResolution
		}
		return p, fmt.Errorf("网页获取失败；请检查地址或换用其他来源")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return p, fmt.Errorf("网页获取失败：HTTP %d", resp.StatusCode)
	}
	mime := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(mime, "text/html") && !strings.Contains(mime, "text/plain") && !strings.Contains(mime, "application/xhtml+xml") {
		return p, fmt.Errorf("当前网页工具只读取 HTML 或纯文本，不读取 PDF 或二进制数据")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil || len(b) > 2*1024*1024 {
		if ctx.Err() != nil {
			return p, ctx.Err()
		}
		return p, fmt.Errorf("网页过大或读取失败")
	}
	p.URL = resp.Request.URL.String()
	p.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
	if strings.Contains(mime, "text/plain") {
		p.Text = string(b)
	} else {
		doc, e := html.Parse(strings.NewReader(string(b)))
		if e != nil {
			return p, e
		}
		var clean func(*html.Node)
		clean = func(n *html.Node) {
			for child := n.FirstChild; child != nil; {
				next := child.NextSibling
				if child.Type == html.ElementNode && (child.Data == "script" || child.Data == "style" || child.Data == "noscript" || child.Data == "svg") {
					n.RemoveChild(child)
				} else {
					if child.Data == "title" {
						p.Title = textOf(child)
					}
					if child.Data == "a" && len(p.Links) < 40 {
						link, e := resp.Request.URL.Parse(attr(child, "href"))
						if e == nil && attr(child, "href") != "" && (link.Scheme == "http" || link.Scheme == "https") {
							p.Links = append(p.Links, Item{Title: clip(textOf(child), 200), URL: link.String()})
						}
					}
					clean(child)
				}
				child = next
			}
		}
		clean(doc)
		p.Text = textOf(doc)
	}
	if find != "" {
		r := []rune(p.Text)
		lower := []rune(strings.ToLower(p.Text))
		needle := []rune(strings.ToLower(find))
		at := -1
		for i := 0; i+len(needle) <= len(lower); i++ {
			if string(lower[i:i+len(needle)]) == string(needle) {
				at = i
				break
			}
		}
		if at < 0 {
			p.Text = "未在本次读取的网页文本中找到关键词。"
			p.Truncated = true
		} else {
			start := at - 1000
			if start < 0 {
				start = 0
			}
			end := at + 9000
			if end > len(r) {
				end = len(r)
			}
			p.Truncated = start > 0 || end < len(r)
			p.Text = string(r[start:end])
		}
	}
	if len([]rune(p.Text)) > 12000 {
		p.Truncated = true
		p.Text = clip(p.Text, 12000)
	}
	return p, nil
}

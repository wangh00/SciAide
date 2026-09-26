package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/browserhttp"
	"golang.org/x/net/html"
)

func (s *Service) request(ctx context.Context, id, key, q string, limit int) ([]Item, string, time.Duration) {
	endpoint, method, header := "", http.MethodPost, "Authorization"
	body := map[string]any{"query": q}
	switch id {
	case "baidu":
		length := 0
		for _, r := range q {
			length++
			if r > 127 {
				length++
			}
		}
		if length > 72 {
			return nil, "query_too_long", 0
		}
		endpoint = "https://qianfan.baidubce.com/v2/ai_search/web_search"
		header = "X-Appbuilder-Authorization"
		key = "Bearer " + key
		body = map[string]any{"messages": []map[string]string{{"role": "user", "content": q}}, "search_source": "baidu_search_v2", "resource_type_filter": []map[string]any{{"type": "web", "top_k": limit}}}
	case "firecrawl":
		endpoint = "https://api.firecrawl.dev/v2/search"
		body["limit"] = limit
		body["sources"] = []string{"web"}
		key = "Bearer " + key
	case "brave":
		endpoint = "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(q) + "&count=" + strconv.Itoa(limit)
		method = http.MethodGet
		header = "X-Subscription-Token"
	case "tavily":
		endpoint = "https://api.tavily.com/search"
		body["max_results"] = limit
		body["search_depth"] = "basic"
		body["include_answer"] = false
		key = "Bearer " + key
	case "exa":
		endpoint = "https://api.exa.ai/search"
		body["numResults"] = limit
		body["contents"] = map[string]any{"highlights": true}
		header = "x-api-key"
	case "duckduckgo":
		endpoint = "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(q)
		method = http.MethodGet
	default:
		return nil, "invalid_provider", 0
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, "request_error", 0
	}
	if method == http.MethodGet {
		req.Body = nil
		req.ContentLength = 0
	}
	req.Header.Set("Content-Type", "application/json")
	if id != "duckduckgo" {
		req.Header.Set(header, key)
	}
	proxy, proxyErr := s.proxyURL(ctx)
	if proxyErr != nil {
		return nil, "proxy_configuration_error", 0
	}
	client := *s.client
	if _, production := client.Transport.(*browserhttp.Transport); production {
		transport := browserhttp.NewFixed(proxyTransport(proxy))
		defer transport.CloseIdleConnections()
		client.Transport = transport
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "network_error", 30 * time.Second
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil {
		return nil, "read_error", 30 * time.Second
	}
	if len(b) > 2*1024*1024 {
		return nil, "response_too_large", time.Minute
	}
	if id == "duckduckgo" && (bytes.Contains(b, []byte("anomaly-modal")) || bytes.Contains(b, []byte("challenge-form"))) {
		return nil, "challenge", 5 * time.Minute
	}
	if resp.StatusCode != 200 {
		status, delay := "http_error", time.Minute
		switch resp.StatusCode {
		case 401, 403:
			status = "authentication_failed"
			delay = 10 * time.Minute
		case 402:
			status = "quota_exhausted"
			delay = 30 * time.Minute
		case 429:
			status = "rate_limited"
			delay = 5 * time.Minute
			if id == "brave" {
				var payload struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if json.Unmarshal(b, &payload) == nil && payload.Error.Code == "USAGE_LIMIT_EXCEEDED" {
					status = "quota_exhausted"
					delay = 30 * time.Minute
				}
			}
		case 432, 433:
			if id == "tavily" {
				status = "quota_exhausted"
				delay = 30 * time.Minute
			}
		}
		if resp.StatusCode == 429 || resp.StatusCode == 503 {
			delay = retryAfter(resp.Header.Get("Retry-After"), delay)
		}
		return nil, status, delay
	}
	var items []Item
	if id == "duckduckgo" {
		items, err = parseDDG(b)
	} else {
		items, err = parseAPI(id, b)
	}
	if err != nil {
		return nil, "invalid_response", time.Minute
	}
	result := []Item{}
	seen := map[string]bool{}
	for _, it := range items {
		u, e := url.Parse(it.URL)
		if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || seen[it.URL] {
			continue
		}
		seen[it.URL] = true
		it.Title = clip(it.Title, 500)
		it.Snippet = clip(it.Snippet, 1600)
		result = append(result, it)
		if len(result) >= limit {
			break
		}
	}
	if len(items) > 0 && len(result) == 0 {
		return nil, "invalid_response", time.Minute
	}
	if len(result) == 0 {
		return result, "empty", 0
	}
	return result, "ok", 0
}
func retryAfter(v string, fallback time.Duration) time.Duration {
	d := fallback
	if n, e := strconv.Atoi(v); e == nil && n >= 0 {
		d = time.Duration(n) * time.Second
	} else if t, e := http.ParseTime(v); e == nil {
		d = time.Until(t)
	}
	if d < time.Second {
		d = time.Second
	}
	if d > 24*time.Hour {
		d = 24 * time.Hour
	}
	return d
}
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func parseAPI(id string, b []byte) ([]Item, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	raw := root["results"]
	if id == "baidu" {
		if code := string(root["code"]); code != "" && code != "0" && code != `"0"` {
			return nil, io.ErrUnexpectedEOF
		}
		raw = root["references"]
	}
	if id == "firecrawl" {
		var d map[string]json.RawMessage
		if err := json.Unmarshal(root["data"], &d); err != nil {
			return nil, err
		}
		raw = d["web"]
		if string(root["success"]) != "true" {
			return nil, io.ErrUnexpectedEOF
		}
	}
	if id == "brave" {
		var d map[string]json.RawMessage
		if err := json.Unmarshal(root["web"], &d); err != nil {
			return nil, err
		}
		raw = d["results"]
	}
	var rows []struct {
		Title       string   `json:"title"`
		URL         string   `json:"url"`
		Description string   `json:"description"`
		Content     string   `json:"content"`
		Snippet     string   `json:"snippet"`
		Highlights  []string `json:"highlights"`
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, io.ErrUnexpectedEOF
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := []Item{}
	for _, v := range rows {
		snippet := v.Description
		if snippet == "" {
			snippet = v.Snippet
		}
		if snippet == "" {
			snippet = v.Content
		}
		if snippet == "" {
			snippet = strings.Join(v.Highlights, "\n")
		}
		out = append(out, Item{v.Title, v.URL, snippet})
	}
	return out, nil
}
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func textOf(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(textOf(c))
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
func parseDDG(b []byte) ([]Item, error) {
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	out := []Item{}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && strings.Contains(" "+attr(n, "class")+" ", " result__a ") {
			href := attr(n, "href")
			u, e := url.Parse(href)
			if e == nil {
				if target := u.Query().Get("uddg"); target != "" {
					href = target
				}
			}
			out = append(out, Item{Title: textOf(n), URL: href})
		}
		if strings.Contains(" "+attr(n, "class")+" ", " result__snippet ") && len(out) > 0 {
			out[len(out)-1].Snippet = textOf(n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	if len(out) == 0 && !strings.Contains(strings.ToLower(textOf(doc)), "no results found") {
		return nil, io.ErrUnexpectedEOF
	}
	return out, nil
}

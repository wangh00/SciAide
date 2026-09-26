package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wangh00/SciAide/internal/browserhttp"
	"github.com/wangh00/SciAide/internal/httpua"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

const (
	defaultTimeout       = 30 * time.Second
	defaultCacheTTL      = 5 * time.Minute
	defaultMaxResponse   = 8 << 20
	defaultRetryAttempts = 3
)

type cacheEntry struct {
	status  int
	header  http.Header
	body    []byte
	expires time.Time
}

// Credentials never enter source snapshots, query URLs, or third-party hosts.
func applySourceCredential(request *http.Request, source string) {
	if request.URL.Scheme == "https" && source == "semantic-scholar" && request.URL.Host == "api.semanticscholar.org" {
		if key := strings.TrimSpace(os.Getenv("SEMANTIC_SCHOLAR_API_KEY")); key != "" {
			request.Header.Set("x-api-key", key)
		}
	}
}

type Client struct {
	http        *http.Client
	timeout     time.Duration
	cacheTTL    time.Duration
	maxResponse int64
	attempts    int
	allowHTTP   bool
	now         func() time.Time

	mu        sync.Mutex
	cache     map[string]cacheEntry
	nextStart map[string]time.Time
}

type requestOptions struct {
	SourceID   string
	Host       string
	MinSpacing time.Duration
	Cache      bool
}

func NewClient() *Client {
	client := &Client{
		timeout: defaultTimeout, cacheTTL: defaultCacheTTL, maxResponse: defaultMaxResponse,
		attempts: defaultRetryAttempts, now: func() time.Time { return time.Now().UTC() },
		cache: map[string]cacheEntry{}, nextStart: map[string]time.Time{},
	}
	client.http = &http.Client{
		Transport: browserhttp.NewScoped(&http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: defaultTimeout,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   4,
		}, "research"),
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return fmt.Errorf("research source redirected too many times")
			}
			if len(via) == 0 || !strings.EqualFold(request.URL.Hostname(), via[0].URL.Hostname()) || request.URL.Scheme != via[0].URL.Scheme {
				return fmt.Errorf("research source redirected outside its fixed host")
			}
			return nil
		},
	}
	return client
}

func (c *Client) getJSON(ctx context.Context, target string, options requestOptions, destination any) error {
	body, _, err := c.get(ctx, target, options)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(destination); err != nil {
		return &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "source returned malformed JSON", Cause: err}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "source JSON contains trailing data", Cause: err}
	}
	return nil
}

func (c *Client) get(ctx context.Context, target string, options requestOptions) ([]byte, http.Header, error) {
	if c == nil || c.http == nil {
		return nil, nil, fmt.Errorf("research HTTP client is not configured")
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && !(c.allowHTTP && parsed.Scheme == "http")) {
		return nil, nil, &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "Connector produced an invalid endpoint"}
	}
	if !strings.EqualFold(parsed.Hostname(), strings.TrimSpace(options.Host)) {
		return nil, nil, &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "Connector endpoint escaped its fixed source host"}
	}
	cacheKey := target
	if options.Cache {
		if entry, ok := c.cached(cacheKey); ok {
			return append([]byte(nil), entry.body...), entry.header.Clone(), nil
		}
	}
	attempts := c.attempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := c.waitForHost(ctx, parsed.Hostname(), options.MinSpacing); err != nil {
			return nil, nil, err
		}
		requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		request.Header.Set("Accept", "application/json, application/atom+xml, application/xml, text/xml;q=0.9")
		httpua.Apply(request)
		applySourceCredential(request, options.SourceID)
		response, requestErr := c.http.Do(request)
		if requestErr != nil {
			requestContextErr := requestCtx.Err()
			cancel()
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			if errors.Is(requestContextErr, context.DeadlineExceeded) {
				lastErr = &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureTimeout, Message: "source request timed out", Retryable: true, Cause: requestErr}
			} else {
				lastErr = &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureUnavailable, Message: "source network request failed", Retryable: true, Cause: requestErr}
			}
			if attempt+1 < attempts {
				if err := sleepContext(ctx, retryDelay(attempt, "")); err != nil {
					return nil, nil, err
				}
				continue
			}
			return nil, nil, lastErr
		}
		body, readErr := readBounded(response.Body, c.maxResponse)
		closeErr := response.Body.Close()
		cancel()
		if readErr != nil || closeErr != nil {
			lastErr = &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "source response exceeded the safe size limit or could not be read", Retryable: false, Cause: readErr}
			return nil, nil, lastErr
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			if len(strings.TrimSpace(string(body))) == 0 {
				return nil, nil, &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "source returned an empty response"}
			}
			if options.Cache {
				c.storeCache(cacheKey, response.StatusCode, response.Header, body)
			}
			return body, response.Header.Clone(), nil
		}
		code, retryable := appresearch.FailureSource, false
		switch {
		case response.StatusCode == http.StatusNotFound:
			code = appresearch.FailureNotFound
		case response.StatusCode == http.StatusTooManyRequests:
			code, retryable = appresearch.FailureRateLimited, true
		case response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= 500:
			code, retryable = appresearch.FailureUnavailable, true
		}
		message := fmt.Sprintf("source returned HTTP %d", response.StatusCode)
		lastErr = &appresearch.SourceError{SourceID: options.SourceID, Code: code, Message: message, Retryable: retryable}
		if code == appresearch.FailureRateLimited {
			return nil, nil, lastErr
		}
		if retryable && attempt+1 < attempts {
			if err := sleepContext(ctx, retryDelay(attempt, response.Header.Get("Retry-After"))); err != nil {
				return nil, nil, err
			}
			continue
		}
		return nil, nil, lastErr
	}
	return nil, nil, lastErr
}

func (c *Client) cached(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok || !entry.expires.After(c.now()) {
		delete(c.cache, key)
		return cacheEntry{}, false
	}
	return entry, true
}

func (c *Client) storeCache(key string, status int, header http.Header, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= 512 {
		for existing, entry := range c.cache {
			if !entry.expires.After(c.now()) {
				delete(c.cache, existing)
			}
		}
	}
	if len(c.cache) >= 512 {
		for existing := range c.cache {
			delete(c.cache, existing)
			break
		}
	}
	c.cache[key] = cacheEntry{status: status, header: header.Clone(), body: append([]byte(nil), body...), expires: c.now().Add(c.cacheTTL)}
}

func (c *Client) waitForHost(ctx context.Context, host string, spacing time.Duration) error {
	if spacing <= 0 {
		return nil
	}
	c.mu.Lock()
	now := c.now()
	ready := c.nextStart[host]
	if ready.Before(now) {
		ready = now
	}
	c.nextStart[host] = ready.Add(spacing)
	c.mu.Unlock()
	return sleepContext(ctx, time.Until(ready))
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		maximum = defaultMaxResponse
	}
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	return body, nil
}

func retryDelay(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
		delay := time.Duration(seconds) * time.Second
		if delay > 10*time.Second {
			return 10 * time.Second
		}
		return delay
	}
	delay := 250 * time.Millisecond * time.Duration(1<<attempt)
	if delay > 2*time.Second {
		return 2 * time.Second
	}
	return delay
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

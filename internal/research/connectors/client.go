package connectors

import (
	"bytes"
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

	mu               sync.Mutex
	cache            map[string]cacheEntry
	nextStart        map[string]time.Time
	rateLimitedUntil map[string]time.Time
}

type requestOptions struct {
	SourceID     string
	Host         string
	MinSpacing   time.Duration
	Cache        bool
	validateBody func([]byte) error
}

// Typed JSON decoding alone cannot distinguish a source error object from a
// successful response with no records. Search envelopes validate before caching.
type sourceResponseValidator interface {
	validateSourceResponse() error
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
			if len(via) == 0 || !strings.EqualFold(request.URL.Hostname(), via[0].URL.Hostname()) || request.URL.Scheme != via[0].URL.Scheme || effectivePort(request.URL) != effectivePort(via[0].URL) {
				return fmt.Errorf("research source redirected outside its fixed host")
			}
			return nil
		},
	}
	return client
}

func (c *Client) getJSON(ctx context.Context, target string, options requestOptions, destination sourceResponseValidator) error {
	// HTTP success is not yet a cacheable JSON response. Decode inside get's
	// success boundary so an explicit later search can reach a recovered source.
	options.validateBody = func(body []byte) error {
		decoder := json.NewDecoder(bytes.NewReader(body))
		if err := decoder.Decode(destination); err != nil {
			return &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "source returned malformed JSON", Cause: err}
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "source JSON contains trailing data", Cause: err}
		}
		return destination.validateSourceResponse()
	}
	_, _, err := c.get(ctx, target, options)
	return err
}

func (c *Client) get(ctx context.Context, target string, options requestOptions) ([]byte, http.Header, error) {
	if c == nil || c.http == nil {
		return nil, nil, fmt.Errorf("research HTTP client is not configured")
	}
	if options.Cache && options.validateBody == nil {
		return nil, nil, &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "cacheable source requests require semantic response validation"}
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && !(c.allowHTTP && parsed.Scheme == "http")) {
		return nil, nil, &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "Connector produced an invalid endpoint"}
	}
	if !c.allowHTTP && parsed.Port() != "" && parsed.Port() != "443" {
		return nil, nil, &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "Connector endpoint escaped its authorized HTTPS port"}
	}
	if !strings.EqualFold(parsed.Hostname(), strings.TrimSpace(options.Host)) {
		return nil, nil, &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "Connector endpoint escaped its fixed source host"}
	}
	cacheKey := target
	if options.Cache {
		if entry, ok := c.cached(cacheKey); ok {
			if options.validateBody != nil {
				if err := options.validateBody(entry.body); err != nil {
					c.discardCache(cacheKey, entry.body)
					return nil, nil, err
				}
			}
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
		if err := c.sourceCooldown(options); err != nil {
			return nil, nil, err
		}
		if err := c.waitForHost(ctx, parsed.Hostname(), options.MinSpacing); err != nil {
			return nil, nil, err
		}
		if err := c.sourceCooldown(options); err != nil {
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
		requestClient := c.http
		if budget, ok := appresearch.SourceHTTPBudgetFromContext(ctx); ok {
			bounded := *c.http
			bounded.Transport = sourceBudgetTransport{next: c.http.Transport, budget: budget, sourceID: options.SourceID}
			requestClient = &bounded
		}
		response, requestErr := requestClient.Do(request)
		if requestErr != nil {
			requestContextErr := requestCtx.Err()
			cancel()
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			var budgetErr *appresearch.SourceError
			if errors.As(requestErr, &budgetErr) && (budgetErr.Code == appresearch.FailureRequestBudget || budgetErr.Code == appresearch.FailureInvalidData) {
				return nil, nil, budgetErr
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
		c.observeRateLimit(response, options.Host)
		body, readErr := readBounded(response.Body, c.maxResponse)
		closeErr := response.Body.Close()
		cancel()
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		// Preserve the original no-retry policy for unreadable responses. Only
		// explicit access/rate statuses take precedence over their error body;
		// observing headers above must not add retries for a broken 5xx body.
		if (readErr != nil || closeErr != nil) && response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
			lastErr = &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "source response exceeded the safe size limit or could not be read", Retryable: false, Cause: readErr}
			return nil, nil, lastErr
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			if len(strings.TrimSpace(string(body))) == 0 {
				return nil, nil, &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureInvalidData, Message: "source returned an empty response"}
			}
			if options.validateBody != nil {
				if err := options.validateBody(body); err != nil {
					return nil, nil, err
				}
			}
			if options.Cache {
				c.storeCache(cacheKey, response.StatusCode, response.Header, body)
			}
			return body, response.Header.Clone(), nil
		}
		code, retryable := appresearch.FailureSource, false
		switch {
		case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
			code = appresearch.FailureAccessDenied
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

type sourceBudgetTransport struct {
	next     http.RoundTripper
	budget   *appresearch.SourceHTTPBudget
	sourceID string
}

func (t sourceBudgetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	transport, ok := t.next.(interface {
		RoundTripSingleAttempt(*http.Request) (*http.Response, error)
	})
	if !ok {
		return nil, &appresearch.SourceError{SourceID: t.sourceID, Code: appresearch.FailureInvalidData, Message: "budgeted sources require an explicit non-replaying transport"}
	}
	if err := t.budget.Consume(t.sourceID); err != nil {
		return nil, err
	}
	return transport.RoundTripSingleAttempt(r)
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

func (c *Client) discardCache(key string, rejected []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// A slower validation must not evict a different, newer response.
	if entry, ok := c.cache[key]; ok && bytes.Equal(entry.body, rejected) {
		delete(c.cache, key)
	}
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

// Honor server-provided retry windows across separate searches, not just one
// request's retry loop. Cached successful metadata remains usable without HTTP.
func (c *Client) sourceCooldown(options requestOptions) error {
	c.mu.Lock()
	until := c.rateLimitedUntil[strings.ToLower(options.Host)]
	c.mu.Unlock()
	if until.After(c.now()) {
		return &appresearch.SourceError{SourceID: options.SourceID, Code: appresearch.FailureRateLimited, Message: "source requests paused until " + until.UTC().Format(time.RFC3339) + "; this is not a guaranteed quota reset", Retryable: true}
	}
	return nil
}

func (c *Client) observeRateLimit(response *http.Response, host string) {
	remaining := strings.TrimSpace(response.Header.Get("X-RateLimit-Remaining"))
	if response.StatusCode != http.StatusTooManyRequests && remaining != "0" {
		return
	}
	now := c.now()
	until := time.Time{}
	for _, name := range []string{"Retry-After", "X-RateLimit-Retry-After"} {
		value := strings.TrimSpace(response.Header.Get(name))
		candidate := time.Time{}
		if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
			candidate = now.Add(time.Duration(seconds) * time.Second)
		} else {
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05-0700", http.TimeFormat} {
				if parsed, err := time.Parse(layout, value); err == nil {
					candidate = parsed
					break
				}
			}
		}
		if candidate.After(until) {
			until = candidate
		}
	}
	if !until.After(now) {
		// The server gave no usable recovery window. Pause new requests briefly;
		// do not invent a reset time, replay a request, or call this an empty search.
		until = now.Add(time.Minute)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rateLimitedUntil == nil {
		c.rateLimitedUntil = map[string]time.Time{}
	}
	host = strings.ToLower(host)
	if until.After(c.rateLimitedUntil[host]) {
		c.rateLimitedUntil[host] = until
	}
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

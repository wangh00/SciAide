package responses

import (
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DebugHTTPResponse is private opt-in evidence, not a public error or a model
// result. It contains no request URL/headers/body and never reads response.Body.
// IDs and routing headers can still be sensitive even after known-key redaction.
type DebugHTTPResponse struct {
	HTTPStatus            int               `json:"httpStatus"`
	HTTPMajor             int               `json:"httpMajor"`
	HTTPMinor             int               `json:"httpMinor"`
	ElapsedMillis         int64             `json:"elapsedMillis"`
	DeclaredContentLength int64             `json:"declaredContentLength"`
	Headers               map[string]string `json:"headers"`
	OmittedHeaders        []string          `json:"omittedHeaders,omitempty"`
}

var debugResponseHeaders = [...]string{
	"Content-Type", "Server", "Via", "Date", "Retry-After",
	"X-Request-Id", "Request-Id", "Openai-Request-Id", "X-Openai-Request-Id",
	"X-Correlation-Id", "Cf-Ray", "Traceparent", "X-Envoy-Upstream-Service-Time",
}

var (
	debugHeaderURL    = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
	debugHeaderAuth   = regexp.MustCompile(`(?i)(\b(?:bearer|basic)\s+)[^\s"',;]+`)
	debugHeaderSecret = regexp.MustCompile(`(?i)((?:api[_ -]?key|token|secret|password|authorization|cookie)\s*(?:is|=|:)\s*)[^\s,;]+`)
)

// Configure once before sharing the client. Nil leaves its transport untouched.
func (c *Client) SetDebugHTTPResponseObserver(observe func(DebugHTTPResponse)) {
	if observe == nil {
		return
	}
	copy := *c.http
	base := copy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copy.Transport = &debugHTTPResponseTransport{base: base, observe: observe, redact: debugTextRedactor(c.secret, c.profile.CustomHeaders)}
	c.http = &copy
}

type debugHTTPResponseTransport struct {
	base    http.RoundTripper
	observe func(DebugHTTPResponse)
	redact  func(string, int) string
}

func (t *debugHTTPResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	response, err := t.base.RoundTrip(req)
	if response != nil {
		event := DebugHTTPResponse{
			HTTPStatus: response.StatusCode, HTTPMajor: response.ProtoMajor, HTTPMinor: response.ProtoMinor,
			ElapsedMillis: time.Since(started).Milliseconds(), DeclaredContentLength: response.ContentLength,
			Headers: make(map[string]string),
		}
		for _, name := range debugResponseHeaders {
			values := response.Header.Values(name)
			if len(values) == 0 {
				continue
			}
			// Omit, never prefix-truncate, oversized values before redaction: a
			// partial credential would no longer match the known full secret.
			size := 0
			for i, value := range values {
				if i > 0 {
					size += 2
				}
				size += len(value)
				if size > 1024 {
					break
				}
			}
			if size > 1024 {
				event.OmittedHeaders = append(event.OmittedHeaders, name)
				continue
			}
			value := t.redact(strings.Join(values, ", "), 0)
			value = debugHeaderAuth.ReplaceAllString(value, "${1}[REDACTED]")
			value = debugHeaderSecret.ReplaceAllString(value, "${1}[REDACTED]")
			value = debugHeaderURL.ReplaceAllString(value, "[URL]")
			value = strings.Join(strings.Fields(value), " ")
			if chars := []rune(value); len(chars) > 256 {
				value = string(chars[:256]) + "[TRUNCATED]"
			}
			event.Headers[name] = value
		}
		t.observe(event)
	}
	return response, err
}

func (t *debugHTTPResponseTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

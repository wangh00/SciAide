package responses

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

type DebugProviderError struct {
	Kind    string `json:"kind"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SetDebugErrorObserver is a developer-only opt-in, configured once before
// sharing the client. It is instance-scoped, not tied to a request/lifecycle
// context: Chat deliberately starts detached contexts on execute and resume.
// Classification/retry policy is unchanged; recording can add local IO latency.
func (c *Client) SetDebugErrorObserver(observe func(DebugProviderError)) {
	c.debugObserver = observe
}

func debugErrorObserver(observe func(DebugProviderError), secret []byte, headers map[string]string) func(string, string, string) {
	if observe == nil {
		return nil
	}
	redact := debugTextRedactor(secret, headers)
	return func(kind, code, message string) {
		observe(DebugProviderError{Kind: redact(kind, 128), Code: redact(code, 256), Message: redact(message, 2048)})
	}
}

func debugTextRedactor(secret []byte, headers map[string]string) func(string, int) string {
	values := []string{string(secret)}
	for _, value := range headers {
		values = append(values, value)
		if strings.HasPrefix(strings.ToLower(value), "bearer ") {
			values = append(values, strings.TrimSpace(value[7:]))
		}
	}
	for _, value := range append([]string(nil), values...) {
		if value != "" {
			values = append(values, url.QueryEscape(value), url.PathEscape(value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	var patterns []string
	seen := make(map[string]bool)
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			patterns = append(patterns, percentEquivalentPattern(value))
		}
	}
	var matcher *regexp.Regexp
	var compileErr error
	if len(patterns) > 0 {
		matcher, compileErr = regexp.Compile(strings.Join(patterns, "|"))
	}
	return func(text string, limit int) string {
		if compileErr != nil {
			return "[REDACTED]" // fail closed for an unrepresentable secret pattern
		}
		if matcher != nil {
			// One pass over the original text; replacement markers are never
			// reprocessed, including for single-character configured values.
			text = matcher.ReplaceAllLiteralString(text, "[REDACTED]")
		}
		if limit > 0 {
			if runes := []rune(text); len(runes) > limit {
				text = string(runes[:limit]) + "[TRUNCATED]"
			}
		}
		return text
	}
}

// Match percent-escape hex case only inside each known value. Canonicalizing
// the entire text could change a literal key following a '%' outside the key.
// No unmatched wire bytes (even invalid UTF-8) are rewritten.
func percentEquivalentPattern(value string) string {
	hex := func(b byte) bool { return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F' }
	var pattern strings.Builder
	start := 0
	for i := 0; i+2 < len(value); i++ {
		if value[i] == '%' && hex(value[i+1]) && hex(value[i+2]) {
			pattern.WriteString(regexp.QuoteMeta(value[start:i]))
			pattern.WriteString("(?i:")
			pattern.WriteString(value[i : i+3])
			pattern.WriteByte(')')
			i += 2
			start = i + 1
		}
	}
	pattern.WriteString(regexp.QuoteMeta(value[start:]))
	return pattern.String()
}

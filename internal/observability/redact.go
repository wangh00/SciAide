package observability

import (
	"log/slog"
	"regexp"
	"strings"
)

const masked = "[REDACTED]"

var sensitiveKey = regexp.MustCompile(`(?i)(authorization|api[_-]?key|token|secret|cookie|password|credential)`)

func IsSensitiveKey(key string) bool { return sensitiveKey.MatchString(key) }

func RedactString(value string, knownSecrets []string) string {
	redacted := value
	for _, secret := range knownSecrets {
		if len(secret) >= 4 {
			redacted = strings.ReplaceAll(redacted, secret, masked)
		}
	}
	return redacted
}

func RedactAttr(attr slog.Attr, knownSecrets []string) slog.Attr {
	if IsSensitiveKey(attr.Key) {
		return slog.String(attr.Key, masked)
	}
	if attr.Value.Kind() == slog.KindString {
		return slog.String(attr.Key, RedactString(attr.Value.String(), knownSecrets))
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		for i := range group {
			group[i] = RedactAttr(group[i], knownSecrets)
		}
		return slog.Group(attr.Key, attrsToAny(group)...)
	}
	return attr
}

func attrsToAny(attrs []slog.Attr) []any {
	values := make([]any, len(attrs))
	for i := range attrs {
		values[i] = attrs[i]
	}
	return values
}

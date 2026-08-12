package observability

import (
	"context"
	"log/slog"
)

type RedactingHandler struct {
	next         slog.Handler
	knownSecrets []string
}

func NewRedactingHandler(next slog.Handler, knownSecrets []string) *RedactingHandler {
	return &RedactingHandler{next: next, knownSecrets: append([]string(nil), knownSecrets...)}
}

func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	copyRecord := slog.NewRecord(record.Time, record.Level, RedactString(record.Message, h.knownSecrets), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		copyRecord.AddAttrs(RedactAttr(attr, h.knownSecrets))
		return true
	})
	return h.next.Handle(ctx, copyRecord)
}

func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i := range attrs {
		redacted[i] = RedactAttr(attrs[i], h.knownSecrets)
	}
	return &RedactingHandler{next: h.next.WithAttrs(redacted), knownSecrets: h.knownSecrets}
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: h.next.WithGroup(name), knownSecrets: h.knownSecrets}
}

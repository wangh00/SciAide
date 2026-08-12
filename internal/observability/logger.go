package observability

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

type Logger struct {
	*slog.Logger
	closer io.Closer
}

func NewLogger(logDir string, level slog.Level) (*Logger, error) {
	writer, err := NewRotatingWriter(filepath.Join(logDir, "sciaide.jsonl"), 5*1024*1024, 3)
	if err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(io.MultiWriter(os.Stderr, writer), &slog.HandlerOptions{Level: level})
	return &Logger{
		Logger: slog.New(NewRedactingHandler(handler, nil)),
		closer: writer,
	}, nil
}

func (l *Logger) Close() error { return l.closer.Close() }

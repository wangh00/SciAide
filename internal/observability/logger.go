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
	return newLogger(logDir, level, os.Stderr)
}

func newLogger(logDir string, level slog.Level, console io.Writer) (*Logger, error) {
	writer, err := NewRotatingWriter(filepath.Join(logDir, "sciaide.jsonl"), 5*1024*1024, 3)
	if err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(FileFirstWriter(writer, console), &slog.HandlerOptions{Level: level})
	return &Logger{
		Logger: slog.New(NewRedactingHandler(handler, nil)),
		closer: writer,
	}, nil
}

func (l *Logger) Close() error { return l.closer.Close() }

// FileFirstWriter makes the file authoritative. GUI processes need not have a
// valid stderr handle; an unavailable console must never suppress durable logs.
func FileFirstWriter(file, console io.Writer) io.Writer {
	return fileFirstWriter{file: file, console: console}
}

type fileFirstWriter struct{ file, console io.Writer }

func (w fileFirstWriter) Write(data []byte) (int, error) {
	n, err := w.file.Write(data)
	if w.console != nil {
		_, _ = w.console.Write(data)
	}
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

package observability

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type unavailableConsole struct{}

func (unavailableConsole) Write([]byte) (int, error) { return 0, errors.New("invalid handle") }

func TestFileLogSurvivesMissingGUIConsole(t *testing.T) {
	root := t.TempDir()
	logger, err := newLogger(root, slog.LevelInfo, unavailableConsole{})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("desktop started without a console", "pid", 42)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "sciaide.jsonl"))
	if err != nil || !strings.Contains(string(data), "desktop started without a console") {
		t.Fatal("GUI stderr suppressed the file log", string(data), err)
	}
}

func TestFileFirstWriterPreservesFileFailures(t *testing.T) {
	var console bytes.Buffer
	if _, err := FileFirstWriter(unavailableConsole{}, &console).Write([]byte("message")); err == nil || console.String() != "message" {
		t.Fatal("file failure hidden", err, console.String())
	}
	if _, err := FileFirstWriter(shortWriter{}, nil).Write([]byte("message")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short durable write hidden", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 1, nil }

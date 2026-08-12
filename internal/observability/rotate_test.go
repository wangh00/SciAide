package observability

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingWriterLimitsBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sciaide.log")
	writer, err := NewRotatingWriter(path, 12, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := writer.Write([]byte("12345678\n")); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for _, expected := range []string{path, path + ".1", path + ".2"} {
		if _, err := os.Stat(expected); err != nil {
			t.Fatalf("expected log file %q: %v", expected, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected extra backup: %v", err)
	}
}

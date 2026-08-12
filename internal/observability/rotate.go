package observability

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
}

func NewRotatingWriter(path string, maxBytes int64, backups int) (*RotatingWriter, error) {
	if maxBytes <= 0 || backups < 1 {
		return nil, fmt.Errorf("invalid log rotation settings")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &RotatingWriter{path: path, maxBytes: maxBytes, backups: backups}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > 0 && w.size+int64(len(data)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(data)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}

func (w *RotatingWriter) open() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *RotatingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close log before rotation: %w", err)
	}
	for i := w.backups - 1; i >= 1; i-- {
		oldName := fmt.Sprintf("%s.%d", w.path, i)
		newName := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(oldName); err == nil {
			_ = os.Remove(newName)
			if err := os.Rename(oldName, newName); err != nil {
				return fmt.Errorf("rotate log backup: %w", err)
			}
		}
	}
	firstBackup := w.path + ".1"
	_ = os.Remove(firstBackup)
	if err := os.Rename(w.path, firstBackup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate active log: %w", err)
	}
	return w.open()
}

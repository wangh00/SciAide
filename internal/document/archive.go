package document

import (
	"archive/zip"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	maxArchiveEntries       = 20_000
	maxArchiveExpandedBytes = 512 << 20
	maxArchiveEntryBytes    = 64 << 20
	maxCompressionRatio     = 250
)

type safeArchive struct {
	reader *zip.ReadCloser
	files  map[string]*zip.File
}

func openSafeArchive(filePath string) (*safeArchive, error) {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("open document archive: %w", err)
	}
	closeOnError := func(err error) (*safeArchive, error) {
		_ = reader.Close()
		return nil, err
	}
	if len(reader.File) > maxArchiveEntries {
		return closeOnError(fmt.Errorf("document archive contains too many entries"))
	}
	files := make(map[string]*zip.File, len(reader.File))
	var expanded uint64
	for _, file := range reader.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		clean := path.Clean(name)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || clean != strings.TrimSuffix(name, "/") {
			return closeOnError(fmt.Errorf("document archive contains an unsafe path"))
		}
		if _, duplicate := files[clean]; duplicate {
			return closeOnError(fmt.Errorf("document archive contains duplicate entries"))
		}
		if file.UncompressedSize64 > maxArchiveEntryBytes {
			return closeOnError(fmt.Errorf("document archive entry is too large"))
		}
		expanded += file.UncompressedSize64
		if expanded > maxArchiveExpandedBytes {
			return closeOnError(fmt.Errorf("document archive expands beyond the safety limit"))
		}
		if file.UncompressedSize64 > 1<<20 {
			compressed := file.CompressedSize64
			if compressed == 0 || file.UncompressedSize64/compressed > maxCompressionRatio {
				return closeOnError(fmt.Errorf("document archive compression ratio is unsafe"))
			}
		}
		files[clean] = file
	}
	return &safeArchive{reader: reader, files: files}, nil
}

func (a *safeArchive) close() error { return a.reader.Close() }

func (a *safeArchive) read(name string, maximum int64) ([]byte, error) {
	file, exists := a.files[path.Clean(strings.ReplaceAll(name, "\\", "/"))]
	if !exists {
		return nil, fmt.Errorf("document archive entry %q is missing", name)
	}
	if maximum <= 0 || maximum > maxArchiveEntryBytes {
		maximum = maxArchiveEntryBytes
	}
	if file.UncompressedSize64 > uint64(maximum) {
		return nil, fmt.Errorf("document archive entry %q exceeds its read limit", name)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	contents, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("document archive entry %q exceeds its read limit", name)
	}
	return contents, nil
}

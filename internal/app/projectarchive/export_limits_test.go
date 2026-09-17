package projectarchive

import (
	"archive/zip"
	"bytes"
	"testing"
)

func TestExportBudgetMatchesRestoreLimits(t *testing.T) {
	valid := Manifest{Database: DatabaseEntry{SizeBytes: maxDatabaseBytes}, Files: make([]FileEntry, 6)}
	for i := range valid.Files {
		valid.Files[i].SizeBytes = maxArchiveFileBytes
	}
	if err := validateExportBudget(valid, 0); err != nil {
		t.Fatal(err)
	}
	if err := validateExportBudget(valid, 1); err == nil {
		t.Fatal("manifest bytes excluded from total")
	}
	if err := validateExportBudget(Manifest{Files: make([]FileEntry, maxArchiveEntries-2)}, 1); err != nil {
		t.Fatal(err)
	}
	if err := validateExportBudget(Manifest{Files: make([]FileEntry, maxArchiveEntries-1)}, 1); err == nil {
		t.Fatal("manifest and database entries excluded from count")
	}
	for _, size := range []int64{-1, maxArchiveFileBytes + 1} {
		if err := validateExportBudget(Manifest{Files: []FileEntry{{SizeBytes: size}}}, 1); err == nil {
			t.Fatalf("accepted size %d", size)
		}
	}
}

func TestLargeManifestUsesSafeZIPMethod(t *testing.T) {
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	if err := writeZIPBytes(w, manifestPath, bytes.Repeat([]byte("a"), (1<<20)+1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if r.File[0].Method != zip.Store {
		t.Fatal("large manifest uses unsafe compression")
	}
	if exportZIPMethod(1<<20) != zip.Deflate {
		t.Fatal("small entries should retain compression")
	}
}

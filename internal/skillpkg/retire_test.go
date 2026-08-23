package skillpkg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveRetiredPackageMovesOnlySelectedVersion(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "skills")
	backups := filepath.Join(root, "backups", "skills")
	retired := filepath.Join(installed, "hello-multimodal", "0.3.3")
	userVersion := filepath.Join(installed, "hello-multimodal", "9.0.0")
	if err := os.MkdirAll(retired, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userVersion, 0o700); err != nil {
		t.Fatal(err)
	}
	moved, err := ArchiveRetiredPackage(installed, backups, "hello-multimodal/0.3.3")
	if err != nil || !moved {
		t.Fatalf("ArchiveRetiredPackage() = %v, %v", moved, err)
	}
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Fatalf("retired package still exists: %v", err)
	}
	if _, err := os.Stat(userVersion); err != nil {
		t.Fatalf("unrelated version was moved: %v", err)
	}
}

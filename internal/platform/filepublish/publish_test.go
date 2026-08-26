package filepublish

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoReplaceOpenPublishesStableFileIdentity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	destination := filepath.Join(root, "destination.txt")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := NoReplaceOpen(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	owned, err := handle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	published, err := os.Stat(destination)
	if err != nil || !os.SameFile(owned, published) {
		t.Fatalf("published identity is unstable: %#v, %#v, %v", owned, published, err)
	}
	if err := os.WriteFile(source, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if second, err := NoReplaceOpen(source, destination); err == nil {
		second.Close()
		t.Fatal("NoReplaceOpen replaced an existing destination")
	}
}

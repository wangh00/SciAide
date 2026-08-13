package pathguard

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGuardRejectsAbsoluteTraversalAndSiblingPrefix(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	guard, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	for _, value := range []string{"../secret.txt", filepath.Join("..", "workspace-other", "secret.txt"), filepath.Join(parent, "secret.txt")} {
		if _, err := guard.Relative(value); err == nil {
			t.Fatalf("unsafe path %q accepted", value)
		}
	}
	if relative, err := guard.Relative("papers/../notes.md"); err != nil || relative != "notes.md" {
		t.Fatalf("Relative() = %q, %v", relative, err)
	}
}

func TestGuardOpenFileRejectsEscapingSymlink(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	outside := filepath.Join(parent, "outside.txt")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		t.Fatal(err)
	}
	guard, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if file, _, err := guard.OpenFile("escape.txt"); err == nil {
		file.Close()
		t.Fatal("symlink escaping Workspace was opened")
	}
}

func TestGuardOpenFileAllowsWorkspaceFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "papers"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "papers", "a.md"), []byte("paper"), 0o600); err != nil {
		t.Fatal(err)
	}
	guard, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	file, relative, err := guard.OpenFile(filepath.Join("papers", "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if relative != filepath.Join("papers", "a.md") {
		t.Fatalf("relative = %q", relative)
	}
}

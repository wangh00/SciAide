package pathguard

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestGuardOpenFileMissingLeafReturnsNotExist(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "analysis-output"), 0o700); err != nil {
		t.Fatal(err)
	}
	guard, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()

	file, _, err := guard.OpenFile(filepath.Join("analysis-output", "new.csv"))
	if file != nil {
		file.Close()
		t.Fatal("missing workspace file was opened")
	}
	if err == nil || strings.Contains(err.Error(), "inspect workspace path attributes") {
		t.Fatalf("OpenFile() error = %v, want missing-file error after reparse validation", err)
	}
}

func TestGuardMkdirAllCreatesOnlyRealWorkspaceDirectories(t *testing.T) {
	workspace := t.TempDir()
	guard, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()

	created, err := guard.MkdirAll(filepath.FromSlash("analysis-output/run-1"), 0o700)
	if err != nil || filepath.ToSlash(created) != "analysis-output/run-1" {
		t.Fatalf("MkdirAll() = %q, %v", created, err)
	}
	if info, err := os.Stat(filepath.Join(workspace, "analysis-output", "run-1")); err != nil || !info.IsDir() {
		t.Fatalf("created directory = %#v, %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "blocked"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.MkdirAll(filepath.FromSlash("blocked/child"), 0o700); err == nil {
		t.Fatal("MkdirAll accepted a file as a directory component")
	}
	for _, value := range []string{"../outside", filepath.Join(workspace, "absolute")} {
		if _, err := guard.MkdirAll(value, 0o700); err == nil {
			t.Fatalf("MkdirAll accepted escaped path %q", value)
		}
	}
}

func TestGuardCreateFileIsExclusiveAndWorkspaceScoped(t *testing.T) {
	workspace := t.TempDir()
	guard, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if _, err := guard.MkdirAll("research-inputs", 0o700); err != nil {
		t.Fatal(err)
	}
	file, relative, err := guard.CreateFile(filepath.FromSlash("research-inputs/data.csv"), 0o600)
	if err != nil || filepath.ToSlash(relative) != "research-inputs/data.csv" {
		t.Fatalf("CreateFile() = %q, %v", relative, err)
	}
	if _, err := file.WriteString("value\n1\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if duplicate, _, err := guard.CreateFile(relative, 0o600); err == nil {
		duplicate.Close()
		t.Fatal("CreateFile replaced an existing file")
	}
	if escaped, _, err := guard.CreateFile("../outside.csv", 0o600); err == nil {
		escaped.Close()
		t.Fatal("CreateFile accepted an escaped path")
	}
	if err := guard.Remove(relative); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, relative)); !os.IsNotExist(err) {
		t.Fatalf("Remove() left the file behind: %v", err)
	}
}

func TestGuardRemoveAllStaysWithinWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	guard, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if _, err := guard.MkdirAll("private/owned/nested", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "private", "owned", "nested", "value.txt"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := guard.RemoveAll("private/owned"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "private", "owned")); !os.IsNotExist(err) {
		t.Fatalf("owned directory remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("outside file changed: %v", err)
	}
	for _, value := range []string{".", "..", "../outside"} {
		if err := guard.RemoveAll(value); err == nil {
			t.Fatalf("RemoveAll(%q) succeeded", value)
		}
	}
}

func TestGuardWalkDirStaysWithinWorkspace(t *testing.T) {
	workspace := t.TempDir()
	guard, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if _, err := guard.MkdirAll("owned/nested", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "owned", "nested", "value.txt"), []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	visited := 0
	if err := guard.WalkDir("owned", func(string, os.DirEntry, error) error { visited++; return nil }); err != nil {
		t.Fatal(err)
	}
	if visited != 3 {
		t.Fatalf("visited entries = %d", visited)
	}
	if err := guard.WalkDir("../outside", func(string, os.DirEntry, error) error { return nil }); err == nil {
		t.Fatal("WalkDir accepted escaped path")
	}
}

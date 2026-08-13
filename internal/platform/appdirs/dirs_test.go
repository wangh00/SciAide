package appdirs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureCreatesSeparatedDirectories(t *testing.T) {
	root := t.TempDir()
	dirs := ResolveUnder(root)
	if err := dirs.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	for _, dir := range []string{dirs.Config, dirs.Data, dirs.Cache, dirs.Logs, dirs.Skills, dirs.MCP, dirs.Backups, dirs.Workspaces, dirs.Trash} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("expected directory %q: %v", dir, err)
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == ".." || filepath.IsAbs(rel) {
			t.Fatalf("directory escaped test root: %q", dir)
		}
	}
}

func TestResolveUsesSciAideDirectoryUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOMEDRIVE", filepath.VolumeName(home))
		t.Setenv("HOMEPATH", home[len(filepath.VolumeName(home)):])
	}
	dirs, err := Resolve("SciAide")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".sciaide")
	if dirs.Root != want || dirs.Workspaces != filepath.Join(want, "data", "workspaces") {
		t.Fatalf("Resolve() = %#v, want root %q", dirs, want)
	}
}

func TestMigrateLegacyCopiesOnceAndPreservesSource(t *testing.T) {
	local := t.TempDir()
	config := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", config)
	legacyData := filepath.Join(local, "SciAide", "data")
	if err := os.MkdirAll(legacyData, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(legacyData, "sciaide.db")
	if err := os.WriteFile(source, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirs := ResolveUnder(filepath.Join(t.TempDir(), ".sciaide"))
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	result, err := MigrateLegacy("SciAide", dirs)
	if err != nil || !result.Migrated {
		t.Fatalf("MigrateLegacy() = %#v, %v", result, err)
	}
	contents, err := os.ReadFile(filepath.Join(dirs.Data, "sciaide.db"))
	if err != nil || string(contents) != "legacy" {
		t.Fatalf("target contents = %q, %v", contents, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("legacy source removed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirs.Data, "sciaide.db"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := MigrateLegacy("SciAide", dirs)
	if err != nil || second.Migrated {
		t.Fatalf("second migration = %#v, %v", second, err)
	}
	contents, _ = os.ReadFile(filepath.Join(dirs.Data, "sciaide.db"))
	if string(contents) != "new" {
		t.Fatalf("second migration overwrote target: %q", contents)
	}
}

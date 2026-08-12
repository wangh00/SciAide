package appdirs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCreatesSeparatedDirectories(t *testing.T) {
	root := t.TempDir()
	dirs := ResolveUnder(root)
	if err := dirs.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	for _, dir := range []string{dirs.Config, dirs.Data, dirs.Cache, dirs.Logs, dirs.Skills, dirs.MCP, dirs.Backups} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("expected directory %q: %v", dir, err)
		}
		if filepath.Dir(dir) != root {
			t.Fatalf("directory escaped test root: %q", dir)
		}
	}
}

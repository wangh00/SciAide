package skillpkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `schema_version: 1
id: literature-review
name: 文献阅读
version: 1.2.0
description: 提取论文方法、证据与局限
entry: SKILL.md
activation:
  mode: explicit
  triggers: [文献阅读]
requires:
  tools: [builtin.workspace.read_text]
  optional_tools: []
permissions: [workspace.read]
compatibility:
  sciaide: ">=0.2.0 <1.0.0"
context:
  max_tokens: 4000
`

func writePackage(t *testing.T, root, id, version, manifest, instructions string) string {
	t.Helper()
	directory := filepath.Join(root, id, version)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "skill.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(instructions), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestCatalogDiscoversStrictPackageAndDetectsPostRefreshMutation(t *testing.T) {
	root := t.TempDir()
	directory := writePackage(t, root, "literature-review", "1.2.0", validYAML, "# Literature\nRead evidence first.")
	catalog := NewCatalog(root)
	snapshot, err := catalog.Discover(context.Background())
	if err != nil || len(snapshot.Packages) != 1 || len(snapshot.Diagnostics) != 0 {
		t.Fatalf("Discover() = %#v, %v", snapshot, err)
	}
	value := snapshot.Packages[0]
	if value.Skill.Manifest.Context.MaxTokens != 4000 || value.Skill.ContentHash == "" || value.Skill.PackageHash == "" || value.Instructions == "" {
		t.Fatalf("package = %#v", value)
	}
	loaded, err := catalog.Load(context.Background(), "literature-review/1.2.0", value.Skill.PackageHash)
	if err != nil || loaded.Skill.PackageHash != value.Skill.PackageHash {
		t.Fatalf("Load() = %#v, %v", loaded, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("mutated instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Load(context.Background(), "literature-review/1.2.0", value.Skill.PackageHash); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("mutated package Load() error = %v", err)
	}
	if _, err := catalog.Load(context.Background(), "../outside/1.0.0", value.Skill.PackageHash); err == nil {
		t.Fatal("path traversal package was loaded")
	}
}

func TestCatalogRejectsEmptyRoot(t *testing.T) {
	if _, err := NewCatalog("  ").Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Discover() empty root error = %v", err)
	}
}

func TestCatalogRejectsUnknownManifestFieldsAliasesAndUnsafeEntries(t *testing.T) {
	root := t.TempDir()
	writePackage(t, root, "unknown-field", "1.0.0", strings.Replace(validYAML, "id: literature-review", "id: unknown-field\nunknown: true", 1), "instructions")
	writePackage(t, root, "anchor-skill", "1.0.0", strings.Replace(strings.Replace(validYAML, "id: literature-review", "id: anchor-skill", 1), "name: 文献阅读", "name: &shared 文献阅读", 1), "instructions")
	writePackage(t, root, "uppercase-manifest", "1.0.0", strings.Replace(validYAML, "id: literature-review", "id: Uppercase-Manifest", 1), "instructions")
	if err := os.MkdirAll(filepath.Join(root, "literature-review", "not-a-version"), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewCatalog(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Packages) != 0 || len(snapshot.Diagnostics) != 4 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	joined := ""
	for _, diagnostic := range snapshot.Diagnostics {
		joined += diagnostic.Message + "\n"
	}
	if !strings.Contains(joined, "field unknown not found") || !strings.Contains(joined, "aliases and anchors") || !strings.Contains(joined, "invalid Skill version") || !strings.Contains(joined, "invalid Skill id") {
		t.Fatalf("diagnostics = %s", joined)
	}
}

func TestCatalogRejectsInstructionsBeyondDeclaredContextBudget(t *testing.T) {
	root := t.TempDir()
	manifest := strings.Replace(strings.Replace(strings.Replace(validYAML, "id: literature-review", "id: budgeted-skill", 1), "version: 1.2.0", "version: 1.0.0", 1), "max_tokens: 4000", "max_tokens: 256", 1)
	writePackage(t, root, "budgeted-skill", "1.0.0", manifest, strings.Repeat("研", 257))
	result, err := NewCatalog(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packages) != 0 || len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "context.max_tokens") {
		t.Fatalf("over-budget discovery = %#v", result)
	}
}

func TestCatalogRejectsSymlinkInsidePackage(t *testing.T) {
	root := t.TempDir()
	directory := writePackage(t, root, "literature-review", "1.2.0", validYAML, "instructions")
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(directory, "reference.txt")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	snapshot, err := NewCatalog(root).Discover(context.Background())
	if err != nil || len(snapshot.Packages) != 0 || len(snapshot.Diagnostics) != 1 || !strings.Contains(snapshot.Diagnostics[0].Message, "symbolic links") {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
}

func TestCatalogLoadRejectsParentDirectoryReplacedBySymlink(t *testing.T) {
	root := t.TempDir()
	idDirectory := filepath.Join(root, "literature-review")
	writePackage(t, root, "literature-review", "1.2.0", validYAML, "instructions")
	catalog := NewCatalog(root)
	snapshot, err := catalog.Discover(context.Background())
	if err != nil || len(snapshot.Packages) != 1 {
		t.Fatalf("Discover() = %#v, %v", snapshot, err)
	}
	trustedHash := snapshot.Packages[0].Skill.PackageHash
	backup := filepath.Join(root, "trusted-backup")
	if err := os.Rename(idDirectory, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, idDirectory); err != nil {
		_ = os.Rename(backup, idDirectory)
		t.Skipf("directory symlink creation is unavailable: %v", err)
	}
	if _, err := catalog.Load(context.Background(), "literature-review/1.2.0", trustedHash); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("Load() after parent symlink error = %v", err)
	}
}

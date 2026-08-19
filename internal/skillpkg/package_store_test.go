package skillpkg

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/skill"
)

func packageManifest(id, version string) string {
	return `schema_version: 1
id: ` + id + `
name: Package fixture
version: ` + version + `
description: Package store integration fixture
entry: SKILL.md
activation:
  mode: explicit
requires:
  tools: []
  optional_tools: []
permissions: []
compatibility:
  sciaide: ">=0.2.0 <1.0.0"
context:
  max_tokens: 2000
`
}

func writeSourcePackage(t *testing.T, root, id, version, instructions string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skill.yaml"), []byte(packageManifest(id, version)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(instructions), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestPackageStore(t *testing.T) (*FilePackageStore, string, string, string) {
	t.Helper()
	root := t.TempDir()
	installed := filepath.Join(root, "skills")
	staging := filepath.Join(root, "cache", "skill-staging")
	backup := filepath.Join(root, "backups", "skills")
	return NewFilePackageStore(installed, staging, backup), installed, staging, backup
}

func TestFilePackageStoreInstallsIdempotentlyAndRollsBackReplacement(t *testing.T) {
	ctx := context.Background()
	store, installedRoot, _, backupRoot := newTestPackageStore(t)
	source := writeSourcePackage(t, filepath.Join(t.TempDir(), "source"), "literature-review", "1.0.0", "original instructions")
	first, err := store.Install(ctx, skill.InstallCommand{SourcePath: source, SourceKind: skill.SourceFolder})
	if err != nil {
		t.Fatal(err)
	}
	if first.Replaced || first.Idempotent || first.Source.Kind != skill.SourceFolder || !first.Source.Archived {
		t.Fatalf("first install = %#v", first)
	}
	if _, err := os.Stat(filepath.Join(backupRoot, filepath.FromSlash(first.Source.ArchiveRelativePath))); err != nil {
		t.Fatalf("archived folder source: %v", err)
	}
	originalHash := first.Package.Skill.PackageHash
	uninstalled, err := store.Uninstall(ctx, first.Package.Skill)
	if err != nil || !uninstalled.Moved {
		t.Fatalf("Uninstall() = %#v, %v", uninstalled, err)
	}
	if err := store.RollbackUninstall(ctx, uninstalled); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCatalog(installedRoot).Load(ctx, "literature-review/1.0.0", originalHash); err != nil {
		t.Fatalf("restored uninstall: %v", err)
	}
	second, err := store.Install(ctx, skill.InstallCommand{SourcePath: source, SourceKind: skill.SourceFolder})
	if err != nil || !second.Idempotent || second.Package.Skill.PackageHash != originalHash {
		t.Fatalf("idempotent install = %#v, %v", second, err)
	}

	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("replacement instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(ctx, skill.InstallCommand{SourcePath: source, SourceKind: skill.SourceFolder}); err == nil || !strings.Contains(err.Error(), "explicit replacement") {
		t.Fatalf("replacement without confirmation error = %v", err)
	}
	replacement, err := store.Install(ctx, skill.InstallCommand{SourcePath: source, SourceKind: skill.SourceFolder, ReplaceExisting: true})
	if err != nil || !replacement.Replaced || replacement.PreviousPath == "" || replacement.Package.Skill.PackageHash == originalHash {
		t.Fatalf("replacement install = %#v, %v", replacement, err)
	}
	if err := store.RollbackInstall(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	restored, err := NewCatalog(installedRoot).Load(ctx, "literature-review/1.0.0", originalHash)
	if err != nil || restored.Instructions != "original instructions" {
		t.Fatalf("restored package = %#v, %v", restored, err)
	}
}

type zipFixtureEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func writeZIPFixture(t *testing.T, filename string, entries []zipFixtureEntry) {
	t.Helper()
	output, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFilePackageStoreInstallsZIPWithSingleWrapper(t *testing.T) {
	store, installedRoot, _, backupRoot := newTestPackageStore(t)
	archive := filepath.Join(t.TempDir(), "skill.zip")
	writeZIPFixture(t, archive, []zipFixtureEntry{
		{name: "package/skill.yaml", data: []byte(packageManifest("academic-writing", "1.1.0"))},
		{name: "package/SKILL.md", data: []byte("writing instructions")},
		{name: "package/references/style.md", data: []byte("style")},
	})
	operation, err := store.Install(context.Background(), skill.InstallCommand{SourcePath: archive, SourceKind: skill.SourceZIP})
	if err != nil {
		t.Fatal(err)
	}
	if operation.Source.Kind != skill.SourceZIP || operation.Source.Name != "skill.zip" || operation.Package.Instructions != "writing instructions" {
		t.Fatalf("ZIP operation = %#v", operation)
	}
	if _, err := NewCatalog(installedRoot).Load(context.Background(), "academic-writing/1.1.0", operation.Package.Skill.PackageHash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(backupRoot, filepath.FromSlash(operation.Source.ArchiveRelativePath))); err != nil {
		t.Fatal(err)
	}
}

func TestFilePackageStoreReadsRunResourceFromInstalledAndArchivedPackage(t *testing.T) {
	ctx := context.Background()
	store, _, _, _ := newTestPackageStore(t)
	source := writeSourcePackage(t, filepath.Join(t.TempDir(), "source"), "resource-skill", "1.0.0", "Read references/note.md before answering.")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "references", "note.md"), []byte("immutable evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation, err := store.Install(ctx, skill.InstallCommand{SourcePath: source, SourceKind: skill.SourceFolder})
	if err != nil {
		t.Fatal(err)
	}
	installed := operation.Package.Skill
	selected := skill.RunSkill{Manifest: installed.Manifest, PackagePath: installed.PackageRelativePath, ManifestHash: installed.ManifestHash, ContentHash: installed.ContentHash, PackageHash: installed.PackageHash, SourceHash: operation.Source.Hash, SourceArchive: operation.Source.ArchiveRelativePath}
	read, err := store.ReadResource(ctx, selected, "references/note.md", 1024)
	if err != nil || string(read.Content) != "immutable evidence" || read.Truncated {
		t.Fatalf("installed resource = %#v, %v", read, err)
	}
	if _, err := store.Uninstall(ctx, installed); err != nil {
		t.Fatal(err)
	}
	read, err = store.ReadResource(ctx, selected, "references/note.md", 1024)
	if err != nil || string(read.Content) != "immutable evidence" {
		t.Fatalf("archived resource = %#v, %v", read, err)
	}
	if _, err := store.ReadResource(ctx, selected, "../outside.txt", 1024); err == nil {
		t.Fatal("escaping Skill resource path was accepted")
	}
	if _, err := store.ReadResource(ctx, selected, "references/note.md:stream", 1024); err == nil {
		t.Fatal("Windows alternate-data-stream resource path was accepted")
	}
}

func TestFilePackageStoreImportsCodexStyleFolderWithoutChangingSourceArchive(t *testing.T) {
	store, installedRoot, _, backupRoot := newTestPackageStore(t)
	source := filepath.Join(t.TempDir(), "evidence-review")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	instructions := "---\nname: evidence-review\ndescription: Review evidence: methods and limitations\nmetadata:\n  ignored: true\n---\n\nRead methods before conclusions."
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(instructions), 0o600); err != nil {
		t.Fatal(err)
	}
	operation, err := store.Install(context.Background(), skill.InstallCommand{SourcePath: source, SourceKind: skill.SourceFolder})
	if err != nil {
		t.Fatal(err)
	}
	if operation.Package.Skill.Manifest.ID != "evidence-review" || operation.Package.Skill.Manifest.Version != "0.0.0" || operation.Package.Instructions != instructions {
		t.Fatalf("Codex-compatible import = %#v", operation.Package)
	}
	if _, err := os.Stat(filepath.Join(installedRoot, "evidence-review", "0.0.0", "skill.yaml")); err != nil {
		t.Fatalf("generated native manifest: %v", err)
	}
	archivePath := filepath.Join(backupRoot, filepath.FromSlash(operation.Source.ArchiveRelativePath))
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if entry.Name == "skill.yaml" {
			t.Fatal("generated manifest leaked into the preserved original source snapshot")
		}
	}
}

func TestFilePackageStoreImportsCodexStyleZIPAndRejectsMalformedFrontmatter(t *testing.T) {
	store, _, _, _ := newTestPackageStore(t)
	archive := filepath.Join(t.TempDir(), "writing.zip")
	writeZIPFixture(t, archive, []zipFixtureEntry{{
		name: "academic-writing/SKILL.md",
		data: []byte("---\nname: academic-writing\ndescription: Draft clearly: claims and evidence\n---\n\nWrite with citations."),
	}})
	operation, err := store.Install(context.Background(), skill.InstallCommand{SourcePath: archive, SourceKind: skill.SourceZIP})
	if err != nil || operation.Package.Skill.Manifest.ID != "academic-writing" || operation.Package.Skill.Manifest.Description != "Draft clearly: claims and evidence" {
		t.Fatalf("Codex-compatible ZIP import = %#v, %v", operation, err)
	}

	malformed := filepath.Join(t.TempDir(), "malformed")
	if err := os.MkdirAll(malformed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformed, "SKILL.md"), []byte("no frontmatter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(context.Background(), skill.InstallCommand{SourcePath: malformed, SourceKind: skill.SourceFolder}); err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Fatalf("malformed Codex-compatible package error = %v", err)
	}
}

func TestFilePackageStoreRejectsFolderSymlinkAndCancellation(t *testing.T) {
	store, installedRoot, stagingRoot, _ := newTestPackageStore(t)
	source := writeSourcePackage(t, filepath.Join(t.TempDir(), "source"), "linked-skill", "1.0.0", "instructions")
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(source, "reference.txt")); err == nil {
		if _, err := store.Install(context.Background(), skill.InstallCommand{SourcePath: source, SourceKind: skill.SourceFolder}); err == nil || !strings.Contains(err.Error(), "symbolic links") {
			t.Fatalf("folder symlink error = %v", err)
		}
		if err := os.Remove(filepath.Join(source, "reference.txt")); err != nil {
			t.Fatal(err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Install(cancelled, skill.InstallCommand{SourcePath: source, SourceKind: skill.SourceFolder}); err == nil {
		t.Fatal("cancelled folder installation completed")
	}
	if entries, _ := os.ReadDir(installedRoot); len(entries) != 0 {
		t.Fatalf("failed folder install left installed files: %v", entries)
	}
	if entries, _ := os.ReadDir(stagingRoot); len(entries) != 0 {
		t.Fatalf("failed folder install left staging files: %v", entries)
	}
}

func TestFilePackageStoreRejectsSymlinkInsideBackupRoot(t *testing.T) {
	store, installedRoot, _, backupRoot := newTestPackageStore(t)
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(backupRoot, "packages")); err != nil {
		t.Skipf("directory symlink creation is unavailable: %v", err)
	}
	source := writeSourcePackage(t, filepath.Join(t.TempDir(), "source"), "backup-link", "1.0.0", "instructions")
	if _, err := store.Install(context.Background(), skill.InstallCommand{SourcePath: source, SourceKind: skill.SourceFolder}); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("backup symlink error = %v", err)
	}
	if entries, _ := os.ReadDir(installedRoot); len(entries) != 0 {
		t.Fatalf("unsafe backup root left installed files: %v", entries)
	}
	if entries, _ := os.ReadDir(external); len(entries) != 0 {
		t.Fatalf("unsafe backup symlink was followed: %v", entries)
	}
}

func TestFilePackageStoreRejectsUnsafeZIPs(t *testing.T) {
	manifest := []byte(packageManifest("unsafe-skill", "1.0.0"))
	tests := []struct {
		name    string
		entries []zipFixtureEntry
	}{
		{name: "traversal", entries: []zipFixtureEntry{{name: "../escaped.txt", data: []byte("escape")}, {name: "skill.yaml", data: manifest}, {name: "SKILL.md", data: []byte("x")}}},
		{name: "backslash", entries: []zipFixtureEntry{{name: `folder\skill.yaml`, data: manifest}, {name: "SKILL.md", data: []byte("x")}}},
		{name: "absolute", entries: []zipFixtureEntry{{name: "/skill.yaml", data: manifest}, {name: "SKILL.md", data: []byte("x")}}},
		{name: "reserved", entries: []zipFixtureEntry{{name: "skill.yaml", data: manifest}, {name: "SKILL.md", data: []byte("x")}, {name: "NUL.txt", data: []byte("x")}}},
		{name: "symlink", entries: []zipFixtureEntry{{name: "skill.yaml", data: manifest}, {name: "SKILL.md", data: []byte("x")}, {name: "link", data: []byte("outside"), mode: os.ModeSymlink | 0o777}}},
		{name: "case duplicate", entries: []zipFixtureEntry{{name: "skill.yaml", data: manifest}, {name: "SKILL.yaml", data: manifest}, {name: "SKILL.md", data: []byte("x")}}},
		{name: "multiple roots", entries: []zipFixtureEntry{{name: "one/skill.yaml", data: manifest}, {name: "one/SKILL.md", data: []byte("x")}, {name: "two/extra.txt", data: []byte("x")}}},
		{name: "oversized", entries: []zipFixtureEntry{{name: "skill.yaml", data: manifest}, {name: "SKILL.md", data: []byte("x")}, {name: "large.bin", data: bytes.Repeat([]byte("a"), maxSingleFile+1)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, installedRoot, stagingRoot, _ := newTestPackageStore(t)
			archive := filepath.Join(t.TempDir(), "unsafe.zip")
			writeZIPFixture(t, archive, test.entries)
			if _, err := store.Install(context.Background(), skill.InstallCommand{SourcePath: archive, SourceKind: skill.SourceZIP}); err == nil {
				t.Fatal("unsafe ZIP was installed")
			}
			if entries, _ := os.ReadDir(installedRoot); len(entries) != 0 {
				t.Fatalf("unsafe ZIP left installed files: %v", entries)
			}
			if entries, _ := os.ReadDir(stagingRoot); len(entries) != 0 {
				t.Fatalf("unsafe ZIP left staging files: %v", entries)
			}
		})
	}
}

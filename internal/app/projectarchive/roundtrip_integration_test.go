package projectarchive_test

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestRepetitiveDocumentsRealProjectRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaces, trash := filepath.Join(root, "workspaces"), filepath.Join(root, "trash")
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), workspaces, trash)
	created, err := projects.Create(ctx, "Repetitive scientific data", "round trip")
	if err != nil {
		t.Fatal(err)
	}
	attachments := attachment.NewService(sqlite.NewAttachmentRepository(store.DB()), projects)
	payloads := map[string][]byte{
		"repeated.txt": bytes.Repeat([]byte("measurement repeated\n"), 110000),
		"repeated.csv": bytes.Repeat([]byte("0,0,0,0,0,0,0,0\n"), 140000),
	}
	for name, payload := range payloads {
		source := filepath.Join(created.WorkspacePath, name)
		if err := os.WriteFile(source, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		batch, err := attachments.ImportPaths(ctx, created.ID, []string{source})
		if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 1 {
			t.Fatalf("import %s: %#v, %v", name, batch, err)
		}
	}
	archives, err := projectarchive.NewService(sqlite.NewProjectArchiveRepository(store.DB()), projects, workspaces, filepath.Join(root, "staging"), trash, "test")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := archives.Export(ctx, created.ID, filepath.Join(root, "roundtrip.sciaide-project"))
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(exported.Path)
	if err != nil {
		t.Fatal(err)
	}
	large := 0
	for _, entry := range z.File {
		if entry.UncompressedSize64 > 1<<20 {
			large++
			if entry.Method != zip.Store {
				t.Errorf("large entry %s not stored", entry.Name)
			}
		}
	}
	z.Close()
	if large < 2 {
		t.Fatalf("large archive entries = %d", large)
	}
	restored, err := archives.Restore(ctx, projectarchive.RestoreCommand{Path: exported.Path})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Project.ID == created.ID {
		t.Fatal("restore reused source project")
	}
	// Compare every exported file, including parser caches, against its restored
	// bytes. The real SQLite repository validates and remaps the project graph.
	for _, entry := range exported.Manifest.Files {
		original, err := os.ReadFile(filepath.Join(project.PrivateDataPath(created), filepath.FromSlash(entry.StorageRelativePath)))
		if err != nil {
			t.Fatal(err)
		}
		copy, err := os.ReadFile(filepath.Join(project.PrivateDataPath(restored.Project), filepath.FromSlash(entry.StorageRelativePath)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, copy) {
			t.Errorf("restored bytes differ: %s", entry.Path)
		}
	}
	rows, err := attachments.List(ctx, restored.Project.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("restored attachments = %d, %v", len(rows), err)
	}
}

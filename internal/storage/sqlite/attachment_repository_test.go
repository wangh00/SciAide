package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
)

func TestAttachmentRepositoryPersistsAndCascadesWithProject(t *testing.T) {
	root := t.TempDir()
	store, err := Open(context.Background(), filepath.Join(root, "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	created, err := projects.Create(context.Background(), "Attachments", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := attachment.Attachment{ID: "attachment", ProjectID: created.ID, OriginalName: "paper.pdf", MIMEType: "application/pdf", Format: document.FormatPDF, SizeBytes: 12, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StorageRelativePath: "attachments/objects/aa/paper.pdf", CacheRelativePath: "cache/documents/attachment.json", Status: attachment.StatusParsing, CreatedAt: now, UpdatedAt: now}
	repository := NewAttachmentRepository(store.DB())
	if err := repository.Create(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	value.Status, value.UnitCount, value.ExtractedRunes, value.Truncated, value.UpdatedAt = attachment.StatusReady, 3, 120, true, now.Add(time.Second)
	if err := repository.UpdateParse(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Get(context.Background(), value.ID)
	if err != nil || loaded.Status != attachment.StatusReady || loaded.UnitCount != 3 || !loaded.Truncated {
		t.Fatalf("loaded attachment = %#v, %v", loaded, err)
	}
	if found, exists, err := repository.FindByHash(context.Background(), created.ID, value.SHA256); err != nil || !exists || found.ID != value.ID {
		t.Fatalf("find by hash = %#v, %v, %v", found, exists, err)
	}
	if _, err := projects.Remove(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM attachments WHERE id=?`, value.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("attachment cascade count = %d, %v", count, err)
	}
}

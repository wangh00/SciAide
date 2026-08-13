package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
)

func TestProjectRepositoryPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sciaide.db")

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	root := t.TempDir()
	service := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	created, err := service.Create(ctx, "RNA 研究", "P0 persistence test")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	loaded, err := NewProjectRepository(reopened.DB()).Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if loaded.Name != created.Name || loaded.Description != created.Description {
		t.Fatalf("loaded project = %#v, want %#v", loaded, created)
	}

	var migrations int
	if err := reopened.DB().QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrations != 4 {
		t.Fatalf("migration count = %d, want 4", migrations)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "sciaide.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	var enabled int
	if err := store.DB().QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys = %d, want 1", enabled)
	}
}

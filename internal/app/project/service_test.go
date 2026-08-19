package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type memoryRepository struct{ values map[string]Project }

func (r *memoryRepository) Create(_ context.Context, value Project) error {
	r.values[value.ID] = value
	return nil
}
func (r *memoryRepository) Get(_ context.Context, id string) (Project, error) {
	value, ok := r.values[id]
	if !ok {
		return Project{}, fmt.Errorf("project not found")
	}
	return value, nil
}
func (r *memoryRepository) List(context.Context) ([]Project, error) {
	values := make([]Project, 0, len(r.values))
	for _, value := range r.values {
		values = append(values, value)
	}
	return values, nil
}
func (r *memoryRepository) UpdateWorkspace(_ context.Context, id, path, kind string, at time.Time) error {
	value := r.values[id]
	value.WorkspacePath, value.WorkspaceKind, value.UpdatedAt = path, kind, at
	r.values[id] = value
	return nil
}
func (r *memoryRepository) Delete(_ context.Context, id string) error {
	delete(r.values, id)
	return nil
}

func TestManagedAndExternalWorkspaceRemoval(t *testing.T) {
	root := t.TempDir()
	managed, trash := filepath.Join(root, "workspaces"), filepath.Join(root, "trash")
	repository := &memoryRepository{values: map[string]Project{}}
	service := NewService(repository, managed, trash)
	ctx := context.Background()
	created, err := service.Create(ctx, "managed", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(created.WorkspacePath, PrivateDirectoryName, projectMarkerName)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(created.WorkspacePath, ".sciaide-workspace.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy marker should not remain: %v", err)
	}
	removed, err := service.Remove(ctx, created.ID)
	if err != nil || removed.TrashPath == "" {
		t.Fatalf("Remove() = %#v, %v", removed, err)
	}
	if _, err := os.Stat(removed.TrashPath); err != nil {
		t.Fatal(err)
	}

	external := filepath.Join(root, "external")
	created, err = service.CreateWithWorkspace(ctx, CreateCommand{Name: "external", WorkspacePath: external})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(external, PrivateDirectoryName, "attachments")); err != nil {
		t.Fatal(err)
	}
	removed, err = service.Remove(ctx, created.ID)
	if err != nil || !removed.WorkspacePreserved {
		t.Fatalf("external Remove() = %#v, %v", removed, err)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("external workspace was removed: %v", err)
	}
}

func TestReconcileMigratesLegacyMarkerIntoPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "external")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, legacyMarkerName), []byte("{\"version\":1,\"projectId\":\"existing\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{values: map[string]Project{"existing": {ID: "existing", Name: "Existing", WorkspacePath: workspace, WorkspaceKind: WorkspaceExternal}}}
	service := NewService(repository, filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	if err := service.ReconcileWorkspacePaths(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, PrivateDirectoryName, projectMarkerName)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, legacyMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("legacy marker was not removed: %v", err)
	}
}

func TestCreateRefusesUnownedPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(root, "external")
	if err := os.MkdirAll(filepath.Join(external, PrivateDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{values: map[string]Project{}}
	service := NewService(repository, filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	if _, err := service.CreateWithWorkspace(context.Background(), CreateCommand{Name: "unsafe", WorkspacePath: external}); err == nil {
		t.Fatal("unowned private directory was adopted")
	}
}

func TestCreateRefusesPrivateDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(root, "external")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(external, PrivateDirectoryName)); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		t.Fatal(err)
	}
	repository := &memoryRepository{values: map[string]Project{}}
	service := NewService(repository, filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	if _, err := service.CreateWithWorkspace(context.Background(), CreateCommand{Name: "unsafe", WorkspacePath: external}); err == nil {
		t.Fatal("private directory symlink was accepted")
	}
}

func TestReconcileLegacyWorkspacePathIsIdempotent(t *testing.T) {
	root := t.TempDir()
	repository := &memoryRepository{values: map[string]Project{"legacy": {ID: "legacy", Name: "old"}}}
	service := NewService(repository, filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	if err := service.ReconcileWorkspacePaths(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := repository.values["legacy"].WorkspacePath
	if first == "" {
		t.Fatal("workspace path was not reconciled")
	}
	if err := service.ReconcileWorkspacePaths(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.values["legacy"].WorkspacePath != first {
		t.Fatal("reconciliation was not idempotent")
	}
}

func TestReconcileDoesNotRecreateUnavailableExternalWorkspace(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "detached-drive", "project")
	repository := &memoryRepository{values: map[string]Project{"external": {ID: "external", Name: "External", WorkspacePath: missing, WorkspaceKind: WorkspaceExternal}}}
	service := NewService(repository, filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	if err := service.ReconcileWorkspacePaths(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("unavailable workspace was recreated: %v", err)
	}
}

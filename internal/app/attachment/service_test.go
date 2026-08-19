package attachment

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
)

type attachmentMemoryRepository struct{ values map[string]Attachment }

func (r *attachmentMemoryRepository) Create(_ context.Context, value Attachment) error {
	for _, existing := range r.values {
		if existing.ProjectID == value.ProjectID && existing.SHA256 == value.SHA256 {
			return fmt.Errorf("duplicate attachment")
		}
	}
	r.values[value.ID] = value
	return nil
}
func (r *attachmentMemoryRepository) Get(_ context.Context, id string) (Attachment, error) {
	value, exists := r.values[id]
	if !exists {
		return Attachment{}, fmt.Errorf("attachment not found")
	}
	return value, nil
}
func (r *attachmentMemoryRepository) FindByHash(_ context.Context, projectID, hash string) (Attachment, bool, error) {
	for _, value := range r.values {
		if value.ProjectID == projectID && value.SHA256 == hash {
			return value, true, nil
		}
	}
	return Attachment{}, false, nil
}
func (r *attachmentMemoryRepository) ListByProject(_ context.Context, projectID string) ([]Attachment, error) {
	values := make([]Attachment, 0)
	for _, value := range r.values {
		if value.ProjectID == projectID {
			values = append(values, value)
		}
	}
	return values, nil
}
func (r *attachmentMemoryRepository) UpdateParse(_ context.Context, value Attachment) error {
	if _, exists := r.values[value.ID]; !exists {
		return fmt.Errorf("attachment not found")
	}
	r.values[value.ID] = value
	return nil
}

type attachmentProjectLoader struct{ value project.Project }

func (l attachmentProjectLoader) Get(_ context.Context, projectID string) (project.Project, error) {
	if projectID != l.value.ID {
		return project.Project{}, fmt.Errorf("project not found")
	}
	return l.value, nil
}

func TestImportDeduplicatesAndRebuildsDeletedCache(t *testing.T) {
	workspace := t.TempDir()
	createPrivateFixture(t, workspace)
	source := filepath.Join(workspace, "paper.md")
	if err := os.WriteFile(source, []byte("# Evidence\nalpha result is 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &attachmentMemoryRepository{values: map[string]Attachment{}}
	service := NewService(repository, attachmentProjectLoader{value: project.Project{ID: "project", WorkspacePath: workspace}})
	first, err := service.ImportPaths(context.Background(), "project", []string{source})
	if err != nil || len(first.Errors) != 0 || len(first.Attachments) != 1 {
		t.Fatalf("first import = %#v, %v", first, err)
	}
	value := first.Attachments[0]
	if value.Status != StatusReady || value.UnitCount != 1 || value.SHA256 == "" {
		t.Fatalf("attachment = %#v", value)
	}
	if _, err := os.Stat(filepath.Join(workspace, project.PrivateDirectoryName, filepath.FromSlash(value.StorageRelativePath))); err != nil {
		t.Fatal(err)
	}
	second, err := service.ImportPaths(context.Background(), "project", []string{source})
	if err != nil || len(second.Attachments) != 1 || second.Attachments[0].ID != value.ID || len(repository.values) != 1 {
		t.Fatalf("duplicate import = %#v, %v", second, err)
	}
	cache := filepath.Join(workspace, project.PrivateDirectoryName, filepath.FromSlash(value.CacheRelativePath))
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	loaded, parsed, err := service.Parsed(context.Background(), "project", value.ID)
	if err != nil || loaded.ID != value.ID || len(parsed.Units) != 1 {
		t.Fatalf("cache rebuild = %#v, %#v, %v", loaded, parsed, err)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("parsed cache was not rebuilt")
	}
	if err := os.WriteFile(cache, []byte(`{"schemaVersion":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, parsed, err := service.Parsed(context.Background(), "project", value.ID); err != nil || parsed.SchemaVersion != document.SchemaVersion || len(parsed.Units) != 1 {
		t.Fatalf("invalid cache was not rebuilt: %#v, %v", parsed, err)
	}
	stored := filepath.Join(workspace, project.PrivateDirectoryName, filepath.FromSlash(value.StorageRelativePath))
	contents, err := os.ReadFile(stored)
	if err != nil {
		t.Fatal(err)
	}
	contents[0] ^= 1
	if err := os.WriteFile(stored, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Parsed(context.Background(), "project", value.ID); err == nil {
		t.Fatal("tampered attachment object was reparsed")
	}
}

func TestImportRejectsPrivateDataAndResolveEnforcesProject(t *testing.T) {
	workspace := t.TempDir()
	createPrivateFixture(t, workspace)
	privateFile := filepath.Join(workspace, project.PrivateDirectoryName, "cache", "private.txt")
	if err := os.WriteFile(privateFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &attachmentMemoryRepository{values: map[string]Attachment{}}
	service := NewService(repository, attachmentProjectLoader{value: project.Project{ID: "project", WorkspacePath: workspace}})
	batch, err := service.ImportPaths(context.Background(), "project", []string{privateFile})
	if err != nil || len(batch.Errors) != 1 || len(batch.Attachments) != 0 {
		t.Fatalf("private import = %#v, %v", batch, err)
	}
	repository.values["attachment"] = Attachment{ID: "attachment", ProjectID: "other", Status: StatusReady}
	if _, err := service.Resolve(context.Background(), "project", []string{"attachment"}); err == nil {
		t.Fatal("cross-project attachment was resolved")
	}
}

func TestParsedRejectsReplacedProjectMarker(t *testing.T) {
	workspace := t.TempDir()
	createPrivateFixture(t, workspace)
	source := filepath.Join(workspace, "notes.txt")
	if err := os.WriteFile(source, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &attachmentMemoryRepository{values: map[string]Attachment{}}
	service := NewService(repository, attachmentProjectLoader{value: project.Project{ID: "project", WorkspacePath: workspace}})
	batch, err := service.ImportPaths(context.Background(), "project", []string{source})
	if err != nil || len(batch.Attachments) != 1 {
		t.Fatalf("import = %#v, %v", batch, err)
	}
	marker := filepath.Join(workspace, project.PrivateDirectoryName, "project.json")
	if err := os.WriteFile(marker, []byte("{\"version\":1,\"projectId\":\"other\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Parsed(context.Background(), "project", batch.Attachments[0].ID); err == nil {
		t.Fatal("replaced project marker was trusted")
	}
}

func createPrivateFixture(t *testing.T, workspace string) {
	t.Helper()
	for _, name := range []string{"attachments", "cache", "artifacts", "tmp"} {
		if err := os.MkdirAll(filepath.Join(workspace, project.PrivateDirectoryName, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, project.PrivateDirectoryName, "project.json"), []byte("{\"version\":1,\"projectId\":\"project\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

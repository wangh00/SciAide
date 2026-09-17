package attachment

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
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

type attachmentConversationValidator struct {
	values map[string]conversation.Conversation
}

func (v attachmentConversationValidator) GetConversation(_ context.Context, conversationID string) (conversation.Conversation, error) {
	value, ok := v.values[conversationID]
	if !ok {
		return conversation.Conversation{}, fmt.Errorf("conversation not found")
	}
	return value, nil
}

func TestConversationAttachmentsRequireLiveProjectOwnership(t *testing.T) {
	workspace := t.TempDir()
	createPrivateFixture(t, workspace)
	source := filepath.Join(workspace, "chat.txt")
	if err := os.WriteFile(source, []byte("conversation-local input"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &attachmentMemoryRepository{values: map[string]Attachment{}}
	service := NewService(repository, attachmentProjectLoader{value: project.Project{ID: "project", WorkspacePath: workspace}})
	service.SetConversationValidator(attachmentConversationValidator{values: map[string]conversation.Conversation{
		"current": {ID: "current", ProjectID: "project"},
		"foreign": {ID: "foreign", ProjectID: "other"},
	}})

	batch, err := service.ImportPathsForConversation(context.Background(), "project", []string{source}, "current")
	if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 1 {
		t.Fatalf("conversation import = %#v, %v", batch, err)
	}
	value := batch.Attachments[0]
	if value.ScopeKind != ScopeConversation || value.ResearchTaskID != "conversation:current" {
		t.Fatalf("conversation attachment scope = %#v", value)
	}
	visible, err := service.ListForConversation(context.Background(), "project", "current")
	if err != nil || len(visible) != 1 || visible[0].ID != value.ID {
		t.Fatalf("conversation attachments = %#v, %v", visible, err)
	}
	if _, err := service.ListForConversation(context.Background(), "project", "missing"); err == nil {
		t.Fatal("missing conversation was accepted")
	}
	if _, err := service.ImportPathsForConversation(context.Background(), "project", []string{source}, "foreign"); err == nil {
		t.Fatal("cross-project conversation import was accepted")
	}
	if _, err := service.ResolveForConversation(context.Background(), "project", "foreign", []string{value.ID}); err == nil {
		t.Fatal("cross-project conversation resolved an attachment")
	}
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

func TestImportAndResolvePNGImage(t *testing.T) {
	workspace := t.TempDir()
	createPrivateFixture(t, workspace)
	imageValue := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	imageValue.Set(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	imageValue.Set(1, 0, color.NRGBA{R: 200, G: 210, B: 220, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageValue); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "figure.png")
	if err := os.WriteFile(source, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &attachmentMemoryRepository{values: map[string]Attachment{}}
	service := NewService(repository, attachmentProjectLoader{value: project.Project{ID: "project", WorkspacePath: workspace}})
	batch, err := service.ImportPaths(context.Background(), "project", []string{source})
	if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 1 {
		t.Fatalf("image import = %#v, %v", batch, err)
	}
	value := batch.Attachments[0]
	if value.Status != StatusReady || value.Format != document.FormatImage || value.MIMEType != "image/png" || value.UnitCount != 0 || value.ParseMetadata["width"] != "2" || value.ParseMetadata["height"] != "1" {
		t.Fatalf("image attachment = %#v", value)
	}
	part, err := service.ResolveImage(context.Background(), "project", value.ID)
	if err != nil || part.Type != "input_image" || part.MediaType != "image/png" || part.AttachmentID != value.ID || part.Name != "figure.png" {
		t.Fatalf("resolved image = %#v, %v", part, err)
	}
	if _, _, err := service.Parsed(context.Background(), "project", value.ID); err == nil {
		t.Fatal("image was accepted by the parsed-document path")
	}
	if storedValue := repository.values[value.ID]; storedValue.Status != StatusReady || storedValue.ErrorMessage != "" {
		t.Fatalf("document inspection changed image status = %#v", storedValue)
	}
	failed := repository.values[value.ID]
	failed.Status = StatusFailed
	failed.ErrorMessage = `unsupported document format "image"`
	repository.values[value.ID] = failed
	reimported, err := service.ImportPaths(context.Background(), "project", []string{source})
	if err != nil || len(reimported.Errors) != 0 || len(reimported.Attachments) != 1 || reimported.Attachments[0].Status != StatusReady || reimported.Attachments[0].ErrorMessage != "" {
		t.Fatalf("image reimport repair = %#v, %v", reimported, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(part.Data)
	if err != nil || !bytes.Equal(decoded, encoded.Bytes()) {
		t.Fatalf("resolved image bytes changed: %v", err)
	}
	stored := filepath.Join(workspace, project.PrivateDirectoryName, filepath.FromSlash(value.StorageRelativePath))
	if err := os.WriteFile(stored, append([]byte(nil), encoded.Bytes()[:encoded.Len()-1]...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveImage(context.Background(), "project", value.ID); err == nil {
		t.Fatal("tampered image object was resolved")
	}
}

func TestImportUsesVerifiedImageContentInsteadOfExtension(t *testing.T) {
	workspace := t.TempDir()
	createPrivateFixture(t, workspace)
	// A VP8X WebP header for a 1100x688 canvas, deliberately named .jpg.
	encoded := []byte{
		'R', 'I', 'F', 'F', 22, 0, 0, 0, 'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X', 10, 0, 0, 0, 0, 0, 0, 0,
		0x4b, 0x04, 0, 0xaf, 0x02, 0,
	}
	source := filepath.Join(workspace, "downloaded.jpg")
	if err := os.WriteFile(source, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &attachmentMemoryRepository{values: map[string]Attachment{}}
	service := NewService(repository, attachmentProjectLoader{value: project.Project{ID: "project", WorkspacePath: workspace}})
	batch, err := service.ImportPaths(context.Background(), "project", []string{source})
	if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 1 {
		t.Fatalf("renamed WebP import = %#v, %v", batch, err)
	}
	value := batch.Attachments[0]
	if value.OriginalName != "downloaded.jpg" || value.MIMEType != "image/webp" || value.ParseMetadata["width"] != "1100" || value.ParseMetadata["height"] != "688" {
		t.Fatalf("renamed WebP attachment = %#v", value)
	}
	part, err := service.ResolveImage(context.Background(), "project", value.ID)
	if err != nil || part.MediaType != "image/webp" || part.Name != "downloaded.jpg" {
		t.Fatalf("resolved renamed WebP = %#v, %v", part, err)
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

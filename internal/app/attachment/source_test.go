package attachment

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/project"
)

func TestConversationSourceImportPreservesOriginalAndParsedCode(t *testing.T) {
	workspace := t.TempDir()
	createPrivateFixture(t, workspace)
	source := filepath.Join(workspace, "analysis.py")
	code := "# imported, never executed\ndef mean(values):\n    return sum(values) / len(values)\n"
	if err := os.WriteFile(source, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	repo := &attachmentMemoryRepository{values: map[string]Attachment{}}
	selected := project.Project{ID: "project", WorkspacePath: workspace}
	service := NewService(repo, attachmentProjectLoader{value: selected})
	service.SetConversationValidator(attachmentConversationValidator{values: map[string]conversation.Conversation{"current": {ID: "current", ProjectID: "project"}, "other": {ID: "other", ProjectID: "project"}}})
	batch, err := service.ImportPathsForConversation(context.Background(), "project", []string{source}, "current")
	if err != nil || len(batch.Errors) > 0 || len(batch.Attachments) != 1 {
		t.Fatalf("%+v %v", batch, err)
	}
	item, parsed, err := service.Parsed(context.Background(), "project", batch.Attachments[0].ID)
	if err != nil || len(parsed.Units) != 1 || parsed.Units[0].Content != code || parsed.Metadata["sourceLanguage"] != "python" {
		t.Fatalf("%+v %v", parsed, err)
	}
	stored, err := os.ReadFile(filepath.Join(project.PrivateDataPath(selected), filepath.FromSlash(item.StorageRelativePath)))
	if err != nil || string(stored) != code || item.SHA256 == "" || item.ScopeKind != ScopeConversation {
		t.Fatal("original bytes or scope lost", err)
	}
	if _, err := service.ResolveForConversation(context.Background(), "project", "other", []string{item.ID}); err == nil {
		t.Fatal("private source leaked to other conversation")
	}
	_, handle, err := service.OpenRevisionSource(context.Background(), "project", "current", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	actual, err := io.ReadAll(handle)
	if err != nil || string(actual) != code {
		t.Fatal("source handle changed code", err)
	}
	if _, handle, err := service.OpenRevisionSource(context.Background(), "project", "other", item.ID); err == nil {
		handle.Close()
		t.Fatal("source adapter leaked another conversation")
	}
	if _, handle, err := service.OpenRevisionData(context.Background(), "project", "current", item.ID); err == nil {
		handle.Close()
		t.Fatal("source treated as table input")
	}
}

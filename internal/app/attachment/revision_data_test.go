package attachment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/project"
)

func TestDiscussionTablesReadableButNotPromotedToLiterature(t *testing.T) {
	workspace := t.TempDir()
	createPrivateFixture(t, workspace)
	path := filepath.Join(workspace, "data.csv")
	if err := os.WriteFile(path, []byte("group,value\na,2\nb,9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := &attachmentMemoryRepository{values: map[string]Attachment{}}
	s := NewService(repo, attachmentProjectLoader{value: project.Project{ID: "project", WorkspacePath: workspace}})
	s.SetConversationValidator(attachmentConversationValidator{values: map[string]conversation.Conversation{"current": {ID: "current", ProjectID: "project"}, "other": {ID: "other", ProjectID: "project"}}})
	batch, err := s.ImportPathsForConversation(context.Background(), "project", []string{path}, "current")
	if err != nil || len(batch.Errors) > 0 || len(batch.Attachments) != 1 {
		t.Fatal(batch, err)
	}
	id := batch.Attachments[0].ID
	content, err := s.ReadDiscussionMaterial(context.Background(), "project", "current", id)
	if err != nil || !strings.Contains(string(content), "b,9") || !strings.Contains(string(content), `"adopted":false`) {
		t.Fatal(string(content), err)
	}
	_, file, err := s.OpenRevisionData(context.Background(), "project", "current", id)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := s.RevisionMaterials(context.Background(), "project", "current", []string{id}); err == nil {
		t.Fatal("table adopted as evidence")
	}
	if _, _, err := s.OpenRevisionData(context.Background(), "project", "other", id); err == nil {
		t.Fatal("table leaked to other conversation")
	}
	if _, err := s.ReadDiscussionMaterial(context.Background(), "project", "other", id); err == nil {
		t.Fatal("table read from other conversation")
	}
}

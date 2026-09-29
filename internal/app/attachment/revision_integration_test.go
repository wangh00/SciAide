package attachment_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

type revisionConversationFixture struct{ projectID string }

func (f revisionConversationFixture) GetConversation(_ context.Context, id string) (conversation.Conversation, error) {
	return conversation.Conversation{ID: id, ProjectID: f.projectID}, nil
}

func TestRevisionPromotionPreservesOwnersBytesAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	p, err := projects.Create(ctx, "Fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	repo := sqlite.NewAttachmentRepository(store.DB())
	service := attachment.NewService(repo, projects)
	service.SetConversationValidator(revisionConversationFixture{p.ID})
	service.SetTaskValidator(sqlite.NewResearchTaskRepository(store.DB()))
	if _, err := store.DB().Exec(`INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES('chat',?,'Fixture',?,?)`, p.ID, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO research_tasks(id,project_id,title,origin_kind,created_at,updated_at) VALUES('task',?,'Fixture','template',?,?)`, p.ID, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "paper.md")
	os.WriteFile(file, []byte("# Study\nObserved sleep result."), 0600)
	batch, err := service.ImportPathsForConversation(ctx, p.ID, []string{file}, "chat")
	if err != nil || len(batch.Attachments) != 1 {
		t.Fatal(batch, err)
	}
	v := batch.Attachments[0]
	if _, err := service.RevisionMaterials(ctx, p.ID, "foreign", []string{v.ID}); err == nil {
		t.Fatal("foreign conversation access")
	}
	if _, err := service.PromoteRevisionMaterial(ctx, p.ID, "chat", "task", v.ID, "wrong-hash"); err == nil {
		t.Fatal("changed material accepted")
	}
	promoted, err := service.PromoteRevisionMaterial(ctx, p.ID, "chat", "task", v.ID, v.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.PromoteRevisionMaterial(ctx, p.ID, "chat", "task", v.ID, v.SHA256)
	if err != nil || again.ID != promoted.ID {
		t.Fatal("duplicate promotion", err)
	}
	if promoted.ID == v.ID || promoted.ScopeKind != attachment.ScopeTask || promoted.StorageRelativePath != v.StorageRelativePath {
		t.Fatal("ownership mutated or bytes duplicated")
	}
	old, err := repo.Get(ctx, v.ID)
	if err != nil || old.ScopeKind != attachment.ScopeConversation {
		t.Fatal("original discussion lost")
	}
	if _, _, err := service.ParsedForTask(ctx, p.ID, "task", promoted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PromoteRevisionMaterial(ctx, p.ID, "chat", "missing-task", v.ID, v.SHA256); err == nil {
		t.Fatal("foreign task accepted")
	}
	csv := filepath.Join(root, "data.csv")
	os.WriteFile(csv, []byte("x,y\n1,2\n"), 0600)
	data, err := service.ImportPathsForConversation(ctx, p.ID, []string{csv}, "chat")
	if err != nil || len(data.Attachments) != 1 {
		t.Fatal(data, err)
	}
	if _, err := service.RevisionMaterials(ctx, p.ID, "chat", []string{data.Attachments[0].ID}); err == nil {
		t.Fatal("research data replaced through literature path")
	}
}

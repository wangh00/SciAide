package knowledge_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestIndexedReadServicePreservesTaskAndProjectOwnership(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "isolated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	p, err := projects.Create(ctx, "Indexed scope", "")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := projects.Create(ctx, "Other scope", "")
	if err != nil {
		t.Fatal(err)
	}
	tasks := sqlite.NewResearchTaskRepository(store.DB())
	for _, id := range []string{"one", "two"} {
		if err := tasks.Upsert(ctx, researchtask.UpsertCommand{ID: id, ProjectID: p.ID, Title: id, OriginKind: researchtask.OriginTemplate, At: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	attachments := attachment.NewService(sqlite.NewAttachmentRepository(store.DB()), projects)
	attachments.SetTaskValidator(tasks)
	service := knowledge.NewService(sqlite.NewKnowledgeRepository(store.DB()), projects, attachments)
	service.SetTaskValidator(tasks)
	if _, err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ids := map[string]string{}
	for _, scope := range []string{"shared", "one", "two"} {
		path := filepath.Join(root, scope+".txt")
		if err := os.WriteFile(path, []byte("Verified scope evidence belonging to "+scope+"."), 0600); err != nil {
			t.Fatal(err)
		}
		var batch attachment.ImportBatch
		if scope == "shared" {
			batch, err = attachments.ImportPaths(ctx, p.ID, []string{path})
		} else {
			batch, err = attachments.ImportPathsForTask(ctx, p.ID, []string{path}, scope)
		}
		if err != nil || len(batch.Attachments) != 1 || len(batch.Errors) != 0 {
			t.Fatal("import", scope, err)
		}
		ids[scope] = batch.Attachments[0].ID
		if err := service.Enqueue(ctx, batch.Attachments[0]); err != nil {
			t.Fatal(err)
		}
	}
	for _, taskID := range []string{"one", "two"} {
		if _, err := service.SearchWithTask(ctx, p.ID, taskID, knowledge.SearchOptions{Query: "scope evidence", Limit: 20}); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []string{"shared", "one"} {
		chunks, more, err := service.ReadIndexedEvidence(ctx, p.ID, "one", ids[scope], "", 0)
		if err != nil || len(chunks) != 1 || more || !strings.Contains(chunks[0].Content, scope) {
			t.Fatal("authorized read", scope, chunks, more, err)
		}
	}
	for _, target := range []struct{ projectID, taskID, attachmentID string }{
		{p.ID, "one", ids["two"]}, {p.ID, "", ids["one"]}, {p.ID, "missing", ids["shared"]}, {foreign.ID, "one", ids["one"]},
	} {
		if _, _, err := service.ReadIndexedEvidence(ctx, target.projectID, target.taskID, target.attachmentID, "", 0); err == nil {
			t.Fatal("foreign scope accepted", target)
		}
	}
}

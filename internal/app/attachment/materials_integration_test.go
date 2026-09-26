package attachment_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestMaterialLibraryMetadataArchiveReadExportAndExplicitRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "materials.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	first, err := projects.Create(ctx, "materials", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := projects.Create(ctx, "other", "")
	if err != nil {
		t.Fatal(err)
	}
	service := attachment.NewService(sqlite.NewAttachmentRepository(store.DB()), projects)
	tasks := sqlite.NewResearchTaskRepository(store.DB())
	service.SetTaskValidator(tasks)
	for _, id := range []string{"task-a", "task-b"} {
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO research_tasks(id,project_id,title,research_question,origin_kind,status,created_at,updated_at) VALUES (?,?,?,'question','ai_route','active',?,?)`, id, first.ID, id, time.Now().UTC(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "local-notes.md")
	original := "# Notes\n\n" + strings.Repeat("研究 evidence 🧪 ", 1_200)
	if err := os.WriteFile(source, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	imported, err := service.ImportPaths(ctx, first.ID, []string{source})
	if err != nil || len(imported.Errors) != 0 || len(imported.Attachments) != 1 {
		t.Fatalf("import=%#v err=%v", imported, err)
	}
	value := imported.Attachments[0]
	// The material library must be usable before any knowledge-index job exists.
	listed, err := service.ListMaterials(ctx, first.ID, "")
	if err != nil || len(listed) != 1 || listed[0].ID != value.ID || listed[0].IndexStatus != "" || listed[0].Title != value.OriginalName {
		t.Fatalf("unindexed list=%#v err=%v", listed, err)
	}
	updated, err := service.SaveMaterial(ctx, first.ID, "", value.ID, "实验室原始笔记", "由研究者提供；尚待核验。", false)
	if err != nil || updated.SHA256 != value.SHA256 || updated.Title != "实验室原始笔记" || updated.Notes == "" {
		t.Fatalf("save metadata=%#v err=%v", updated, err)
	}
	page, err := service.ReadMaterial(ctx, first.ID, "", value.ID, 0)
	if err != nil || len([]rune(page.Text)) != 12_000 || page.NextOffset != 12_000 || page.TotalRunes <= page.NextOffset {
		t.Fatalf("first page=%#v err=%v", page, err)
	}
	last, err := service.ReadMaterial(ctx, first.ID, "", value.ID, page.NextOffset)
	if err != nil || last.NextOffset != -1 || len(last.Text) == 0 {
		t.Fatalf("final page=%#v err=%v", last, err)
	}
	if len([]rune(page.Text+last.Text)) != page.TotalRunes {
		t.Fatal("unicode pagination lost or duplicated content")
	}
	destination := filepath.Join(root, "exported.md")
	if err := service.ExportMaterial(ctx, first.ID, "", value.ID, destination); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bytes)
	if got := hex.EncodeToString(digest[:]); got != value.SHA256 {
		t.Fatalf("export sha=%s want=%s", got, value.SHA256)
	}
	if err := service.ExportMaterial(ctx, first.ID, "", value.ID, destination); err == nil {
		t.Fatal("export overwrote an existing file")
	}
	if _, err := service.ReadMaterial(ctx, first.ID, "", value.ID, -1); err == nil {
		t.Fatal("negative preview offset was accepted")
	}
	if _, err := service.GetMaterial(ctx, other.ID, "", value.ID); err == nil {
		t.Fatal("cross-project material read was accepted")
	}
	taskSource := filepath.Join(root, "task-a-notes.md")
	if err := os.WriteFile(taskSource, []byte("# Intermittent restriction trial\n\nAbstract only."), 0o600); err != nil {
		t.Fatal(err)
	}
	taskImport, err := service.ImportPathsForTask(ctx, first.ID, []string{taskSource}, "task-a")
	if err != nil || len(taskImport.Attachments) != 1 {
		t.Fatalf("task import=%#v err=%v", taskImport, err)
	}
	if _, err := service.GetMaterial(ctx, first.ID, "task-b", taskImport.Attachments[0].ID); err == nil {
		t.Fatal("other task accessed task-scoped material")
	}
	// Historical metadata remains readable under a paper title without being
	// mistaken for user-uploaded/shared material or for full text.
	if _, err = store.DB().ExecContext(ctx, `UPDATE attachments SET source_kind='research_import',original_name='research-old-metadata.md' WHERE id=?`, taskImport.Attachments[0].ID); err != nil {
		t.Fatal(err)
	}
	// No bibliography record: a safe display fallback reads the generated heading.
	display, err := service.GetMaterial(ctx, first.ID, "task-a", taskImport.Attachments[0].ID)
	if err != nil || display.Title != "Intermittent restriction trial" || display.Reusable || display.ContentKind != "metadata_abstract" {
		t.Fatalf("display=%#v err=%v", display, err)
	}
	if _, err := service.SelectReferenceMaterials(ctx, first.ID, "task-b", []string{taskImport.Attachments[0].ID}); err == nil {
		t.Fatal("other task selected task-scoped material")
	}
	// Collect creates a shared record; direct cross-task selection stays forbidden.
	if _, err := service.CollectMaterial(ctx, first.ID, "task-b", taskImport.Attachments[0].ID); err == nil {
		t.Fatal("cross-task collection accepted")
	}
	collected, err := service.CollectMaterial(ctx, first.ID, "task-a", taskImport.Attachments[0].ID)
	if err != nil || !collected.Reusable || !collected.Collected || collected.ID == taskImport.Attachments[0].ID || collected.OriginTaskTitle != "task-a" || collected.ContentKind != "metadata_abstract" {
		t.Fatalf("collect=%#v err=%v", collected, err)
	}
	again, err := service.CollectMaterial(ctx, first.ID, "task-a", taskImport.Attachments[0].ID)
	if err != nil || again.ID != collected.ID {
		t.Fatalf("collection not idempotent: %v", err)
	}
	if _, err = service.SelectReferenceMaterials(ctx, first.ID, "", []string{collected.ID}); err != nil {
		t.Fatal(err)
	}
	originalTask, _ := service.Get(ctx, taskImport.Attachments[0].ID)
	if originalTask.ScopeKind != attachment.ScopeTask {
		t.Fatal("collection changed original ownership")
	}
	if _, err = service.SaveMaterial(ctx, first.ID, "", collected.ID, "Collected title", "Notes", true); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SelectReferenceMaterials(ctx, first.ID, "", []string{collected.ID}); err == nil {
		t.Fatal("removed collection selectable")
	}
	again, err = service.CollectMaterial(ctx, first.ID, "task-a", taskImport.Attachments[0].ID)
	if err != nil || !again.Reusable || again.Title != "Collected title" {
		t.Fatalf("recollection=%#v err=%v", again, err)
	}
	// Removing the original task must not revoke the independently collected row.
	userSource := filepath.Join(root, "user-method.md")
	if err := os.WriteFile(userSource, []byte("# User method\nDo not clear"), 0600); err != nil {
		t.Fatal(err)
	}
	userBatch, err := service.ImportPathsForTask(ctx, first.ID, []string{userSource}, "task-a")
	if err != nil || len(userBatch.Attachments) != 1 {
		t.Fatalf("user input: %#v %v", userBatch, err)
	}
	if _, err := service.ClearTaskMetadata(ctx, first.ID, "task-a", []string{userBatch.Attachments[0].ID}); err == nil {
		t.Fatal("clear accepted user Markdown")
	}
	if _, err := service.ClearTaskMetadata(ctx, first.ID, "task-a", []string{collected.ID}); err == nil {
		t.Fatal("clear accepted shared collection")
	}
	if _, err := service.ClearTaskMetadata(ctx, first.ID, "task-b", []string{taskImport.Attachments[0].ID}); err == nil {
		t.Fatal("clear accepted other task")
	}
	if _, err := service.ClearTaskMetadata(ctx, first.ID, "task-a", []string{taskImport.Attachments[0].ID, value.ID}); err == nil {
		t.Fatal("clear accepted mixed invalid scope")
	}
	before, _ := service.GetMaterial(ctx, first.ID, "task-a", taskImport.Attachments[0].ID)
	if before.Archived {
		t.Fatal("invalid clear partially applied")
	}
	cleared, err := service.ClearTaskMetadata(ctx, first.ID, "task-a", []string{taskImport.Attachments[0].ID})
	if err != nil || len(cleared.RemovedIDs) != 1 || len(cleared.Errors) != 0 {
		t.Fatalf("clear=%#v err=%v", cleared, err)
	}
	if _, err := service.ReferenceMaterials(ctx, first.ID, "task-a", []string{taskImport.Attachments[0].ID}); err != nil {
		t.Fatalf("clear broke historical reference: %v", err)
	}
	if _, err := service.SelectReferenceMaterials(ctx, first.ID, "", []string{collected.ID}); err != nil {
		t.Fatalf("clear removed shared collection: %v", err)
	}
	if _, err = store.DB().ExecContext(ctx, `DELETE FROM research_tasks WHERE id='task-a'`); err != nil {
		t.Fatal(err)
	}
	if kept, err := service.GetMaterial(ctx, first.ID, "", collected.ID); err != nil || !kept.Reusable || kept.ContentKind != "metadata_abstract" {
		t.Fatalf("collection lost with task: %#v %v", kept, err)
	}
	if _, err := service.SaveMaterial(ctx, first.ID, "", value.ID, "实验室原始笔记", "归档", true); err != nil {
		t.Fatal(err)
	}
	if listed, err = service.ListMaterials(ctx, first.ID, ""); err != nil {
		t.Fatalf("list archived material: %v", err)
	} else {
		for _, material := range listed {
			if material.ID == value.ID {
				t.Fatalf("archived material remains in new-material list=%#v", listed)
			}
		}
	}
	if _, err := service.SelectReferenceMaterials(ctx, first.ID, "", []string{value.ID}); err == nil {
		t.Fatal("archived material was accepted for a new selection")
	}
	// A pre-existing frozen run continues to resolve its exact attachment.
	if historical, err := service.ReferenceMaterials(ctx, first.ID, "", []string{value.ID}); err != nil || len(historical) != 1 || historical[0].SHA256 != value.SHA256 {
		t.Fatalf("historical reference=%#v err=%v", historical, err)
	}
	if restored, err := service.RestoreMaterial(ctx, first.ID, "", value.ID); err != nil || restored.Archived || restored.Title != "实验室原始笔记" || restored.Notes != "归档" {
		t.Fatalf("explicit restore=%#v err=%v", restored, err)
	}
	if selected, err := service.SelectReferenceMaterials(ctx, first.ID, "", []string{value.ID}); err != nil || len(selected) != 1 {
		t.Fatalf("restored material cannot be selected=%#v err=%v", selected, err)
	}
}

package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
)

func TestKnowledgeRepositoryRecoversJobsAndRejectsCrossProjectDocuments(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := NewProjectRepository(store.DB())
	now := time.Now().UTC()
	for _, value := range []project.Project{
		{ID: "project-a", Name: "A", WorkspacePath: "C:/a", WorkspaceKind: project.WorkspaceExternal, CreatedAt: now, UpdatedAt: now},
		{ID: "project-b", Name: "B", WorkspacePath: "C:/b", WorkspaceKind: project.WorkspaceExternal, CreatedAt: now, UpdatedAt: now},
	} {
		if err := projects.Create(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	attachments := NewAttachmentRepository(store.DB())
	values := []attachment.Attachment{
		{ID: "attachment-a", ProjectID: "project-a", OriginalName: "a.pdf", MIMEType: document.MIMEType(document.FormatPDF), Format: document.FormatPDF, SizeBytes: 10, SHA256: strings.Repeat("a", 64), StorageRelativePath: "attachments/objects/a/a.pdf", CacheRelativePath: "cache/documents/a.json", Status: attachment.StatusReady, CreatedAt: now, UpdatedAt: now},
		{ID: "attachment-b", ProjectID: "project-b", OriginalName: "b.pdf", MIMEType: document.MIMEType(document.FormatPDF), Format: document.FormatPDF, SizeBytes: 10, SHA256: strings.Repeat("b", 64), StorageRelativePath: "attachments/objects/b/b.pdf", CacheRelativePath: "cache/documents/b.json", Status: attachment.StatusReady, CreatedAt: now, UpdatedAt: now},
	}
	for _, value := range values {
		if err := attachments.Create(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewKnowledgeRepository(store.DB())
	legacyID := "legacy-index"
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO knowledge_index_versions(id,project_id,version_number,schema_version,parser_schema_version,chunking_version,search_kind,storage_relative_path,status,error_message,created_at,activated_at,updated_at) VALUES (?, 'project-a',1,1,1,'unit-v1','lexical_v1','cache/knowledge/index-v1.db','ready','',?,?,?)`, legacyID, formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	version, err := repository.EnsureVersion(ctx, "project-a", knowledge.DefaultIndexSpec(), now)
	if err != nil {
		t.Fatal(err)
	}
	if version.VersionNumber != 2 || version.Status != knowledge.IndexBuilding || version.RetrievalEngine != knowledge.RetrievalEngine {
		t.Fatalf("P5.2 target version = %#v", version)
	}
	if ready, found, err := repository.ReadyVersion(ctx, "project-a"); err != nil || !found || ready.ID != legacyID {
		t.Fatalf("ready version during shadow build = %#v, %v, %v", ready, found, err)
	}
	if _, queued, err := repository.Enqueue(ctx, values[0], version, false, now); err != nil || !queued {
		t.Fatalf("Enqueue() = %v, %v", queued, err)
	}
	work, found, err := repository.ClaimNext(ctx, "project-a", now.Add(time.Second))
	if err != nil || !found || work.Job.Status != knowledge.JobRunning {
		t.Fatalf("ClaimNext() = %#v, %v, %v", work, found, err)
	}
	if can, err := repository.CanActivate(ctx, "project-a", version.ID, 1); err != nil || can {
		t.Fatalf("incomplete index activation = %v, %v", can, err)
	}
	if err := repository.UpdateStage(ctx, work, "chunking", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateStage(ctx, work, "indexing", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Complete(ctx, work, 1, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if can, err := repository.CanActivate(ctx, "project-a", version.ID, 1); err != nil || !can {
		t.Fatalf("completed index activation = %v, %v", can, err)
	}
	if err := repository.MarkVersionReady(ctx, version.ID, version.ProjectID, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if ready, found, err := repository.ReadyVersion(ctx, "project-a"); err != nil || !found || ready.ID != version.ID {
		t.Fatalf("activated version = %#v, %v, %v", ready, found, err)
	}
	var legacyStatus string
	if err := store.DB().QueryRowContext(ctx, `SELECT status FROM knowledge_index_versions WHERE id=?`, legacyID).Scan(&legacyStatus); err != nil || legacyStatus != "retired" {
		t.Fatalf("legacy index status = %q, %v", legacyStatus, err)
	}
	if _, queued, err := repository.Enqueue(ctx, values[0], version, true, now.Add(6*time.Second)); err != nil || !queued {
		t.Fatalf("forced Enqueue() = %v, %v", queued, err)
	}
	work, found, err = repository.ClaimNext(ctx, "project-a", now.Add(7*time.Second))
	if err != nil || !found {
		t.Fatalf("second ClaimNext() = %#v, %v, %v", work, found, err)
	}
	if recovered, err := repository.Recover(ctx, now.Add(8*time.Second)); err != nil || recovered != 1 {
		t.Fatalf("Recover() = %d, %v", recovered, err)
	}
	status, err := repository.ProjectStatus(ctx, "project-a")
	if err != nil || status.Pending != 1 || status.QueuedJobs != 1 || status.RunningJobs != 0 {
		t.Fatalf("ProjectStatus() = %#v, %v", status, err)
	}
	if _, _, err := repository.Enqueue(ctx, values[1], version, false, now); err == nil {
		t.Fatal("cross-project attachment was accepted by knowledge repository")
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO knowledge_documents(id,project_id,attachment_id,index_version_id,title,attachment_sha256,status,parser_schema_version,chunking_version,created_at,updated_at) VALUES ('invalid','project-a','attachment-b',?,'invalid',?,'pending',1,'unit-v1',?,?)`, version.ID, values[1].SHA256, formatTime(now), formatTime(now)); err == nil {
		t.Fatal("knowledge document foreign keys accepted a cross-project attachment")
	}
}

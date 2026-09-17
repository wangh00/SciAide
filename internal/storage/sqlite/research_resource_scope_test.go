package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/project"
)

// A task may be archived before its knowledge document is removed from the
// project resource manager. Deleting that document must still clear nullable
// bibliography/evidence references; the cleanup is not a new task write.
func TestArchivedTaskKnowledgeDocumentCanBeRemovedWithResearchReferences(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "resource-cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	selected, err := projects.Create(ctx, "Resource cleanup", "")
	if err != nil {
		t.Fatal(err)
	}
	const at = "2026-09-04T00:00:00.000Z"
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO research_tasks(id,project_id,title,research_question,origin_kind,status,created_at,updated_at,archived_at) VALUES ('archived-task',?,'Archived task','question','ai_route','active',?,?,NULL)`, selected.ID, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO attachments(id,project_id,original_name,mime_type,document_format,size_bytes,sha256,storage_relative_path,cache_relative_path,status,created_at,updated_at,scope_kind,research_task_id) VALUES ('paper-attachment',?,'paper.md','text/markdown','markdown',1,?,'attachments/paper.md','cache/paper.json','ready',?,?, 'task','archived-task')`, selected.ID, strings.Repeat("a", 64), at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO knowledge_index_versions(id,project_id,version_number,schema_version,parser_schema_version,chunking_version,search_kind,storage_relative_path,status,error_message,created_at,updated_at) VALUES ('index-version',?,1,1,1,'unit-v1','lexical_v1','cache/index.db','ready','',?,?)`, selected.ID, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO knowledge_documents(id,project_id,attachment_id,index_version_id,title,attachment_sha256,status,parser_schema_version,chunking_version,created_at,updated_at,scope_kind,research_task_id) VALUES ('research-doc',?,'paper-attachment','index-version','paper',?,'ready',1,'unit-v1',?,?, 'task','archived-task')`, selected.ID, strings.Repeat("a", 64), at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO research_candidates(id,project_id,candidate_key,created_at,updated_at) VALUES ('candidate',?,?,?,?)`, selected.ID, strings.Repeat("b", 64), at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO research_bibliographies(id,project_id,candidate_id,canonical_json,selected_sources_json,created_at,updated_at) VALUES ('bibliography',?,'candidate','{}','{}',?,?)`, selected.ID, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO research_bibliography_materials(id,bibliography_id,project_id,attachment_id,knowledge_document_id,attachment_id_snapshot,knowledge_document_id_snapshot,attachment_sha256_snapshot,import_kind,evidence_level,research_task_id,created_at,updated_at) VALUES ('material','bibliography',?,'paper-attachment','research-doc','paper-attachment','research-doc',?,'metadata_abstract','metadata_abstract','archived-task',?,?)`, selected.ID, strings.Repeat("a", 64), at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO research_evidence_entries(id,project_id,bibliography_id,field_kind,content,provenance,review_status,evidence_level,knowledge_document_id,knowledge_document_id_snapshot,research_task_id,created_at,updated_at) VALUES ('evidence',?,'bibliography','method','method','user','verified','metadata_abstract','research-doc','research-doc','archived-task',?,?)`, selected.ID, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE research_tasks SET status='archived',archived_at=? WHERE id='archived-task'`, at); err != nil {
		t.Fatal(err)
	}
	// A user-authored update must not use the cascade exception to mutate an
	// archived task's evidence relationship.
	if _, err := store.DB().ExecContext(ctx, `UPDATE research_bibliography_materials SET knowledge_document_id=NULL WHERE id='material'`); err == nil {
		t.Fatal("manual bibliography reference clearing bypassed archived-task guard")
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE research_evidence_entries SET knowledge_document_id=NULL WHERE id='evidence'`); err == nil {
		t.Fatal("manual evidence reference clearing bypassed archived-task guard")
	}

	if _, err := store.DB().ExecContext(ctx, `DELETE FROM knowledge_documents WHERE project_id=? AND id='research-doc'`, selected.ID); err != nil {
		t.Fatalf("delete archived-task knowledge document: %v", err)
	}
	var materialDocument, evidenceDocument string
	if err := store.DB().QueryRowContext(ctx, `SELECT COALESCE(knowledge_document_id,''),COALESCE((SELECT knowledge_document_id FROM research_evidence_entries WHERE id='evidence'),'') FROM research_bibliography_materials WHERE id='material'`).Scan(&materialDocument, &evidenceDocument); err != nil {
		t.Fatal(err)
	}
	if materialDocument != "" || evidenceDocument != "" {
		t.Fatalf("document references after cascade cleanup = %q/%q, want empty", materialDocument, evidenceDocument)
	}
}

func TestArchivedTaskArtifactStatusCanBeManagedWithoutChangingScope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "artifact-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	selected, err := projects.Create(ctx, "Artifact status", "")
	if err != nil {
		t.Fatal(err)
	}
	const at = "2026-09-04T00:00:00.000Z"
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO research_tasks(id,project_id,title,research_question,origin_kind,status,created_at,updated_at,archived_at) VALUES ('archived-artifact-task',?,'Archived task','question','ai_route','active',?,?,NULL)`, selected.ID, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO artifacts(id,project_id,scope_kind,research_task_id,name,kind,status,current_version_id,created_at,updated_at,trashed_at) VALUES ('archived-artifact',?,'task','archived-artifact-task','analysis.json','document','active',NULL,?,?,NULL)`, selected.ID, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE research_tasks SET status='archived',archived_at=? WHERE id='archived-artifact-task'`, at); err != nil {
		t.Fatal(err)
	}

	repository := NewArtifactRepository(store.DB())
	updated, err := repository.SetStatus(ctx, selected.ID, "archived-artifact", artifact.StatusTrashed, time.Date(2026, 9, 4, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("archive task artifact status update: %v", err)
	}
	if updated.Status != artifact.StatusTrashed || updated.ResearchTaskID != "archived-artifact-task" {
		t.Fatalf("updated artifact = %#v", updated)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE artifacts SET research_task_id='another-task' WHERE id='archived-artifact'`); err == nil {
		t.Fatal("artifact scope reassignment bypassed immutable scope guard")
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE artifacts SET current_version_id='new-version' WHERE id='archived-artifact'`); err == nil {
		t.Fatal("archived task artifact accepted a new version")
	}
}

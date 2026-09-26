package artifact_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-pdf/fpdf"
	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestPublishWorkflowReportFreezesSourcesExportsAndReplaysIdempotently(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "workflow-report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	selected, err := projects.Create(ctx, "Workflow report", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	definition := `{"schemaVersion":1,"name":"Fixture","description":"","inputs":[],"nodes":[{"id":"publish","name":"Publish","kind":"tool","toolName":"builtin.research.workflow.report","arguments":{}}],"edges":[],"outputs":[]}`
	compilation := `{"schemaVersion":1,"compilerVersion":"fixture","definitionSha256":"` + strings.Repeat("a", 64) + `","compilationSha256":"` + strings.Repeat("b", 64) + `","order":["publish"],"nodes":[],"edges":[],"inputs":[],"outputs":[],"diagnostics":[]}`
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflows(id,project_id,name,description,current_version_id,version,created_at,updated_at) VALUES ('workflow',?,'Fixture','',NULL,1,?,?)`, []any{selected.ID, now, now}},
		{`INSERT INTO workflow_versions(id,workflow_id,version_number,definition_json,definition_sha256,compilation_json,compilation_sha256,created_at) VALUES ('workflow-version','workflow',1,?,?,?,?,?)`, []any{definition, strings.Repeat("a", 64), compilation, strings.Repeat("b", 64), now}},
		{`UPDATE workflows SET current_version_id='workflow-version' WHERE id='workflow'`, nil},
		{`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,current_step_ordinal,error_code,error_message,cancel_requested,resume_status,created_at,started_at,completed_at,updated_at) VALUES ('workflow-run',?,'workflow','workflow-version','completed','{}',? ,?,?,'{}',1,'','',0,'',?,?,?,?)`, []any{selected.ID, strings.Repeat("c", 64), compilation, strings.Repeat("b", 64), now, now, now, now}},
	} {
		if _, err := store.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, callID := range []string{"upstream-call", "report-call"} {
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO tool_calls(id,run_id,workflow_run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,idempotency_key,error_code,error_message,created_at,started_at,completed_at,updated_at) VALUES (?,NULL,'workflow-run',?,'builtin.fixture','1','{}','completed','low','[]',1,?,'','',?,?,?,?)`, callID, callID, callID, now, now, now, now); err != nil {
			t.Fatal(err)
		}
	}

	bibliography, err := json.Marshal(map[string]any{"schemaVersion": 1, "bibliographyId": "bibliography", "revision": 2, "data": map[string]any{"title": "Verified source"}, "capturedAt": now})
	if err != nil {
		t.Fatal(err)
	}
	quote := "The verified result is reproducible."
	citation := artifact.Citation{
		Reference: "[K-0123456789AB]", SourceRunIDSnapshot: "workflow-run", SourceToolCallIDSnapshot: "upstream-call",
		IndexVersionID: "index", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk",
		SourceName: "paper.md", MIMEType: "text/markdown", Locator: "section:results", Title: "Verified source",
		Quote: quote, QuoteSHA256: artifactQuoteSHA256(quote), SourceStart: 10, SourceEnd: 46,
		BibliographyIDSnapshot: "bibliography", BibliographySnapshot: bibliography, EvidenceLevel: "full_text",
	}
	service := artifact.NewService(sqlite.NewArtifactRepository(store.DB()), projects)
	command := artifact.WorkflowReportCommand{
		ProjectID: selected.ID, WorkflowRunID: "workflow-run", ToolCallID: "report-call",
		ToolName: "builtin.research.workflow.report", ToolVersion: "1", Name: "可复现科研报告",
		Markdown:  "# 结论\n\nThe verified result is reproducible. [K-0123456789AB]",
		Citations: []artifact.Citation{citation}, SourceToolCallIDs: []string{"upstream-call", "upstream-call"},
		SourceWorkspaceFiles: []string{"outputs/analysis.csv", "outputs/analysis.svg", "outputs/analysis.csv"},
	}
	first, err := service.PublishWorkflowReport(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Version.Provenance.WorkflowRunID != "workflow-run" || first.Version.Provenance.ToolCallID != "report-call" || len(first.Version.Citations) != 1 || len(first.Version.Lineage) != 5 {
		t.Fatalf("workflow report = %#v", first)
	}
	if first.DOCX.Format != artifact.ExportDOCX || first.PDF.Format != artifact.ExportPDF || first.DOCX.SourceSHA256 != first.Version.SHA256 || first.PDF.SourceSHA256 != first.Version.SHA256 {
		t.Fatalf("workflow report exports = docx:%#v pdf:%#v", first.DOCX, first.PDF)
	}
	for _, value := range []artifact.Export{first.DOCX, first.PDF} {
		destination := filepath.Join(root, value.FileName)
		if err := service.DownloadExport(ctx, selected.ID, value.ID, destination); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(destination); err != nil || info.Size() != value.SizeBytes {
			t.Fatalf("downloaded export %s = %#v, %v", value.Format, info, err)
		}
	}
	replayed, err := service.PublishWorkflowReport(ctx, command)
	if err != nil || replayed.Created || replayed.Artifact.ID != first.Artifact.ID || replayed.Version.ID != first.Version.ID || replayed.DOCX.ID != first.DOCX.ID || replayed.PDF.ID != first.PDF.ID {
		t.Fatalf("idempotent workflow report = %#v, %v", replayed, err)
	}
}

func TestPublishWorkflowReportPersistsUserMaterialCitationWithoutBibliographyID(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "user-material-report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	selected, err := projects.Create(ctx, "User material report", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	definition := `{"schemaVersion":1,"name":"Fixture","description":"","inputs":[],"nodes":[{"id":"publish","name":"Publish","kind":"tool","toolName":"builtin.research.workflow.report","arguments":{}}],"edges":[],"outputs":[]}`
	compilation := `{"schemaVersion":1,"compilerVersion":"fixture","definitionSha256":"` + strings.Repeat("a", 64) + `","compilationSha256":"` + strings.Repeat("b", 64) + `","order":["publish"],"nodes":[],"edges":[],"inputs":[],"outputs":[],"diagnostics":[]}`
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflows(id,project_id,name,description,current_version_id,version,created_at,updated_at) VALUES ('workflow',?,'Fixture','',NULL,1,?,?)`, []any{selected.ID, now, now}},
		{`INSERT INTO workflow_versions(id,workflow_id,version_number,definition_json,definition_sha256,compilation_json,compilation_sha256,created_at) VALUES ('workflow-version','workflow',1,?,?,?,?,?)`, []any{definition, strings.Repeat("a", 64), compilation, strings.Repeat("b", 64), now}},
		{`UPDATE workflows SET current_version_id='workflow-version' WHERE id='workflow'`, nil},
		{`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,current_step_ordinal,error_code,error_message,cancel_requested,resume_status,created_at,started_at,completed_at,updated_at) VALUES ('workflow-run',?,'workflow','workflow-version','completed','{}',? ,?,?,'{}',1,'','',0,'',?,?,?,?)`, []any{selected.ID, strings.Repeat("c", 64), compilation, strings.Repeat("b", 64), now, now, now, now}},
	} {
		if _, err := store.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, callID := range []string{"upstream-call", "report-call"} {
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO tool_calls(id,run_id,workflow_run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,idempotency_key,error_code,error_message,created_at,started_at,completed_at,updated_at) VALUES (?,NULL,'workflow-run',?,'builtin.fixture','1','{}','completed','low','[]',1,?,'','',?,?,?,?)`, callID, callID, callID, now, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	quote := "The supplied local notes have limited evidence."
	bibliography := json.RawMessage(`{"schemaVersion":1,"materialOrigin":"user_selected","attachmentId":"attachment","attachmentSha256":"` + strings.Repeat("d", 64) + `","data":{"title":"local-notes.pdf","workType":"user_material"}}`)
	citation := artifact.Citation{
		Reference: "[K-0123456789AB]", SourceRunIDSnapshot: "workflow-run", SourceToolCallIDSnapshot: "upstream-call",
		IndexVersionID: "index", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "local-notes.pdf", MIMEType: "application/pdf", Locator: "page:1", Title: "",
		Quote: quote, QuoteSHA256: artifactQuoteSHA256(quote), SourceStart: 0, SourceEnd: len(quote),
		BibliographySnapshot: bibliography,
	}
	service := artifact.NewService(sqlite.NewArtifactRepository(store.DB()), projects)
	result, err := service.PublishWorkflowReport(ctx, artifact.WorkflowReportCommand{
		ProjectID: selected.ID, WorkflowRunID: "workflow-run", ToolCallID: "report-call", ToolName: "builtin.research.workflow.report", ToolVersion: "1", Name: "本地资料报告",
		Markdown: "# 结论\n\n" + quote + " [K-0123456789AB]", Citations: []artifact.Citation{citation}, SourceToolCallIDs: []string{"upstream-call"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Version.Citations) != 1 || result.Version.Citations[0].BibliographyIDSnapshot != "" || result.Version.Citations[0].EvidenceLevel != "" || string(result.Version.Citations[0].BibliographySnapshot) != string(bibliography) {
		t.Fatalf("user material citation was not persistently frozen: %#v", result.Version.Citations)
	}
	for _, value := range []artifact.Export{result.DOCX, result.PDF} {
		if err := service.DownloadExport(ctx, selected.ID, value.ID, filepath.Join(root, value.FileName)); err != nil {
			t.Fatalf("export with user material citation %s: %v", value.Format, err)
		}
	}
}

func artifactQuoteSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func TestCreateExportIsImmutableDownloadableAndRenameStable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "artifact-export.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	selected, err := projects.Create(ctx, "Export project", "")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(selected.WorkspacePath, "research-summary.md")
	contents := []byte("# 结论\n\n可信结果。\n\n| 指标 | 数值 |\n| --- | --- |\n| 准确率 | 98% |\n")
	if err := os.WriteFile(source, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	service := artifact.NewService(sqlite.NewArtifactRepository(store.DB()), projects)
	saved, err := service.RegisterWorkspaceFile(ctx, artifact.RegisterWorkspaceCommand{ProjectID: selected.ID, Path: source, Name: "可重命名产物"})
	if err != nil {
		t.Fatal(err)
	}
	command := artifact.ExportCommand{ProjectID: selected.ID, VersionID: saved.Version.ID, Format: artifact.ExportDOCX, CitationStyle: artifact.CitationGB7714}
	firstExport, err := service.CreateExport(ctx, command)
	if err != nil || !firstExport.Created {
		t.Fatalf("CreateExport() = %#v, %v", firstExport, err)
	}
	if firstExport.Export.SourceSHA256 != saved.Version.SHA256 || firstExport.Export.ArtifactVersionID != saved.Version.ID {
		t.Fatalf("export provenance = %#v", firstExport.Export)
	}
	if _, err := service.Rename(ctx, selected.ID, saved.Artifact.ID, "已重命名产物"); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CreateExport(ctx, command)
	if err != nil || replayed.Created || replayed.Export.ID != firstExport.Export.ID || replayed.Export.SHA256 != firstExport.Export.SHA256 {
		t.Fatalf("rename-stable CreateExport() = %#v, %v", replayed, err)
	}
	detail, err := service.Get(ctx, selected.ID, saved.Artifact.ID)
	if err != nil || detail.Artifact.CurrentVersionID != saved.Version.ID || len(detail.Versions) != 1 || len(detail.Versions[0].Exports) != 1 {
		t.Fatalf("detail after export = %#v, %v", detail, err)
	}
	destination := filepath.Join(root, "download.docx")
	if err := service.DownloadExport(ctx, selected.ID, firstExport.Export.ID, destination); err != nil {
		t.Fatal(err)
	}
	downloaded, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(downloaded)
	if hex.EncodeToString(digest[:]) != firstExport.Export.SHA256 || int64(len(downloaded)) != firstExport.Export.SizeBytes {
		t.Fatal("downloaded export does not match its immutable record")
	}
	if err := service.DownloadExport(ctx, selected.ID, firstExport.Export.ID, destination); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing export destination error = %v", err)
	}
}

func TestCreateExportRejectsArtifactWithoutReadableBody(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "artifact-empty-export.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	selected, err := projects.Create(ctx, "Empty export project", "")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(selected.WorkspacePath, "scan-without-text.pdf")
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	if err := pdf.OutputFileAndClose(source); err != nil {
		t.Fatal(err)
	}
	service := artifact.NewService(sqlite.NewArtifactRepository(store.DB()), projects)
	saved, err := service.RegisterWorkspaceFile(ctx, artifact.RegisterWorkspaceCommand{ProjectID: selected.ID, Path: source})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateExport(ctx, artifact.ExportCommand{
		ProjectID: selected.ID, VersionID: saved.Version.ID, Format: artifact.ExportPDF, CitationStyle: artifact.CitationAPA7,
	})
	if err == nil || !strings.Contains(err.Error(), "no readable content") {
		t.Fatalf("CreateExport() error = %v", err)
	}
	detail, err := service.Get(ctx, selected.ID, saved.Artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Versions) != 1 || len(detail.Versions[0].Exports) != 0 {
		t.Fatalf("failed export persisted state: %#v", detail.Versions)
	}
}

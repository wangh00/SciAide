package artifact

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	appcitation "github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type projectFixture struct{ value project.Project }

func (p projectFixture) Get(context.Context, string) (project.Project, error) { return p.value, nil }
func (p projectFixture) List(context.Context) ([]project.Project, error) {
	return []project.Project{p.value}, nil
}

type readerCaptureRepository struct {
	record     CreateVersionRecord
	createCall int
	detail     Detail
	toolSource ToolSource
	version    Version
	blob       BlobRecord
	versionErr error
}

func (r *readerCaptureRepository) CreateVersion(_ context.Context, record CreateVersionRecord) (SaveResult, error) {
	r.record = record
	r.createCall++
	value := Artifact{ID: record.ArtifactID, ProjectID: record.ProjectID, Name: record.Name, Kind: record.Kind, Status: StatusActive, CurrentVersionID: record.Version.ID, CurrentVersion: &record.Version, CreatedAt: record.Version.CreatedAt, UpdatedAt: record.Version.CreatedAt}
	return SaveResult{Artifact: value, Version: record.Version, Created: true}, nil
}
func (*readerCaptureRepository) List(context.Context, string, bool) ([]Artifact, error) {
	return nil, nil
}
func (r *readerCaptureRepository) Get(context.Context, string, string) (Detail, error) {
	return r.detail, nil
}
func (r *readerCaptureRepository) GetVersion(context.Context, string, string) (Version, BlobRecord, error) {
	return r.version, r.blob, r.versionErr
}
func (*readerCaptureRepository) CreateExport(context.Context, CreateExportRecord) (ExportResult, error) {
	return ExportResult{}, nil
}
func (*readerCaptureRepository) GetExport(context.Context, string, string) (Export, BlobRecord, error) {
	return Export{}, BlobRecord{}, nil
}
func (*readerCaptureRepository) Rename(context.Context, string, string, string, time.Time) (Artifact, error) {
	return Artifact{}, nil
}
func (*readerCaptureRepository) SetStatus(context.Context, string, string, Status, time.Time) (Artifact, error) {
	return Artifact{}, nil
}
func (*readerCaptureRepository) AssistantSource(context.Context, string, string) (AssistantSource, error) {
	return AssistantSource{}, nil
}
func (r *readerCaptureRepository) ToolSource(context.Context, string) (ToolSource, error) {
	return r.toolSource, nil
}
func (*readerCaptureRepository) RecoverableToolCallIDs(context.Context) ([]string, error) {
	return nil, nil
}
func (*readerCaptureRepository) BlobPaths(context.Context, string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func TestRegisterWorkspaceFileConfinesSourceAndPublishesContentAddress(t *testing.T) {
	workspace, selected := newArtifactWorkspace(t)
	source := filepath.Join(workspace, "result.csv")
	if err := os.WriteFile(source, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &readerCaptureRepository{}
	service := NewService(repository, projectFixture{value: selected})
	result, err := service.RegisterWorkspaceFile(context.Background(), RegisterWorkspaceCommand{ProjectID: selected.ID, Path: source})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version.SourceKind != SourceWorkspaceFile || result.Artifact.Kind != KindData || len(result.Version.SHA256) != 64 {
		t.Fatalf("result = %#v", result)
	}
	object := filepath.Join(workspace, ".sciaide", filepath.FromSlash(repository.record.Blob.StorageRelativePath))
	file, err := os.Open(object)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := io.ReadAll(file)
	_ = file.Close()
	if string(contents) != "x,y\n1,2\n" {
		t.Fatalf("object = %q", contents)
	}
	if _, err := service.RegisterWorkspaceFile(context.Background(), RegisterWorkspaceCommand{ProjectID: selected.ID, Path: filepath.Join(workspace, ".sciaide", "project.json")}); err == nil {
		t.Fatal("private project data was accepted")
	}
}

func TestInspectSourceUsesContentBeforeExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "figure.jpg")
	contents := []byte("RIFF\x10\x00\x00\x00WEBPVP8 \x00\x00\x00\x00")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mimeType, kind, err := inspectSource(file, filepath.Ext(path))
	_ = file.Close()
	if err != nil || mimeType != "image/webp" || kind != KindImage {
		t.Fatalf("inspectSource() = %q, %q, %v", mimeType, kind, err)
	}
}

func TestInspectSourceRecognizesOfficeContainer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.bin")
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(output)
	for name, contents := range map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types/>`,
		"word/document.xml":   `<?xml version="1.0"?><document/>`,
	} {
		entry, createErr := archive.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := io.WriteString(entry, contents); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mimeType, kind, err := inspectSource(input, filepath.Ext(path))
	_ = input.Close()
	if err != nil || mimeType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || kind != KindDocument {
		t.Fatalf("inspectSource(office) = %q, %q, %v", mimeType, kind, err)
	}
}

func TestRegisterWorkspaceFileRejectsExistingObjectWithWrongHash(t *testing.T) {
	workspace, selected := newArtifactWorkspace(t)
	source := filepath.Join(workspace, "result.csv")
	if err := os.WriteFile(source, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &readerCaptureRepository{}
	service := NewService(repository, projectFixture{value: selected})
	if _, err := service.RegisterWorkspaceFile(context.Background(), RegisterWorkspaceCommand{ProjectID: selected.ID, Path: source}); err != nil {
		t.Fatal(err)
	}
	object := filepath.Join(workspace, ".sciaide", filepath.FromSlash(repository.record.Blob.StorageRelativePath))
	if err := os.WriteFile(object, []byte("a,b\n9,8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterWorkspaceFile(context.Background(), RegisterWorkspaceCommand{ProjectID: selected.ID, Path: source}); err == nil || !strings.Contains(err.Error(), "content address") {
		t.Fatalf("tampered object error = %v", err)
	}
}

func TestRegisterWorkspaceFilePropagatesRelationIDFailure(t *testing.T) {
	workspace, selected := newArtifactWorkspace(t)
	source := filepath.Join(workspace, "result.csv")
	if err := os.WriteFile(source, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &readerCaptureRepository{}
	service := NewService(repository, projectFixture{value: selected})
	wantErr := errors.New("fixture random source failed")
	call := 0
	service.newID = func() (string, error) {
		call++
		if call == 5 {
			return "", wantErr
		}
		return "id-" + strings.Repeat("x", call), nil
	}
	if _, err := service.RegisterWorkspaceFile(context.Background(), RegisterWorkspaceCommand{ProjectID: selected.ID, Path: source}); !errors.Is(err, wantErr) {
		t.Fatalf("ID error = %v", err)
	}
	if repository.createCall != 0 {
		t.Fatal("repository was called after relation ID generation failed")
	}
}

func TestRegisterWorkspaceVersionCapturesPreviousVersion(t *testing.T) {
	workspace, selected := newArtifactWorkspace(t)
	source := filepath.Join(workspace, "result.csv")
	if err := os.WriteFile(source, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &readerCaptureRepository{detail: Detail{Artifact: Artifact{ID: "artifact", ProjectID: selected.ID, Kind: KindData, Status: StatusActive, CurrentVersionID: "version-1"}}}
	service := NewService(repository, projectFixture{value: selected})
	if _, err := service.RegisterWorkspaceFile(context.Background(), RegisterWorkspaceCommand{ProjectID: selected.ID, Path: source, ArtifactID: "artifact"}); err != nil {
		t.Fatal(err)
	}
	last := repository.record.Version.Lineage[len(repository.record.Version.Lineage)-1]
	if last.RelationKind != "artifact_version" || last.SourceIDSnapshot != "version-1" || last.SourceArtifactVersionID != "version-1" {
		t.Fatalf("previous-version lineage = %#v", last)
	}
}

func TestRegisterToolArtifactsContinuesAfterInvalidDeclaration(t *testing.T) {
	workspace, selected := newArtifactWorkspace(t)
	if err := os.WriteFile(filepath.Join(workspace, "valid.csv"), []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &readerCaptureRepository{toolSource: ToolSource{
		ProjectID:   selected.ID,
		RunID:       "run",
		CallID:      "call",
		ToolName:    "fixture.tool",
		Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}},
		Artifacts:   []tool.ArtifactRef{{WorkspacePath: "missing.csv"}, {WorkspacePath: "valid.csv"}},
	}}
	service := NewService(repository, projectFixture{value: selected})
	results, err := service.RegisterToolArtifacts(context.Background(), "call")
	if err == nil || len(results) != 1 || repository.createCall != 1 || repository.record.Version.SourceKey != "tool:call:1" {
		t.Fatalf("RegisterToolArtifacts() = %#v, %v, calls=%d", results, err, repository.createCall)
	}
}

func TestRegisterToolArtifactsUsesFrozenBytesAfterWorkspaceFileChanges(t *testing.T) {
	workspace, selected := newArtifactWorkspace(t)
	path := filepath.Join(workspace, "result.csv")
	if err := os.WriteFile(path, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := ToolSource{
		ProjectID: selected.ID, RunID: "run", CallID: "call", ToolName: "fixture.tool",
		Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}},
		Artifacts:   []tool.ArtifactRef{{WorkspacePath: "result.csv"}},
	}
	repository := &readerCaptureRepository{toolSource: source}
	service := NewService(repository, projectFixture{value: selected})
	bound, err := service.BindToolArtifacts(context.Background(), selected.ID, tool.Call{ID: source.CallID, Permissions: source.Permissions}, source.Artifacts)
	if err != nil || len(bound) != 1 || len(bound[0].SHA256) != 64 || bound[0].SizeBytes != 8 {
		t.Fatalf("BindToolArtifacts() = %#v, %v", bound, err)
	}
	repository.toolSource.Artifacts = bound
	if err := os.WriteFile(path, []byte("x,y\n9,9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := service.RegisterToolArtifacts(context.Background(), source.CallID)
	if err != nil || len(results) != 1 || repository.createCall != 1 || repository.record.Version.SHA256 != bound[0].SHA256 {
		t.Fatalf("changed deferred Tool Artifact = %#v, %v, calls=%d", results, err, repository.createCall)
	}
	object := filepath.Join(workspace, project.PrivateDirectoryName, filepath.FromSlash(repository.record.Blob.StorageRelativePath))
	contents, err := os.ReadFile(object)
	if err != nil || string(contents) != "x,y\n1,2\n" {
		t.Fatalf("registered frozen object = %q, %v", contents, err)
	}
}

func TestBindToolArtifactsRejectsToolComputedHashMismatch(t *testing.T) {
	workspace, selected := newArtifactWorkspace(t)
	if err := os.WriteFile(filepath.Join(workspace, "result.csv"), []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(&readerCaptureRepository{}, projectFixture{value: selected})
	_, err := service.BindToolArtifacts(context.Background(), selected.ID, tool.Call{Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}}}, []tool.ArtifactRef{{WorkspacePath: "result.csv", SHA256: strings.Repeat("a", 64)}})
	if err == nil || !strings.Contains(err.Error(), "tool-computed SHA256") {
		t.Fatalf("BindToolArtifacts() hash mismatch = %v", err)
	}
	objects, globErr := filepath.Glob(filepath.Join(workspace, ".sciaide", "artifacts", "objects", "*", "*"))
	if globErr != nil || len(objects) != 0 {
		t.Fatalf("hash mismatch published content objects = %#v, %v", objects, globErr)
	}
	temporary, globErr := filepath.Glob(filepath.Join(workspace, ".sciaide", "tmp", "artifact-*"))
	if globErr != nil || len(temporary) != 0 {
		t.Fatalf("hash mismatch left temporary files = %#v, %v", temporary, globErr)
	}
}

func TestRegisterToolArtifactsRequiresWorkspacePermission(t *testing.T) {
	_, selected := newArtifactWorkspace(t)
	repository := &readerCaptureRepository{toolSource: ToolSource{
		ProjectID: selected.ID,
		CallID:    "call",
		ToolName:  "fixture.tool",
		Artifacts: []tool.ArtifactRef{{WorkspacePath: "secret.csv"}},
	}}
	service := NewService(repository, projectFixture{value: selected})
	if _, err := service.RegisterToolArtifacts(context.Background(), "call"); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("workspace permission error = %v", err)
	}
}

func TestRegisterToolArtifactsEnforcesWorkspacePermissionResource(t *testing.T) {
	workspace, selected := newArtifactWorkspace(t)
	for _, relative := range []string{"outputs/result.csv", "outputs-other/result.csv", "papers/secret.csv"} {
		path := filepath.Join(workspace, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x,y\n1,2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name          string
		resource      string
		workspacePath string
		wantAllowed   bool
	}{
		{name: "directory descendant", resource: "outputs", workspacePath: "outputs/result.csv", wantAllowed: true},
		{name: "exact file", resource: "outputs/result.csv", workspacePath: "outputs/result.csv", wantAllowed: true},
		{name: "adjacent prefix", resource: "outputs", workspacePath: "outputs-other/result.csv", wantAllowed: false},
		{name: "outside directory", resource: "outputs", workspacePath: "papers/secret.csv", wantAllowed: false},
		{name: "invalid parent scope", resource: "..", workspacePath: "outputs/result.csv", wantAllowed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &readerCaptureRepository{toolSource: ToolSource{
				ProjectID:   selected.ID,
				CallID:      "call",
				ToolName:    "fixture.tool",
				Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceWrite, Resource: test.resource}},
				Artifacts:   []tool.ArtifactRef{{WorkspacePath: test.workspacePath}},
			}}
			service := NewService(repository, projectFixture{value: selected})
			results, err := service.RegisterToolArtifacts(context.Background(), "call")
			if test.wantAllowed {
				if err != nil || len(results) != 1 || repository.createCall != 1 {
					t.Fatalf("RegisterToolArtifacts() = %#v, %v, calls=%d", results, err, repository.createCall)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "permission scope") || len(results) != 0 || repository.createCall != 0 {
				t.Fatalf("RegisterToolArtifacts() = %#v, %v, calls=%d", results, err, repository.createCall)
			}
		})
	}
}

func TestToolArtifactCitationsRequireRunBoundKnowledgeEvidence(t *testing.T) {
	source := ToolSource{ProjectID: "project", RunID: "run", CallID: "call", ToolName: "mcp.untrusted.report"}
	spoofed := tool.CitationRef{Reference: "[K-000000000000]", Quote: "spoofed", SourceName: "fake.pdf", SourceEnd: 7}
	if values := citationsFromTool([]tool.CitationRef{spoofed}, source); len(values) != 0 {
		t.Fatalf("untrusted Tool citations = %#v", values)
	}

	quote := "verified evidence"
	valid := tool.CitationRef{
		Kind: appcitation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "document",
		AttachmentID: "attachment", ChunkID: "chunk", SourceName: "paper.pdf", Locator: "page:1",
		Quote: quote, QuoteSHA256: appcitation.QuoteSHA256(quote), SourceEnd: len(quote),
	}
	valid.Reference = appcitation.KnowledgeReference(source.RunID, valid.IndexVersionID, valid.ChunkID, valid.QuoteSHA256)
	source.ToolName = appcitation.KnowledgeToolName
	values := citationsFromTool([]tool.CitationRef{valid}, source)
	if len(values) != 1 || values[0].Reference != valid.Reference || values[0].QuoteSHA256 != valid.QuoteSHA256 || values[0].SourceToolCallIDSnapshot != source.CallID {
		t.Fatalf("trusted Tool citations = %#v", values)
	}
	valid.QuoteSHA256 = appcitation.QuoteSHA256("changed")
	if values := citationsFromTool([]tool.CitationRef{valid}, source); len(values) != 0 {
		t.Fatalf("tampered Tool citations = %#v", values)
	}
	valid.QuoteSHA256 = appcitation.QuoteSHA256(valid.Quote)
	valid.ProjectID = "other-project"
	if values := citationsFromTool([]tool.CitationRef{valid}, source); len(values) != 0 {
		t.Fatalf("cross-project Tool citations = %#v", values)
	}
}

func TestCheckIntegrityReportsMissingObjectWithRecordedIdentity(t *testing.T) {
	_, selected := newArtifactWorkspace(t)
	repository := &readerCaptureRepository{
		version: Version{ID: "version", ArtifactID: "artifact", SizeBytes: 12, SHA256: strings.Repeat("a", 64)},
		blob:    BlobRecord{StorageRelativePath: "artifacts/objects/aa/" + strings.Repeat("a", 64)},
	}
	service := NewService(repository, projectFixture{value: selected})
	result, err := service.CheckIntegrity(context.Background(), selected.ID, "version")
	if err != nil || result.Status != IntegrityMissing || result.ArtifactID != "artifact" || result.ExpectedSize != 12 || result.ExpectedSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("CheckIntegrity(missing) = %#v, %v", result, err)
	}
}

func TestTrimIncompleteUTF8OnlyRemovesTruncatedTail(t *testing.T) {
	validPrefix := append([]byte("result:"), []byte("研")[:2]...)
	trimmed := trimIncompleteUTF8(validPrefix)
	if string(trimmed) != "result:" || !utf8.Valid(trimmed) {
		t.Fatalf("trimIncompleteUTF8(truncated) = %q", trimmed)
	}
	invalid := []byte("valid\xfftail")
	if trimmed := trimIncompleteUTF8(invalid); !bytes.Equal(trimmed, invalid) {
		t.Fatalf("internal invalid UTF-8 was hidden: %q", trimmed)
	}
}

func TestEnsureExtensionPreservesDottedArtifactName(t *testing.T) {
	if value := ensureExtension("analysis.v1", ".md"); value != "analysis.v1.md" {
		t.Fatalf("ensureExtension() = %q", value)
	}
	if value := ensureExtension("analysis.MD", ".md"); value != "analysis.MD" {
		t.Fatalf("ensureExtension(existing) = %q", value)
	}
}

func TestDelimitedPreviewStopsAtBoundAndExportRejectsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.csv")
	var source strings.Builder
	source.WriteString("row,value\n")
	for index := 0; index < maxPreviewTableRows+1; index++ {
		source.WriteString("1,data\n")
	}
	if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	blocks, truncated, err := previewDelimitedBlocks(path, false)
	if err != nil || !truncated || len(blocks) == 0 {
		t.Fatalf("previewDelimitedBlocks() = blocks:%d truncated:%t err:%v", len(blocks), truncated, err)
	}

	tooManyColumns := filepath.Join(t.TempDir(), "wide.csv")
	columns := strings.Repeat("cell,", maxExportTableColumns) + "cell\n"
	if err := os.WriteFile(tooManyColumns, []byte(columns), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := delimitedBlocks(tooManyColumns, false); err == nil || !strings.Contains(err.Error(), "columns") {
		t.Fatalf("wide CSV export error = %v", err)
	}
}

func TestRecoverRemovesEveryArtifactTemporaryPrefix(t *testing.T) {
	workspace, selected := newArtifactWorkspace(t)
	repository := &readerCaptureRepository{}
	service := NewService(repository, projectFixture{value: selected})
	temporary := filepath.Join(workspace, ".sciaide", "tmp")
	for _, name := range []string{"artifact-save", "artifact-export-save", "export-source-copy.md", "export-validate-copy.pdf", "preview-source-copy.docx"} {
		if err := os.WriteFile(filepath.Join(temporary, name), []byte("temporary"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(temporary, "knowledge-worker"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.Recover(context.Background())
	if err != nil || result.TemporaryFilesRemoved != 5 {
		t.Fatalf("Recover() = %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(temporary, "knowledge-worker")); err != nil {
		t.Fatal("unrelated temporary file was removed")
	}
}

func newArtifactWorkspace(t *testing.T) (string, project.Project) {
	t.Helper()
	workspace := t.TempDir()
	selected := project.Project{ID: "project", WorkspacePath: workspace}
	if err := os.MkdirAll(filepath.Join(workspace, ".sciaide", "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".sciaide", "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".sciaide", "project.json"), []byte(`{"version":1,"projectId":"project"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace, selected
}

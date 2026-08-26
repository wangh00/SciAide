package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/chat"
	appcitation "github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestArtifactAssistantSnapshotSurvivesConversationDeletionAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "artifact.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	createdProject, err := projects.Create(ctx, "Artifact project", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	profile := modelprofile.Profile{ID: "artifact-profile", Name: "Research model", ProviderType: modelprofile.ProviderOpenAICompatible, APIProtocol: modelcap.ProtocolOpenAIResponses, BaseURL: "https://example.test/v1", ModelID: "fixture-model", Models: []modelprofile.ProfileModel{{ID: "fixture-model", Enabled: true, IsDefault: true}}, SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	conversationService := conversation.NewService(NewConversationRepository(store.DB()))
	createdConversation, err := conversationService.Create(ctx, createdProject.ID, "Experiment summary")
	if err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "artifact-run", ConversationID: createdConversation.ID, UserMessageID: "artifact-user", AssistantMessageID: "artifact-assistant", ModelProfileID: profile.ID, ModelID: profile.ModelID, APIProtocol: profile.APIProtocol, Status: chat.RunRunning, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "artifact-user-part", MessageID: run.UserMessageID, Type: "text", Text: "question", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{ID: "artifact-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	runs := NewRunRepository(store.DB())
	if err := runs.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	quote := "verified evidence"
	digest := sha256.Sum256([]byte(quote))
	quoteSHA256 := hex.EncodeToString(digest[:])
	reference := appcitation.KnowledgeReference(run.ID, "index-v1", "chunk-1", quoteSHA256)
	citation := conversation.Citation{ID: "artifact-citation", MessageID: assistant.ID, RunID: run.ID, ToolCallID: "artifact-call", ProjectID: createdProject.ID, Reference: reference, Ordinal: 0, IndexVersionID: "index-v1", DocumentID: "document-1", AttachmentID: "attachment-1", ChunkID: "chunk-1", SourceName: "paper.pdf", Locator: "page:1", Quote: quote, QuoteSHA256: quoteSHA256, SourceStart: 0, SourceEnd: len(quote), CreatedAt: now}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at) VALUES ('artifact-call',?,'provider-call',?,'1','{}','completed','low','[]',1,?,?)`, run.ID, appcitation.KnowledgeToolName, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO run_dynamic_skills(run_id,project_id,ordinal,tool_call_id,skill_name,origin,category,content_hash,package_hash,instruction_snapshot,loaded_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, createdProject.ID, 0, "artifact-call", "scientific-writing", "default", "writing", strings64("d"), strings64("e"), "Immutable dynamic Skill instructions.", formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO tool_results(tool_call_id,status,created_at) VALUES ('artifact-call','success',?)`, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	completed := now.Add(time.Second)
	run.Status, run.FinishReason, run.CompletedAt, run.UpdatedAt = chat.RunCompleted, "stop", &completed, completed
	if err := runs.Complete(ctx, run, "Final answer "+reference, []conversation.Citation{citation}); err != nil {
		t.Fatal(err)
	}
	repository := NewArtifactRepository(store.DB())
	source, err := repository.AssistantSource(ctx, createdProject.ID, assistant.ID)
	if err != nil || source.ModelProfileName != profile.Name || len(source.Citations) != 1 || len(source.Skills) != 1 {
		t.Fatalf("source = %#v, %v", source, err)
	}
	if skill := source.Skills[0]; skill.ID != "scientific-writing" || skill.Version != "dynamic" || skill.Origin != "default" || !skill.Dynamic || skill.ContentHash != strings64("d") || skill.PackageHash != strings64("e") {
		t.Fatalf("dynamic Skill source = %#v", skill)
	}
	provenance := artifact.Provenance{SchemaVersion: 1, ProjectID: createdProject.ID, SourceKind: artifact.SourceAssistantMessage, ConversationID: source.ConversationID, ConversationTitle: source.ConversationTitle, RunID: source.RunID, MessageID: source.MessageID, ModelProfileName: source.ModelProfileName, ModelID: source.ModelID, APIProtocol: source.APIProtocol, Skills: source.Skills}
	versionTime := completed.Add(time.Second)
	record := artifact.CreateVersionRecord{ArtifactID: "artifact", ProjectID: createdProject.ID, Name: "Answer", Kind: artifact.KindDocument,
		Blob:    artifact.BlobRecord{ID: "blob", ProjectID: createdProject.ID, SHA256: strings64("a"), SizeBytes: 12, MIMEType: "text/markdown", StorageRelativePath: "artifacts/objects/aa/" + strings64("a"), CreatedAt: versionTime},
		Version: artifact.Version{ID: "version", ArtifactID: "artifact", BlobID: "blob", FileName: "answer.md", MIMEType: "text/markdown", SizeBytes: 12, SHA256: strings64("a"), SourceKind: artifact.SourceAssistantMessage, SourceKey: "assistant:" + assistant.ID, Provenance: provenance, Lineage: []artifact.Lineage{{ID: "lineage-run", RelationKind: "run", SourceIDSnapshot: run.ID, SourceRunID: run.ID, CreatedAt: versionTime}, {ID: "lineage-message", Ordinal: 1, RelationKind: "message", SourceIDSnapshot: assistant.ID, SourceMessageID: assistant.ID, CreatedAt: versionTime}}, Citations: source.Citations, CreatedAt: versionTime}}
	for index := range record.Version.Citations {
		record.Version.Citations[index].ID = "artifact-citation-snapshot"
		record.Version.Citations[index].CreatedAt = versionTime
	}
	created, err := repository.CreateVersion(ctx, record)
	if err != nil || !created.Created || created.Version.VersionNumber != 1 {
		t.Fatalf("CreateVersion() = %#v, %v", created, err)
	}
	replayed, err := repository.CreateVersion(ctx, record)
	if err != nil || replayed.Created || replayed.Version.ID != created.Version.ID {
		t.Fatalf("idempotent CreateVersion() = %#v, %v", replayed, err)
	}
	if err := conversationService.Remove(ctx, createdConversation.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := repository.Get(ctx, createdProject.ID, created.Artifact.ID)
	if err != nil || len(detail.Versions) != 1 || detail.Versions[0].Provenance.ModelProfileName != profile.Name || len(detail.Versions[0].Citations) != 1 || len(detail.Versions[0].Provenance.Skills) != 1 || !detail.Versions[0].Provenance.Skills[0].Dynamic || detail.Versions[0].Provenance.Skills[0].Origin != "default" {
		t.Fatalf("detail after conversation delete = %#v, %v", detail, err)
	}
	if detail.Versions[0].Lineage[0].SourceRunID != "" || detail.Versions[0].Lineage[0].SourceIDSnapshot != run.ID {
		t.Fatalf("lineage after delete = %#v", detail.Versions[0].Lineage)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE artifact_versions SET file_name='changed.md' WHERE id=?`, created.Version.ID); err == nil {
		t.Fatal("immutable Artifact version was updated")
	}
	encoded, _ := json.Marshal(detail.Versions[0].Provenance)
	if len(encoded) == 0 {
		t.Fatal("provenance snapshot is empty")
	}
}

func strings64(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}

func TestArtifactGraphCascadesWhenProjectIsRemoved(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "artifact-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	createdProject, err := projects.Create(ctx, "Artifact deletion", "")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewArtifactRepository(store.DB())
	now := time.Date(2026, 8, 23, 14, 0, 0, 0, time.UTC)
	first := artifact.CreateVersionRecord{
		ArtifactID: "artifact-delete", ProjectID: createdProject.ID, Name: "Results", Kind: artifact.KindData,
		Blob:    artifact.BlobRecord{ID: "blob-delete-1", ProjectID: createdProject.ID, SHA256: strings64("b"), SizeBytes: 8, MIMEType: "text/csv", StorageRelativePath: "artifacts/objects/bb/" + strings64("b"), CreatedAt: now},
		Version: artifact.Version{ID: "version-delete-1", ArtifactID: "artifact-delete", BlobID: "blob-delete-1", FileName: "results.csv", MIMEType: "text/csv", SizeBytes: 8, SHA256: strings64("b"), SourceKind: artifact.SourceWorkspaceFile, SourceKey: "delete-v1", Provenance: artifact.Provenance{SchemaVersion: 1, ProjectID: createdProject.ID, SourceKind: artifact.SourceWorkspaceFile, Skills: []artifact.SkillSnapshot{}}, Lineage: []artifact.Lineage{{ID: "lineage-delete-1", RelationKind: "workspace_file", SourceIDSnapshot: "results.csv", CreatedAt: now}}, CreatedAt: now},
	}
	if _, err := repository.CreateVersion(ctx, first); err != nil {
		t.Fatal(err)
	}
	quote := "measured result"
	digest := sha256.Sum256([]byte(quote))
	secondAt := now.Add(time.Second)
	second := artifact.CreateVersionRecord{
		ArtifactID: first.ArtifactID, ProjectID: createdProject.ID, Name: first.Name, Kind: artifact.KindData,
		Blob:    artifact.BlobRecord{ID: "blob-delete-2", ProjectID: createdProject.ID, SHA256: strings64("c"), SizeBytes: 9, MIMEType: "text/csv", StorageRelativePath: "artifacts/objects/cc/" + strings64("c"), CreatedAt: secondAt},
		Version: artifact.Version{ID: "version-delete-2", ArtifactID: first.ArtifactID, BlobID: "blob-delete-2", FileName: "results.csv", MIMEType: "text/csv", SizeBytes: 9, SHA256: strings64("c"), SourceKind: artifact.SourceWorkspaceFile, SourceKey: "delete-v2", Provenance: artifact.Provenance{SchemaVersion: 1, ProjectID: createdProject.ID, SourceKind: artifact.SourceWorkspaceFile, Skills: []artifact.SkillSnapshot{}}, Lineage: []artifact.Lineage{{ID: "lineage-delete-2", RelationKind: "artifact_version", SourceIDSnapshot: first.Version.ID, SourceArtifactVersionID: first.Version.ID, CreatedAt: secondAt}}, Citations: []artifact.Citation{{ID: "citation-delete", Reference: "fixture-reference", SourceName: "fixture.csv", Quote: quote, QuoteSHA256: hex.EncodeToString(digest[:]), SourceEnd: len(quote), CreatedAt: secondAt}}, CreatedAt: secondAt},
	}
	if _, err := repository.CreateVersion(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.Remove(ctx, createdProject.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"artifact_exports", "artifact_citations", "artifact_lineage", "artifact_versions", "artifact_blobs", "artifacts"} {
		var count int
		if err := store.DB().QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count after project delete = %d, %v", table, count, err)
		}
	}
}

func TestArtifactExportIsImmutableAndIdempotent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "artifact-export.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	createdProject, err := projects.Create(ctx, "Artifact export", "")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewArtifactRepository(store.DB())
	now := time.Date(2026, 8, 23, 17, 0, 0, 0, time.UTC)
	sourceSHA := strings64("f")
	created, err := repository.CreateVersion(ctx, artifact.CreateVersionRecord{
		ArtifactID: "export-artifact", ProjectID: createdProject.ID, Name: "Report", Kind: artifact.KindDocument,
		Blob:    artifact.BlobRecord{ID: "export-source-blob", ProjectID: createdProject.ID, SHA256: sourceSHA, SizeBytes: 4, MIMEType: "text/markdown", StorageRelativePath: "artifacts/objects/ff/" + sourceSHA, CreatedAt: now},
		Version: artifact.Version{ID: "export-source-version", ArtifactID: "export-artifact", BlobID: "export-source-blob", FileName: "report.md", MIMEType: "text/markdown", SizeBytes: 4, SHA256: sourceSHA, SourceKind: artifact.SourceWorkspaceFile, SourceKey: "export-source", Provenance: artifact.Provenance{SchemaVersion: 1, ProjectID: createdProject.ID, SourceKind: artifact.SourceWorkspaceFile, Skills: []artifact.SkillSnapshot{}}, Lineage: []artifact.Lineage{{ID: "export-source-lineage", RelationKind: "workspace_file", SourceIDSnapshot: "report.md", CreatedAt: now}}, CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	exportSHA := strings64("9")
	record := artifact.CreateExportRecord{
		Export: artifact.Export{ID: "export-id", ProjectID: createdProject.ID, ArtifactVersionID: created.Version.ID, Format: artifact.ExportPDF, CitationStyle: artifact.CitationGB7714, GeneratorVersion: "p6.2-v1", FileName: "report.pdf", MIMEType: "application/pdf", SizeBytes: 9, SHA256: exportSHA, SourceSHA256: sourceSHA, CreatedAt: now.Add(time.Second)},
		Blob:   artifact.BlobRecord{ID: "export-blob", ProjectID: createdProject.ID, SHA256: exportSHA, SizeBytes: 9, MIMEType: "application/pdf", StorageRelativePath: "artifacts/objects/99/" + exportSHA, CreatedAt: now.Add(time.Second)},
	}
	first, err := repository.CreateExport(ctx, record)
	if err != nil || !first.Created || first.Export.BlobID == "" {
		t.Fatalf("CreateExport() = %#v, %v", first, err)
	}
	replayed, err := repository.CreateExport(ctx, record)
	if err != nil || replayed.Created || replayed.Export.ID != first.Export.ID {
		t.Fatalf("idempotent CreateExport() = %#v, %v", replayed, err)
	}
	detail, err := repository.Get(ctx, createdProject.ID, created.Artifact.ID)
	if err != nil || len(detail.Versions) != 1 || len(detail.Versions[0].Exports) != 1 {
		t.Fatalf("Artifact exports = %#v, %v", detail, err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE artifact_exports SET file_name='changed.pdf' WHERE id=?`, first.Export.ID); err == nil {
		t.Fatal("immutable Artifact export was updated")
	}
	if _, err := projects.Remove(ctx, createdProject.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"artifact_exports", "artifact_versions", "artifact_blobs", "artifacts"} {
		var count int
		if err := store.DB().QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count after project delete = %d, %v", table, count, err)
		}
	}
}

func TestRecoverableToolCallIDsOnlyReturnsMissingOrdinals(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "artifact-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	createdProject, err := projects.Create(ctx, "Tool recovery", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	profile := modelprofile.Profile{ID: "recovery-profile", Name: "Recovery model", ProviderType: modelprofile.ProviderOpenAICompatible, APIProtocol: modelcap.ProtocolOpenAIResponses, BaseURL: "https://example.test/v1", ModelID: "fixture-model", Models: []modelprofile.ProfileModel{{ID: "fixture-model", Enabled: true, IsDefault: true}}, SecretRef: "recovery-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	conversationService := conversation.NewService(NewConversationRepository(store.DB()))
	createdConversation, err := conversationService.Create(ctx, createdProject.ID, "Recovery conversation")
	if err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "recovery-run", ConversationID: createdConversation.ID, UserMessageID: "recovery-user", AssistantMessageID: "recovery-assistant", ModelProfileID: profile.ID, ModelID: profile.ModelID, APIProtocol: profile.APIProtocol, Status: chat.RunRunning, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "recovery-user-part", MessageID: run.UserMessageID, Type: "text", Text: "question", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{ID: "recovery-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	if err := NewRunRepository(store.DB()).CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	artifactsJSON := `[{"workspacePath":"output-a.csv","sizeBytes":8,"sha256":"` + strings64("d") + `"},{"workspacePath":"output-b.csv","sizeBytes":8,"sha256":"` + strings64("e") + `"}]`
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at) VALUES ('recovery-call',?,'provider-call','fixture.tool','1','{}','completed','low','[]',1,?,?)`, run.ID, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO tool_results(tool_call_id,status,artifacts_json,created_at) VALUES ('recovery-call','success',?,?)`, artifactsJSON, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	repository := NewArtifactRepository(store.DB())
	record := artifact.CreateVersionRecord{
		ArtifactID: "recovery-artifact", ProjectID: createdProject.ID, Name: "Recovered", Kind: artifact.KindData,
		Blob:    artifact.BlobRecord{ID: "recovery-blob", ProjectID: createdProject.ID, SHA256: strings64("d"), SizeBytes: 8, MIMEType: "text/csv", StorageRelativePath: "artifacts/objects/dd/" + strings64("d"), CreatedAt: now},
		Version: artifact.Version{ID: "recovery-version", ArtifactID: "recovery-artifact", BlobID: "recovery-blob", FileName: "output-a.csv", MIMEType: "text/csv", SizeBytes: 8, SHA256: strings64("d"), SourceKind: artifact.SourceTool, SourceKey: "tool:recovery-call:0", Provenance: artifact.Provenance{SchemaVersion: 1, ProjectID: createdProject.ID, SourceKind: artifact.SourceTool, Skills: []artifact.SkillSnapshot{}}, Lineage: []artifact.Lineage{{ID: "recovery-lineage", RelationKind: "tool_call", SourceIDSnapshot: "recovery-call", SourceToolCallID: "recovery-call", CreatedAt: now}}, CreatedAt: now},
	}
	if _, err := repository.CreateVersion(ctx, record); err != nil {
		t.Fatal(err)
	}
	callIDs, err := repository.RecoverableToolCallIDs(ctx)
	if err != nil || len(callIDs) != 1 || callIDs[0] != "recovery-call" {
		t.Fatalf("recoverable call IDs = %#v, %v", callIDs, err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE tool_results SET artifacts_json=? WHERE tool_call_id='recovery-call'`, `[{"workspacePath":"output-a.csv","sizeBytes":8,"sha256":"`+strings64("d")+`"}]`); err != nil {
		t.Fatal(err)
	}
	callIDs, err = repository.RecoverableToolCallIDs(ctx)
	if err != nil || len(callIDs) != 0 {
		t.Fatalf("fully registered call IDs = %#v, %v", callIDs, err)
	}
}

func TestArtifactBlobDedupAllowsVersionSpecificMIME(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "artifact-mime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	createdProject, err := projects.Create(ctx, "Artifact MIME", "")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewArtifactRepository(store.DB())
	now := time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)
	digest := strings64("e")
	objectPath := "artifacts/objects/ee/" + digest
	newRecord := func(artifactID, blobID, versionID, sourceKey, fileName, mimeType string) artifact.CreateVersionRecord {
		return artifact.CreateVersionRecord{
			ArtifactID: artifactID, ProjectID: createdProject.ID, Name: fileName, Kind: artifact.KindDocument,
			Blob:    artifact.BlobRecord{ID: blobID, ProjectID: createdProject.ID, SHA256: digest, SizeBytes: 4, MIMEType: mimeType, StorageRelativePath: objectPath, CreatedAt: now},
			Version: artifact.Version{ID: versionID, ArtifactID: artifactID, BlobID: blobID, FileName: fileName, MIMEType: mimeType, SizeBytes: 4, SHA256: digest, SourceKind: artifact.SourceWorkspaceFile, SourceKey: sourceKey, Provenance: artifact.Provenance{SchemaVersion: 1, ProjectID: createdProject.ID, SourceKind: artifact.SourceWorkspaceFile, Skills: []artifact.SkillSnapshot{}}, Lineage: []artifact.Lineage{{ID: "lineage-" + versionID, RelationKind: "workspace_file", SourceIDSnapshot: fileName, CreatedAt: now}}, CreatedAt: now},
		}
	}
	if _, err := repository.CreateVersion(ctx, newRecord("artifact-text", "blob-text", "version-text", "mime-text", "same.txt", "text/plain; charset=utf-8")); err != nil {
		t.Fatal(err)
	}
	created, err := repository.CreateVersion(ctx, newRecord("artifact-csv", "blob-csv", "version-csv", "mime-csv", "same.csv", "text/csv; charset=utf-8"))
	if err != nil || created.Version.MIMEType != "text/csv; charset=utf-8" {
		t.Fatalf("CreateVersion(shared bytes) = %#v, %v", created, err)
	}
	var blobs int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM artifact_blobs WHERE project_id=? AND sha256=?`, createdProject.ID, digest).Scan(&blobs); err != nil || blobs != 1 {
		t.Fatalf("shared blob count = %d, %v", blobs, err)
	}
}

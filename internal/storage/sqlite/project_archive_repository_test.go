package sqlite

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestProjectArchiveRoundTripRestoresFilesAndExcludesSecrets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "data", "sciaide.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaces := filepath.Join(root, "data", "workspaces")
	trash := filepath.Join(root, "backups", "trash")
	projects := project.NewService(NewProjectRepository(store.DB()), workspaces, trash)
	created, err := projects.Create(ctx, "中文科研项目", "archive round trip")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(created.WorkspacePath, "研究记录.txt")
	contents := []byte("第一条可信证据\n第二条结论\n")
	if err := os.WriteFile(source, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	attachments := attachment.NewService(NewAttachmentRepository(store.DB()), projects)
	batch, err := attachments.ImportPaths(ctx, created.ID, []string{source})
	if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 1 {
		t.Fatalf("attachment import = %#v, %v", batch, err)
	}
	originalAttachment := batch.Attachments[0]
	if _, err := attachments.CollectMaterial(ctx, created.ID, "", originalAttachment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := attachments.SaveMaterial(ctx, created.ID, "", originalAttachment.ID, "研究者原始记录", "导出前的人工备注", true); err != nil {
		t.Fatal(err)
	}
	artifacts := artifact.NewService(NewArtifactRepository(store.DB()), projects)
	saved, err := artifacts.RegisterWorkspaceFile(ctx, artifact.RegisterWorkspaceCommand{ProjectID: created.ID, Path: source, Name: "研究产物"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "secret-profile", Name: "Secret model", ProviderType: modelprofile.ProviderOpenAICompatible, APIProtocol: modelcap.ProtocolOpenAIChat, BaseURL: "https://secret-api.example/v1?access_token=URL-SECRET", ModelID: "secret-model", Models: []modelprofile.ProfileModel{{ID: "secret-model", Enabled: true, IsDefault: true}}, SecretRef: "SUPER-SECRET-API-KEY", TimeoutSeconds: 60, CustomHeaders: map[string]string{"Authorization": "Bearer HEADER-SECRET"}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	conversations := conversation.NewService(NewConversationRepository(store.DB()))
	historical, err := conversations.Create(ctx, created.ID, "Historical conversation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversations.SetModelSelection(ctx, historical.ID, profile.ID, profile.ModelID); err != nil {
		t.Fatal(err)
	}
	if _, err := conversations.SetPermissionMode(ctx, historical.ID, conversation.PermissionFullAccess); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO mcp_servers(id,name,namespace,transport,command,env_json,secret_env_json,created_at,updated_at) VALUES ('mcp','MCP','mcp','stdio','node','{}','{"TOKEN":"MCP-SUPER-SECRET"}',?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO vision_fallback_channels(id,name,base_url,model_id,api_protocol,secret_ref,created_at,updated_at) VALUES ('vision','Vision','https://vision.example/v1','vision','openai_chat_completions','VISION-SUPER-SECRET',?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	archiveService := newProjectArchiveTestService(t, store, projects, root)
	archivePath := filepath.Join(root, "exports", "科研项目.sciaide-project")
	exported, err := archiveService.Export(ctx, created.ID, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if exported.FileCount < 3 || exported.Manifest.Stats.Attachments != 1 || exported.Manifest.Stats.Artifacts != 1 {
		t.Fatalf("export result = %#v", exported)
	}
	assertArchiveOmits(t, archivePath, "SUPER-SECRET-API-KEY", "HEADER-SECRET", "MCP-SUPER-SECRET", "VISION-SUPER-SECRET", "URL-SECRET", "secret-api.example")
	assertSanitizedArchiveDatabase(t, archivePath)

	restored, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: archivePath})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Project.ID == created.ID || restored.Project.Name != "中文科研项目 (restored)" || restored.Project.WorkspaceKind != project.WorkspaceManaged || restored.HistoricalProfiles != 1 || !restored.SecretsRequireRebinding {
		t.Fatalf("restored project = %#v", restored.Project)
	}
	values, err := attachments.List(ctx, restored.Project.ID)
	if err != nil || len(values) != 1 || values[0].ID == originalAttachment.ID || values[0].SHA256 != originalAttachment.SHA256 {
		t.Fatalf("restored attachments = %#v, %v", values, err)
	}
	restoredMaterial, err := attachments.GetMaterial(ctx, restored.Project.ID, "", values[0].ID)
	if err != nil || restoredMaterial.Title != "研究者原始记录" || restoredMaterial.Notes != "导出前的人工备注" || !restoredMaterial.Archived || !restoredMaterial.Collected {
		t.Fatalf("restored material-library metadata = %#v, %v", restoredMaterial, err)
	}
	if visible, err := attachments.ListMaterials(ctx, restored.Project.ID, ""); err != nil || len(visible) != 0 {
		t.Fatalf("archived metadata was not preserved in restored listing: %#v, %v", visible, err)
	}
	loaded, parsed, err := attachments.Parsed(ctx, restored.Project.ID, values[0].ID)
	if err != nil || loaded.Status != attachment.StatusReady || len(parsed.Units) == 0 || !strings.Contains(parsed.Units[0].Content, "可信证据") {
		t.Fatalf("restored parsed attachment = %#v, %#v, %v", loaded, parsed, err)
	}
	restoredArtifacts, err := artifacts.List(ctx, restored.Project.ID, true)
	if err != nil || len(restoredArtifacts) != 1 || restoredArtifacts[0].ID == saved.Artifact.ID {
		t.Fatalf("restored artifacts = %#v, %v", restoredArtifacts, err)
	}
	detail, err := artifacts.Get(ctx, restored.Project.ID, restoredArtifacts[0].ID)
	if err != nil || len(detail.Versions) != 1 || detail.Versions[0].SHA256 != saved.Version.SHA256 {
		t.Fatalf("restored Artifact detail = %#v, %v", detail, err)
	}
	verified, err := artifacts.CheckIntegrity(ctx, restored.Project.ID, detail.Versions[0].ID)
	if err != nil || verified.Status != artifact.IntegrityVerified {
		t.Fatalf("restored Artifact verification = %#v, %v", verified, err)
	}
	var profileCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM model_profiles WHERE id<>'secret-profile'`).Scan(&profileCount); err != nil || profileCount != 1 {
		t.Fatalf("historical profile placeholder count=%d, err=%v", profileCount, err)
	}
	var restoredBaseURL, restoredSecretRef, restoredHeaders, restoredPermission string
	var restoredEnabled, restoredDefault bool
	if err := store.DB().QueryRowContext(ctx, `
		SELECT p.base_url,p.secret_ref,p.custom_headers_json,p.enabled,p.is_default,c.permission_mode
		FROM conversations c JOIN model_profiles p ON p.id=c.model_profile_id
		WHERE c.project_id=?`, restored.Project.ID).Scan(&restoredBaseURL, &restoredSecretRef, &restoredHeaders, &restoredEnabled, &restoredDefault, &restoredPermission); err != nil {
		t.Fatal(err)
	}
	if restoredBaseURL != archiveModelBaseURL || !strings.HasPrefix(restoredSecretRef, "archive/profile/") || restoredHeaders != "{}" || restoredEnabled || restoredDefault || restoredPermission != string(conversation.PermissionPlan) {
		t.Fatalf("unsafe historical profile or permission restored: base=%q ref=%q headers=%q enabled=%v default=%v permission=%q", restoredBaseURL, restoredSecretRef, restoredHeaders, restoredEnabled, restoredDefault, restoredPermission)
	}
	marker, err := os.ReadFile(filepath.Join(restored.Project.WorkspacePath, project.PrivateDirectoryName, "project.json"))
	if err != nil || !bytes.Contains(marker, []byte(restored.Project.ID)) || bytes.Contains(marker, []byte(created.ID)) {
		t.Fatalf("restored project marker = %q, %v", marker, err)
	}

	second, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: archivePath, Name: "第二份恢复"})
	if err != nil || second.Project.ID == restored.Project.ID || second.Project.Name != "第二份恢复" {
		t.Fatalf("second restore = %#v, %v", second, err)
	}
	projectsList, err := projects.List(ctx)
	if err != nil || len(projectsList) != 3 {
		t.Fatalf("projects after duplicate restore = %#v, %v", projectsList, err)
	}
}

func TestProjectArchivePreservesPendingToolArtifactObjectForRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "data", "sciaide.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	created, err := projects.Create(ctx, "pending Artifact", "")
	if err != nil {
		t.Fatal(err)
	}
	conversations := conversation.NewService(NewConversationRepository(store.DB()))
	selectedConversation, err := conversations.Create(ctx, created.ID, "tool source")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "pending-artifact-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "fixture-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "pending-artifact-user", ConversationID: selectedConversation.ID, RunID: "pending-artifact-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "pending-artifact-user-part", MessageID: "pending-artifact-user", Type: "text", Text: "produce", Ordinal: 0, CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	assistant := conversation.Message{ID: "pending-artifact-assistant", ConversationID: selectedConversation.ID, RunID: "pending-artifact-run", Role: conversation.RoleAssistant, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "pending-artifact-assistant-part", MessageID: "pending-artifact-assistant", Type: "text", Text: "done", Ordinal: 0, CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	run := chat.Run{ID: "pending-artifact-run", ConversationID: selectedConversation.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", APIProtocol: modelcap.ProtocolOpenAIChat, Status: chat.RunCompleted, FinishReason: "stop", CreatedAt: now, StartedAt: &now, CompletedAt: &now, UpdatedAt: now}
	if err := NewRunRepository(store.DB()).CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	payload := []byte("x,y\n1,2\n")
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	objectRelative := filepath.ToSlash(filepath.Join("artifacts", "objects", hash[:2], hash))
	objectPath := filepath.Join(project.PrivateDataPath(created), filepath.FromSlash(objectRelative))
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "pending-artifact-call", RunID: run.ID, ProviderCallID: "fixture-call", ToolName: "fixture.tool", ToolVersion: "1", Arguments: json.RawMessage(`{}`), Status: tool.CallRunning, Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}}, Idempotent: true, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	result := tool.Result{Status: tool.ResultSuccess, Artifacts: []tool.ArtifactRef{{Name: "result", MIMEType: "text/csv", WorkspacePath: "result.csv", SizeBytes: int64(len(payload)), SHA256: hash}}, Citations: []tool.CitationRef{}, CreatedAt: now}
	toolRepository := NewToolRepository(store.DB())
	if err := toolRepository.Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := toolRepository.Finish(ctx, call.ID, tool.CallRunning, tool.CallCompleted, result, "", "", now); err != nil {
		t.Fatal(err)
	}
	archiveService := newProjectArchiveTestService(t, store, projects, root)
	archivePath := filepath.Join(root, "exports", "pending.sciaide-project")
	exported, err := archiveService.Export(ctx, created.ID, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range exported.Manifest.Files {
		if file.StorageRelativePath == objectRelative && file.SHA256 == hash {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending Tool Artifact object missing from manifest: %#v", exported.Manifest.Files)
	}
	restored, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: archivePath})
	if err != nil {
		t.Fatal(err)
	}
	restoredObject := filepath.Join(project.PrivateDataPath(restored.Project), filepath.FromSlash(objectRelative))
	contents, err := os.ReadFile(restoredObject)
	if err != nil || !bytes.Equal(contents, payload) {
		t.Fatalf("restored pending object = %q, %v", contents, err)
	}
	artifacts := artifact.NewService(NewArtifactRepository(store.DB()), projects)
	recovery, err := artifacts.Recover(ctx)
	if err != nil || recovery.ToolArtifactsRecovered != 2 || recovery.ToolArtifactsFailed != 0 {
		t.Fatalf("Artifact Recover() = %#v, %v", recovery, err)
	}
	values, err := artifacts.List(ctx, restored.Project.ID, false)
	if err != nil || len(values) != 1 || values[0].CurrentVersion == nil || values[0].CurrentVersion.SHA256 != hash {
		t.Fatalf("restored pending Artifact = %#v, %v", values, err)
	}
}

func TestProjectArchiveValidationMigratesTrustedOlderSnapshotInIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive-v59.sqlite")
	db, err := OpenExisting(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateToVersion(ctx, db, 59); err != nil {
		db.Close()
		t.Fatal(err)
	}
	const at = "2026-08-25T00:00:00Z"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('archive-project','Legacy archive','','C:/archive-workspace','external',?,?)`, at, at); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := pruneArchiveSnapshot(ctx, db, "archive-project"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := writeArchiveSkillBindings(ctx, db, nil); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repository := NewProjectArchiveRepository(nil)
	snapshot, err := repository.ValidateSnapshot(ctx, path, "archive-project", 59)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Project.ID != "archive-project" || snapshot.DatabaseSchemaVersion != 59 {
		t.Fatalf("migrated archive snapshot = %#v", snapshot)
	}
	upgraded, err := OpenExisting(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	current, err := CurrentSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := upgraded.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != current {
		t.Fatalf("migrated archive schema version = %d, %v", version, err)
	}
	var workflowStepSQL string
	if err := upgraded.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='workflow_steps'`).Scan(&workflowStepSQL); err != nil || !strings.Contains(workflowStepSQL, "candidate_selection") {
		t.Fatalf("migrated Workflow step schema = %q, %v", workflowStepSQL, err)
	}
}

func TestProjectArchiveValidationRejectsFutureAndForgedOlderSchemas(t *testing.T) {
	ctx := context.Background()
	current, err := CurrentSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	repository := NewProjectArchiveRepository(nil)

	t.Run("future schema", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "future.sqlite")
		db, err := OpenExisting(ctx, path, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := migrateToVersion(ctx, db, current); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ValidateSnapshot(ctx, path, "archive-project", current+1); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("future archive schema error = %v", err)
		}
	})

	t.Run("forged older schema", func(t *testing.T) {
		const older = 59
		path := filepath.Join(t.TempDir(), "forged-v59.sqlite")
		db, err := OpenExisting(ctx, path, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := migrateToVersion(ctx, db, older); err != nil {
			db.Close()
			t.Fatal(err)
		}
		const at = "2026-08-25T00:00:00Z"
		if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('archive-project','Forged archive','','C:/archive-workspace','external',?,?)`, at, at); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := pruneArchiveSnapshot(ctx, db, "archive-project"); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := writeArchiveSkillBindings(ctx, db, nil); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE forged_archive_state(id TEXT PRIMARY KEY)`); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ValidateSnapshot(ctx, path, "archive-project", older); err == nil || !strings.Contains(err.Error(), "schema does not match migration 59") {
			t.Fatalf("forged older archive schema error = %v", err)
		}
	})
}

type archiveWorkflowTool struct {
	reportPath string
	artifactID string
}

func (archiveWorkflowTool) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: "fixture.archive.workflow", Description: "Archive Workflow fixture", Version: "1", Risk: tool.RiskLow, Idempotent: true,
		InputSchema:  json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"},"references":{"type":"object"},"inputPaths":{"type":"array","items":{"type":"string"}}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"},"references":{"type":"object"}}}`),
		Permissions:  []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}},
	}, nil
}

func (value archiveWorkflowTool) Invoke(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
	var arguments struct {
		Query      string          `json:"query"`
		References json.RawMessage `json:"references"`
	}
	_ = json.Unmarshal(invocation.Arguments, &arguments)
	structured, _ := json.Marshal(map[string]any{"answer": "result:" + arguments.Query, "references": arguments.References})
	result := tool.Result{Status: tool.ResultSuccess, Text: "completed", Structured: structured, Artifacts: []tool.ArtifactRef{}, Citations: []tool.CitationRef{}}
	if value.reportPath != "" {
		result.Artifacts = append(result.Artifacts, tool.ArtifactRef{ID: value.artifactID, Name: "Workflow report", MIMEType: "text/plain", WorkspacePath: value.reportPath})
	}
	return result, nil
}

func TestProjectArchiveRestoresVersionedWorkflow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, filepath.Join(root, "data", "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "data", "workspaces"), filepath.Join(root, "backups", "trash"))
	created, err := projects.Create(ctx, "Workflow archive", "")
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	if err := registry.Register(ctx, archiveWorkflowTool{}); err != nil {
		t.Fatal(err)
	}
	workflows, err := workflow.NewService(NewWorkflowRepository(store.DB()), projects, workflow.NewCompiler(registry))
	if err != nil {
		t.Fatal(err)
	}
	definition := workflow.Definition{
		SchemaVersion: 1, Name: "Archived review", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}},
		Nodes:   []workflow.Node{{ID: "review", Name: "Review", Kind: workflow.NodeTool, ToolName: "fixture.archive.workflow", Arguments: json.RawMessage(`{}`)}},
		Edges:   []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "review", ToPort: "query"}},
		Outputs: []workflow.Output{{Name: "answer", Type: workflow.TypeString, FromNode: "review", FromPort: "structured.answer", Required: true}},
	}
	first, err := workflows.Save(ctx, workflow.SaveCommand{ProjectID: created.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	definition.Description = "Second immutable version"
	second, err := workflows.Save(ctx, workflow.SaveCommand{ProjectID: created.ID, WorkflowID: first.Workflow.ID, ExpectedCurrentVersionID: first.Version.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	archiveService := newProjectArchiveTestService(t, store, projects, root)
	archivePath := filepath.Join(root, "workflow.sciaide-project")
	exported, err := archiveService.Export(ctx, created.ID, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Manifest.Stats.Workflows != 1 {
		t.Fatalf("Workflow archive stats = %#v", exported.Manifest.Stats)
	}
	restored, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: archivePath})
	if err != nil {
		t.Fatal(err)
	}
	values, err := workflows.List(ctx, restored.Project.ID)
	if err != nil || len(values) != 1 || values[0].ID == first.Workflow.ID || values[0].CurrentVersionID == second.Version.ID {
		t.Fatalf("restored Workflows = %#v, %v", values, err)
	}
	detail, err := workflows.Get(ctx, restored.Project.ID, values[0].ID)
	if err != nil || len(detail.Versions) != 2 || detail.Versions[0].Version != 2 || detail.Versions[0].Definition.Description != definition.Description || detail.Workflow.CurrentVersionID != detail.Versions[0].ID {
		t.Fatalf("restored Workflow detail = %#v, %v", detail, err)
	}
	if detail.Versions[0].DefinitionSHA256 != second.Version.DefinitionSHA256 || detail.Versions[0].CompilationSHA256 != second.Version.CompilationSHA256 {
		t.Fatalf("Workflow immutable hashes changed: before=%#v after=%#v", second.Version, detail.Versions[0])
	}
}

func TestProjectArchiveRestoresWorkflowRuntimeGraphAndInterruptsPausedRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, filepath.Join(root, "data", "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "data", "workspaces"), filepath.Join(root, "backups", "trash"))
	created, err := projects.Create(ctx, "Workflow runtime archive", "")
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(created.WorkspacePath, "input.txt")
	datasetPath := filepath.Join(created.WorkspacePath, "dataset.csv")
	reportPath := filepath.Join(created.WorkspacePath, "workflow-report.txt")
	if err := os.WriteFile(inputPath, []byte("input evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	datasetBytes := []byte("group,value\nA,1\nB,2\n")
	if err := os.WriteFile(datasetPath, datasetBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, []byte("workflow result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactService := artifact.NewService(NewArtifactRepository(store.DB()), projects)
	inputArtifact, err := artifactService.RegisterWorkspaceFile(ctx, artifact.RegisterWorkspaceCommand{ProjectID: created.ID, Path: inputPath, Name: "Input artifact"})
	if err != nil {
		t.Fatal(err)
	}

	registry := tool.NewRegistry()
	if err := registry.Register(ctx, archiveWorkflowTool{reportPath: "workflow-report.txt", artifactID: inputArtifact.Artifact.ID}); err != nil {
		t.Fatal(err)
	}
	definitionRepo := NewWorkflowRepository(store.DB())
	workflows, err := workflow.NewService(definitionRepo, projects, workflow.NewCompiler(registry))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := workflows.Save(ctx, workflow.SaveCommand{ProjectID: created.ID, Definition: workflow.Definition{
		SchemaVersion: 1,
		Name:          "Runtime graph",
		Inputs: []workflow.Port{
			{Name: "topic", Type: workflow.TypeString, Required: true},
			{Name: "references", Type: workflow.TypeObject, Required: true},
			{Name: "input_paths", Type: workflow.TypeArray, FileKind: "delimited", MinItems: 1, MaxItems: 1, Required: true},
		},
		Nodes: []workflow.Node{
			{ID: "confirm", Name: "Confirm evidence", Kind: workflow.NodeHumanConfirmation, Prompt: "Use selected evidence?", Arguments: json.RawMessage(`{}`)},
			{ID: "report", Name: "Build report", Kind: workflow.NodeTool, ToolName: "fixture.archive.workflow", Arguments: json.RawMessage(`{}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "$input", FromPort: "references", ToNode: "confirm", ToPort: "context"},
			{FromNode: "$input", FromPort: "topic", ToNode: "report", ToPort: "query"},
			{FromNode: "$input", FromPort: "references", ToNode: "report", ToPort: "references"},
			{FromNode: "$input", FromPort: "input_paths", ToNode: "report", ToPort: "inputPaths"},
		},
		Outputs: []workflow.Output{
			{Name: "answer", Type: workflow.TypeString, FromNode: "report", FromPort: "structured.answer", Required: true},
			{Name: "references", Type: workflow.TypeObject, FromNode: "report", FromPort: "structured.references", Required: true},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	toolService := tool.NewService(NewToolRepository(store.DB()), tool.JSONSchemaValidator{})
	permissionEngine := permission.NewEngine(NewPermissionRepository(store.DB()))
	runtimeRepo := NewWorkflowRuntimeRepository(store.DB())
	executor := tool.NewExecutor(registry, toolService, tool.CompositeProjectResolver{Runs: NewRunRepository(store.DB()), Workflows: runtimeRepo}, tool.ExecutorOptions{})
	if err := executor.SetArtifactRegistrar(artifactService); err != nil {
		t.Fatal(err)
	}
	runtimeService, err := workflow.NewRuntimeService(runtimeRepo, definitionRepo, projects, registry, toolService, permissionEngine, executor)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeService.Close()
	inputs := json.RawMessage(fmt.Sprintf(`{"topic":"archive","references":{"projectId":%q,"artifactId":%q},"input_paths":["dataset.csv"]}`, created.ID, inputArtifact.Artifact.ID))
	started, err := runtimeService.Start(ctx, workflow.StartCommand{ProjectID: created.ID, WorkflowID: saved.Workflow.ID, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitWorkflowStatus(t, runtimeService, created.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	completed, err = runtimeService.Decide(ctx, workflow.HumanDecisionCommand{
		ProjectID: created.ID, RunID: completed.Run.ID, StepID: completed.Steps[0].ID, Approved: true, Note: "selected",
		Context: json.RawMessage(fmt.Sprintf(`{"projectId":%q,"artifactId":%q}`, created.ID, inputArtifact.Artifact.ID)),
	})
	if err != nil {
		t.Fatal(err)
	}
	completed = waitWorkflowStatus(t, runtimeService, created.ID, completed.Run.ID, workflow.RunWaitingApproval)
	for completed.Run.Status == workflow.RunWaitingApproval {
		if len(completed.PendingApprovals) != 1 {
			t.Fatalf("pending Workflow approval = %#v", completed.PendingApprovals)
		}
		completed, err = runtimeService.ResolveApproval(ctx, permission.ResolveCommand{ApprovalID: completed.PendingApprovals[0].ID, Allow: true, Scope: permission.ScopeCall})
		if err != nil {
			t.Fatal(err)
		}
		completed = waitWorkflowStatus(t, runtimeService, created.ID, completed.Run.ID, workflow.RunWaitingApproval, workflow.RunCompleted)
	}
	if completed.Run.Status != workflow.RunCompleted || len(completed.Steps) != 2 || completed.Steps[1].ToolCallID == "" {
		t.Fatalf("completed Workflow before archive = %#v", completed)
	}
	// Restored archives must never inherit an unattended execution policy from
	// another machine. Make the source explicitly unsafe so the assertion below
	// proves that restore performs the downgrade instead of passing by default.
	if _, err := store.DB().ExecContext(ctx, `UPDATE workflow_runs SET permission_mode='full_access' WHERE id=?`, completed.Run.ID); err != nil {
		t.Fatal(err)
	}
	if source, err := runtimeRepo.GetRun(ctx, created.ID, completed.Run.ID); err != nil || source.Run.PermissionMode != conversation.PermissionFullAccess {
		t.Fatalf("source Workflow permission mode = %q, %v", source.Run.PermissionMode, err)
	}
	oldRunID, oldConversationID, oldHumanStepID, oldToolStepID, oldCallID := completed.Run.ID, completed.Run.ConversationID, completed.Steps[0].ID, completed.Steps[1].ID, completed.Steps[1].ToolCallID

	pausedStart, err := runtimeService.Start(ctx, workflow.StartCommand{ProjectID: created.ID, WorkflowID: saved.Workflow.ID, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	paused := waitWorkflowStatus(t, runtimeService, created.ID, pausedStart.Run.ID, workflow.RunWaitingHumanConfirmation)
	paused, err = runtimeService.Pause(ctx, created.ID, paused.Run.ID)
	if err != nil || paused.Run.Status != workflow.RunPaused {
		t.Fatalf("pause Workflow before archive = %#v, %v", paused, err)
	}
	oldPausedRunID, oldPausedConversationID := paused.Run.ID, paused.Run.ConversationID

	archiveService := newProjectArchiveTestService(t, store, projects, root)
	archivePath := filepath.Join(root, "workflow-runtime.sciaide-project")
	exported, err := archiveService.Export(ctx, created.ID, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Manifest.Stats.Workflows != 1 || exported.Manifest.Stats.WorkflowRuns != 2 || exported.Manifest.Stats.Artifacts != 2 {
		t.Fatalf("Workflow Runtime archive stats = %#v", exported.Manifest.Stats)
	}
	workflowInputEntries := 0
	for _, entry := range exported.Manifest.Files {
		if entry.Kind == projectarchive.FileWorkflowInput {
			workflowInputEntries++
		}
	}
	if workflowInputEntries != 1 {
		t.Fatalf("Workflow frozen input entries = %d, manifest=%#v", workflowInputEntries, exported.Manifest.Files)
	}
	var workflowInputEntry projectarchive.FileEntry
	for _, entry := range exported.Manifest.Files {
		if entry.Kind == projectarchive.FileWorkflowInput {
			workflowInputEntry = entry
			break
		}
	}
	projectCountBeforeInvalidRestore, err := projects.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	missingInputArchive := filepath.Join(root, "workflow-runtime-missing-input.sciaide-project")
	rewriteArchiveEntries(t, archivePath, missingInputArchive, func(entries map[string][]byte) {
		delete(entries, workflowInputEntry.Path)
	})
	if _, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: missingInputArchive}); err == nil || !strings.Contains(err.Error(), "declared file") {
		t.Fatalf("missing Workflow input restore error = %v", err)
	}
	tamperedInputArchive := filepath.Join(root, "workflow-runtime-tampered-input.sciaide-project")
	rewriteArchiveEntries(t, archivePath, tamperedInputArchive, func(entries map[string][]byte) {
		contents := append([]byte(nil), entries[workflowInputEntry.Path]...)
		if len(contents) == 0 {
			t.Fatal("Workflow input archive entry is empty")
		}
		contents[0] ^= 0xff
		entries[workflowInputEntry.Path] = contents
	})
	if _, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: tamperedInputArchive}); err == nil || !strings.Contains(err.Error(), "SHA256 validation") {
		t.Fatalf("tampered Workflow input restore error = %v", err)
	}
	projectCountAfterInvalidRestore, err := projects.List(ctx)
	if err != nil || len(projectCountAfterInvalidRestore) != len(projectCountBeforeInvalidRestore) {
		t.Fatalf("invalid Workflow input restore published a project: before=%d after=%d err=%v", len(projectCountBeforeInvalidRestore), len(projectCountAfterInvalidRestore), err)
	}
	restored, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: archivePath})
	if err != nil {
		t.Fatal(err)
	}
	restoredWorkflows, err := workflows.List(ctx, restored.Project.ID)
	if err != nil || len(restoredWorkflows) != 1 {
		t.Fatalf("restored Workflow list = %#v, %v", restoredWorkflows, err)
	}
	restoredRuns, err := runtimeRepo.ListRuns(ctx, restored.Project.ID, restoredWorkflows[0].ID, 10)
	if err != nil || len(restoredRuns) != 2 {
		t.Fatalf("restored Workflow Runs = %#v, %v", restoredRuns, err)
	}
	var restoredCompleted, restoredInterrupted workflow.RunDetail
	for _, restoredRun := range restoredRuns {
		detail, loadErr := runtimeRepo.GetRun(ctx, restored.Project.ID, restoredRun.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		switch detail.Run.Status {
		case workflow.RunCompleted:
			restoredCompleted = detail
		case workflow.RunInterrupted:
			restoredInterrupted = detail
		}
	}
	if restoredCompleted.Run.ID == "" || restoredCompleted.Run.ID == oldRunID || restoredInterrupted.Run.ID == "" || restoredInterrupted.Run.ID == oldPausedRunID {
		t.Fatalf("restored Workflow identities/statuses = completed:%#v interrupted:%#v", restoredCompleted.Run, restoredInterrupted.Run)
	}
	if restoredCompleted.Run.ConversationID == "" || restoredCompleted.Run.ConversationID == oldConversationID || restoredInterrupted.Run.ConversationID == "" || restoredInterrupted.Run.ConversationID == oldPausedConversationID {
		t.Fatalf("restored research conversation identities = completed:%q interrupted:%q", restoredCompleted.Run.ConversationID, restoredInterrupted.Run.ConversationID)
	}
	bound, exists, err := runtimeRepo.GetRunByConversation(ctx, restoredCompleted.Run.ConversationID)
	if err != nil || !exists || bound.Run.ID != restoredCompleted.Run.ID || bound.Run.ProjectID != restored.Project.ID {
		t.Fatalf("restored research conversation binding = %#v, %v, %v", bound.Run, exists, err)
	}
	if restoredInterrupted.Run.ErrorCode != "ARCHIVE_RESTORED" || len(restoredInterrupted.Steps) != 2 || restoredInterrupted.Steps[0].Status != workflow.StepInterrupted {
		t.Fatalf("paused Workflow was not safely interrupted = %#v", restoredInterrupted)
	}
	recoverable, err := runtimeRepo.RecoverableRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range recoverable {
		if runID == restoredInterrupted.Run.ID {
			t.Fatal("restored paused Workflow remained automatically recoverable")
		}
	}
	restoredArtifacts, err := artifactService.List(ctx, restored.Project.ID, true)
	if err != nil || len(restoredArtifacts) != 2 {
		t.Fatalf("restored Workflow Artifacts = %#v, %v", restoredArtifacts, err)
	}
	restoredInputArtifactID := ""
	for _, value := range restoredArtifacts {
		if value.Name == "Input artifact" {
			restoredInputArtifactID = value.ID
		}
	}
	if restoredInputArtifactID == "" || restoredInputArtifactID == inputArtifact.Artifact.ID {
		t.Fatalf("restored input Artifact ID = %q", restoredInputArtifactID)
	}
	assertWorkflowArchivePayload(t, restoredCompleted.Run.Inputs, restored.Project.ID, restoredInputArtifactID, oldRunID, oldCallID, oldHumanStepID, oldToolStepID)
	if restoredCompleted.Run.InputsSHA256 != workflowPayloadSHA256(restoredCompleted.Run.Inputs) {
		t.Fatalf("restored Workflow input hash = %q", restoredCompleted.Run.InputsSHA256)
	}
	if restoredCompleted.Run.PermissionMode != "plan" {
		t.Fatalf("restored Workflow permission mode = %q", restoredCompleted.Run.PermissionMode)
	}
	var restoredInputs struct {
		InputPaths []string `json:"input_paths"`
	}
	if err := json.Unmarshal(restoredCompleted.Run.Inputs, &restoredInputs); err != nil || len(restoredInputs.InputPaths) != 1 {
		t.Fatalf("restored Workflow frozen inputs = %#v, %v", restoredInputs, err)
	}
	restoredDataset, err := os.ReadFile(filepath.Join(restored.Project.WorkspacePath, filepath.FromSlash(restoredInputs.InputPaths[0])))
	if err != nil || !bytes.Equal(restoredDataset, datasetBytes) {
		t.Fatalf("restored Workflow input bytes = %q, %v", restoredDataset, err)
	}
	if expected, ok := workflow.FrozenInputSHA256(restoredInputs.InputPaths[0]); !ok || expected != workflowPayloadSHA256(restoredDataset) {
		t.Fatalf("restored Workflow input digest path = %q, %v", expected, ok)
	}
	if len(restoredCompleted.Steps) != 2 || restoredCompleted.Steps[0].ID == oldHumanStepID || restoredCompleted.Steps[1].ID == oldToolStepID || restoredCompleted.Steps[1].ToolCallID == oldCallID {
		t.Fatalf("restored Workflow step identities = %#v", restoredCompleted.Steps)
	}
	assertWorkflowArchivePayload(t, restoredCompleted.Steps[1].Input, restored.Project.ID, restoredInputArtifactID, oldRunID, oldCallID, oldHumanStepID, oldToolStepID)
	assertWorkflowArchivePayload(t, restoredCompleted.Steps[1].Output, restored.Project.ID, restoredInputArtifactID, oldRunID, oldCallID, oldHumanStepID, oldToolStepID)
	if restoredCompleted.Steps[1].InputSHA256 != workflowPayloadSHA256(restoredCompleted.Steps[1].Input) {
		t.Fatalf("restored Workflow step input hash = %q", restoredCompleted.Steps[1].InputSHA256)
	}
	var decisionContext string
	if err := store.DB().QueryRowContext(ctx, `SELECT context_json FROM workflow_human_decisions WHERE workflow_run_id=?`, restoredCompleted.Run.ID).Scan(&decisionContext); err != nil {
		t.Fatal(err)
	}
	assertWorkflowArchivePayload(t, []byte(decisionContext), restored.Project.ID, restoredInputArtifactID, oldRunID, oldCallID, oldHumanStepID, oldToolStepID)
	var callArguments, structured string
	if err := store.DB().QueryRowContext(ctx, `SELECT tc.arguments_json,tr.structured_json FROM tool_calls tc JOIN tool_results tr ON tr.tool_call_id=tc.id WHERE tc.id=? AND tc.workflow_run_id=?`, restoredCompleted.Steps[1].ToolCallID, restoredCompleted.Run.ID).Scan(&callArguments, &structured); err != nil {
		t.Fatal(err)
	}
	assertWorkflowArchivePayload(t, []byte(callArguments), restored.Project.ID, restoredInputArtifactID, oldRunID, oldCallID, oldHumanStepID, oldToolStepID)
	assertWorkflowArchivePayload(t, []byte(structured), restored.Project.ID, restoredInputArtifactID, oldRunID, oldCallID, oldHumanStepID, oldToolStepID)
	var lineageRunID, lineageToolCallID, provenanceJSON string
	if err := store.DB().QueryRowContext(ctx, `
		SELECT COALESCE(l.source_workflow_run_id,''),COALESCE(l.source_tool_call_id,''),v.provenance_json
		FROM artifact_versions v
		JOIN artifact_lineage l ON l.artifact_version_id=v.id AND l.relation_kind='workflow_run'
		JOIN artifacts a ON a.id=v.artifact_id
		WHERE a.project_id=? AND a.name='Workflow report'`, restored.Project.ID).Scan(&lineageRunID, &lineageToolCallID, &provenanceJSON); err != nil {
		t.Fatal(err)
	}
	if lineageRunID != restoredCompleted.Run.ID || lineageToolCallID != "" {
		t.Fatalf("restored Workflow Artifact lineage = run:%q tool:%q", lineageRunID, lineageToolCallID)
	}
	var provenance artifact.Provenance
	if err := json.Unmarshal([]byte(provenanceJSON), &provenance); err != nil || provenance.ProjectID != restored.Project.ID || provenance.WorkflowRunID != restoredCompleted.Run.ID || provenance.ToolCallID != restoredCompleted.Steps[1].ToolCallID {
		t.Fatalf("restored Workflow Artifact provenance = %#v, %v", provenance, err)
	}
	var foreignKeys int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("foreign key check after Workflow restore = %d, %v", foreignKeys, err)
	}
}

func assertWorkflowArchivePayload(t *testing.T, raw []byte, projectID, artifactID string, forbidden ...string) {
	t.Helper()
	if !json.Valid(raw) || !bytes.Contains(raw, []byte(projectID)) || !bytes.Contains(raw, []byte(artifactID)) {
		t.Fatalf("restored Workflow payload did not contain remapped identities: %s", raw)
	}
	for _, value := range forbidden {
		if value != "" && bytes.Contains(raw, []byte(value)) {
			t.Fatalf("restored Workflow payload retained old identity %q: %s", value, raw)
		}
	}
}

func TestProjectArchiveRestoresMatchingSkillBindingWithoutPackagingSkill(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "data", "sciaide.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "data", "workspaces"), filepath.Join(root, "backups", "trash"))
	created, err := projects.Create(ctx, "Skill project", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := skill.Manifest{SchemaVersion: skill.CurrentSchemaVersion, ID: "literature-reading", Name: "Literature reading", Version: "1.0.0", Description: "Review papers", Entry: "SKILL.md", Activation: skill.Activation{Mode: skill.ActivationExplicit}, Compatibility: skill.Compatibility{SciAide: ">=0.4.0 <1.0.0"}, Context: skill.ContextPolicy{MaxTokens: 1000}}
	manifestJSON, _ := json.Marshal(manifest)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO installed_skills(skill_id,skill_version,manifest_json,package_rel_path,manifest_hash,content_hash,package_hash,integrity_status,installed_at,updated_at) VALUES (?,?,?,?,?,?,?,'valid',?,?)`, []any{manifest.ID, manifest.Version, string(manifestJSON), manifest.ID + "/" + manifest.Version, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), formatTime(now), formatTime(now)}},
		{`INSERT INTO project_skills(project_id,skill_id,skill_version,enabled,priority,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, []any{created.ID, manifest.ID, manifest.Version, true, 12, formatTime(now), formatTime(now)}},
	} {
		if _, err := store.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	archiveService := newProjectArchiveTestService(t, store, projects, root)
	archivePath := filepath.Join(root, "skill.sciaide-project")
	exported, err := archiveService.Export(ctx, created.ID, archivePath)
	if err != nil || len(exported.Manifest.SkillBindings) != 1 {
		t.Fatalf("Skill binding manifest = %#v, %v", exported.Manifest.SkillBindings, err)
	}
	restored, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: archivePath})
	if err != nil || restored.RestoredSkillBindings != 1 || len(restored.MissingSkillBindings) != 0 {
		t.Fatalf("Skill restore report = %#v, %v", restored, err)
	}
	var enabled bool
	var priority int
	if err := store.DB().QueryRowContext(ctx, `SELECT enabled,priority FROM project_skills WHERE project_id=? AND skill_id=?`, restored.Project.ID, manifest.ID).Scan(&enabled, &priority); err != nil || !enabled || priority != 12 {
		t.Fatalf("restored Skill binding = enabled:%v priority:%d err:%v", enabled, priority, err)
	}
	assertArchiveOmits(t, archivePath, string(manifestJSON))

	tamperedPath := filepath.Join(root, "skill-tampered.sciaide-project")
	rewriteArchiveManifest(t, archivePath, tamperedPath, func(value *projectarchive.Manifest) {
		value.SkillBindings[0].ContentHash = strings.Repeat("d", 64)
	})
	if _, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: tamperedPath}); err == nil || !strings.Contains(err.Error(), "manifest does not match") {
		t.Fatalf("tampered Skill binding restore error = %v", err)
	}
	values, err := projects.List(ctx)
	if err != nil || len(values) != 2 {
		t.Fatalf("tampered restore published a project: projects=%#v err=%v", values, err)
	}
}

func TestProjectArchiveRemapsSchema2RunSkillContextAndRecomputesHash(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "data", "sciaide.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "data", "workspaces"), filepath.Join(root, "backups", "trash"))
	created, err := projects.Create(ctx, "Skill context archive", "")
	if err != nil {
		t.Fatal(err)
	}
	conversationService := conversation.NewService(NewConversationRepository(store.DB()))
	conversationValue, err := conversationService.Create(ctx, created.ID, "Context history")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	profile := modelprofile.Profile{ID: "skill-context-profile", Name: "Historical model", ProviderType: modelprofile.ProviderOpenAICompatible, APIProtocol: modelcap.ProtocolOpenAIChat, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "fixture-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "schema2-run", ConversationID: conversationValue.ID, UserMessageID: "schema2-user", AssistantMessageID: "schema2-assistant", ModelProfileID: profile.ID, ModelID: profile.ModelID, APIProtocol: profile.APIProtocol, PermissionMode: conversation.PermissionPlan, Status: chat.RunRunning, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	userMessage := conversation.Message{ID: run.UserMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "schema2-user-part", MessageID: run.UserMessageID, Type: "text", Text: "$evidence-review", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	assistantMessage := conversation.Message{ID: run.AssistantMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{ID: "schema2-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	if err := NewRunRepository(store.DB()).CreateWithMessages(ctx, run, userMessage, assistantMessage); err != nil {
		t.Fatal(err)
	}
	instructions := "Review every claim against a local evidence chunk."
	contentHash := sha256.Sum256([]byte(instructions))
	manifest := skill.Manifest{SchemaVersion: skill.CurrentSchemaVersion, ID: "evidence-review", Name: "Evidence review", Version: "1.0.0", Description: "Review local evidence", Entry: "SKILL.md", Activation: skill.Activation{Mode: skill.ActivationExplicit}, Compatibility: skill.Compatibility{SciAide: ">=0.4.0 <1.0.0"}, Context: skill.ContextPolicy{MaxTokens: 1000}}
	runContext := skill.RunContext{
		SchemaVersion: skill.RunContextSchemaVersion, RunID: run.ID, ProjectID: created.ID,
		ContextWindowTokens: 200_000, CatalogBudgetTokens: 4_000, InstructionBudgetTokens: 40_000,
		Catalog:     []skill.RunCatalogSkill{{SkillID: manifest.ID, Version: manifest.Version, Name: manifest.Name, Description: manifest.Description, Activation: manifest.Activation.Mode, Priority: 10}},
		CatalogText: "- $evidence-review [Evidence review@1.0.0; activation=explicit; priority=10] Review local evidence", SelectionNotices: []skill.RunSkillNotice{},
		Decisions: []skill.RunSkillDecision{{SkillID: manifest.ID, Version: manifest.Version, Status: skill.DecisionSelected, Reason: skill.SelectionExplicit, Ordinal: 0}},
		Skills:    []skill.RunSkill{{Manifest: manifest, Priority: 10, Reason: skill.SelectionExplicit, PackagePath: "evidence-review/1.0.0", ManifestHash: strings.Repeat("a", 64), ContentHash: hex.EncodeToString(contentHash[:]), PackageHash: strings.Repeat("c", 64), Instructions: instructions}}, CreatedAt: now,
	}
	encodedBefore, hashBefore, err := skill.EncodeRunContext(runContext)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the archived snapshot through the storage contract directly. The
	// active runtime never creates schema-1/2 snapshots; this fixture represents
	// data imported from an older database before exercising archive remapping.
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO run_skill_contexts(run_id,project_id,schema_version,snapshot_json,snapshot_hash,created_at) VALUES (?,?,?,?,?,?)`, runContext.RunID, runContext.ProjectID, runContext.SchemaVersion, string(encodedBefore), hashBefore, formatTime(runContext.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	for ordinal, selected := range runContext.Skills {
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO run_skills(run_id,ordinal,skill_id,skill_version,content_hash,package_hash,created_at) VALUES (?,?,?,?,?,?,?)`, runContext.RunID, ordinal, selected.Manifest.ID, selected.Manifest.Version, selected.ContentHash, selected.PackageHash, formatTime(runContext.CreatedAt)); err != nil {
			t.Fatal(err)
		}
	}
	completedAt := now.Add(time.Second)
	run.Status, run.FinishReason, run.CompletedAt, run.UpdatedAt = chat.RunCompleted, "stop", &completedAt, completedAt
	if err := NewRunRepository(store.DB()).Complete(ctx, run, "Evidence review completed.", nil); err != nil {
		t.Fatal(err)
	}
	archiveService := newProjectArchiveTestService(t, store, projects, root)
	archivePath := filepath.Join(root, "schema2.sciaide-project")
	if _, err := archiveService.Export(ctx, created.ID, archivePath); err != nil {
		t.Fatal(err)
	}
	restored, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: archivePath})
	if err != nil {
		t.Fatal(err)
	}
	var restoredRunID string
	if err := store.DB().QueryRowContext(ctx, `SELECT id FROM runs WHERE conversation_id IN (SELECT id FROM conversations WHERE project_id=?)`, restored.Project.ID).Scan(&restoredRunID); err != nil {
		t.Fatal(err)
	}
	if restoredRunID == run.ID {
		t.Fatal("restored Run kept the source identity")
	}
	decoded, err := NewSkillRepository(store.DB()).GetRunContext(ctx, restoredRunID)
	if err != nil {
		t.Fatal(err)
	}
	encodedAfter, hashAfter, err := skill.EncodeRunContext(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != skill.RunContextSchemaVersion || decoded.RunID != restoredRunID || decoded.ProjectID != restored.Project.ID || decoded.Skills[0].ContentHash != runContext.Skills[0].ContentHash || decoded.Decisions[0].Status != skill.DecisionSelected {
		t.Fatalf("restored Run Skill context = %#v", decoded)
	}
	if bytes.Equal(encodedBefore, encodedAfter) || hashBefore == hashAfter || decoded.SnapshotHash != hashAfter {
		t.Fatalf("Run Skill context integrity was not refreshed: before=%s after=%s stored=%s", hashBefore, hashAfter, decoded.SnapshotHash)
	}
}

func TestProjectArchiveRestoresDynamicRunSkillSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "data", "sciaide.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "data", "workspaces"), filepath.Join(root, "backups", "trash"))
	created, err := projects.Create(ctx, "Dynamic Skill archive", "")
	if err != nil {
		t.Fatal(err)
	}
	conversationValue, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, created.ID, "Dynamic Skill history")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	profile := modelprofile.Profile{ID: "dynamic-archive-profile", Name: "Historical model", ProviderType: modelprofile.ProviderOpenAICompatible, APIProtocol: modelcap.ProtocolOpenAIChat, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "fixture-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "dynamic-archive-run", ConversationID: conversationValue.ID, UserMessageID: "dynamic-archive-user", AssistantMessageID: "dynamic-archive-assistant", ModelProfileID: profile.ID, ModelID: profile.ModelID, APIProtocol: profile.APIProtocol, PermissionMode: conversation.PermissionPlan, Status: chat.RunRunning, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	userMessage := conversation.Message{ID: run.UserMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "dynamic-archive-user-part", MessageID: run.UserMessageID, Type: "text", Text: "Use the scientific-writing skill: revise the manuscript", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	assistantMessage := conversation.Message{ID: run.AssistantMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{ID: "dynamic-archive-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	runs := NewRunRepository(store.DB())
	if err := runs.CreateWithMessages(ctx, run, userMessage, assistantMessage); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(time.Second)
	call := tool.Call{ID: "dynamic-archive-call", RunID: run.ID, ProviderCallID: "provider-dynamic-archive", ToolName: "builtin.skill.load", ToolVersion: "1", Arguments: json.RawMessage(`{"name":"scientific-writing"}`), Status: tool.CallCompleted, Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{}, Idempotent: true, CreatedAt: now, StartedAt: &now, CompletedAt: &completedAt, UpdatedAt: completedAt}
	if err := NewToolRepository(store.DB()).Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	const instructions = "Immutable dynamic Skill instructions used by this Run."
	contentHash, packageHash := strings.Repeat("d", 64), strings.Repeat("e", 64)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO run_dynamic_skills(run_id,project_id,ordinal,tool_call_id,skill_name,origin,category,content_hash,package_hash,instruction_snapshot,loaded_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, created.ID, 0, call.ID, "scientific-writing", "default", "writing", contentHash, packageHash, instructions, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO process_execution_audits(tool_call_id,run_id,project_id,tool_name,executable_path,executable_version,executable_sha256,script_path,script_sha256,command_sha256,workdir,timeout_millis,environment_names_json,process_id,state,termination_reason,exit_code,stdout_bytes,stdout_sha256,stdout_truncated,stderr_bytes,stderr_sha256,stderr_truncated,error_message,prepared_at,started_at,completed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		call.ID, run.ID, created.ID, "builtin.python.execute", `C:\Python\python.exe`, "3.13.1.0", strings.Repeat("a", 64), "analysis.py", strings.Repeat("b", 64), "", ".", 30_000, `["PATH","TEMP"]`, 4242, "completed", "completed", 0, 3, strings.Repeat("c", 64), 0, 0, strings.Repeat("d", 64), 0, "", formatTime(now), formatTime(now), formatTime(completedAt)); err != nil {
		t.Fatal(err)
	}
	const routingPrompt = "Immutable routing candidates shown to the model for this Run."
	routingDigest := sha256.Sum256([]byte(routingPrompt))
	routingHash := hex.EncodeToString(routingDigest[:])
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO run_skill_routing(run_id,project_id,prompt_snapshot,prompt_hash,created_at) VALUES (?,?,?,?,?)`,
		run.ID, created.ID, routingPrompt, routingHash, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	run.Status, run.FinishReason, run.CompletedAt, run.UpdatedAt = chat.RunCompleted, "stop", &completedAt, completedAt
	if err := runs.Complete(ctx, run, "Revised manuscript.", nil); err != nil {
		t.Fatal(err)
	}

	archiveService := newProjectArchiveTestService(t, store, projects, root)
	archivePath := filepath.Join(root, "dynamic-skill.sciaide-project")
	if _, err := archiveService.Export(ctx, created.ID, archivePath); err != nil {
		t.Fatal(err)
	}
	restored, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: archivePath})
	if err != nil {
		t.Fatal(err)
	}
	var restoredRunID, restoredProjectID, restoredCallID, name, origin, category, restoredContentHash, restoredPackageHash, restoredInstructions string
	if err := store.DB().QueryRowContext(ctx, `SELECT ds.run_id,ds.project_id,ds.tool_call_id,ds.skill_name,ds.origin,ds.category,ds.content_hash,ds.package_hash,ds.instruction_snapshot
		FROM run_dynamic_skills ds JOIN runs r ON r.id=ds.run_id JOIN conversations c ON c.id=r.conversation_id WHERE c.project_id=?`, restored.Project.ID).
		Scan(&restoredRunID, &restoredProjectID, &restoredCallID, &name, &origin, &category, &restoredContentHash, &restoredPackageHash, &restoredInstructions); err != nil {
		t.Fatal(err)
	}
	if restoredRunID == run.ID || restoredCallID == call.ID || restoredProjectID != restored.Project.ID {
		t.Fatalf("dynamic Skill identities were not remapped: run=%q project=%q call=%q", restoredRunID, restoredProjectID, restoredCallID)
	}
	if name != "scientific-writing" || origin != "default" || category != "writing" || restoredContentHash != contentHash || restoredPackageHash != packageHash || restoredInstructions != instructions {
		t.Fatalf("restored dynamic Skill snapshot changed: name=%q origin=%q category=%q content=%q package=%q instructions=%q", name, origin, category, restoredContentHash, restoredPackageHash, restoredInstructions)
	}
	var routingRunID, routingProjectID, restoredRoutingPrompt, restoredRoutingHash string
	if err := store.DB().QueryRowContext(ctx, `SELECT routing.run_id,routing.project_id,routing.prompt_snapshot,routing.prompt_hash
		FROM run_skill_routing routing JOIN runs r ON r.id=routing.run_id JOIN conversations c ON c.id=r.conversation_id WHERE c.project_id=?`, restored.Project.ID).
		Scan(&routingRunID, &routingProjectID, &restoredRoutingPrompt, &restoredRoutingHash); err != nil {
		t.Fatal(err)
	}
	if routingRunID != restoredRunID || routingProjectID != restored.Project.ID || restoredRoutingPrompt != routingPrompt || restoredRoutingHash != routingHash {
		t.Fatalf("restored routing snapshot changed: run=%q project=%q prompt=%q hash=%q", routingRunID, routingProjectID, restoredRoutingPrompt, restoredRoutingHash)
	}
	var toolRunID string
	if err := store.DB().QueryRowContext(ctx, `SELECT run_id FROM tool_calls WHERE id=?`, restoredCallID).Scan(&toolRunID); err != nil || toolRunID != restoredRunID {
		t.Fatalf("restored dynamic Skill ToolCall relation = %q, %v", toolRunID, err)
	}
	var auditCallID, auditRunID, auditProjectID string
	if err := store.DB().QueryRowContext(ctx, `SELECT tool_call_id,run_id,project_id FROM process_execution_audits WHERE project_id=?`, restored.Project.ID).Scan(&auditCallID, &auditRunID, &auditProjectID); err != nil {
		t.Fatal(err)
	}
	if auditCallID != restoredCallID || auditRunID != restoredRunID || auditProjectID != restored.Project.ID {
		t.Fatalf("restored process audit identities call=%q run=%q project=%q", auditCallID, auditRunID, auditProjectID)
	}
}

func TestProjectArchiveRejectsTamperedMigrationHistoryAndViewSubstitution(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "data", "sciaide.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "data", "workspaces"), filepath.Join(root, "backups", "trash"))
	created, err := projects.Create(ctx, "Untrusted archive", "")
	if err != nil {
		t.Fatal(err)
	}
	archiveService := newProjectArchiveTestService(t, store, projects, root)
	source := filepath.Join(root, "source.sciaide-project")
	if _, err := archiveService.Export(ctx, created.ID, source); err != nil {
		t.Fatal(err)
	}

	currentSchema, err := CurrentSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*sql.DB) error
		want   string
	}{
		{name: "migration checksum", mutate: func(db *sql.DB) error {
			_, err := db.Exec(`UPDATE schema_migrations SET checksum=? WHERE version=49`, strings.Repeat("0", 64))
			return err
		}, want: "migration 49"},
		{name: "view substitution", mutate: func(db *sql.DB) error {
			if _, err := db.Exec(`DROP TABLE mcp_servers`); err != nil {
				return err
			}
			_, err := db.Exec(`CREATE VIEW mcp_servers AS SELECT '' AS id,'' AS name,'' AS namespace,'' AS transport,'' AS command,'' AS args_json,'' AS env_json,'' AS secret_env_json,'' AS working_directory,'' AS url,'' AS headers_json,'' AS secret_headers_json,0 AS connect_timeout_seconds,0 AS tool_timeout_seconds,0 AS enabled,'' AS created_at,'' AS updated_at WHERE 0`)
			return err
		}, want: fmt.Sprintf("schema does not match migration %d", currentSchema)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-")+".sciaide-project")
			mutateArchiveDatabase(t, source, tampered, test.mutate)
			before, err := projects.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: tampered}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("tampered archive error = %v", err)
			}
			after, err := projects.List(ctx)
			if err != nil || len(after) != len(before) {
				t.Fatalf("tampered restore published a project: before=%d after=%d err=%v", len(before), len(after), err)
			}
		})
	}
}

func newProjectArchiveTestService(t *testing.T, store *Store, projects *project.Service, root string) *projectarchive.Service {
	t.Helper()
	service, err := projectarchive.NewService(NewProjectArchiveRepository(store.DB()), projects, filepath.Join(root, "data", "workspaces"), filepath.Join(root, "cache", "project-archives"), filepath.Join(root, "backups", "trash"), "0.4.0-test")
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertArchiveOmits(t *testing.T, archivePath string, forbidden ...string) {
	t.Helper()
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, entry := range reader.File {
		input, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(input)
		_ = input.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range forbidden {
			if value != "" && bytes.Contains(contents, []byte(value)) {
				t.Fatalf("archive entry %q contains forbidden secret or package payload %q", entry.Name, value)
			}
		}
	}
}

func assertSanitizedArchiveDatabase(t *testing.T, archivePath string) {
	t.Helper()
	databaseContents := readArchiveEntry(t, archivePath, "project.sqlite")
	databaseFile := filepath.Join(t.TempDir(), "project.sqlite")
	if err := os.WriteFile(databaseFile, databaseContents, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := OpenExisting(context.Background(), databaseFile, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var baseURL, secretRef, headers string
	var enabled, isDefault bool
	if err := db.QueryRow(`SELECT base_url,secret_ref,custom_headers_json,enabled,is_default FROM model_profiles`).Scan(&baseURL, &secretRef, &headers, &enabled, &isDefault); err != nil {
		t.Fatal(err)
	}
	if baseURL != archiveModelBaseURL || !strings.HasPrefix(secretRef, "archive/profile/") || headers != "{}" || enabled || isDefault {
		t.Fatalf("archive historical profile is not sanitized: base=%q ref=%q headers=%q enabled=%v default=%v", baseURL, secretRef, headers, enabled, isDefault)
	}
	for _, table := range []string{"mcp_servers", "vision_fallback_channels", "knowledge_embedding_config", "settings", "permission_grants", "approvals"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + quoteIdentifier(table)).Scan(&count); err != nil || count != 0 {
			t.Fatalf("archive sensitive table %s count=%d err=%v", table, count, err)
		}
	}
}

func readArchiveEntry(t *testing.T, archivePath, name string) []byte {
	t.Helper()
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if entry.Name != name {
			continue
		}
		input, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(input)
		_ = input.Close()
		if err != nil {
			t.Fatal(err)
		}
		return contents
	}
	t.Fatalf("archive entry %q not found", name)
	return nil
}

func rewriteArchiveManifest(t *testing.T, source, destination string, mutate func(*projectarchive.Manifest)) {
	t.Helper()
	reader, err := zip.OpenReader(source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range reader.File {
		contents := readArchiveEntry(t, source, entry.Name)
		if entry.Name == "manifest.json" {
			var manifest projectarchive.Manifest
			if err := json.Unmarshal(contents, &manifest); err != nil {
				t.Fatal(err)
			}
			mutate(&manifest)
			contents, err = json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			contents = append(contents, '\n')
		}
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		header.SetMode(0o600)
		created, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := created.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func rewriteArchiveEntries(t *testing.T, source, destination string, mutate func(map[string][]byte)) {
	t.Helper()
	reader, err := zip.OpenReader(source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entries := make(map[string][]byte, len(reader.File))
	order := make([]string, 0, len(reader.File))
	for _, entry := range reader.File {
		entries[entry.Name] = readArchiveEntry(t, source, entry.Name)
		order = append(order, entry.Name)
	}
	mutate(entries)
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, name := range order {
		contents, ok := entries[name]
		if !ok {
			continue
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o600)
		created, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := created.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func mutateArchiveDatabase(t *testing.T, source, destination string, mutate func(*sql.DB) error) {
	t.Helper()
	reader, err := zip.OpenReader(source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entries := make(map[string][]byte, len(reader.File))
	for _, entry := range reader.File {
		entries[entry.Name] = readArchiveEntry(t, source, entry.Name)
	}
	databaseFile := filepath.Join(t.TempDir(), "project.sqlite")
	if err := os.WriteFile(databaseFile, entries["project.sqlite"], 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := OpenExisting(context.Background(), databaseFile, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := mutate(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	entries["project.sqlite"], err = os.ReadFile(databaseFile)
	if err != nil {
		t.Fatal(err)
	}
	var manifest projectarchive.Manifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(entries["project.sqlite"])
	manifest.Database.SizeBytes = int64(len(entries["project.sqlite"]))
	manifest.Database.SHA256 = hex.EncodeToString(digest[:])
	entries["manifest.json"], err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	entries["manifest.json"] = append(entries["manifest.json"], '\n')
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range reader.File {
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		header.SetMode(0o600)
		created, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := created.Write(entries[entry.Name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

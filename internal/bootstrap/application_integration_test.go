package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func writeBootstrapSkill(t *testing.T, root, id, requiredTool, instructions string) string {
	t.Helper()
	directory := filepath.Join(root, "skills", id, "1.0.0")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`schema_version: 1
id: %s
name: Integration Skill
version: 1.0.0
description: Bootstrap integration fixture
entry: SKILL.md
activation:
  mode: explicit
requires:
  tools: [%s]
  optional_tools: []
permissions: [workspace.read]
compatibility:
  sciaide: ">=0.2.0 <1.0.0"
context:
  max_tokens: 2000
`, id, requiredTool)
	if err := os.WriteFile(filepath.Join(directory, "skill.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(path, []byte(instructions), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeExternalBootstrapSkill(t *testing.T, directory, id, version, instructions string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`schema_version: 1
id: %s
name: Installed Skill
version: %s
description: P4.2 integration fixture
entry: SKILL.md
activation:
  mode: explicit
requires:
  tools: []
  optional_tools: []
permissions: []
compatibility:
  sciaide: ">=0.2.0 <1.0.0"
context:
  max_tokens: 2000
`, id, version)
	if err := os.WriteFile(filepath.Join(directory, "skill.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(instructions), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestApplicationSeedsBuiltinResearchSkills(t *testing.T) {
	root := t.TempDir()
	application, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := application.SkillFacade.ListInstalledSkills()
	if err != nil || len(installed) != 2 {
		_ = application.Close()
		t.Fatalf("built-in Skills = %#v, %v", installed, err)
	}
	want := map[string]string{"academic-writing": "学术写作助手", "literature-reading": "文献阅读助手"}
	wantVersion := map[string]string{"academic-writing": "1.0.0", "literature-reading": "1.1.0"}
	for _, value := range installed {
		if want[value.Manifest.ID] != value.Manifest.Name || value.Manifest.Version != wantVersion[value.Manifest.ID] {
			_ = application.Close()
			t.Fatalf("unexpected built-in Skill: %#v", value)
		}
		if value.Integrity != skill.IntegrityValid || value.Availability != skill.AvailabilityAvailable || value.Source.Kind != skill.SourceBuiltin || !value.Source.Archived || value.Source.Hash == "" {
			_ = application.Close()
			t.Fatalf("invalid built-in Skill provenance: %#v", value)
		}
	}
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Built-in Skills"})
	if err != nil {
		_ = application.Close()
		t.Fatal(err)
	}
	selection, err := application.SkillFacade.SetProjectSkill(skill.SetProjectSkillCommand{ProjectID: projectValue.ID, SkillID: "literature-reading", Version: "1.1.0", Enabled: true, Priority: 10})
	if err != nil || !selection.Enabled || selection.Skill.Manifest.Activation.Mode != skill.ActivationSuggest {
		_ = application.Close()
		t.Fatalf("enable built-in Skill = %#v, %v", selection, err)
	}
	if _, err := application.SkillFacade.UninstallSkill(skill.UninstallCommand{SkillID: "literature-reading", Version: "1.1.0", RemoveProjectLinks: true}); err == nil || !strings.Contains(err.Error(), "built-in Skill cannot be uninstalled") {
		_ = application.Close()
		t.Fatalf("built-in uninstall error = %v", err)
	}
	conversationValue, err := application.ConversationFacade.CreateConversation(wailstransport.CreateConversationRequest{ProjectID: projectValue.ID, Title: "内置 Skill 选择"})
	if err != nil {
		_ = application.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "builtin-skill-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "builtin-skill-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewModelProfileRepository(application.store.DB()).Save(context.Background(), profile); err != nil {
		_ = application.Close()
		t.Fatal(err)
	}
	user := conversation.Message{ID: "builtin-skill-user", ConversationID: conversationValue.ID, RunID: "builtin-skill-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "builtin-skill-user-part", MessageID: "builtin-skill-user", Type: "text", Text: "请帮我做论文解读", CreatedAt: now}}}
	assistant := conversation.Message{ID: "builtin-skill-assistant", ConversationID: conversationValue.ID, RunID: "builtin-skill-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "builtin-skill-assistant-part", MessageID: "builtin-skill-assistant", Type: "text", CreatedAt: now}}}
	run := chat.Run{ID: "builtin-skill-run", ConversationID: conversationValue.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunRunning, ContextWindowTokens: 200_000, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewRunRepository(application.store.DB()).CreateWithMessages(context.Background(), run, user, assistant); err != nil {
		_ = application.Close()
		t.Fatal(err)
	}
	runContext, err := application.skills.PrepareRunContext(context.Background(), run.ID, projectValue.ID, user.Parts[0].Text, run.ContextWindowTokens)
	if err != nil || len(runContext.Skills) != 1 || runContext.Skills[0].Manifest.ID != "literature-reading" || runContext.Skills[0].Reason != skill.SelectionSuggest || runContext.Skills[0].MatchedTrigger != "论文解读" || !strings.Contains(runContext.Skills[0].Instructions, "以可复核证据为中心") {
		_ = application.Close()
		t.Fatalf("built-in Run Skill context = %#v, %v", runContext, err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	installed, err = restarted.SkillFacade.ListInstalledSkills()
	if err != nil || len(installed) != 2 {
		t.Fatalf("built-in Skills after restart = %#v, %v", installed, err)
	}
	links, err := restarted.SkillFacade.ListProjectSkills(projectValue.ID)
	if err != nil || len(links) != 1 || !links[0].Enabled || links[0].Skill.Source.Kind != skill.SourceBuiltin {
		t.Fatalf("built-in project selection after restart = %#v, %v", links, err)
	}
}

func TestBuiltinSeedPreservesExistingUserPackageAtSameVersion(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "skills", "literature-reading", "1.1.0")
	writeExternalBootstrapSkill(t, destination, "literature-reading", "1.1.0", "user-owned instructions")
	application, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	contents, err := os.ReadFile(filepath.Join(destination, "SKILL.md"))
	if err != nil || string(contents) != "user-owned instructions" {
		t.Fatalf("existing user package was changed: %q, %v", contents, err)
	}
	installed, err := application.SkillFacade.ListInstalledSkills()
	if err != nil || len(installed) != 2 {
		t.Fatalf("installed Skills = %#v, %v", installed, err)
	}
	for _, value := range installed {
		if value.Manifest.ID == "literature-reading" && (value.Manifest.Name != "Installed Skill" || value.Source.Kind == skill.SourceBuiltin) {
			t.Fatalf("user package was reclassified as built-in: %#v", value)
		}
	}
}

func TestApplicationSkillCatalogPersistsSelectionAndRejectsMutation(t *testing.T) {
	root := t.TempDir()
	options := Options{RootDir: root, DisableBuiltinSkills: true}
	instructionsPath := writeBootstrapSkill(t, root, "literature-review", "builtin.workspace.read_text", "# Review\nRead the evidence before concluding.")
	writeBootstrapSkill(t, root, "missing-tool", "mcp.fixture.unavailable", "# Missing tool")

	first, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := first.SkillFacade.RefreshSkills()
	if err != nil || refresh.Valid != 2 || refresh.Invalid != 0 {
		t.Fatalf("RefreshSkills() = %#v, %v", refresh, err)
	}
	installed, err := first.SkillFacade.ListInstalledSkills()
	if err != nil || len(installed) != 2 {
		t.Fatalf("ListInstalledSkills() = %#v, %v", installed, err)
	}
	encoded, err := json.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("packageRelativePath")) || bytes.Contains(encoded, []byte("Instructions")) || bytes.Contains(encoded, []byte(filepath.ToSlash(root))) {
		t.Fatalf("public Skill DTO leaked private package data: %s", encoded)
	}
	createdProject, err := first.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Skill integration"})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := first.SkillFacade.SetProjectSkill(skill.SetProjectSkillCommand{ProjectID: createdProject.ID, SkillID: "literature-review", Version: "1.0.0", Enabled: true, Priority: 20})
	if err != nil || !selection.Enabled || selection.Skill.Availability != skill.AvailabilityAvailable {
		t.Fatalf("SetProjectSkill() = %#v, %v", selection, err)
	}
	if _, err := first.SkillFacade.SetProjectSkill(skill.SetProjectSkillCommand{ProjectID: createdProject.ID, SkillID: "missing-tool", Version: "1.0.0", Enabled: true, Priority: 30}); err == nil {
		t.Fatal("Skill with a missing required tool was enabled")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	projectSkills, err := second.SkillFacade.ListProjectSkills(createdProject.ID)
	if err != nil || len(projectSkills) != 1 || !projectSkills[0].Enabled {
		t.Fatalf("persisted project Skills = %#v, %v", projectSkills, err)
	}
	updated, err := second.SkillFacade.SetProjectSkill(skill.SetProjectSkillCommand{ProjectID: createdProject.ID, SkillID: "literature-review", Version: "1.0.0", Enabled: true, Priority: 10})
	if err != nil || !updated.CreatedAt.Equal(selection.CreatedAt) {
		t.Fatalf("updated selection = %#v, %v; original createdAt=%v", updated, err, selection.CreatedAt)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(instructionsPath, []byte("# Mutated\nDifferent instructions."), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	refresh, err = third.SkillFacade.RefreshSkills()
	if err != nil || refresh.Valid != 1 || refresh.Invalid != 1 || len(refresh.Diagnostics) != 1 {
		t.Fatalf("RefreshSkills() after mutation = %#v, %v", refresh, err)
	}
	installed, err = third.SkillFacade.ListInstalledSkills()
	if err != nil {
		t.Fatal(err)
	}
	var mutated skill.InstalledSkill
	for _, value := range installed {
		if value.Manifest.ID == "literature-review" {
			mutated = value
		}
	}
	if mutated.Integrity != skill.IntegrityInvalid || mutated.Availability != skill.AvailabilityUnavailable {
		t.Fatalf("mutated Skill = %#v", mutated)
	}
	projectSkills, err = third.SkillFacade.ListProjectSkills(createdProject.ID)
	if err != nil || len(projectSkills) != 1 || !projectSkills[0].Enabled || projectSkills[0].Skill.Integrity != skill.IntegrityInvalid {
		t.Fatalf("project Skill after mutation = %#v, %v", projectSkills, err)
	}
}

func TestApplicationSkillFacadeInstallsReplacesRollsBackAndUninstalls(t *testing.T) {
	root := t.TempDir()
	options := Options{RootDir: root, DisableBuiltinSkills: true}
	sources := t.TempDir()
	v1Source := writeExternalBootstrapSkill(t, filepath.Join(sources, "v1"), "research-method", "1.0.0", "version one")
	v2Source := writeExternalBootstrapSkill(t, filepath.Join(sources, "v2"), "research-method", "2.0.0", "version two")
	application, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := application.SkillFacade.InstallSkill(skill.InstallCommand{SourcePath: v1Source, SourceKind: skill.SourceFolder})
	if err != nil || v1.Skill.Manifest.Version != "1.0.0" || !v1.Skill.Source.Archived {
		t.Fatalf("install v1 = %#v, %v", v1, err)
	}
	v2, err := application.SkillFacade.InstallSkill(skill.InstallCommand{SourcePath: v2Source, SourceKind: skill.SourceFolder})
	if err != nil || v2.Skill.Manifest.Version != "2.0.0" {
		t.Fatalf("install v2 = %#v, %v", v2, err)
	}
	originalV2Hash := v2.Skill.PackageHash
	if err := os.WriteFile(filepath.Join(v2Source, "SKILL.md"), []byte("version two replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SkillFacade.InstallSkill(skill.InstallCommand{SourcePath: v2Source, SourceKind: skill.SourceFolder}); err == nil {
		t.Fatal("same version replacement did not require explicit confirmation")
	}
	v2, err = application.SkillFacade.InstallSkill(skill.InstallCommand{SourcePath: v2Source, SourceKind: skill.SourceFolder, ReplaceExisting: true})
	if err != nil || !v2.Replaced || v2.Skill.PackageHash == originalV2Hash {
		t.Fatalf("replace v2 = %#v, %v", v2, err)
	}
	encoded, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("archiveRelativePath")) || bytes.Contains(encoded, []byte(filepath.ToSlash(root))) || bytes.Contains(encoded, []byte(filepath.ToSlash(sources))) {
		t.Fatalf("install result leaked a private path: %s", encoded)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	application, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := application.SkillFacade.ListInstalledSkills()
	if err != nil || len(persisted) != 2 {
		t.Fatalf("installed Skills after replacement restart = %#v, %v", persisted, err)
	}
	for _, value := range persisted {
		if value.Integrity != skill.IntegrityValid || !value.Source.Archived {
			t.Fatalf("persisted explicit Skill = %#v", value)
		}
	}

	createdProject, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Skill versions"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.SkillFacade.SetProjectSkill(skill.SetProjectSkillCommand{ProjectID: createdProject.ID, SkillID: "research-method", Version: "2.0.0", Enabled: true, Priority: 25}); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := application.SkillFacade.RollbackProjectSkill(skill.RollbackProjectSkillCommand{ProjectID: createdProject.ID, SkillID: "research-method", TargetVersion: "1.0.0"})
	if err != nil || rolledBack.FromVersion != "2.0.0" || rolledBack.ToVersion != "1.0.0" || rolledBack.Selection.Version != "1.0.0" {
		t.Fatalf("RollbackProjectSkill() = %#v, %v", rolledBack, err)
	}
	if _, err := application.SkillFacade.UninstallSkill(skill.UninstallCommand{SkillID: "research-method", Version: "1.0.0"}); err == nil {
		t.Fatal("referenced Skill version was uninstalled without explicit link removal")
	}
	uninstalledV1, err := application.SkillFacade.UninstallSkill(skill.UninstallCommand{SkillID: "research-method", Version: "1.0.0", RemoveProjectLinks: true})
	if err != nil || uninstalledV1.RemovedProjectLinks != 1 || !uninstalledV1.Recoverable {
		t.Fatalf("uninstall v1 = %#v, %v", uninstalledV1, err)
	}
	if _, err := application.SkillFacade.UninstallSkill(skill.UninstallCommand{SkillID: "research-method", Version: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	installed, err := restarted.SkillFacade.ListInstalledSkills()
	if err != nil || len(installed) != 0 {
		t.Fatalf("installed Skills after restart = %#v, %v", installed, err)
	}
}

func TestApplicationCanRestartOnSameData(t *testing.T) {
	root := t.TempDir()
	first, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	first.Startup(context.Background())
	created, err := first.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{
		Name: "可重复启动", Description: "baseline",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	defer second.Close()
	projects, err := second.ProjectFacade.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if len(projects) != 1 || projects[0].ID != created.ID {
		t.Fatalf("projects = %#v, want created project", projects)
	}
}

func TestApplicationRecoveryExpiresApprovalBeforeInterruptingCallAndRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	application, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "恢复测试"})
	if err != nil {
		t.Fatal(err)
	}
	conversationValue, err := application.ConversationFacade.CreateConversation(wailstransport.CreateConversationRequest{ProjectID: projectValue.ID, Title: "P2.2"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	profile := modelprofile.Profile{ID: "recovery-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "recovery-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewModelProfileRepository(application.store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "recovery-user", ConversationID: conversationValue.ID, RunID: "recovery-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "recovery-user-part", MessageID: "recovery-user", Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: "recovery-assistant", ConversationID: conversationValue.ID, RunID: "recovery-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "recovery-assistant-part", MessageID: "recovery-assistant", Type: "text", CreatedAt: now}}}
	run := chat.Run{ID: "recovery-run", ConversationID: conversationValue.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunWaitingApproval, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewRunRepository(application.store.DB()).CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "recovery-call", RunID: run.ID, ProviderCallID: "recovery-provider", ToolName: "builtin.workspace.read", ToolVersion: "1", Arguments: json.RawMessage(`{"path":"paper.md"}`), Status: tool.CallAwaitingApproval, Risk: tool.RiskModerate, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "paper.md"}}, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewToolRepository(application.store.DB()).Create(ctx, call); err != nil {
		t.Fatal(err)
	}
	approval := permission.Approval{ID: "recovery-approval", RunID: run.ID, ToolCallID: call.ID, ProjectID: projectValue.ID, ToolName: call.ToolName, ToolVersion: call.ToolVersion, PermissionKind: tool.PermissionToolInvoke, Resource: call.ToolName, Risk: call.Risk, Status: permission.ApprovalPending, RequestedScope: permission.ScopeProject, CreatedAt: now}
	if _, err := application.store.DB().ExecContext(ctx, `INSERT INTO approvals(id,run_id,tool_call_id,project_id,tool_name,tool_version,permission_kind,resource,risk,status,requested_scope,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, approval.ID, approval.RunID, approval.ToolCallID, approval.ProjectID, approval.ToolName, approval.ToolVersion, approval.PermissionKind, approval.Resource, approval.Risk, approval.Status, permission.ScopeCall, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	loadedApproval, err := sqlite.NewPermissionRepository(restarted.store.DB()).GetApproval(ctx, approval.ID)
	if err != nil || loadedApproval.Status != permission.ApprovalExpired {
		t.Fatalf("approval = %#v, %v", loadedApproval, err)
	}
	loadedCall, err := sqlite.NewToolRepository(restarted.store.DB()).Get(ctx, call.ID)
	if err != nil || loadedCall.Status != tool.CallInterrupted {
		t.Fatalf("call = %#v, %v", loadedCall, err)
	}
	loadedRun, err := sqlite.NewRunRepository(restarted.store.DB()).Get(ctx, run.ID)
	if err != nil || loadedRun.Status != chat.RunInterrupted {
		t.Fatalf("run = %#v, %v", loadedRun, err)
	}
	var sequences []string
	rows, err := restarted.store.DB().QueryContext(ctx, `SELECT event_type FROM run_events WHERE aggregate_id=? ORDER BY sequence`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		sequences = append(sequences, eventType)
	}
	if len(sequences) != 1 || sequences[0] != "approval.expired" {
		t.Fatalf("recovery audit events = %v", sequences)
	}
}

func TestApplicationRegistersKnowledgeSearch(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir(), DisableBuiltinSkills: true})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	definitions, err := application.ToolFacade.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if definition.QualifiedName == "builtin.knowledge.search" {
			return
		}
	}
	t.Fatal("builtin.knowledge.search is not registered")
}

func TestApplicationRegistersAndExecutesBuiltinWorkspaceTool(t *testing.T) {
	ctx := context.Background()
	application, err := New(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	projectValue, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "内置工具"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectValue.WorkspacePath, "paper.md"), []byte("科研内容"), 0o600); err != nil {
		t.Fatal(err)
	}
	conversationValue, err := application.ConversationFacade.CreateConversation(wailstransport.CreateConversationRequest{ProjectID: projectValue.ID, Title: "工具执行"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "builtin-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "builtin-secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewModelProfileRepository(application.store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	user := conversation.Message{ID: "builtin-user", ConversationID: conversationValue.ID, RunID: "builtin-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "builtin-user-part", MessageID: "builtin-user", Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: "builtin-assistant", ConversationID: conversationValue.ID, RunID: "builtin-run", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "builtin-assistant-part", MessageID: "builtin-assistant", Type: "text", CreatedAt: now}}}
	run := chat.Run{ID: "builtin-run", ConversationID: conversationValue.ID, UserMessageID: user.ID, AssistantMessageID: assistant.ID, ModelProfileID: profile.ID, ModelID: "fixture", Status: chat.RunRunning, CreatedAt: now, UpdatedAt: now}
	runRepository := sqlite.NewRunRepository(application.store.DB())
	if err := runRepository.CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	definition := tool.Definition{QualifiedName: "builtin.workspace.read_text", Description: "读取当前科研项目 Workspace 中一个 UTF-8 文本文件的有界内容。", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}}}`), OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path","content","bytesRead","originalBytes","truncated"],"properties":{"path":{"type":"string"},"content":{"type":"string"},"bytesRead":{"type":"integer","minimum":0},"originalBytes":{"type":"integer","minimum":0},"truncated":{"type":"boolean"}}}`), Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}}, Idempotent: true, Version: "1"}
	service := tool.NewService(sqlite.NewToolRepository(application.store.DB()), tool.JSONSchemaValidator{})
	call, err := service.Propose(ctx, definition, tool.CreateCommand{RunID: run.ID, ProviderCallID: "builtin-provider", Arguments: json.RawMessage(`{"path":"paper.md"}`)})
	if err != nil {
		t.Fatal(err)
	}
	call, err = service.Start(ctx, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := application.tools.Execute(ctx, projectValue.ID, call.ID)
	if err != nil || execution.Result.Status != tool.ResultSuccess || execution.Result.Text != "科研内容" {
		t.Fatalf("Execute() = %#v, %v", execution, err)
	}
}

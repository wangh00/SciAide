package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/skill"
)

func installedSkillFixture(at time.Time) skill.InstalledSkill {
	manifest := skill.NormalizeManifest(skill.Manifest{
		SchemaVersion: 1,
		ID:            "literature-review",
		Name:          "文献阅读",
		Version:       "1.0.0",
		Description:   "阅读论文",
		Entry:         "SKILL.md",
		Activation:    skill.Activation{Mode: skill.ActivationExplicit},
		Compatibility: skill.Compatibility{SciAide: ">=0.2.0 <1.0.0"},
	})
	return skill.InstalledSkill{Manifest: manifest, PackageRelativePath: "literature-review/1.0.0", ManifestHash: strings.Repeat("a", 64), ContentHash: strings.Repeat("b", 64), PackageHash: strings.Repeat("c", 64), Integrity: skill.IntegrityValid, InstalledAt: at, UpdatedAt: at}
}

func TestSkillRepositoryReconcilesIntegrityAndKeepsProjectSelection(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Skills", "")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewSkillRepository(store.DB())
	now := time.Now().UTC()
	value := installedSkillFixture(now)
	reconciled, err := repository.Reconcile(ctx, []skill.InstalledSkill{value}, nil, []string{value.PackageRelativePath}, now)
	if err != nil || reconciled.Missing != 0 {
		t.Fatalf("Reconcile(valid) = %#v,%v", reconciled, err)
	}
	loaded, err := repository.GetInstalled(ctx, value.Manifest.ID, value.Manifest.Version)
	if err != nil || loaded.Integrity != skill.IntegrityValid || loaded.PackageHash != value.PackageHash {
		t.Fatalf("installed = %#v,%v", loaded, err)
	}
	selection := skill.ProjectSkill{ProjectID: createdProject.ID, SkillID: value.Manifest.ID, Version: value.Manifest.Version, Enabled: true, Priority: 20, CreatedAt: now, UpdatedAt: now}
	if err := repository.SetProjectSkill(ctx, selection); err != nil {
		t.Fatal(err)
	}

	_, err = repository.Reconcile(ctx, nil, []skill.Diagnostic{{PackageRelativePath: value.PackageRelativePath, Message: "manifest changed"}}, []string{value.PackageRelativePath}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ = repository.GetInstalled(ctx, value.Manifest.ID, value.Manifest.Version)
	if loaded.Integrity != skill.IntegrityInvalid || loaded.IntegrityError != "manifest changed" {
		t.Fatalf("invalid installed = %#v", loaded)
	}
	reconciled, err = repository.Reconcile(ctx, nil, nil, nil, now.Add(2*time.Minute))
	if err != nil || reconciled.Missing != 1 {
		t.Fatalf("Reconcile(missing) = %#v,%v", reconciled, err)
	}
	loaded, _ = repository.GetInstalled(ctx, value.Manifest.ID, value.Manifest.Version)
	if loaded.Integrity != skill.IntegrityMissing {
		t.Fatalf("missing installed = %#v", loaded)
	}
	if _, err := repository.Reconcile(ctx, []skill.InstalledSkill{value}, nil, []string{value.PackageRelativePath}, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	links, err := repository.ListProjectSkills(ctx, createdProject.ID)
	if err != nil || len(links) != 1 || !links[0].Enabled || links[0].Version != value.Manifest.Version {
		t.Fatalf("project Skills = %#v,%v", links, err)
	}
	var runSkillTable string
	if err := store.DB().QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='run_skills'`).Scan(&runSkillTable); err != nil || runSkillTable != "run_skills" {
		t.Fatalf("run_skills table = %q,%v", runSkillTable, err)
	}
}

func TestSkillRepositoryDoesNotTrustChangedPackageDuringDiscovery(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "changed-skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewSkillRepository(store.DB())
	now := time.Now().UTC()
	original := installedSkillFixture(now)
	if _, err := repository.Reconcile(ctx, []skill.InstalledSkill{original}, nil, []string{original.PackageRelativePath}, now); err != nil {
		t.Fatal(err)
	}

	changed := original
	changed.ContentHash = strings.Repeat("d", 64)
	changed.PackageHash = strings.Repeat("e", 64)
	reconciled, err := repository.Reconcile(ctx, []skill.InstalledSkill{changed}, nil, []string{changed.PackageRelativePath}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(reconciled.RejectedChanges) != 1 {
		t.Fatalf("rejected changes = %#v", reconciled.RejectedChanges)
	}
	loaded, err := repository.GetInstalled(ctx, original.Manifest.ID, original.Manifest.Version)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Integrity != skill.IntegrityInvalid || !strings.Contains(loaded.IntegrityError, "changed after installation") {
		t.Fatalf("changed package integrity = %#v", loaded)
	}
	if loaded.ContentHash != original.ContentHash || loaded.PackageHash != original.PackageHash {
		t.Fatalf("trusted provenance was overwritten: %#v", loaded)
	}

	if _, err := repository.Reconcile(ctx, []skill.InstalledSkill{original}, nil, []string{original.PackageRelativePath}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.GetInstalled(ctx, original.Manifest.ID, original.Manifest.Version)
	if err != nil || loaded.Integrity != skill.IntegrityValid {
		t.Fatalf("restored package = %#v, %v", loaded, err)
	}
}

func TestSkillRepositoryRejectsInvalidProjectAndIntegrityMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "invalid-skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewSkillRepository(store.DB())
	value := installedSkillFixture(time.Now().UTC())
	value.PackageHash = "not-a-hash"
	if _, err := repository.Reconcile(ctx, []skill.InstalledSkill{value}, nil, []string{value.PackageRelativePath}, time.Now().UTC()); err == nil {
		t.Fatal("invalid integrity metadata was accepted")
	}
	if err := repository.SetProjectSkill(ctx, skill.ProjectSkill{ProjectID: "missing", SkillID: "literature-review", Version: "1.0.0", Priority: 10, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("project Skill without project/install foreign keys was accepted")
	}
}

func TestSkillRepositoryAcceptsExplicitSourceAndUninstallsTransactionally(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "explicit-skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Explicit Skill", "")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewSkillRepository(store.DB())
	now := time.Now().UTC()
	value := installedSkillFixture(now)
	source := skill.PackageSource{Kind: skill.SourceZIP, Name: "literature-review.zip", Hash: strings.Repeat("d", 64), Archived: true, ArchiveRelativePath: "packages/literature-review/1.0.0/" + strings.Repeat("d", 64) + ".zip"}
	if err := repository.AcceptInstalled(ctx, value, source, now); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.GetInstalled(ctx, value.Manifest.ID, value.Manifest.Version)
	if err != nil || loaded.Source.Kind != skill.SourceZIP || loaded.Source.Hash != source.Hash || !loaded.Source.Archived {
		t.Fatalf("explicit installed Skill = %#v, %v", loaded, err)
	}
	selection := skill.ProjectSkill{ProjectID: createdProject.ID, SkillID: value.Manifest.ID, Version: value.Manifest.Version, Enabled: true, Priority: 10, CreatedAt: now, UpdatedAt: now}
	if err := repository.SetProjectSkill(ctx, selection); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.CountProjectSkillReferences(ctx, value.Manifest.ID, value.Manifest.Version); err != nil || count != 1 {
		t.Fatalf("project references = %d, %v", count, err)
	}
	if _, err := repository.RemoveInstalled(ctx, value.Manifest.ID, value.Manifest.Version, false); err == nil {
		t.Fatal("referenced Skill was removed without explicit link removal")
	}
	removed, err := repository.RemoveInstalled(ctx, value.Manifest.ID, value.Manifest.Version, true)
	if err != nil || removed != 1 {
		t.Fatalf("RemoveInstalled() = %d, %v", removed, err)
	}
	if _, err := repository.GetInstalled(ctx, value.Manifest.ID, value.Manifest.Version); !errors.Is(err, skill.ErrSkillNotFound) {
		t.Fatalf("removed Skill error = %v", err)
	}
	var sources int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM skill_package_sources`).Scan(&sources); err != nil || sources != 0 {
		t.Fatalf("remaining source rows = %d, %v", sources, err)
	}
}

func TestSkillRepositoryPersistsImmutableRunContextAndAuditRowsAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "run-skill-context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Run Skills", "")
	if err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(store.DB())).Create(ctx, createdProject.ID, "Snapshot")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO model_profiles(id,name,provider_type,base_url,model_id,secret_ref,timeout_seconds,custom_headers_json,enabled,is_default,created_at,updated_at) VALUES ('profile','fixture','openai_compatible','https://example.test/v1','model','secret',60,'{}',1,1,?,?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "run", ConversationID: createdConversation.ID, UserMessageID: "user", AssistantMessageID: "assistant", ModelProfileID: "profile", ModelID: "model", PermissionMode: conversation.PermissionPlan, Status: chat.RunRunning, CreatedAt: now, UpdatedAt: now}
	user := conversation.Message{ID: "user", ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "user-part", MessageID: "user", Type: "text", Text: "$literature-review", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	assistant := conversation.Message{ID: "assistant", ConversationID: createdConversation.ID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{ID: "assistant-part", MessageID: "assistant", Type: "text", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	if err := NewRunRepository(store.DB()).CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}

	installed := installedSkillFixture(now)
	instructions := "Read evidence first."
	contentHash := sha256.Sum256([]byte(instructions))
	value := skill.RunContext{
		SchemaVersion:           skill.RunContextSchemaVersion,
		RunID:                   run.ID,
		ProjectID:               createdProject.ID,
		ContextWindowTokens:     200_000,
		CatalogBudgetTokens:     4_000,
		InstructionBudgetTokens: 40_000,
		Catalog:                 []skill.RunCatalogSkill{},
		Skills: []skill.RunSkill{{Manifest: installed.Manifest, Priority: 10, Reason: skill.SelectionExplicit, PackagePath: installed.PackageRelativePath, ManifestHash: installed.ManifestHash,
			ContentHash: fmt.Sprintf("%x", contentHash), PackageHash: installed.PackageHash, Instructions: instructions}},
		CreatedAt: now,
	}
	_, hash, err := skill.EncodeRunContext(value)
	if err != nil {
		t.Fatal(err)
	}
	value.SnapshotHash = hash
	repository := NewSkillRepository(store.DB())
	var wait sync.WaitGroup
	errorsByWriter := make(chan error, 2)
	for writer := 0; writer < 2; writer++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByWriter <- repository.CreateRunContext(ctx, value)
		}()
	}
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("concurrent idempotent snapshot creation failed: %v", err)
		}
	}
	loaded, err := repository.GetRunContext(ctx, run.ID)
	if err != nil || loaded.SnapshotHash != hash || len(loaded.Skills) != 1 || loaded.Skills[0].Instructions != "Read evidence first." {
		t.Fatalf("loaded Run Skill context = %#v, %v", loaded, err)
	}
	var auditRows int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM run_skills WHERE run_id=?`, run.ID).Scan(&auditRows); err != nil || auditRows != 1 {
		t.Fatalf("run_skills rows = %d, %v", auditRows, err)
	}
	if err := repository.CreateRunContext(ctx, value); err != nil {
		t.Fatalf("idempotent snapshot retry failed: %v", err)
	}
	changed := value
	changed.Skills = append([]skill.RunSkill(nil), value.Skills...)
	changed.Skills[0].Instructions = "Changed"
	changed.SnapshotHash = ""
	if err := repository.CreateRunContext(ctx, changed); err == nil {
		t.Fatal("immutable Run Skill snapshot was replaced")
	}
}

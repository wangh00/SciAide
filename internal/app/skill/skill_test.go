package skill

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func validManifest() Manifest {
	return NormalizeManifest(Manifest{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "literature-review",
		Name:          "文献阅读",
		Version:       "1.2.0",
		Description:   "提取论文方法、证据与局限",
		Entry:         "SKILL.md",
		Activation:    Activation{Mode: ActivationExplicit, Triggers: []string{"文献阅读"}},
		Requires:      Requirements{Tools: []string{"builtin.workspace.read_text"}, OptionalTools: []string{"mcp.zotero.search"}},
		Permissions:   []string{"workspace.read"},
		Compatibility: Compatibility{SciAide: ">=0.2.0 <1.0.0"},
		Context:       ContextPolicy{MaxTokens: 4000},
	})
}

func TestManifestValidationAndVersionConstraints(t *testing.T) {
	manifest := validManifest()
	if err := ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		version    string
		constraint string
		want       bool
	}{
		{"0.3.0-dev", ">=0.2.0 <1.0.0", true},
		{"1.0.0", ">=0.2.0 <1.0.0", false},
		{"0.3.0-beta.2", ">0.3.0-beta.1 <0.3.0", true},
		{"0.3.0-beta.2", ">=0.3.0", false},
		{"0.3.0+desktop.1", "=0.3.0", true},
		{"0.3.0-999999999999999999999999", ">0.3.0-2 <0.3.0", true},
	} {
		got, err := SatisfiesVersion(test.version, test.constraint)
		if err != nil || got != test.want {
			t.Fatalf("SatisfiesVersion(%q,%q) = %v,%v", test.version, test.constraint, got, err)
		}
	}

	duplicate := manifest
	duplicate.Requires.OptionalTools = []string{manifest.Requires.Tools[0]}
	if err := ValidateManifest(duplicate); err == nil {
		t.Fatal("required/optional dependency overlap was accepted")
	}
	badPermission := manifest
	badPermission.Permissions = []string{"tool.invoke"}
	if err := ValidateManifest(badPermission); err == nil {
		t.Fatal("unsupported permission was accepted")
	}
	if _, err := SatisfiesVersion("0.3.0", ">=0.2.0 || <1.0.0"); err == nil {
		t.Fatal("unsupported OR constraint was accepted")
	}
	if _, err := SatisfiesVersion("999999999999999999999999.0.0", ">=0.2.0"); err == nil {
		t.Fatal("overflowing semantic version was accepted")
	}
	if ValidVersion("1.0") || ValidVersion("1.0.0-01") || ValidVersion("999999999999999999999999.0.0") || ValidID("A-B") {
		t.Fatal("invalid identity was accepted")
	}
	normalized := manifest
	normalized.Name = "  Research\n  review  "
	normalized.Description = "Evidence\t synthesis\r\nworkflow"
	normalized.Activation.Triggers = []string{"  systematic\n review "}
	normalized = NormalizeManifest(normalized)
	if normalized.Name != "Research review" || normalized.Description != "Evidence synthesis workflow" || normalized.Activation.Triggers[0] != "systematic review" {
		t.Fatalf("single-line normalization = %#v", normalized)
	}
	suggestWithoutTriggers := manifest
	suggestWithoutTriggers.Activation = Activation{Mode: ActivationSuggest}
	if err := ValidateManifest(suggestWithoutTriggers); err == nil {
		t.Fatal("suggest activation without deterministic triggers was accepted")
	}
}

type memoryRepository struct {
	installed   map[string]InstalledSkill
	projects    map[string]ProjectSkill
	runContexts map[string]RunContext
	acceptErr   error
	removeErr   error
}

func (r *memoryRepository) GetRunContext(_ context.Context, runID string) (RunContext, error) {
	value, exists := r.runContexts[runID]
	if !exists {
		return RunContext{}, ErrRunContextNotFound
	}
	return value, nil
}

func (r *memoryRepository) CreateRunContext(_ context.Context, value RunContext) error {
	if r.runContexts == nil {
		r.runContexts = map[string]RunContext{}
	}
	if existing, exists := r.runContexts[value.RunID]; exists && existing.SnapshotHash != value.SnapshotHash {
		return fmt.Errorf("immutable conflict")
	}
	r.runContexts[value.RunID] = value
	return nil
}

func skillKey(id, version string) string { return id + "@" + version }

func (r *memoryRepository) Reconcile(_ context.Context, values []InstalledSkill, _ []Diagnostic, _ []string, _ time.Time) (ReconcileResult, error) {
	for _, value := range values {
		r.installed[skillKey(value.Manifest.ID, value.Manifest.Version)] = value
	}
	return ReconcileResult{}, nil
}
func (r *memoryRepository) ListInstalled(context.Context) ([]InstalledSkill, error) {
	result := make([]InstalledSkill, 0, len(r.installed))
	for _, value := range r.installed {
		result = append(result, value)
	}
	return result, nil
}
func (r *memoryRepository) GetInstalled(_ context.Context, id, version string) (InstalledSkill, error) {
	value, exists := r.installed[skillKey(id, version)]
	if !exists {
		return InstalledSkill{}, fmt.Errorf("not found")
	}
	return value, nil
}
func (r *memoryRepository) SetProjectSkill(_ context.Context, value ProjectSkill) error {
	key := value.ProjectID + "/" + value.SkillID
	if existing, ok := r.projects[key]; ok {
		value.CreatedAt = existing.CreatedAt
	}
	r.projects[key] = value
	return nil
}
func (r *memoryRepository) GetProjectSkill(_ context.Context, projectID, skillID string) (ProjectSkill, error) {
	value, exists := r.projects[projectID+"/"+skillID]
	if !exists {
		return ProjectSkill{}, fmt.Errorf("not found")
	}
	return value, nil
}
func (r *memoryRepository) ListProjectSkills(_ context.Context, projectID string) ([]ProjectSkill, error) {
	result := []ProjectSkill{}
	for _, value := range r.projects {
		if value.ProjectID == projectID {
			result = append(result, value)
		}
	}
	return result, nil
}
func (r *memoryRepository) AcceptInstalled(_ context.Context, value InstalledSkill, source PackageSource, _ time.Time) error {
	if r.acceptErr != nil {
		return r.acceptErr
	}
	value.Source = source
	r.installed[skillKey(value.Manifest.ID, value.Manifest.Version)] = value
	return nil
}
func (r *memoryRepository) CountProjectSkillReferences(_ context.Context, id, version string) (int, error) {
	count := 0
	for _, value := range r.projects {
		if value.SkillID == id && value.Version == version {
			count++
		}
	}
	return count, nil
}
func (r *memoryRepository) RemoveInstalled(_ context.Context, id, version string, removeProjectLinks bool) (int, error) {
	if r.removeErr != nil {
		return 0, r.removeErr
	}
	key := skillKey(id, version)
	if _, exists := r.installed[key]; !exists {
		return 0, ErrSkillNotFound
	}
	removed := 0
	for projectKey, value := range r.projects {
		if value.SkillID == id && value.Version == version {
			if !removeProjectLinks {
				return 0, fmt.Errorf("referenced")
			}
			delete(r.projects, projectKey)
			removed++
		}
	}
	delete(r.installed, key)
	return removed, nil
}

type fixedCatalog struct{ value Package }

func (c fixedCatalog) Discover(context.Context) (CatalogSnapshot, error) {
	return CatalogSnapshot{Packages: []Package{c.value}, SeenPaths: []string{c.value.Skill.PackageRelativePath}}, nil
}
func (c fixedCatalog) Load(_ context.Context, path, hash string) (Package, error) {
	if path != c.value.Skill.PackageRelativePath || hash != c.value.Skill.PackageHash {
		return Package{}, fmt.Errorf("integrity mismatch")
	}
	return c.value, nil
}

type fixedTools []string

func (t fixedTools) AvailableToolNames(context.Context) ([]string, error) { return t, nil }

func TestServiceBlocksUnavailableSkillAndLoadsOnlyEnabledIntegrityMatch(t *testing.T) {
	manifest := validManifest()
	installed := InstalledSkill{Manifest: manifest, PackageRelativePath: "literature-review/1.2.0", ManifestHash: string(make([]byte, 64)), ContentHash: string(make([]byte, 64)), PackageHash: string(make([]byte, 64)), Integrity: IntegrityValid}
	packageValue := Package{Skill: installed, Instructions: "Read evidence before drawing conclusions."}
	repository := &memoryRepository{installed: map[string]InstalledSkill{skillKey(manifest.ID, manifest.Version): installed}, projects: map[string]ProjectSkill{}}
	withoutDependency := NewService(repository, fixedCatalog{value: packageValue}, fixedTools{}, "0.3.0-dev")
	if _, err := withoutDependency.SetProjectSkill(context.Background(), SetProjectSkillCommand{ProjectID: "project", SkillID: manifest.ID, Version: manifest.Version, Enabled: true, Priority: 10}); err == nil {
		t.Fatal("Skill with missing required tool was enabled")
	}

	service := NewService(repository, fixedCatalog{value: packageValue}, fixedTools{"builtin.workspace.read_text"}, "0.3.0-dev")
	view, err := service.SetProjectSkill(context.Background(), SetProjectSkillCommand{ProjectID: "project", SkillID: manifest.ID, Version: manifest.Version, Enabled: true, Priority: 10})
	if err != nil || !view.Enabled || view.Skill.Availability != AvailabilityAvailable || len(view.Skill.MissingOptionalTools) != 1 {
		t.Fatalf("enabled view = %#v, %v", view, err)
	}
	loaded, err := service.LoadEnabled(context.Background(), "project")
	if err != nil || len(loaded) != 1 || loaded[0].Instructions == "" || loaded[0].Skill.Availability != AvailabilityAvailable {
		t.Fatalf("LoadEnabled() = %#v, %v", loaded, err)
	}
}

type recordingPackageStore struct {
	installOperation   PackageInstallOperation
	uninstallOperation PackageUninstallOperation
	installRollback    bool
	uninstallRollback  bool
	readSelected       RunSkill
}

func (s *recordingPackageStore) Install(context.Context, InstallCommand) (PackageInstallOperation, error) {
	return s.installOperation, nil
}
func (s *recordingPackageStore) RollbackInstall(context.Context, PackageInstallOperation) error {
	s.installRollback = true
	return nil
}
func (s *recordingPackageStore) Uninstall(context.Context, InstalledSkill) (PackageUninstallOperation, error) {
	return s.uninstallOperation, nil
}
func (s *recordingPackageStore) RollbackUninstall(context.Context, PackageUninstallOperation) error {
	s.uninstallRollback = true
	return nil
}
func (s *recordingPackageStore) ReadResource(_ context.Context, selected RunSkill, resourcePath string, _ int) (ResourceContent, error) {
	s.readSelected = selected
	return ResourceContent{Path: resourcePath, Content: []byte("reference"), OriginalBytes: 9}, nil
}

func TestServiceReadsResourcesOnlyFromRunSelectedSkill(t *testing.T) {
	selectedPackage, _ := runContextPackage("selected-skill", ActivationExplicit, nil, "instructions", 10)
	repository := &memoryRepository{installed: map[string]InstalledSkill{}, projects: map[string]ProjectSkill{}, runContexts: map[string]RunContext{
		"run": {RunID: "run", Skills: []RunSkill{{Manifest: selectedPackage.Skill.Manifest}}},
	}}
	store := &recordingPackageStore{}
	service := NewService(repository, fixedCatalog{}, fixedTools{}, "0.3.0-dev")
	if err := service.SetPackageStore(store); err != nil {
		t.Fatal(err)
	}
	value, err := service.ReadRunResource(context.Background(), "run", "selected-skill", "references/note.md", 1024)
	if err != nil || string(value.Content) != "reference" || store.readSelected.Manifest.ID != "selected-skill" {
		t.Fatalf("selected resource = %#v, selected=%#v, err=%v", value, store.readSelected, err)
	}
	if _, err := service.ReadRunResource(context.Background(), "run", "other-skill", "references/note.md", 1024); err == nil {
		t.Fatal("unselected Skill resource was readable")
	}
}

func TestServiceRollsBackPackageFilesWhenPersistenceFails(t *testing.T) {
	manifest := validManifest()
	installed := InstalledSkill{Manifest: manifest, PackageRelativePath: "literature-review/1.2.0", ManifestHash: string(make([]byte, 64)), ContentHash: string(make([]byte, 64)), PackageHash: string(make([]byte, 64)), Integrity: IntegrityValid}
	source := PackageSource{Kind: SourceFolder, Name: "fixture", Hash: string(make([]byte, 64)), Archived: true, ArchiveRelativePath: "packages/literature-review/1.2.0/" + string(make([]byte, 64)) + ".zip"}
	store := &recordingPackageStore{installOperation: PackageInstallOperation{Package: Package{Skill: installed, Instructions: "instructions"}, Source: source}, uninstallOperation: PackageUninstallOperation{Moved: true}}
	repository := &memoryRepository{installed: map[string]InstalledSkill{}, projects: map[string]ProjectSkill{}, acceptErr: fmt.Errorf("database unavailable")}
	service := NewService(repository, fixedCatalog{}, fixedTools{}, "0.3.0-dev")
	if err := service.SetPackageStore(store); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Install(context.Background(), InstallCommand{SourcePath: "fixture", SourceKind: SourceFolder}); err == nil || !store.installRollback {
		t.Fatalf("Install() error=%v rollback=%v", err, store.installRollback)
	}

	repository.acceptErr = nil
	repository.removeErr = fmt.Errorf("database unavailable")
	repository.installed[skillKey(manifest.ID, manifest.Version)] = installed
	if _, err := service.Uninstall(context.Background(), UninstallCommand{SkillID: manifest.ID, Version: manifest.Version}); err == nil || !store.uninstallRollback {
		t.Fatalf("Uninstall() error=%v rollback=%v", err, store.uninstallRollback)
	}
}

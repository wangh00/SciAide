package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

type recordingCatalog struct {
	packages map[string]Package
	loads    []string
	errors   map[string]error
}

func (c *recordingCatalog) Discover(context.Context) (CatalogSnapshot, error) {
	return CatalogSnapshot{}, nil
}

func (c *recordingCatalog) Load(_ context.Context, path, expectedHash string) (Package, error) {
	c.loads = append(c.loads, path)
	if err := c.errors[path]; err != nil {
		return Package{}, err
	}
	value, exists := c.packages[path]
	if !exists || value.Skill.PackageHash != expectedHash {
		return Package{}, ErrSkillNotFound
	}
	return value, nil
}

func runContextPackage(id string, mode ActivationMode, triggers []string, instructions string, priority int) (Package, ProjectSkill) {
	manifest := NormalizeManifest(Manifest{
		SchemaVersion: CurrentSchemaVersion,
		ID:            id,
		Name:          strings.ReplaceAll(id, "-", " "),
		Version:       "1.0.0",
		Description:   "Research workflow for " + id,
		Entry:         "SKILL.md",
		Activation:    Activation{Mode: mode, Triggers: triggers},
		Compatibility: Compatibility{SciAide: ">=0.2.0 <1.0.0"},
		Context:       ContextPolicy{MaxTokens: 8_000},
	})
	contentHash := sha256.Sum256([]byte(instructions))
	value := InstalledSkill{
		Manifest:            manifest,
		PackageRelativePath: id + "/1.0.0",
		ManifestHash:        strings.Repeat("a", 64),
		ContentHash:         hex.EncodeToString(contentHash[:]),
		PackageHash:         strings.Repeat("c", 64),
		Integrity:           IntegrityValid,
	}
	now := time.Now().UTC()
	return Package{Skill: value, Instructions: instructions}, ProjectSkill{ProjectID: "project", SkillID: id, Version: "1.0.0", Enabled: true, Priority: priority, CreatedAt: now, UpdatedAt: now}
}

func TestPrepareRunContextSelectsProgressivelyAndReusesImmutableSnapshot(t *testing.T) {
	explicitPackage, explicitLink := runContextPackage("literature-review", ActivationExplicit, nil, "Read the complete paper before assessing evidence.", 20)
	suggestPackage, suggestLink := runContextPackage("data-analysis", ActivationSuggest, []string{"回归分析", "regression"}, "Check assumptions before fitting a regression model.", 10)
	unusedPackage, unusedLink := runContextPackage("academic-writing", ActivationExplicit, nil, "Use precise academic language.", 30)
	repository := &memoryRepository{installed: map[string]InstalledSkill{}, projects: map[string]ProjectSkill{}, runContexts: map[string]RunContext{}}
	catalog := &recordingCatalog{packages: map[string]Package{}, errors: map[string]error{}}
	for _, fixture := range []struct {
		value Package
		link  ProjectSkill
	}{{explicitPackage, explicitLink}, {suggestPackage, suggestLink}, {unusedPackage, unusedLink}} {
		repository.installed[skillKey(fixture.value.Skill.Manifest.ID, fixture.value.Skill.Manifest.Version)] = fixture.value.Skill
		repository.projects[fixture.link.ProjectID+"/"+fixture.link.SkillID] = fixture.link
		catalog.packages[fixture.value.Skill.PackageRelativePath] = fixture.value
	}
	service := NewService(repository, catalog, fixedTools{}, "0.3.0-dev")
	service.now = func() time.Time { return time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC) }

	value, err := service.PrepareRunContext(context.Background(), "run-1", "project", "请用 $literature-review 做回归分析", 200_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Skills) != 2 || value.Skills[0].Manifest.ID != "literature-review" || value.Skills[0].Reason != SelectionExplicit || value.Skills[1].Manifest.ID != "data-analysis" || value.Skills[1].Reason != SelectionSuggest || value.Skills[1].MatchedTrigger != "回归分析" {
		t.Fatalf("selected Skills = %#v", value.Skills)
	}
	if len(catalog.loads) != 2 || catalog.loads[0] != explicitPackage.Skill.PackageRelativePath || catalog.loads[1] != suggestPackage.Skill.PackageRelativePath {
		t.Fatalf("progressive loads = %#v", catalog.loads)
	}
	if !strings.Contains(value.CatalogText, "$academic-writing") || value.SnapshotHash == "" || value.CatalogBudgetTokens != 4_000 {
		t.Fatalf("Run catalog snapshot = %#v", value)
	}

	// A project edit and package replacement after the first request cannot
	// alter approval/tool-loop continuation of the same Run.
	delete(repository.projects, "project/literature-review")
	changed := catalog.packages[explicitPackage.Skill.PackageRelativePath]
	changed.Instructions = "changed instructions"
	catalog.packages[explicitPackage.Skill.PackageRelativePath] = changed
	replayed, err := service.PrepareRunContext(context.Background(), "run-1", "project", "$academic-writing", 200_000)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.SnapshotHash != value.SnapshotHash || replayed.Skills[0].Instructions != explicitPackage.Instructions || len(catalog.loads) != 2 {
		t.Fatalf("immutable replay = %#v, loads=%#v", replayed, catalog.loads)
	}

	// A new Run gets a new selection and never inherits the prior Run's bodies.
	fresh, err := service.PrepareRunContext(context.Background(), "run-2", "project", "普通问题", 200_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Skills) != 0 {
		t.Fatalf("new Run inherited Skills: %#v", fresh.Skills)
	}
}

func TestRunSkillSelectionUsesBoundariesAndRejectsTampering(t *testing.T) {
	packageValue, link := runContextPackage("analysis-helper", ActivationSuggest, []string{"analysis"}, "Analyze carefully.", 10)
	view := ProjectSkillView{ProjectSkill: link, Skill: packageValue.Skill}
	selected, _, err := selectRunSkills([]ProjectSkillView{view}, nil, "metaanalysis is not a standalone trigger")
	if err != nil || len(selected) != 0 {
		t.Fatalf("substring trigger selected = %#v, %v", selected, err)
	}
	selected, _, err = selectRunSkills([]ProjectSkillView{view}, []string{"analysis-helper"}, "$ANALYSIS-HELPER run analysis")
	if err != nil || len(selected) != 1 || selected[0].reason != SelectionExplicit {
		t.Fatalf("explicit selection = %#v, %v", selected, err)
	}

	value := RunContext{
		SchemaVersion:           RunContextSchemaVersion,
		RunID:                   "run",
		ProjectID:               "project",
		ContextWindowTokens:     200_000,
		CatalogBudgetTokens:     4_000,
		InstructionBudgetTokens: 40_000,
		Catalog:                 []RunCatalogSkill{},
		Skills:                  []RunSkill{},
		CreatedAt:               time.Now().UTC(),
	}
	encoded, hash, err := EncodeRunContext(value)
	if err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)-2] ^= 1
	if _, err := DecodeRunContext(encoded, hash); err == nil {
		t.Fatal("tampered Run Skill snapshot was accepted")
	}
}

func TestPrepareRunContextRejectsChangedSelectedPackageBeforePersisting(t *testing.T) {
	packageValue, link := runContextPackage("integrity-check", ActivationExplicit, nil, "Verify the source.", 10)
	repository := &memoryRepository{
		installed:   map[string]InstalledSkill{skillKey(link.SkillID, link.Version): packageValue.Skill},
		projects:    map[string]ProjectSkill{"project/" + link.SkillID: link},
		runContexts: map[string]RunContext{},
	}
	catalog := &recordingCatalog{packages: map[string]Package{packageValue.Skill.PackageRelativePath: packageValue}, errors: map[string]error{packageValue.Skill.PackageRelativePath: fmt.Errorf("package changed")}}
	service := NewService(repository, catalog, fixedTools{}, "0.3.0-dev")
	if _, err := service.PrepareRunContext(context.Background(), "run", "project", "$integrity-check", 200_000); err == nil || !strings.Contains(err.Error(), "package changed") {
		t.Fatalf("changed selected package error = %v", err)
	}
	if len(repository.runContexts) != 0 {
		t.Fatalf("failed snapshot was persisted: %#v", repository.runContexts)
	}
}

func TestRunSkillCatalogRemainsBoundedWithManySkills(t *testing.T) {
	enabled := make([]ProjectSkillView, 0, 200)
	for index := 0; index < 200; index++ {
		packageValue, link := runContextPackage(fmt.Sprintf("skill-%03d", index), ActivationExplicit, nil, strings.Repeat("instruction", 10), index)
		enabled = append(enabled, ProjectSkillView{ProjectSkill: link, Skill: packageValue.Skill})
	}
	catalog, text, omitted := renderRunCatalog(enabled, 500)
	if len([]rune(text)) > 500 || len(catalog) == 0 || omitted == 0 || len(catalog)+omitted != len(enabled) {
		t.Fatalf("bounded catalog: entries=%d omitted=%d chars=%d", len(catalog), omitted, len([]rune(text)))
	}
	if !strings.Contains(text, "additional Skills omitted") {
		t.Fatalf("bounded catalog hid omission marker: %q", text)
	}
}

func TestPrepareRunContextRecordsExplicitSkillSelectionFailures(t *testing.T) {
	availablePackage, availableLink := runContextPackage("available-skill", ActivationExplicit, nil, "available", 10)
	disabledPackage, disabledLink := runContextPackage("disabled-skill", ActivationExplicit, nil, "disabled", 20)
	disabledLink.Enabled = false
	unavailablePackage, unavailableLink := runContextPackage("unavailable-skill", ActivationExplicit, nil, "unavailable", 30)
	unavailablePackage.Skill.Manifest.Compatibility.SciAide = ">=9.0.0 <10.0.0"
	unavailableLink.Enabled = true
	unlinkedPackage, _ := runContextPackage("unlinked-skill", ActivationExplicit, nil, "unlinked", 40)
	repository := &memoryRepository{installed: map[string]InstalledSkill{}, projects: map[string]ProjectSkill{}, runContexts: map[string]RunContext{}}
	catalog := &recordingCatalog{packages: map[string]Package{}, errors: map[string]error{}}
	for _, value := range []Package{availablePackage, disabledPackage, unavailablePackage, unlinkedPackage} {
		repository.installed[skillKey(value.Skill.Manifest.ID, value.Skill.Manifest.Version)] = value.Skill
		catalog.packages[value.Skill.PackageRelativePath] = value
	}
	for _, link := range []ProjectSkill{availableLink, disabledLink, unavailableLink} {
		repository.projects[link.ProjectID+"/"+link.SkillID] = link
	}
	service := NewService(repository, catalog, fixedTools{}, "0.3.0-dev")
	value, err := service.PrepareRunContext(context.Background(), "run-notices", "project", "$available-skill $disabled-skill $unavailable-skill $unlinked-skill $typo-skill", 200_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Skills) != 1 || value.Skills[0].Manifest.ID != "available-skill" {
		t.Fatalf("selected Skills = %#v", value.Skills)
	}
	want := map[string]SelectionNoticeStatus{"disabled-skill": SelectionNotEnabled, "unavailable-skill": SelectionUnavailable, "unlinked-skill": SelectionNotEnabled, "typo-skill": SelectionUnknown}
	if len(value.SelectionNotices) != len(want) {
		t.Fatalf("selection notices = %#v", value.SelectionNotices)
	}
	for _, notice := range value.SelectionNotices {
		if want[notice.SkillID] != notice.Status {
			t.Fatalf("selection notice = %#v, want %q", notice, want[notice.SkillID])
		}
	}
	fragments, err := RenderContextMessages(value)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fragments, "\n")
	if !strings.Contains(joined, "$typo-skill was not found") || !strings.Contains(joined, "$unavailable-skill is enabled but currently unavailable") {
		t.Fatalf("selection status fragment = %q", joined)
	}
}

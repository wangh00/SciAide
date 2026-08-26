package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/opensciskill"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestApplicationUsesEmbeddedDynamicSkillCatalog(t *testing.T) {
	root := t.TempDir()
	application, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := application.SkillFacade.ListSkills("")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DefaultCount < 300 || len(snapshot.Skills) < 300 || snapshot.EnabledCount != len(snapshot.Skills) {
		t.Fatalf("dynamic Skill catalog = defaults %d, total %d, enabled %d", snapshot.DefaultCount, len(snapshot.Skills), snapshot.EnabledCount)
	}
	for _, item := range snapshot.Skills {
		if item.Origin != opensciskill.OriginDefault || item.ContentHash == "" || item.PackageHash == "" {
			t.Fatalf("invalid embedded Skill metadata: %#v", item)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "skills"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("embedded defaults were materialized into legacy Skill directory: %v, %v", entries, err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	stats := application.store.DB().Stats()
	if stats.OpenConnections != 0 || stats.InUse != 0 {
		t.Fatalf("application close retained SQLite connections: %#v", stats)
	}
	databasePath := filepath.Join(root, "data", "sciaide.db")
	probePath := databasePath + ".closed"
	if err := os.Rename(databasePath, probePath); err != nil {
		t.Fatalf("application close retained the SQLite file: %v", err)
	}
	if err := os.Rename(probePath, databasePath); err != nil {
		t.Fatalf("restore closed SQLite file after lifecycle probe: %v", err)
	}
}

func TestApplicationDynamicSkillProjectOverrideAndPolicyPersist(t *testing.T) {
	root := t.TempDir()
	first, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	project, err := first.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Dynamic Skills"})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(project.WorkspacePath, ".openscience", "skills", "scientific-writing")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "---\nname: scientific-writing\ndescription: Project-specific scientific writing workflow\ncategory: writing\ntags: [project]\n---\n\nUse the project-specific review checklist.\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := first.SkillFacade.RefreshDynamicSkills(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := findDynamicSkill(snapshot.Skills, "scientific-writing")
	if item.Origin != opensciskill.OriginProject || len(item.Overridden) == 0 || item.Overridden[0] != opensciskill.OriginDefault {
		t.Fatalf("project override = %#v", item)
	}
	updated, err := first.SkillFacade.SetSkillEnabled(project.ID, item.Name, false)
	if err != nil || updated.Enabled {
		t.Fatalf("disable project Skill = %#v, %v", updated, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	snapshot, err = second.SkillFacade.ListSkills(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if findDynamicSkill(snapshot.Skills, item.Name).Enabled {
		t.Fatal("global Skill policy did not persist across restart")
	}
	batch, err := second.SkillFacade.SetAllSkillsEnabled(project.ID, true)
	if err != nil || batch.Changed < 1 || batch.Total != len(snapshot.Skills) {
		t.Fatalf("enable all Skills = %#v, %v", batch, err)
	}
}

func TestApplicationRegistersDynamicSkillTools(t *testing.T) {
	application, err := New(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	definitions, err := application.ToolFacade.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"builtin.skill.load": false, "builtin.skill.resource.read_text": false}
	for _, definition := range definitions {
		if _, exists := want[definition.QualifiedName]; exists {
			want[definition.QualifiedName] = true
		}
	}
	for name, registered := range want {
		if !registered {
			t.Fatalf("%s is not registered", name)
		}
	}
}

func findDynamicSkill(values []opensciskill.Info, name string) opensciskill.Info {
	for _, item := range values {
		if item.Name == name {
			return item
		}
	}
	return opensciskill.Info{}
}

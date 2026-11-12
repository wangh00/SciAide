package opensciskill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestCompressedBundleMatchesEveryRawResourceAndFSContract(t *testing.T) {
	count := 0
	err := fs.WalkDir(os.DirFS("defaultskills"), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(filepath.Join("defaultskills", filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		packed, err := fs.ReadFile(bundled, path.Join(DefaultRoot, name))
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, packed) {
			t.Fatalf("compression changed %s", name)
		}
		count++
		return nil
	})
	if err != nil || count != 1624 {
		t.Fatal(count, err)
	}
	audit, err := fs.ReadFile(bundled, capabilityAuditFile)
	if err != nil {
		t.Fatal(err)
	}
	rawAudit, err := os.ReadFile(capabilityAuditFile)
	if err != nil || !bytes.Equal(audit, rawAudit) {
		t.Fatal("capability audit differs", err)
	}
	for _, name := range []string{"defaultskills.manifest.json", "defaultskills.upstream.manifest.json"} {
		if _, err := fs.ReadFile(bundled, name); !os.IsNotExist(err) {
			t.Fatal("test-only inventory shipped", name, err)
		}
	}
	if err := fstest.TestFS(bundled, "defaultskills/LICENSE", "defaultskills/NOTICE", "defaultskills/writing/scientific-writing/SKILL.md", capabilityAuditFile); err != nil {
		t.Fatal(err)
	}
}

func TestCompressedCatalogPreservesMetadataHashesAndDoesNotShareMutablePolicy(t *testing.T) {
	raw, rawDiagnostics := scanFS(os.DirFS("."), DefaultRoot, OriginDefault, true)
	packed, packedDiagnostics := bundled.catalog()
	rawSnapshot := cloneSnapshot(Snapshot{Skills: raw, Diagnostics: rawDiagnostics})
	if !reflect.DeepEqual(rawSnapshot.Skills, packed) || !reflect.DeepEqual(rawSnapshot.Diagnostics, packedDiagnostics) || len(packed) != 311 {
		t.Fatal("compressed discovery changed Skill metadata, routing, hashes or inventory", len(raw), len(packed), rawDiagnostics, packedDiagnostics)
	}
	packed[0].Name = "changed"
	packed[0].Enabled = !packed[0].Enabled
	if len(packed[0].Tags) > 0 {
		packed[0].Tags[0] = "changed"
	}
	again, _ := bundled.catalog()
	if !reflect.DeepEqual(rawSnapshot.Skills, again) {
		t.Fatal("catalog cache aliased mutable policy/slices")
	}
}

func TestCompressedResourceReadsAreConcurrentAndReturnIndependentBytes(t *testing.T) {
	name := "defaultskills/LICENSE"
	expected, err := os.ReadFile(filepath.FromSlash(name))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := bundled.ReadFile(name)
			if err != nil || !bytes.Equal(data, expected) {
				t.Error("concurrent read changed bytes", err)
				return
			}
			data[0] ^= 0xff
		}()
	}
	wg.Wait()
	data, err := bundled.ReadFile(name)
	if err != nil || !bytes.Equal(data, expected) {
		t.Fatal("read returned shared mutable storage", err)
	}
}

func TestCompressedDefaultSkillRunKeepsPagingResourcesAndMaterializedScriptBytes(t *testing.T) {
	ctx := context.Background()
	service, store, projects := newTestService(t)
	defer store.Close()
	p, err := projects.Create(ctx, "Compressed Skill replay", "")
	if err != nil {
		t.Fatal(err)
	}
	runID, callID := createRunAndToolCall(t, store, p)
	name := "scientific-visualization"
	info, first, created, err := service.LoadForRun(ctx, runID, p.ID, callID, name, 0, 600)
	if err != nil || !created || !first.Truncated {
		t.Fatal(info, first, created, err)
	}
	_, second, created, err := service.LoadForRun(ctx, runID, p.ID, callID, name, first.NextOffset, 600)
	if err != nil || created || second.Offset != first.NextOffset {
		t.Fatal(second, created, err)
	}
	loaded, err := service.GetRunSkill(ctx, runID, name)
	if err != nil || loaded.ContentHash != info.ContentHash || loaded.PackageHash != info.PackageHash {
		t.Fatal(loaded, err)
	}
	resources, err := service.ListResources(ctx, runID, name)
	if err != nil || len(resources.Resources) == 0 {
		t.Fatal(resources, err)
	}
	script, err := service.MaterializeResource(ctx, runID, name, "scripts/style_presets.py")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("defaultskills/visualization/scientific-visualization/scripts/style_presets.py")
	digest := sha256.Sum256(raw)
	if err != nil || !bytes.Equal(script.Contents, raw) || script.SHA256 != hex.EncodeToString(digest[:]) || script.PackageHash != info.PackageHash {
		t.Fatal("script extraction/hash changed", err)
	}
	resource, err := service.ReadResource(ctx, runID, name, "assets/nature.mplstyle", 0, 128)
	if err != nil || resource.Content == "" {
		t.Fatal(resource, err)
	}
	if _, err := service.MaterializeResource(ctx, runID, name, "../LICENSE"); err == nil {
		t.Fatal("compressed archive bypassed package boundary")
	}
	// Policy changes still invalidate service discovery, not the immutable
	// archive. Disabling a Skill must not leave it callable via a cached catalog.
	if _, err := service.SetEnabled(ctx, p.ID, name, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Load(ctx, p.ID, name, 0, 100); err == nil {
		t.Fatal("compressed metadata cache ignored disabled policy")
	}
}

func TestCompressedDefaultSkillSurvivesRestartAndRunsMaterializedScript(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "fixture.db")
	store, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	service, err := NewService(store.DB(), projects, filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := projects.Create(ctx, "Default Skill restart fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	runID, callID := createRunAndToolCall(t, store, p)
	info, first, _, err := service.LoadForRun(ctx, runID, p.ID, callID, "venue-templates", 0, 600)
	if err != nil || !first.Truncated {
		t.Fatal(first, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	projects = project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	service, err = NewService(store.DB(), projects, filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := service.GetRunSkill(ctx, runID, info.Name)
	if err != nil || loaded.ContentHash != info.ContentHash || loaded.PackageHash != info.PackageHash {
		t.Fatal("default compressed snapshot changed on restart", loaded, err)
	}
	_, second, created, err := service.LoadForRun(ctx, runID, p.ID, callID, info.Name, first.NextOffset, 600)
	if err != nil || created || second.Offset != first.NextOffset {
		t.Fatal("default pagination lost on restart", second, err)
	}
	script, err := service.MaterializeResource(ctx, runID, info.Name, "scripts/query_template.py")
	if err != nil {
		t.Fatal(err)
	}
	// Only a known stdlib-only listing command in a temporary directory. No
	// network, model call, dependency installation or formal project is used.
	if python := os.Getenv("SCIAIDE_TEST_PYTHON"); python != "" {
		target := filepath.Join(root, "query_template.py")
		if err := os.WriteFile(target, script.Contents, 0600); err != nil {
			t.Fatal(err)
		}
		raw, err := filepath.Abs("defaultskills/writing/venue-templates/scripts/query_template.py")
		if err != nil {
			t.Fatal(err)
		}
		outputs := make([][]byte, 0, 2)
		for _, file := range []string{raw, target} {
			commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			command := exec.CommandContext(commandCtx, python, "-I", "-X", "utf8", file, "--list-all")
			command.Dir = root
			command.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
			output, err := command.CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("materialized default Skill execution: %v\n%s", err, output)
			}
			outputs = append(outputs, output)
		}
		if !bytes.Equal(outputs[0], outputs[1]) || !bytes.Contains(outputs[1], []byte("AVAILABLE TEMPLATES")) {
			t.Fatal("compressed script behavior differs from raw script")
		}
		t.Log("raw and compressed/materialized default script executed with identical output")
	} else {
		t.Log("SCIAIDE_TEST_PYTHON not set: restart verified, optional script execution not run")
	}
	// Warming the default metadata cache must not prevent a user override.
	body := "---\nname: venue-templates\ndescription: User template fixture\ncategory: writing\n---\n\nUser instructions.\n"
	if _, err := service.WriteUser(ctx, info.Name, body); err != nil {
		t.Fatal(err)
	}
	override, _, content, err := service.Load(ctx, p.ID, info.Name, 0, 600)
	if err != nil || override.Origin != OriginUser || !strings.Contains(content, "User instructions") || override.PackageHash == info.PackageHash {
		t.Fatal("immutable default cache blocked user override", override, content, err)
	}
	loaded, err = service.GetRunSkill(ctx, runID, info.Name)
	if err != nil || loaded.PackageHash != info.PackageHash {
		t.Fatal("new override replaced the original run snapshot", loaded, err)
	}
}

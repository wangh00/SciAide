package pythonenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
)

func TestPublishedScriptFailureRollsBackCreateRebuildAndInstall(t *testing.T) {
	for _, operation := range []string{"create", "rebuild", "install"} {
		t.Run(operation, func(t *testing.T) {
			workspace := t.TempDir()
			if err := project.PrepareRestoredWorkspace(workspace, "project"); err != nil {
				t.Fatal(err)
			}
			repo := &environmentRepositoryFixture{values: map[string]Environment{}, operations: map[string]Operation{}}
			base := Interpreter{ExecutablePath: filepath.Join(t.TempDir(), "python.exe"), Version: "3.12", Architecture: "64bit", Implementation: "CPython", ExecutableSHA256: strings.Repeat("a", 64), HasVenv: true, HasPip: true, Prefix: "base", BasePrefix: "base"}
			probe := base
			probe.Prefix = "env"
			fixture := environmentRuntimeFixture{discovery: base, probe: probe, lock: []string{"pip==24.0"}}
			service, err := NewService(repo, projectFixture{project.Project{ID: "project", WorkspacePath: workspace}}, fixture, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(workspace, project.PrivateDirectoryName, "python", "venv")
			var previous Environment
			if operation != "create" {
				previous, err = service.Create(context.Background(), "project", base.ExecutablePath, false)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "original-marker"), []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fixture.finalizeErr = errors.New("script regeneration failed")
			service.runtime = fixture
			if operation == "install" {
				_, err = service.Install(context.Background(), "project", []string{"numpy"})
			} else {
				_, err = service.Create(context.Background(), "project", base.ExecutablePath, operation == "rebuild")
			}
			if err == nil || !strings.Contains(err.Error(), "script regeneration failed") {
				t.Fatalf("expected finalization failure: %v", err)
			}
			if operation == "create" {
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatalf("failed target remains: %v", err)
				}
				if repo.values["project"].State == StateReady {
					t.Fatal("unverified environment published ready")
				}
			} else {
				if data, err := os.ReadFile(filepath.Join(target, "original-marker")); err != nil || string(data) != "original" {
					t.Fatalf("original bytes not restored: %v", err)
				}
				if got := repo.values["project"]; got.State != StateReady || got.EnvironmentFingerprint != previous.EnvironmentFingerprint {
					t.Fatalf("original declaration not restored: %#v", got)
				}
			}
		})
	}
}

func TestLegacyScriptValidationRequiresRebuildButDoesNotModifyExternal(t *testing.T) {
	for _, external := range []bool{false, true} {
		service, repo, root, value := recoveryFixture(t, StateReady)
		value.Kind = KindLegacyManaged
		value.EnvironmentPythonPath = environmentPython(filepath.Join(root, value.ProjectID, "venv"))
		if external {
			value.Kind = KindExternal
		}
		repo.values[value.ProjectID] = value
		fixture := service.runtime.(environmentRuntimeFixture)
		fixture.validateScriptsErr = errors.New("stale launcher requires rebuild")
		service.runtime = fixture
		got, err := service.Verify(context.Background(), value.ProjectID)
		if external {
			if err != nil || got.State != StateReady {
				t.Fatalf("external script validation must not be imposed: %#v %v", got, err)
			}
		} else if err == nil || got.State != StateBroken || !strings.Contains(got.ErrorMessage, "rebuild") {
			t.Fatalf("legacy environment not marked for rebuild: %#v %v", got, err)
		}
	}
}

package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type projectFixture struct{ value project.Project }

func (f projectFixture) Get(_ context.Context, id string) (project.Project, error) {
	if id != f.value.ID {
		return project.Project{}, os.ErrNotExist
	}
	return f.value, nil
}

func TestListWorkspaceIsBoundedSortedAndNonRecursive(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"z.txt", "a.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(workspace, "papers", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	value := NewListWorkspace(projectFixture{value: project.Project{ID: "project", WorkspacePath: workspace}})
	result, err := value.Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"limit":2}`)})
	if err != nil || result.Status != tool.ResultSuccess || !result.Truncated {
		t.Fatalf("Invoke() = %#v, %v", result, err)
	}
	var payload struct {
		Entries []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(result.Structured, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Entries) != 2 || payload.Entries[0].Name != "papers" || payload.Entries[0].Kind != "directory" {
		t.Fatalf("entries = %#v", payload.Entries)
	}
}

func TestReadTextSupportsUTF8AndTruncatesAtRuneBoundary(t *testing.T) {
	workspace := t.TempDir()
	contents := "科研助手内容"
	if err := os.WriteFile(filepath.Join(workspace, "paper.md"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	value := NewReadText(projectFixture{value: project.Project{ID: "project", WorkspacePath: workspace}})
	result, err := value.Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"path":"paper.md","maxBytes":7}`)})
	if err != nil || !result.Truncated || result.Text != "科研" {
		t.Fatalf("Invoke() = %#v, %v", result, err)
	}
}

func TestReadTextRejectsBinaryAndTraversal(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "binary.dat"), []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	value := NewReadText(projectFixture{value: project.Project{ID: "project", WorkspacePath: workspace}})
	for _, arguments := range []string{`{"path":"binary.dat"}`, `{"path":"../secret.txt"}`} {
		if _, err := value.Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(arguments)}); err == nil {
			t.Fatalf("unsafe read %s accepted", arguments)
		}
	}
}

func TestWorkspaceToolsHideAndRejectPrivateProjectData(t *testing.T) {
	workspace := t.TempDir()
	private := filepath.Join(workspace, project.PrivateDirectoryName)
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "secret.txt"), []byte("internal"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := projectFixture{value: project.Project{ID: "project", WorkspacePath: workspace}}
	listed, err := NewListWorkspace(fixture).Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listed.Structured), project.PrivateDirectoryName) {
		t.Fatalf("private project data leaked in listing: %s", listed.Structured)
	}
	if _, err := NewReadText(fixture).Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"path":".sciaide/secret.txt"}`)}); err == nil {
		t.Fatal("private project data path was readable through workspace tool")
	}
}

func TestReadTextRejectsEscapingSymlink(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(out, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(workspace, "link.txt")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		t.Fatal(err)
	}
	value := NewReadText(projectFixture{value: project.Project{ID: "project", WorkspacePath: workspace}})
	if result, err := value.Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"path":"link.txt"}`)}); err == nil || strings.Contains(result.Text, "secret") {
		t.Fatalf("escaping symlink result = %#v, %v", result, err)
	}
}

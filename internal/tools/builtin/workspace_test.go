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

func TestReadTextSupportsByteOffsetPagination(t *testing.T) {
	workspace := t.TempDir()
	contents := "甲乙丙丁戊己"
	if err := os.WriteFile(filepath.Join(workspace, "paper.md"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	value := NewReadText(projectFixture{value: project.Project{ID: "project", WorkspacePath: workspace}})
	// Offset 4 lands in the middle of the second three-byte rune. A page beginning in
	// the middle of a rune must drop only that incomplete prefix and remain
	// valid UTF-8.
	result, err := value.Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"path":"paper.md","offset":4,"maxBytes":8}`)})
	if err != nil || result.Status != tool.ResultSuccess || result.Text != "丙丁" {
		t.Fatalf("offset read = %#v, %v", result, err)
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

func TestReadTextMissingPathExplainsHowToRecover(t *testing.T) {
	workspace := t.TempDir()
	value := NewReadText(projectFixture{value: project.Project{ID: "project", WorkspacePath: workspace}})
	_, err := value.Invoke(context.Background(), tool.Invocation{ProjectID: "project", Arguments: json.RawMessage(`{"path":"analysis-input/missing.csv"}`)})
	if err == nil || !strings.Contains(err.Error(), "builtin.workspace.list") || !strings.Contains(err.Error(), "精确路径") {
		t.Fatalf("missing path error = %v", err)
	}
}

func TestWorkspaceToolsExposeOverviewButProtectManagedData(t *testing.T) {
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
	if !strings.Contains(string(listed.Structured), project.PrivateDirectoryName) {
		t.Fatalf("private overview missing in listing: %s", listed.Structured)
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

func TestWorkspacePrivateOverviewAndTaskScopedReads(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"attachments", "cache", "browser", "tasks/t1"} {
		if err := os.MkdirAll(filepath.Join(root, ".sciaide", name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{".sciaide/project.json": "internal", ".sciaide/secret.txt": "secret", ".sciaide/tasks/t1/analysis.py": "print(42)"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f := projectFixture{value: project.Project{ID: "project", WorkspacePath: root}}
	invoke := func(path string, listing bool, task string) (tool.Result, error) {
		args, _ := json.Marshal(map[string]any{"path": path})
		inv := tool.Invocation{ProjectID: "project", ResearchTaskID: task, Arguments: args}
		if listing {
			return NewListWorkspace(f).Invoke(context.Background(), inv)
		}
		return NewReadText(f).Invoke(context.Background(), inv)
	}
	for _, path := range []string{".sciaide", "./.sciaide/"} {
		result, err := invoke(path, true, "")
		if err != nil || !strings.Contains(string(result.Structured), "attachments") || strings.Contains(string(result.Structured), "secret") || strings.Contains(string(result.Structured), "project.json") || !strings.Contains(result.Text, "概览") {
			t.Fatalf("overview = %+v %v", result, err)
		}
	}
	for _, path := range []string{".sciaide/cache", ".sciaide/browser", ".sciaide/tasks/t1", ".sciaide/attachments", ".SCIAIDE/secret.txt", ".sciaide./secret.txt", ".sciaide /secret.txt", "nested/.sciaide/secret.txt", ".sciaide/project.json", "../outside", ".sciaide/tasks/../cache"} {
		for _, listing := range []bool{true, false} {
			_, err := invoke(path, listing, "")
			safe, ok := err.(interface{ UserFacingMessage() string })
			if !ok || safe.UserFacingMessage() == "" {
				t.Fatalf("expected actionable rejection for %s: %v", path, err)
			}
		}
	}
	result, err := invoke("analysis.py", false, "t1")
	if err != nil || result.Text != "print(42)" {
		t.Fatalf("task file read: %+v %v", result, err)
	}
	_, err = invoke("../t2/analysis.py", false, "t1")
	if err == nil {
		t.Fatal("cross-task read allowed")
	}
	contents, _ := os.ReadFile(filepath.Join(root, ".sciaide/project.json"))
	if string(contents) != "internal" {
		t.Fatal("read-only operation changed configuration")
	}
}

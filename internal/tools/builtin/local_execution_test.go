package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/platform/localexec"
)

type localExecutionProjects struct{ selected project.Project }

func (p localExecutionProjects) Get(context.Context, string) (project.Project, error) {
	return p.selected, nil
}

func TestLocalExecutionDefinitionsUseSupportedSchemas(t *testing.T) {
	for _, value := range []tool.Tool{NewShellExecute(nil, nil), NewPythonExecute(nil, nil)} {
		definition, err := value.Definition(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := tool.ValidateDefinition(definition); err != nil {
			t.Fatalf("definition: %v", err)
		}
		if err := (tool.JSONSchemaValidator{}).Validate(definition.InputSchema, []byte(`{}`)); definition.QualifiedName == ShellExecuteName && err == nil {
			t.Fatal("Shell schema accepted missing command")
		}
		fixture := []byte(`{"pid":1,"exitCode":0,"terminationReason":"completed","stdout":{"text":"ok","bytes":2,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","truncated":false},"stderr":{"text":"","bytes":0,"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","truncated":false},"executable":"fixture","executableVersion":"3.13.0.0","workdir":".","startedAt":"2026-01-01T00:00:00Z","finishedAt":"2026-01-01T00:00:01Z"}`)
		if err := (tool.JSONSchemaValidator{}).Validate(definition.OutputSchema, fixture); err != nil {
			t.Fatalf("output schema: %v", err)
		}
	}
}

func TestResolvePythonRejectsMissingInterpreter(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, _, err := resolvePython(); err == nil || !strings.Contains(err.Error(), "no supported Python 3 interpreter") {
		t.Fatalf("resolvePython() error = %v", err)
	}
}

func TestShellExecuteRegistersOnlySuccessfulDeclaredArtifacts(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "project"); err != nil {
		t.Fatal(err)
	}
	runner := localexec.NewRunner(localexec.Options{})
	defer runner.Close()
	value := NewShellExecute(localExecutionProjects{project.Project{ID: "project", WorkspacePath: workspace}}, runner)
	arguments, _ := json.Marshal(map[string]any{"shell": "powershell", "command": "Set-Content -LiteralPath result.txt -Value 'data'", "artifactPaths": []string{"result.txt"}})
	result, err := value.Invoke(context.Background(), tool.Invocation{CallID: "call", RunID: "run", ProjectID: "project", Arguments: arguments})
	if err != nil || result.Status != tool.ResultSuccess || len(result.Artifacts) != 1 || result.Artifacts[0].WorkspacePath != "result.txt" {
		t.Fatalf("result = %#v, %v", result, err)
	}
	if contents, err := os.ReadFile(filepath.Join(workspace, "result.txt")); err != nil || !strings.Contains(string(contents), "data") {
		t.Fatalf("Artifact contents = %q, %v", contents, err)
	}

	arguments, _ = json.Marshal(map[string]any{"shell": "cmd", "command": "echo partial>failed.txt & exit /b 3", "artifactPaths": []string{"failed.txt"}})
	result, err = value.Invoke(context.Background(), tool.Invocation{CallID: "call2", RunID: "run", ProjectID: "project", Arguments: arguments})
	if err != nil || result.Status != tool.ResultError || len(result.Artifacts) != 0 || !strings.Contains(result.Text, "exitCode=3") {
		t.Fatalf("nonzero result = %#v, %v", result, err)
	}
}

func TestLocalExecutionRejectsPrivateAndEscapingPaths(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "project"); err != nil {
		t.Fatal(err)
	}
	projects := localExecutionProjects{project.Project{ID: "project", WorkspacePath: workspace}}
	for _, test := range []struct {
		workdir   string
		artifacts []string
	}{
		{"..", nil}, {".sciaide", nil}, {".", []string{".sciaide/secret.txt"}}, {".", []string{"../escape.txt"}},
	} {
		if prepared, err := prepareExecution(context.Background(), projects, "project", test.workdir, test.artifacts); err == nil {
			prepared.guard.Close()
			t.Fatalf("accepted workdir=%q artifacts=%v", test.workdir, test.artifacts)
		}
	}
}

func TestLocalExecutionRejectsPreexistingArtifactWithUnchangedContent(t *testing.T) {
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "project"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := localexec.NewRunner(localexec.Options{})
	defer runner.Close()
	value := NewShellExecute(localExecutionProjects{project.Project{ID: "project", WorkspacePath: workspace}}, runner)
	arguments, _ := json.Marshal(map[string]any{"shell": "powershell", "command": "(Get-Item -LiteralPath old.txt).LastWriteTimeUtc = [DateTime]::UtcNow", "artifactPaths": []string{"old.txt"}})
	if _, err := value.Invoke(context.Background(), tool.Invocation{CallID: "call", RunID: "run", ProjectID: "project", Arguments: arguments}); err == nil || !strings.Contains(err.Error(), "not produced or changed") {
		t.Fatalf("unchanged Artifact error = %v", err)
	}
}

func TestPythonExecuteWorksInUnicodeWorkspaceWithoutApplicationSecrets(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "中文科研项目")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := project.PrepareRestoredWorkspace(workspace, "project"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCIAIDE_TEST_SECRET_TOKEN", "must-not-leak")
	runner := localexec.NewRunner(localexec.Options{})
	defer runner.Close()
	value := NewPythonExecute(localExecutionProjects{project.Project{ID: "project", WorkspacePath: workspace}}, runner)
	code := `import os, pathlib
assert "SCIAIDE_TEST_SECRET_TOKEN" not in os.environ
pathlib.Path("分析结果.txt").write_text("科研结果", encoding="utf-8")
print("中文输出")`
	arguments, _ := json.Marshal(map[string]any{"code": code, "artifactPaths": []string{"分析结果.txt"}})
	result, err := value.Invoke(context.Background(), tool.Invocation{CallID: "python-call", RunID: "run", ProjectID: "project", Arguments: arguments})
	if err != nil || result.Status != tool.ResultSuccess || len(result.Artifacts) != 1 || !strings.Contains(result.Text, "中文输出") {
		t.Fatalf("Python result = %#v, %v", result, err)
	}
	contents, err := os.ReadFile(filepath.Join(workspace, "分析结果.txt"))
	if err != nil || string(contents) != "科研结果" {
		t.Fatalf("Unicode Artifact = %q, %v", contents, err)
	}
}

func TestPythonExecuteCanUseNetworkWithoutDomainConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("network-ok"))
	}))
	defer server.Close()
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "network-project"); err != nil {
		t.Fatal(err)
	}
	runner := localexec.NewRunner(localexec.Options{})
	defer runner.Close()
	value := NewPythonExecute(localExecutionProjects{project.Project{ID: "network-project", WorkspacePath: workspace}}, runner)
	code := fmt.Sprintf("import urllib.request\nprint(urllib.request.urlopen(%q, timeout=5).read().decode('utf-8'))", server.URL)
	arguments, _ := json.Marshal(map[string]any{"code": code})
	result, err := value.Invoke(context.Background(), tool.Invocation{CallID: "network-call", RunID: "run", ProjectID: "network-project", Arguments: arguments})
	if err != nil || result.Status != tool.ResultSuccess || !strings.Contains(result.Text, "network-ok") {
		t.Fatalf("networked Python result = %#v, %v", result, err)
	}
}

func TestShellExecuteCanUseNetworkWithoutDomainConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("shell-network-ok"))
	}))
	defer server.Close()
	workspace := t.TempDir()
	if err := project.PrepareRestoredWorkspace(workspace, "shell-network-project"); err != nil {
		t.Fatal(err)
	}
	runner := localexec.NewRunner(localexec.Options{})
	defer runner.Close()
	value := NewShellExecute(localExecutionProjects{project.Project{ID: "shell-network-project", WorkspacePath: workspace}}, runner)
	command := fmt.Sprintf("(Invoke-WebRequest -UseBasicParsing -Uri %q -TimeoutSec 5).Content", server.URL)
	arguments, _ := json.Marshal(map[string]any{"shell": "powershell", "command": command})
	result, err := value.Invoke(context.Background(), tool.Invocation{CallID: "shell-network-call", RunID: "run", ProjectID: "shell-network-project", Arguments: arguments})
	if err != nil || result.Status != tool.ResultSuccess || !strings.Contains(result.Text, "shell-network-ok") {
		t.Fatalf("networked Shell result = %#v, %v", result, err)
	}
}

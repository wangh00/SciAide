package workflow

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
)

type fixedProjectLoader struct{ value project.Project }

func (f fixedProjectLoader) Get(context.Context, string) (project.Project, error) {
	return f.value, nil
}

func TestValidateRuntimeInputsRejectsEmptyRequiredResearchValues(t *testing.T) {
	tests := []struct {
		name   string
		ports  []Port
		inputs string
		want   string
	}{
		{"empty string", []Port{{Name: "query", Type: TypeString, Required: true}}, `{"query":"  "}`, "cannot be empty"},
		{"empty paths", []Port{{Name: "input_paths", Type: TypeArray, FileKind: "delimited", MinItems: 1, MaxItems: 1, Required: true}}, `{"input_paths":[]}`, "cannot be empty"},
		{"multiple paths", []Port{{Name: "input_paths", Type: TypeArray, FileKind: "delimited", MinItems: 1, MaxItems: 1, Required: true}}, `{"input_paths":["a.csv","b.csv"]}`, "at most 1"},
		{"empty goal", []Port{{Name: "analysis_request", Type: TypeObject, Control: "analysis_request", Required: true}}, `{"analysis_request":{"goal":" ","method":"overview"}}`, "goal"},
		{"unknown method", []Port{{Name: "analysis_request", Type: TypeObject, Control: "analysis_request", Required: true}}, `{"analysis_request":{"goal":"inspect","method":"execute_code"}}`, "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := validateRuntimeInputs(test.ports, json.RawMessage(test.inputs)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateRuntimeInputs() error = %v, want %q", err, test.want)
			}
		})
	}
	if _, _, err := validateRuntimeInputs(
		[]Port{{Name: "input_paths", Type: TypeArray, FileKind: "delimited", MinItems: 1, MaxItems: 1, Required: true}, {Name: "analysis_request", Type: TypeObject, Control: "analysis_request", Required: true}},
		json.RawMessage(`{"input_paths":["data.csv"],"analysis_request":{"goal":"inspect quality","method":"data_quality"}}`),
	); err != nil {
		t.Fatalf("valid research input rejected: %v", err)
	}
	if _, _, err := validateRuntimeInputs(
		[]Port{{Name: "input_paths", Type: TypeArray}, {Name: "analysis_request", Type: TypeObject}},
		json.RawMessage(`{"input_paths":["a","b"],"analysis_request":{"custom":true}}`),
	); err != nil {
		t.Fatalf("generic inputs were coupled to built-in field names: %v", err)
	}
}

func TestRuntimeWorkspaceInputPreflightRejectsMissingDirectoryAndPrivateFiles(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "data.csv"), []byte("value\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "folder.csv"), 0o700); err != nil {
		t.Fatal(err)
	}
	service := &RuntimeService{projects: fixedProjectLoader{project.Project{ID: "project", WorkspacePath: workspace}}}
	ports := []Port{{Name: "input_paths", Type: TypeArray, FileKind: "delimited", MinItems: 1, MaxItems: 1, Required: true}}
	for _, input := range []string{`{"input_paths":["missing.csv"]}`, `{"input_paths":["folder.csv"]}`, `{"input_paths":[".sciaide/private.csv"]}`} {
		if err := service.validateWorkspaceInputFiles(context.Background(), "project", ports, json.RawMessage(input)); err == nil {
			t.Fatalf("unsafe input passed preflight: %s", input)
		}
	}
	if err := service.validateWorkspaceInputFiles(context.Background(), "project", ports, json.RawMessage(`{"input_paths":["data.csv"]}`)); err != nil {
		t.Fatalf("valid Workspace file rejected: %v", err)
	}
}

func TestRuntimeWorkspaceInputPreflightDetectsModifiedContentAddressedSnapshot(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "data.csv")
	if err := os.WriteFile(source, []byte("value\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := fixedProjectLoader{project.Project{ID: "project", WorkspacePath: workspace}}
	staged, err := (&Service{projects: loader}).StageInputFile(context.Background(), "project", source, "delimited")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, filepath.FromSlash(staged.RelativePath)), []byte("value\n2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &RuntimeService{projects: loader}
	ports := []Port{{Name: "data", Type: TypeArray, FileKind: "delimited", MinItems: 1, MaxItems: 1, Required: true}}
	inputs, _ := json.Marshal(map[string]any{"data": []string{staged.RelativePath}})
	if err := runtime.validateWorkspaceInputFiles(context.Background(), "project", ports, inputs); err == nil || !strings.Contains(err.Error(), "modified after selection") {
		t.Fatalf("modified snapshot error = %v", err)
	}
}

func TestFrozenWorkflowInputsRejectUnaddressedPaths(t *testing.T) {
	ports := []Port{{Name: "data", Type: TypeArray, FileKind: "delimited", Required: true}}
	if err := validateFrozenWorkflowInputs(ports, json.RawMessage(`{"data":["data.csv"]}`)); err == nil || !strings.Contains(err.Error(), "不可变内容快照") {
		t.Fatalf("unaddressed input error = %v", err)
	}
	digest := strings.Repeat("a", 64)
	if err := validateFrozenWorkflowInputs(ports, json.RawMessage(`{"data":["research-inputs/data-`+digest+`.csv"]}`)); err != nil {
		t.Fatalf("content-addressed input rejected: %v", err)
	}
	if err := validateFrozenWorkflowInputs([]Port{{Name: "data", Type: TypeArray}}, json.RawMessage(`{"data":["data.csv"]}`)); err != nil {
		t.Fatalf("generic input was treated as a file contract: %v", err)
	}
}

func TestStageInputFileCopiesExternalDataByContentAndFailsClosedOnCollision(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	source := filepath.Join(external, "observations.csv")
	if err := os.WriteFile(source, []byte("group,value\na,1\nb,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{projects: fixedProjectLoader{project.Project{ID: "project", WorkspacePath: workspace}}}
	first, err := service.StageInputFile(context.Background(), "project", source, "delimited")
	if err != nil || !first.Staged || !strings.HasPrefix(first.RelativePath, "research-inputs/") || len(first.SHA256) != 64 {
		t.Fatalf("StageInputFile() = %#v, %v", first, err)
	}
	second, err := service.StageInputFile(context.Background(), "project", source, "delimited")
	if err != nil || second.RelativePath != first.RelativePath || second.SHA256 != first.SHA256 {
		t.Fatalf("idempotent StageInputFile() = %#v, %v", second, err)
	}
	staged := filepath.Join(workspace, filepath.FromSlash(first.RelativePath))
	if err := os.WriteFile(staged, []byte("tampered same size bytes!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageInputFile(context.Background(), "project", source, "delimited"); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("tampered staged input error = %v", err)
	}
	if _, err := service.StageInputFile(context.Background(), "project", source, "xlsx"); err == nil {
		t.Fatal("CSV was accepted for an XLSX-only Workflow")
	}
}

func TestStageInputFileForTaskWritesOnlyToPrivateTaskWorkspace(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	source := filepath.Join(external, "sleep-study.csv")
	if err := os.WriteFile(source, []byte("exercise_minutes,sleep_score\n120,4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{projects: fixedProjectLoader{project.Project{ID: "project", WorkspacePath: workspace}}}
	staged, err := service.StageInputFileForTask(context.Background(), "project", source, "tabular", "starter-task")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(staged.RelativePath, "research-inputs/") {
		t.Fatalf("task input path = %q", staged.RelativePath)
	}
	taskFile := filepath.Join(workspace, ".sciaide", "tasks", "starter-task", filepath.FromSlash(staged.RelativePath))
	if _, err := os.Stat(taskFile); err != nil {
		t.Fatalf("private task input is unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(staged.RelativePath))); !os.IsNotExist(err) {
		t.Fatalf("task input leaked into the project-level input directory: %v", err)
	}
}

func TestStageInputFileRejectsBinaryNULBeyondHeader(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	contents := append([]byte("column\n"+strings.Repeat("a", 5000)), 0, '\n')
	source := filepath.Join(external, "binary.csv")
	if err := os.WriteFile(source, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{projects: fixedProjectLoader{project.Project{ID: "project", WorkspacePath: workspace}}}
	if _, err := service.StageInputFile(context.Background(), "project", source, "delimited"); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("binary CSV error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(workspace, "research-inputs"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid staged input was not removed: %#v", entries)
	}
}

func TestStageInputFileRejectsUnsafeAndOversizedXLSXArchives(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	service := &Service{projects: fixedProjectLoader{project.Project{ID: "project", WorkspacePath: workspace}}}
	tests := []struct {
		name       string
		entry      string
		entryBytes int
		want       string
	}{
		{name: "path escape", entry: "../escape.xml", entryBytes: 1, want: "unsafe entry path"},
		{name: "oversized entry", entry: "xl/worksheets/sheet1.xml", entryBytes: 65 << 20, want: "larger than 64 MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(external, strings.ReplaceAll(test.name, " ", "-")+".xlsx")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			workbook, _ := writer.Create("xl/workbook.xml")
			_, _ = workbook.Write([]byte("<workbook/>"))
			entry, _ := writer.Create(test.entry)
			chunk := make([]byte, 1<<20)
			for written := 0; written < test.entryBytes; written += len(chunk) {
				remaining := test.entryBytes - written
				if remaining < len(chunk) {
					_, _ = entry.Write(chunk[:remaining])
				} else {
					_, _ = entry.Write(chunk)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := service.StageInputFile(context.Background(), "project", path, "xlsx"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("StageInputFile() error = %v, want %q", err, test.want)
			}
		})
	}
}

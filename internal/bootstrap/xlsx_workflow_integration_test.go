package bootstrap

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

type xlsxWorkflowEvidence struct {
	reproductionSHA256 string
	outputSHA256       map[string]string
	scriptPath         string
	inputPath          string
	environment        pythonenv.Environment
}

func TestXLSXWorkflowReproducesAcrossCleanProjectsAndPublishesArtifacts(t *testing.T) {
	root, err := os.MkdirTemp("", "sciaide-p7-xlsx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	application, err := New(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())

	discovery, err := application.PythonFacade.DetectInterpreters()
	if err != nil || len(discovery.Interpreters) == 0 {
		t.Skipf("64-bit Python 3 with venv is not installed: %v (%s)", err, discovery.Message)
	}
	interpreter := discovery.Interpreters[0].ExecutablePath
	workbook := fixedXLSXFixture(t)
	evidence := make([]xlsxWorkflowEvidence, 0, 2)
	for index, inputName := range []string{"实验 数据 A.xlsx", "same-data-different-name.xlsx"} {
		selected, createErr := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: fmt.Sprintf("XLSX clean project %d", index+1)})
		if createErr != nil {
			t.Fatal(createErr)
		}
		inputPath := filepath.Join(selected.WorkspacePath, inputName)
		if writeErr := os.WriteFile(inputPath, workbook, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		before := fileDigest(t, inputPath)
		environment, createEnvironmentErr := application.PythonFacade.CreateProjectEnvironment(selected.ID, interpreter, false)
		if createEnvironmentErr != nil {
			t.Fatal(createEnvironmentErr)
		}
		if environment.State != pythonenv.StateReady || len(environment.EnvironmentFingerprint) != 64 || len(environment.FreezeSHA256) != 64 {
			t.Fatalf("project Python environment = %#v", environment)
		}

		template := referenceWorkflowTemplate(t, application, "xlsx-descriptive-analysis")
		saved, saveErr := application.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: selected.ID, Definition: template.Definition})
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		inputs, _ := json.Marshal(map[string]any{"input_paths": []string{inputName}})
		started, startErr := application.WorkflowFacade.Start(workflow.StartCommand{
			ProjectID: selected.ID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID, Inputs: inputs,
		})
		if startErr != nil {
			t.Fatal(startErr)
		}
		completed := completeApprovedWorkflow(t, application, selected.ID, started.Run.ID, 3*time.Minute)
		if completed.Run.Status != workflow.RunCompleted || len(completed.Steps) != 2 {
			t.Fatalf("XLSX Workflow = %#v", completed.Run)
		}
		if after := fileDigest(t, inputPath); after != before {
			t.Fatalf("declared XLSX input changed: %s != %s", after, before)
		}

		var analysisStep workflow.Step
		for _, step := range completed.Steps {
			if step.NodeID == "analysis" {
				analysisStep = step
			}
		}
		var output struct {
			Structured pythonenv.KernelResult `json:"structured"`
			Artifacts  []tool.ArtifactRef     `json:"artifacts"`
		}
		if analysisStep.ID == "" || json.Unmarshal(analysisStep.Output, &output) != nil {
			t.Fatalf("XLSX analysis Step output = %s", analysisStep.Output)
		}
		var frozenInputs struct {
			InputPaths []string `json:"input_paths"`
		}
		if json.Unmarshal(completed.Run.Inputs, &frozenInputs) != nil || len(frozenInputs.InputPaths) != 1 {
			t.Fatalf("frozen XLSX inputs = %s", completed.Run.Inputs)
		}
		frozenInput := frozenInputs.InputPaths[0]
		if !strings.HasPrefix(frozenInput, "research-inputs/") || !strings.Contains(frozenInput, before) {
			t.Fatalf("XLSX input was not frozen by content: %q", frozenInput)
		}
		result := output.Structured
		if result.Status != "success" || len(result.ReproductionSHA256) != 64 || result.EnvironmentFingerprint != environment.EnvironmentFingerprint || result.InputSHA256[frozenInput] != before || len(result.OutputSHA256) != 4 || len(output.Artifacts) != 4 {
			t.Fatalf("XLSX Kernel evidence = %#v; artifacts=%#v", result, output.Artifacts)
		}
		value, ok := result.Value.(map[string]any)
		if !ok || value["sheetName"] != "Observations" || value["rowCount"] != float64(3) || value["columnCount"] != float64(4) || value["numericColumnCount"] != float64(2) {
			t.Fatalf("XLSX descriptive result = %#v", result.Value)
		}

		artifacts, listErr := application.ArtifactFacade.ListArtifacts(selected.ID, false)
		if listErr != nil || len(artifacts) != 4 {
			t.Fatalf("XLSX Artifacts = %#v, %v", artifacts, listErr)
		}
		artifactHashes := map[string]string{}
		artifactPaths := map[string]string{}
		for _, value := range artifacts {
			detail, getErr := application.ArtifactFacade.GetArtifact(selected.ID, value.ID)
			if getErr != nil || len(detail.Versions) != 1 {
				t.Fatalf("XLSX Artifact detail = %#v, %v", detail, getErr)
			}
			version := detail.Versions[0]
			if version.SourceKind != artifact.SourceTool || version.Provenance.WorkflowRunID != completed.Run.ID || version.Provenance.ToolCallID != analysisStep.ToolCallID || version.Provenance.ToolName != "builtin.python.kernel.execute" || version.Provenance.WorkspaceRelativePath == "" {
				t.Fatalf("XLSX Artifact provenance = %#v", version.Provenance)
			}
			role := xlsxArtifactRole(version.FileName)
			if role == "" || version.SHA256 != result.OutputSHA256[version.Provenance.WorkspaceRelativePath] {
				t.Fatalf("XLSX Artifact hash/role = %#v", version)
			}
			artifactHashes[role] = version.SHA256
			artifactPaths[role] = version.Provenance.WorkspaceRelativePath
		}
		if len(artifactHashes) != 4 {
			t.Fatalf("XLSX Artifact roles = %#v", artifactHashes)
		}
		cleaned, readErr := os.ReadFile(filepath.Join(selected.WorkspacePath, filepath.FromSlash(artifactPaths["cleaned"])))
		if readErr != nil || !strings.Contains(string(cleaned), "'=2+2") {
			t.Fatalf("XLSX cleaned CSV did not neutralize formula-like cells: %q, %v", cleaned, readErr)
		}

		var auditEnvironment, auditInput, auditOutput, auditReproduction, auditCall string
		if queryErr := application.store.DB().QueryRow(`SELECT environment_fingerprint,input_sha256_json,output_sha256_json,reproduction_sha256,tool_call_id FROM python_kernel_executions WHERE workflow_run_id=?`, completed.Run.ID).
			Scan(&auditEnvironment, &auditInput, &auditOutput, &auditReproduction, &auditCall); queryErr != nil {
			t.Fatal(queryErr)
		}
		if auditEnvironment != environment.EnvironmentFingerprint || auditReproduction != result.ReproductionSHA256 || auditCall != analysisStep.ToolCallID || !strings.Contains(auditInput, before) || !strings.Contains(auditOutput, artifactHashes["summary"]) {
			t.Fatalf("persisted XLSX Kernel audit is incomplete: env=%s input=%s output=%s reproduction=%s call=%s", auditEnvironment, auditInput, auditOutput, auditReproduction, auditCall)
		}

		scriptPath := ""
		for path := range result.OutputSHA256 {
			if strings.HasSuffix(path, "-analysis.py") {
				scriptPath = filepath.Join(selected.WorkspacePath, filepath.FromSlash(path))
			}
		}
		if scriptPath == "" {
			t.Fatal("XLSX replay script is missing")
		}
		evidence = append(evidence, xlsxWorkflowEvidence{
			reproductionSHA256: result.ReproductionSHA256, outputSHA256: artifactHashes, scriptPath: scriptPath,
			inputPath: inputPath, environment: environment,
		})
	}

	if evidence[0].reproductionSHA256 != evidence[1].reproductionSHA256 {
		t.Fatalf("clean-project reproduction hashes differ: %s != %s", evidence[0].reproductionSHA256, evidence[1].reproductionSHA256)
	}
	if fmt.Sprint(evidence[0].outputSHA256) != fmt.Sprint(evidence[1].outputSHA256) {
		t.Fatalf("clean-project output hashes differ: %#v != %#v", evidence[0].outputSHA256, evidence[1].outputSHA256)
	}
	replayXLSXAnalysis(t, evidence[0])
}

func completeApprovedWorkflow(t *testing.T, application *Application, projectID, runID string, timeout time.Duration) workflow.RunDetail {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		detail, err := application.WorkflowFacade.GetRun(projectID, runID)
		if err != nil {
			t.Fatal(err)
		}
		switch detail.Run.Status {
		case workflow.RunCompleted:
			return detail
		case workflow.RunWaitingApproval:
			if len(detail.PendingApprovals) != 1 {
				t.Fatalf("XLSX Workflow approvals = %#v", detail.PendingApprovals)
			}
			approval := detail.PendingApprovals[0]
			if approval.PermissionKind == tool.PermissionNetworkDomain {
				t.Fatalf("XLSX Workflow unexpectedly requested network approval: %#v", approval)
			}
			if _, err := application.WorkflowFacade.ResolveApproval(permission.ResolveCommand{ApprovalID: approval.ID, Allow: true, Scope: permission.ScopeCall}); err != nil {
				t.Fatal(err)
			}
		case workflow.RunFailed, workflow.RunCancelled, workflow.RunInterrupted:
			t.Fatalf("XLSX Workflow stopped at %s: %s (%s); steps=%#v", detail.Run.Status, detail.Run.ErrorMessage, detail.Run.ErrorCode, detail.Steps)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatal("XLSX Workflow did not complete before its deadline")
	return workflow.RunDetail{}
}

func replayXLSXAnalysis(t *testing.T, value xlsxWorkflowEvidence) {
	t.Helper()
	directory := t.TempDir()
	cleaned := filepath.Join(directory, "replayed-cleaned.csv")
	summary := filepath.Join(directory, "replayed-summary.csv")
	chart := filepath.Join(directory, "replayed-summary.svg")
	command := exec.Command(value.environment.EnvironmentPythonPath, "-I", "-u", "-X", "utf8", value.scriptPath, value.inputPath, cleaned, summary, chart)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("replay XLSX analysis: %v\n%s", err, output)
	}
	for role, path := range map[string]string{"cleaned": cleaned, "summary": summary, "chart": chart} {
		if digest := fileDigest(t, path); digest != value.outputSHA256[role] {
			t.Fatalf("replayed %s hash = %s, want %s", role, digest, value.outputSHA256[role])
		}
	}
}

func xlsxArtifactRole(name string) string {
	switch {
	case strings.HasSuffix(name, "-cleaned.csv"):
		return "cleaned"
	case strings.HasSuffix(name, "-summary.csv"):
		return "summary"
	case strings.HasSuffix(name, "-summary.svg"):
		return "chart"
	case strings.HasSuffix(name, "-analysis.py"):
		return "script"
	default:
		return ""
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func fixedXLSXFixture(t *testing.T) []byte {
	t.Helper()
	entries := map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/></Types>`,
		"_rels/.rels":                `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Observations" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="8" uniqueCount="8"><si><t xml:space="preserve"> Group </t></si><si><t>Value</t></si><si><t>Score</t></si><si><t xml:space="preserve"> alpha </t></si><si><t>beta</t></si><si><t>gamma</t></si><si><t>Note</t></si><si><t>=2+2</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c><c r="D1" t="s"><v>6</v></c></row><row r="2"><c r="A2" t="s"><v>3</v></c><c r="B2"><v>10</v></c><c r="C2"><v>1.5</v></c></row><row r="3"><c r="A3" t="s"><v>4</v></c><c r="B3"><v>20</v></c></row><row r="4"><c r="A4" t="s"><v>5</v></c><c r="B4"><v>30</v></c><c r="C4"><v>4.5</v></c><c r="D4" t="s"><v>7</v></c></row></sheetData></worksheet>`,
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entries[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

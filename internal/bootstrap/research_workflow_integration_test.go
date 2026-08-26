package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

type researchClosureConnector struct{}

func (researchClosureConnector) Source() research.Source {
	return research.Source{
		ID: "crossref", Name: "Crossref Fixture", Domain: "literature",
		Description: "Deterministic scholarly metadata used at the public database boundary.",
		Homepage:    "https://api.crossref.org", Host: "api.crossref.org", KeyFree: true,
	}
}

func (researchClosureConnector) Search(ctx context.Context, options research.SearchOptions) ([]research.Work, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.Query) == "" {
		return nil, fmt.Errorf("fixture query is required")
	}
	return []research.Work{researchClosureWork()}, nil
}

func (researchClosureConnector) Fetch(ctx context.Context, recordID string) (research.Work, error) {
	if err := ctx.Err(); err != nil {
		return research.Work{}, err
	}
	if strings.TrimSpace(recordID) != researchClosureWork().SourceRecordID {
		return research.Work{}, &research.SourceError{SourceID: "crossref", Code: research.FailureNotFound, Message: "fixture record not found"}
	}
	return researchClosureWork(), nil
}

func researchClosureWork() research.Work {
	return research.Work{
		SourceID: "crossref", SourceRecordID: "10.5555/sciaide-p7-closure",
		Title:    "A Reproducible Fixture for the SciAide Research Closure",
		Abstract: "The verified fixture finding is epsilon forty two. This evidence supports a deterministic end-to-end research workflow.",
		Authors:  []research.Author{{Name: "Ada Researcher", ORCID: "0000-0001-2345-6789"}},
		Year:     2026, Published: "2026-08-25", Venue: "Journal of Reproducible Fixtures",
		Publisher: "SciAide Test Press", WorkType: "journal-article", Language: "en",
		Identifiers: research.Identifiers{DOI: "10.5555/sciaide-p7-closure"},
		LandingURL:  "https://doi.org/10.5555/sciaide-p7-closure", OpenAccess: false,
		RawSnapshot: json.RawMessage(`{"DOI":"10.5555/sciaide-p7-closure","fixture":true}`),
	}
}

func TestTrustedResearchWorkflowClosesAndSurvivesRestart(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		if _, fallbackErr := exec.LookPath("python3"); fallbackErr != nil {
			t.Skip("Python 3 is not installed on this test host")
		}
	}
	root, err := os.MkdirTemp("", "sciaide-p7-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	options := Options{RootDir: root, ResearchConnectors: []research.Connector{researchClosureConnector{}}}

	application := openResearchClosureApplication(t, options)
	created, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "P7 trusted research closure"})
	if err != nil {
		application.Close()
		t.Fatal(err)
	}
	template := referenceWorkflowTemplate(t, application, "trusted-research-closure")
	saved, err := application.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: created.ID, Definition: template.Definition})
	if err != nil {
		application.Close()
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.Start(workflow.StartCommand{
		ProjectID: created.ID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID,
		Inputs: json.RawMessage(`{"query":"epsilon forty two"}`),
	})
	if err != nil {
		application.Close()
		t.Fatal(err)
	}
	beforeRestart := driveToCandidateSelection(t, application, created.ID, started.Run.ID, 15*time.Second)
	candidateStep := currentWorkflowStep(t, beforeRestart)
	if candidateStep.NodeKind != workflow.NodeCandidateSelection || candidateStep.InputSHA256 == "" {
		application.Close()
		t.Fatalf("candidate checkpoint = %#v", candidateStep)
	}
	candidateInput := append(json.RawMessage(nil), candidateStep.Input...)
	candidateInputHash := candidateStep.InputSHA256
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	application = openResearchClosureApplication(t, options)
	recovered := waitForWorkflowState(t, application, created.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 10*time.Second)
	recoveredStep := currentWorkflowStep(t, recovered)
	if recoveredStep.ID != candidateStep.ID || recoveredStep.InputSHA256 != candidateInputHash || string(recoveredStep.Input) != string(candidateInput) {
		application.Close()
		t.Fatalf("candidate checkpoint changed across restart: before=%#v after=%#v", candidateStep, recoveredStep)
	}
	var offered struct {
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(recoveredStep.Input, &offered); err != nil || len(offered.Candidates) != 1 || offered.Candidates[0].ID == "" {
		application.Close()
		t.Fatalf("candidate input = %s, %v", recoveredStep.Input, err)
	}
	selection, _ := json.Marshal(map[string]any{"selectedCandidateIds": []string{offered.Candidates[0].ID}})
	if _, err := application.WorkflowFacade.Decide(workflow.HumanDecisionCommand{
		ProjectID: created.ID, RunID: started.Run.ID, StepID: recoveredStep.ID, Approved: true,
		Note: "Selected by the integration-test researcher.", Context: selection,
	}); err != nil {
		application.Close()
		t.Fatal(err)
	}

	completed := driveResearchClosure(t, application, created.ID, started.Run.ID, 2*time.Minute)
	if completed.Run.Status != workflow.RunCompleted || completed.Run.CompletedAt == nil {
		application.Close()
		t.Fatalf("Workflow did not complete: %#v", completed.Run)
	}
	assertResearchClosureEvidence(t, application, created.ID, started.Run.ID, created.WorkspacePath)
	artifactIDs := researchClosureArtifactIDs(t, application, created.ID)
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	application = openResearchClosureApplication(t, options)
	defer application.Close()
	persisted, err := application.WorkflowFacade.GetRun(created.ID, started.Run.ID)
	if err != nil || persisted.Run.Status != workflow.RunCompleted || persisted.Run.CompilationSHA256 != completed.Run.CompilationSHA256 {
		t.Fatalf("completed Workflow recovery = %#v, %v", persisted.Run, err)
	}
	for _, artifactID := range artifactIDs {
		if _, err := application.ArtifactFacade.GetArtifact(created.ID, artifactID); err != nil {
			t.Fatalf("Artifact %s was not recovered after restart: %v", artifactID, err)
		}
	}
}

func openResearchClosureApplication(t *testing.T, options Options) *Application {
	t.Helper()
	application, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	application.Startup(context.Background())
	return application
}

func referenceWorkflowTemplate(t *testing.T, application *Application, id string) workflow.Template {
	t.Helper()
	for _, value := range application.WorkflowFacade.Templates() {
		if value.ID == id {
			return value
		}
	}
	t.Fatalf("reference Workflow template %q not found", id)
	return workflow.Template{}
}

func waitForWorkflowState(t *testing.T, application *Application, projectID, runID string, expected workflow.RunStatus, timeout time.Duration) workflow.RunDetail {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last workflow.RunDetail
	for time.Now().Before(deadline) {
		value, err := application.WorkflowFacade.GetRun(projectID, runID)
		if err != nil {
			t.Fatal(err)
		}
		last = value
		if value.Run.Status == expected {
			return value
		}
		if value.Run.Status.Terminal() {
			t.Fatalf("Workflow reached %s while waiting for %s: %s (%s)", value.Run.Status, expected, value.Run.ErrorMessage, value.Run.ErrorCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Workflow timed out waiting for %s; last state=%#v", expected, last.Run)
	return workflow.RunDetail{}
}

func currentWorkflowStep(t *testing.T, detail workflow.RunDetail) workflow.Step {
	t.Helper()
	for _, step := range detail.Steps {
		if step.Ordinal == detail.Run.CurrentStep {
			return step
		}
	}
	t.Fatalf("Workflow current step %d not found", detail.Run.CurrentStep)
	return workflow.Step{}
}

func driveToCandidateSelection(t *testing.T, application *Application, projectID, runID string, timeout time.Duration) workflow.RunDetail {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		detail, err := application.WorkflowFacade.GetRun(projectID, runID)
		if err != nil {
			t.Fatal(err)
		}
		switch detail.Run.Status {
		case workflow.RunWaitingHumanConfirmation:
			step := currentWorkflowStep(t, detail)
			if step.NodeKind != workflow.NodeCandidateSelection {
				t.Fatalf("first human checkpoint is not candidate selection: %#v", step)
			}
			return detail
		case workflow.RunWaitingApproval:
			if len(detail.PendingApprovals) != 1 {
				t.Fatalf("pending search approvals = %#v", detail.PendingApprovals)
			}
			approval := detail.PendingApprovals[0]
			if approval.PermissionKind == tool.PermissionNetworkDomain {
				t.Fatalf("default network access unexpectedly requested an approval: %#v", approval)
			}
			if _, err := application.WorkflowFacade.ResolveApproval(permission.ResolveCommand{ApprovalID: approval.ID, Allow: true, Scope: permission.ScopeCall}); err != nil {
				t.Fatal(err)
			}
		case workflow.RunFailed, workflow.RunCancelled, workflow.RunInterrupted, workflow.RunCompleted:
			t.Fatalf("Workflow reached %s before candidate selection: %s (%s)", detail.Run.Status, detail.Run.ErrorMessage, detail.Run.ErrorCode)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatal("Workflow did not reach candidate selection before its deadline")
	return workflow.RunDetail{}
}

func driveResearchClosure(t *testing.T, application *Application, projectID, runID string, timeout time.Duration) workflow.RunDetail {
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
		case workflow.RunFailed, workflow.RunCancelled, workflow.RunInterrupted:
			environment, _ := application.PythonFacade.GetProjectEnvironment(projectID)
			var operationError string
			_ = application.store.DB().QueryRow(`SELECT error_message FROM python_environment_operations WHERE project_id=? ORDER BY started_at DESC LIMIT 1`, projectID).Scan(&operationError)
			var kernelStatus, kernelException, kernelError string
			_ = application.store.DB().QueryRow(`SELECT status,exception_type,error_message FROM python_kernel_executions WHERE workflow_run_id=? ORDER BY completed_at DESC LIMIT 1`, runID).Scan(&kernelStatus, &kernelException, &kernelError)
			t.Fatalf("Workflow stopped at %s: %s (%s), node=%s, environment=%s, operationError=%q, kernel=%s/%s/%q", detail.Run.Status, detail.Run.ErrorMessage, detail.Run.ErrorCode, currentWorkflowStep(t, detail).NodeID, environment.State, operationError, kernelStatus, kernelException, kernelError)
		case workflow.RunWaitingApproval:
			if len(detail.PendingApprovals) != 1 {
				t.Fatalf("pending approvals = %#v", detail.PendingApprovals)
			}
			approval := detail.PendingApprovals[0]
			if approval.PermissionKind == tool.PermissionNetworkDomain {
				t.Fatalf("default network access unexpectedly requested an approval: %#v", approval)
			}
			if _, err := application.WorkflowFacade.ResolveApproval(permission.ResolveCommand{ApprovalID: approval.ID, Allow: true, Scope: permission.ScopeCall}); err != nil {
				t.Fatal(err)
			}
		case workflow.RunWaitingHumanConfirmation:
			step := currentWorkflowStep(t, detail)
			if step.NodeKind != workflow.NodeCitationSelection {
				t.Fatalf("unexpected human step after candidate selection: %#v", step)
			}
			var input struct {
				Candidates []tool.CitationRef `json:"candidates"`
			}
			if err := json.Unmarshal(step.Input, &input); err != nil || len(input.Candidates) == 0 {
				t.Fatalf("citation candidates = %s, %v", step.Input, err)
			}
			selected, _ := json.Marshal([]tool.CitationRef{input.Candidates[0]})
			if _, err := application.WorkflowFacade.Decide(workflow.HumanDecisionCommand{
				ProjectID: projectID, RunID: runID, StepID: step.ID, Approved: true,
				Note: "Selected after local evidence inspection.", Context: selected,
			}); err != nil {
				t.Fatal(err)
			}
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatal("trusted research Workflow did not complete before its deadline")
	return workflow.RunDetail{}
}

func assertResearchClosureEvidence(t *testing.T, application *Application, projectID, runID, workspacePath string) {
	t.Helper()
	ctx := context.Background()
	toolCalls, err := sqlite.NewToolRepository(application.store.DB()).ListBySubject(ctx, tool.SubjectWorkflowRun, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(toolCalls) != 7 {
		t.Fatalf("Workflow ToolCalls = %d, want 7: %#v", len(toolCalls), toolCalls)
	}
	callIDs := make(map[string]tool.Call, len(toolCalls))
	for _, call := range toolCalls {
		if call.RunID != runID || tool.NormalizeSubjectKind(call.SubjectKind) != tool.SubjectWorkflowRun || call.Status != tool.CallCompleted || call.Result == nil || call.Result.Status != tool.ResultSuccess {
			t.Fatalf("invalid Workflow ToolCall: %#v", call)
		}
		callIDs[call.ID] = call
	}

	approvals, err := sqlite.NewPermissionRepository(application.store.DB()).ListApprovalsBySubject(ctx, tool.SubjectWorkflowRun, runID)
	if err != nil || len(approvals) == 0 {
		t.Fatalf("Workflow approvals = %#v, %v", approvals, err)
	}
	for _, approval := range approvals {
		if approval.PermissionKind == tool.PermissionNetworkDomain || approval.Status != permission.ApprovalGranted {
			t.Fatalf("unexpected Workflow approval: %#v", approval)
		}
	}

	var environmentFingerprint, inputJSON, outputJSON, reproduction, kernelCallID, auditRunID string
	if err := application.store.DB().QueryRowContext(ctx, `SELECT environment_fingerprint,input_sha256_json,output_sha256_json,reproduction_sha256,tool_call_id,workflow_run_id FROM python_kernel_executions WHERE workflow_run_id=?`, runID).
		Scan(&environmentFingerprint, &inputJSON, &outputJSON, &reproduction, &kernelCallID, &auditRunID); err != nil {
		t.Fatal(err)
	}
	var inputs, outputs map[string]string
	if json.Unmarshal([]byte(inputJSON), &inputs) != nil || json.Unmarshal([]byte(outputJSON), &outputs) != nil {
		t.Fatalf("Kernel audit hashes are invalid: input=%s output=%s", inputJSON, outputJSON)
	}
	if len(environmentFingerprint) != 64 || len(reproduction) != 64 || len(inputs["$data"]) != 64 || len(outputs) != 2 || auditRunID != runID {
		t.Fatalf("Kernel audit is incomplete: environment=%q reproduction=%q inputs=%#v outputs=%#v run=%q", environmentFingerprint, reproduction, inputs, outputs, auditRunID)
	}
	if callIDs[kernelCallID].ToolName != "builtin.python.kernel.execute" {
		t.Fatalf("Kernel audit ToolCall = %q", kernelCallID)
	}
	for path, digest := range outputs {
		if len(digest) != 64 {
			t.Fatalf("invalid output hash %s=%q", path, digest)
		}
		contents, err := os.ReadFile(filepath.Join(workspacePath, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		actual := sha256.Sum256(contents)
		if hex.EncodeToString(actual[:]) != digest {
			t.Fatalf("Workspace output %s does not match its Kernel audit", path)
		}
	}
	matches, err := filepath.Glob(filepath.Join(workspacePath, "analysis-output", "research-"+runID+"-1.*"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("declared analysis outputs = %#v, %v", matches, err)
	}

	artifacts, err := application.ArtifactFacade.ListArtifacts(projectID, false)
	if err != nil || len(artifacts) != 3 {
		t.Fatalf("research closure Artifacts = %#v, %v", artifacts, err)
	}
	seenAnalysis := map[string]artifact.Detail{}
	var report artifact.Detail
	for _, value := range artifacts {
		detail, err := application.ArtifactFacade.GetArtifact(projectID, value.ID)
		if err != nil || len(detail.Versions) != 1 {
			t.Fatalf("Artifact detail = %#v, %v", detail, err)
		}
		version := detail.Versions[0]
		extension := strings.ToLower(filepath.Ext(version.FileName))
		switch extension {
		case ".csv", ".svg":
			seenAnalysis[extension] = detail
			if version.Provenance.WorkflowRunID != runID || version.Provenance.ToolCallID != kernelCallID || version.Provenance.WorkspaceRelativePath == "" {
				t.Fatalf("analysis Artifact provenance = %#v", version.Provenance)
			}
		case ".md":
			report = detail
		}
	}
	if len(seenAnalysis) != 2 || report.Artifact.ID == "" {
		t.Fatalf("analysis/report Artifact partition = %#v, report=%#v", seenAnalysis, report)
	}
	reportVersion := report.Versions[0]
	if reportVersion.Provenance.WorkflowRunID != runID || reportVersion.Provenance.ToolName != "builtin.research.workflow.report" || len(reportVersion.Citations) != 1 || len(reportVersion.Exports) != 2 {
		t.Fatalf("trusted report snapshot is incomplete: %#v", reportVersion)
	}
	citation := reportVersion.Citations[0]
	if citation.EvidenceLevel == "" || citation.BibliographyIDSnapshot == "" || len(citation.BibliographySnapshot) == 0 || citation.SourceToolCallIDSnapshot == "" || citation.QuoteSHA256 == "" {
		t.Fatalf("trusted report Citation is incomplete: %#v", citation)
	}
	if callIDs[citation.SourceToolCallIDSnapshot].ToolName != "builtin.knowledge.search" {
		t.Fatalf("Citation source ToolCall was not the local knowledge search: %#v", citation)
	}
	lineageCalls := map[string]struct{}{}
	workflowLinked := false
	for _, lineage := range reportVersion.Lineage {
		if lineage.RelationKind == "workflow_run" && lineage.SourceIDSnapshot == runID {
			workflowLinked = true
		}
		if lineage.RelationKind == "tool_call" {
			if _, exists := callIDs[lineage.SourceIDSnapshot]; !exists {
				t.Fatalf("report lineage escaped the Workflow Run: %#v", lineage)
			}
			lineageCalls[lineage.SourceIDSnapshot] = struct{}{}
		}
	}
	if !workflowLinked || len(lineageCalls) != len(callIDs) {
		t.Fatalf("report lineage does not freeze all Workflow ToolCalls: workflow=%t calls=%d/%d", workflowLinked, len(lineageCalls), len(callIDs))
	}
	formats := map[artifact.ExportFormat]bool{}
	for _, value := range reportVersion.Exports {
		if value.SourceSHA256 != reportVersion.SHA256 || value.SizeBytes <= 0 || len(value.SHA256) != 64 {
			t.Fatalf("invalid report export: %#v", value)
		}
		formats[value.Format] = true
		var objectPath string
		if err := application.store.DB().QueryRowContext(ctx, `SELECT storage_relative_path FROM artifact_blobs WHERE id=?`, value.BlobID).Scan(&objectPath); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(workspacePath, ".sciaide", filepath.FromSlash(objectPath)))
		if err != nil || info.Size() != value.SizeBytes {
			t.Fatalf("report export object %s is incomplete: info=%#v err=%v", value.Format, info, err)
		}
	}
	if !formats[artifact.ExportDOCX] || !formats[artifact.ExportPDF] {
		t.Fatalf("report exports = %#v", formats)
	}
}

func researchClosureArtifactIDs(t *testing.T, application *Application, projectID string) []string {
	t.Helper()
	values, err := application.ArtifactFacade.ListArtifacts(projectID, false)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(values))
	for index := range values {
		ids[index] = values[index].ID
	}
	return ids
}

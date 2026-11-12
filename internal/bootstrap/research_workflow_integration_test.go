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
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/reportview"
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
	testTrustedResearchClosure(t, false)
}

func TestSeparatedReportPublicationRetryAndExportsSurviveRestart(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) { testTrustedResearchClosure(t, true, protocol) })
	}
}

func testTrustedResearchClosure(t *testing.T, separated bool, protocols ...modelprofile.APIProtocol) {
	testTrustedResearchClosureWithPublicationFault(t, separated, false, protocols...)
}

func TestLegacyPartialPublicationUpgradeKeepsReceiptAndOnlyCompletesExports(t *testing.T) {
	testTrustedResearchClosureWithPublicationFault(t, true, true, modelprofile.ProtocolOpenAIResponses)
}

func testTrustedResearchClosureWithPublicationFault(t *testing.T, separated, legacyPartial bool, protocols ...modelprofile.APIProtocol) {
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
	options := Options{RootDir: root, ResearchConnectors: []research.Connector{researchClosureConnector{}}, EventPublisher: workflowAITestPublisher{}}
	aiServer := newWorkflowAITestServer(t, func(output map[string]any, prompt string) {
		if !separated {
			return
		}
		delete(output, "reviewedInputSha256") // exercise host identity binding through real publication
		if markdown, ok := output["markdown"].(string); ok {
			// The model fixture must actually cite the offered evidence so the
			// production host generates the bibliography appendix, not fake one.
			output["markdown"] = markdown + "\n" + workflowAIReissuedCitation(prompt)
		}
	})

	application := openResearchClosureApplication(t, options)
	aiProfile := saveWorkflowAITestProfile(t, application, aiServer.URL)
	if len(protocols) > 0 {
		aiProfile, err = application.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "publication protocol fixture", BaseURL: aiServer.URL + "/v1", APIProtocol: protocols[0], ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 272000, AutoCompactTokenLimit: 244800}}, TimeoutSeconds: 20, Enabled: true, IsDefault: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	created, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "P7 trusted research closure"})
	if err != nil {
		application.Close()
		t.Fatal(err)
	}
	template := referenceWorkflowTemplate(t, application, "trusted-research-closure")
	for i := range template.Definition.Nodes {
		if template.Definition.Nodes[i].ID == "knowledge" {
			// Exercise the same abstract-only retrieval used by dynamic routes.
			template.Definition.Nodes[i].Arguments = json.RawMessage(`{"perDocument":true}`)
		}
	}
	if separated {
		renames := map[string]string{"search": "literature_discovery", "draft_report": "report_drafting", "review": "independent_review", "review_gate": "delivery_gate", "report": "report_publication"}
		rename := func(id string) string {
			if next, ok := renames[id]; ok {
				return next
			}
			return id
		}
		for i := range template.Definition.Nodes {
			n := &template.Definition.Nodes[i]
			n.ID = rename(n.ID)
			if n.ID == "report_drafting" {
				n.PromptVersion = "dynamic-report-v6"
			}
		}
		for i := range template.Definition.Edges {
			e := &template.Definition.Edges[i]
			e.FromNode, e.ToNode = rename(e.FromNode), rename(e.ToNode)
		}
		for i := range template.Definition.Outputs {
			o := &template.Definition.Outputs[i]
			o.FromNode = rename(o.FromNode)
		}
		for _, target := range []string{"report_drafting", "independent_review"} {
			template.Definition.Edges = append(template.Definition.Edges, workflow.Edge{FromNode: "select_citations", FromPort: "citations", ToNode: target, ToPort: "evidenceContext"})
		}
		// Isolated failure AFTER the report-binding and citation checks. No
		// real user database is opened. Recovery must retry publication only.
		fault := `CREATE TRIGGER fixture_publication_failure BEFORE INSERT ON artifacts WHEN NEW.name='可信科研证据报告' BEGIN SELECT RAISE(ABORT, 'fixture publication unavailable'); END`
		if legacyPartial {
			fault = `CREATE TRIGGER fixture_publication_failure BEFORE INSERT ON artifact_exports WHEN NEW.export_format='pdf' BEGIN SELECT RAISE(ABORT, 'fixture PDF publication unavailable'); END`
		}
		if _, err := application.store.DB().Exec(fault); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := application.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: created.ID, Definition: template.Definition})
	if err != nil {
		application.Close()
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.Start(workflow.StartCommand{
		ProjectID: created.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID,
		Inputs:         json.RawMessage(`{"query":"epsilon forty two"}`),
		ModelProfileID: aiProfile.ID, ModelID: workflowAITestModelID,
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

	completed := driveResearchClosure(t, application, created.ID, started.Run.ID, 2*time.Minute, separated)
	if separated {
		failed := currentWorkflowStep(t, completed)
		if completed.Recovery == nil || completed.Recovery.Kind != "retry_publication" || completed.Recovery.CallsModel {
			t.Fatal("wrong recovery projected to client", completed.Recovery)
		}
		if completed.Run.Status != workflow.RunFailed || failed.NodeID != "report_publication" {
			t.Fatalf("expected injected publication failure: %s %s", completed.Run.Status, failed.NodeID)
		}
		var view struct {
			Composition reportview.Composition `json:"reportComposition"`
			Records     []string               `json:"reportRecordAppendices"`
		}
		upstream := map[string]string{}
		for _, step := range completed.Steps {
			if step.NodeID == "independent_review" {
				if err := json.Unmarshal(step.Input, &view); err != nil {
					t.Fatal(err)
				}
			}
			if step.NodeID != "report_publication" {
				upstream[step.ID] = string(mustBootstrapJSON(t, step))
			}
		}
		if view.Composition.ViewVersion != reportview.PartsVersion || len(view.Records) < 2 {
			t.Fatal("test did not exercise real separated report appendices", view.Composition)
		}
		var triggerExists int
		if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='fixture_publication_failure'`).Scan(&triggerExists); err != nil || triggerExists != 1 {
			t.Fatal("missing fault injection", err)
		}
		// A gate mismatch must not be confused with the deliberate storage
		// failure: it would mean the original bug still prevents publication.
		calls, err := sqlite.NewToolRepository(application.store.DB()).ListBySubject(context.Background(), tool.SubjectWorkflowRun, started.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls {
			if call.ToolName == "builtin.research.workflow.report" && (strings.Contains(call.ErrorMessage, "快照") || strings.Contains(call.ErrorMessage, "审查")) {
				t.Fatal("report identity failure before injected storage fault", call.ErrorMessage)
			}
		}
		var legacyArtifactID, legacyVersionID string
		if legacyPartial {
			// Simulate a previous install's stable key AFTER Markdown and DOCX
			// commit, before PDF. Only this owned temporary fixture is altered.
			if err := application.store.DB().QueryRow(`SELECT v.id,v.artifact_id FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id WHERE a.project_id=? AND json_extract(v.provenance_json,'$.workflowRunId')=? AND json_extract(v.provenance_json,'$.toolName')='builtin.research.workflow.report'`, created.ID, started.Run.ID).Scan(&legacyVersionID, &legacyArtifactID); err != nil {
				t.Fatal("partial publication was not saved", err)
			}
			key := "workflow-report-operation:" + started.Run.ID + ":workflow:report_publication"
			// Construct an old-install fixture atomically; restore the exact
			// immutable trigger before committing. Production never rewrites it.
			tx, err := application.store.DB().Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var immutableSQL string
			if err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='artifact_versions_immutable'`).Scan(&immutableSQL); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`DROP TRIGGER artifact_versions_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE artifact_versions SET source_key=? WHERE id=?`, key, legacyVersionID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(immutableSQL); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := application.store.DB().Exec(`UPDATE artifact_versions SET source_key=source_key WHERE id=?`, legacyVersionID); err == nil {
				t.Fatal("fixture did not restore immutable versions")
			}
			var exports int
			if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM artifact_exports WHERE artifact_version_id=?`, legacyVersionID).Scan(&exports); err != nil || exports != 1 {
				t.Fatal("expected saved DOCX with missing PDF", exports, err)
			}
		}
		aiCount := len(completed.AIExecutions)
		if err := application.Close(); err != nil {
			t.Fatal(err)
		}
		application = openResearchClosureApplication(t, options)
		if _, err := application.store.DB().Exec(`DROP TRIGGER fixture_publication_failure`); err != nil {
			t.Fatal(err)
		}
		if _, err := application.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: created.ID, RunID: started.Run.ID, StepID: failed.ID, ConfirmSideEffect: true}); err != nil {
			t.Fatal(err)
		}
		completed = driveResearchClosure(t, application, created.ID, started.Run.ID, 30*time.Second)
		if len(completed.AIExecutions) != aiCount {
			t.Fatal("publication retry reran AI")
		}
		if legacyPartial {
			var versions, exports int
			if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id WHERE a.project_id=? AND json_extract(v.provenance_json,'$.workflowRunId')=? AND json_extract(v.provenance_json,'$.toolName')='builtin.research.workflow.report'`, created.ID, started.Run.ID).Scan(&versions); err != nil || versions != 1 {
				t.Fatal("upgrade duplicated a publication", versions, err)
			}
			if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM artifact_exports WHERE artifact_version_id=?`, legacyVersionID).Scan(&exports); err != nil || exports != 2 {
				t.Fatal("upgrade lost the original export receipt", exports, err)
			}
			var id string
			if err := application.store.DB().QueryRow(`SELECT artifact_id FROM artifact_versions WHERE id=?`, legacyVersionID).Scan(&id); err != nil || id != legacyArtifactID {
				t.Fatal("upgrade changed legacy Artifact identity", id, err)
			}
		}
		for _, step := range completed.Steps {
			if previous, ok := upstream[step.ID]; ok && previous != string(mustBootstrapJSON(t, step)) {
				t.Fatal("publication retry changed completed upstream", step.NodeID)
			}
			if step.NodeID == "report_publication" && step.Attempt != failed.Attempt+1 {
				t.Fatal("publication retry did not advance just this attempt")
			}
		}
		t.Log("real report assembly → HTTP fixture review → gate → publication failure → SQLite restart → publication-only retry → DOCX/PDF")
	}
	if completed.Run.Status != workflow.RunCompleted || completed.Run.CompletedAt == nil {
		application.Close()
		t.Fatalf("Workflow did not complete: %#v", completed.Run)
	}
	assertResearchClosureEvidence(t, application, created.ID, started.Run.ID, created.WorkspacePath, separated, legacyPartial)
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
	states := make([]string, 0, len(last.Steps))
	for _, step := range last.Steps {
		states = append(states, fmt.Sprintf("%s=%s(attempt=%d)", step.NodeID, step.Status, step.Attempt))
	}
	t.Fatalf("Workflow timed out waiting for %s; run=%s status=%s currentStep=%d error=%s: %s steps=%v", expected, last.Run.ID, last.Run.Status, last.Run.CurrentStep, last.Run.ErrorCode, last.Run.ErrorMessage, states)
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

func driveResearchClosure(t *testing.T, application *Application, projectID, runID string, timeout time.Duration, expectPublicationFailure ...bool) workflow.RunDetail {
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
			if len(expectPublicationFailure) > 0 && expectPublicationFailure[0] && detail.Run.Status == workflow.RunFailed && currentWorkflowStep(t, detail).NodeID == "report_publication" {
				return detail
			}
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
			if step.NodeKind == workflow.NodeAgentStage {
				if _, err := application.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: projectID, RunID: runID, StepID: step.ID, Approved: true, Note: "Accepted deterministic AI evidence review."}); err != nil {
					t.Fatal(err)
				}
				continue
			}
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

func assertResearchClosureEvidence(t *testing.T, application *Application, projectID, runID, workspacePath string, retriedPublication ...bool) {
	t.Helper()
	ctx := context.Background()
	taskWorkspacePath := workspacePath
	if detail, getErr := application.WorkflowFacade.GetRun(projectID, runID); getErr == nil && detail.Run.ResearchTaskID != "" {
		taskWorkspacePath = filepath.Join(workspacePath, ".sciaide", "tasks", detail.Run.ResearchTaskID)
	}
	toolCalls, err := sqlite.NewToolRepository(application.store.DB()).ListBySubject(ctx, tool.SubjectWorkflowRun, runID)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := 8
	if len(retriedPublication) > 0 && retriedPublication[0] {
		wantCalls++
	}
	if len(toolCalls) != wantCalls {
		t.Fatalf("Workflow ToolCalls = %d, want %d", len(toolCalls), wantCalls)
	}
	callIDs := make(map[string]tool.Call, len(toolCalls))
	for _, call := range toolCalls {
		if wantCalls == 9 && call.ToolName == "builtin.research.workflow.report" && call.Status == tool.CallFailed {
			continue
		}
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
	var aiChatRunID, seedCallID, seedCitationsJSON, messageReference, messageToolCallID string
	if err := application.store.DB().QueryRowContext(ctx, `
		SELECT binding.chat_run_id, seed.id, result.citations_json, citation.reference_key, citation.tool_call_id
		FROM workflow_ai_executions execution
		JOIN workflow_ai_chat_runs binding ON binding.execution_id=execution.id
		JOIN tool_calls seed ON seed.run_id=binding.chat_run_id AND seed.tool_name='builtin.workflow.citation.seed' AND seed.status='completed'
		JOIN tool_results result ON result.tool_call_id=seed.id AND result.status='success'
		JOIN message_citations citation ON citation.run_id=binding.chat_run_id
		WHERE execution.workflow_run_id=? AND execution.prompt_version='research-interpret-v2'`, runID).Scan(&aiChatRunID, &seedCallID, &seedCitationsJSON, &messageReference, &messageToolCallID); err != nil {
		var executions, seeds, messageCitations int
		var answer string
		_ = application.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_ai_executions WHERE workflow_run_id=?`, runID).Scan(&executions)
		_ = application.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls seed JOIN workflow_ai_chat_runs binding ON binding.chat_run_id=seed.run_id JOIN workflow_ai_executions execution ON execution.id=binding.execution_id WHERE execution.workflow_run_id=? AND seed.tool_name='builtin.workflow.citation.seed'`, runID).Scan(&seeds)
		_ = application.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_citations citation JOIN workflow_ai_chat_runs binding ON binding.chat_run_id=citation.run_id JOIN workflow_ai_executions execution ON execution.id=binding.execution_id WHERE execution.workflow_run_id=?`, runID).Scan(&messageCitations)
		_ = application.store.DB().QueryRowContext(ctx, `SELECT COALESCE(part.text_content,'') FROM workflow_ai_executions execution JOIN workflow_ai_chat_runs binding ON binding.execution_id=execution.id JOIN runs chat_run ON chat_run.id=binding.chat_run_id JOIN message_parts part ON part.message_id=chat_run.assistant_message_id AND part.ordinal=0 WHERE execution.workflow_run_id=?`, runID).Scan(&answer)
		t.Fatalf("Workflow AI citation bridge is incomplete: %v; executions=%d seeds=%d messageCitations=%d answer=%q", err, executions, seeds, messageCitations, answer)
	}
	var seeded []tool.CitationRef
	if json.Unmarshal([]byte(seedCitationsJSON), &seeded) != nil || len(seeded) != 1 || seeded[0].Reference != messageReference || seeded[0].ProjectID != projectID || messageToolCallID != seedCallID || aiChatRunID == "" {
		t.Fatalf("Workflow AI citation bridge mismatch: run=%q seed=%#v messageReference=%q sourceCall=%q want=%q", aiChatRunID, seeded, messageReference, messageToolCallID, seedCallID)
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
		contents, err := os.ReadFile(filepath.Join(taskWorkspacePath, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		actual := sha256.Sum256(contents)
		if hex.EncodeToString(actual[:]) != digest {
			t.Fatalf("Workspace output %s does not match its Kernel audit", path)
		}
	}
	matches, err := filepath.Glob(filepath.Join(taskWorkspacePath, "analysis-output", "research-"+runID+"-1.*"))
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
	if len(retriedPublication) > 1 && retriedPublication[1] {
		// A reused Markdown receipt correctly preserves its original producer,
		// which failed only AFTER committing Markdown/DOCX. The later export
		// recovery call is verified above but cannot rewrite frozen lineage.
		var original *tool.Call
		for i := range toolCalls {
			if toolCalls[i].ID == reportVersion.Provenance.ToolCallID {
				original = &toolCalls[i]
			}
		}
		if original == nil || original.RunID != runID || tool.NormalizeSubjectKind(original.SubjectKind) != tool.SubjectWorkflowRun || original.ToolName != "builtin.research.workflow.report" || original.Status != tool.CallFailed {
			t.Fatal("legacy partial receipt has no verified original producer")
		}
		for id, call := range callIDs {
			if call.ToolName == "builtin.research.workflow.report" {
				delete(callIDs, id)
			}
		}
		callIDs[original.ID] = *original
	}
	if wantCalls == 9 {
		// Read the immutable Markdown object, not the UI's structured preview
		// (which deliberately uses Document blocks instead of Preview.Text).
		var objectPath string
		if err := application.store.DB().QueryRowContext(ctx, `SELECT b.storage_relative_path FROM artifact_blobs b JOIN artifact_versions v ON v.blob_id=b.id WHERE v.id=?`, reportVersion.ID).Scan(&objectPath); err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(filepath.Join(workspacePath, ".sciaide", filepath.FromSlash(objectPath)))
		text := string(contents)
		if err != nil || !strings.Contains(text, "# Frozen empirical report") || strings.Count(text, "## 附录：检索方法记录") != 1 || strings.Count(text, "## 附录：引用来源书目") != 1 {
			t.Fatal("published report lost or duplicated body/appendices", err)
		}
	}
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
	var frozenAIOutput, rawAIOutput string
	if err := application.store.DB().QueryRowContext(ctx, `SELECT output_json,output_text FROM workflow_ai_executions WHERE workflow_run_id=? AND prompt_version='research-interpret-v2'`, runID).Scan(&frozenAIOutput, &rawAIOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawAIOutput, messageReference) || strings.Contains(frozenAIOutput, messageReference) || !strings.Contains(frozenAIOutput, citation.Reference) {
		t.Fatalf("Workflow AI citation domain was not restored: chat=%q workflow=%q raw=%q frozen=%q", messageReference, citation.Reference, rawAIOutput, frozenAIOutput)
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

package bootstrap

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestResearchClosedLoopRevisesMethodsAfterRestartAndExports(t *testing.T) {
	var mu sync.Mutex
	reviews, methodRevisions := 0, 0
	server := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		mu.Lock()
		defer mu.Unlock()
		if _, method := output["methodComponents"]; method && strings.Contains(prompt, "_reviewRevision") {
			methodRevisions++
		}
		checks, review := output["acceptanceChecks"].([]any)
		if !review {
			return
		}
		reviews++
		if reviews == 1 {
			output["approved"] = false
			output["methodIssues"] = []string{"补充方法中的变量定义，再重新形成研究设计"}
			output["requiredCorrections"] = []string{"补充方法中的变量定义，再重新形成研究设计"}
			if len(checks) > 0 {
				check := checks[0].(map[string]any)
				check["status"] = "not_met"
				check["basis"] = "方法未明确变量定义"
			}
		}
	})
	root := t.TempDir()
	application, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	project, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "可返修的研究设计闭环"})
	if err != nil {
		t.Fatal(err)
	}
	starter, err := application.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "没有数据，先设计大学生睡眠日记研究", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, application, project.ID, starter.Run.ID, workflow.RunCompleted, 15*time.Second)
	adopted, err := application.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planned.Run.ID, RouteID: "design_first"})
	if err != nil || adopted.Run == nil {
		t.Fatalf("adopt=%+v err=%v", adopted, err)
	}
	failed := waitForWorkflowState(t, application, project.ID, adopted.Run.Run.ID, workflow.RunFailed, 15*time.Second)
	if failed.DeliveryAssessment == nil || failed.DeliveryAssessment.Status != "revision_required" {
		t.Fatalf("review failure not projected: %+v", failed.DeliveryAssessment)
	}
	firstReview := closedLoopReviewExecution(t, failed, 1)
	var firstReviewOutput struct {
		Findings []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"reviewFindings"`
	}
	if err := json.Unmarshal(firstReview.Output, &firstReviewOutput); err != nil || len(firstReviewOutput.Findings) != 1 || firstReviewOutput.Findings[0].ID == "" || firstReviewOutput.Findings[0].Status != "open" {
		t.Fatalf("first review findings = %s, err=%v", firstReview.Output, err)
	}
	firstFindingID := firstReviewOutput.Findings[0].ID
	var gateID, questionHash string
	for _, step := range failed.Steps {
		if step.NodeID == "delivery_gate" {
			gateID = step.ID
		}
		if step.NodeID == "question_refinement" {
			questionHash = string(step.Output)
		}
	}
	if _, err := application.WorkflowFacade.SaveDeliverable(wailstransport.SaveWorkflowDeliverableRequest{ProjectID: project.ID, RunID: failed.Run.ID, OutputName: "research_design"}); err == nil {
		t.Fatal("rejected research output was exportable")
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	application, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	application.Startup(context.Background())
	if _, err := application.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: failed.Run.ID, StepID: gateID, RevisionNodeID: "question_refinement"}); err == nil {
		t.Fatal("revision silently changed the frozen research agreement")
	}
	if _, err := application.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: failed.Run.ID, StepID: gateID, RevisionNodeID: "method_selection"}); err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, application, project.ID, failed.Run.ID, workflow.RunCompleted, 20*time.Second)
	if completed.Run.ResearchTaskID != failed.Run.ResearchTaskID || completed.DeliveryAssessment == nil || completed.DeliveryAssessment.Status != "reviewed" || completed.DeliveryAssessment.Kind != "research_design" || len(completed.DeliveryAssessment.Checks) == 0 {
		t.Fatalf("closed-loop assessment=%+v", completed.DeliveryAssessment)
	}
	for _, step := range completed.Steps {
		if step.NodeID == "question_refinement" && (step.Attempt != 1 || string(step.Output) != questionHash) {
			t.Fatal("method revision changed the original research agreement")
		}
		if step.NodeID == "method_selection" && step.Attempt != 2 {
			t.Fatalf("method attempt=%d", step.Attempt)
		}
	}
	mu.Lock()
	reviewCount, revisionCount := reviews, methodRevisions
	mu.Unlock()
	if reviewCount != 2 || revisionCount != 1 {
		t.Fatalf("reviews=%d methodRevisions=%d", reviewCount, revisionCount)
	}
	secondReview := closedLoopReviewExecution(t, completed, 2)
	var secondOutput struct {
		Findings []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"reviewFindings"`
	}
	var secondInput struct {
		History struct {
			PreviousOutputSHA256 string `json:"previousOutputSha256"`
		} `json:"reviewHistory"`
	}
	if err := json.Unmarshal(secondReview.Output, &secondOutput); err != nil || len(secondOutput.Findings) != 1 || secondOutput.Findings[0].ID != firstFindingID || secondOutput.Findings[0].Status != "resolved" {
		t.Fatalf("second review did not close the first finding: output=%s err=%v", secondReview.Output, err)
	}
	var secondStageInput json.RawMessage
	for _, step := range completed.Steps {
		if step.ID == secondReview.WorkflowStepID {
			secondStageInput = step.Input
		}
	}
	if err := json.Unmarshal(secondStageInput, &secondInput); err != nil || secondInput.History.PreviousOutputSHA256 != firstReview.OutputSHA256 {
		t.Fatalf("second review history does not bind the first review output: input=%s want=%s err=%v", secondStageInput, firstReview.OutputSHA256, err)
	}
	request := wailstransport.SaveWorkflowDeliverableRequest{ProjectID: project.ID, RunID: completed.Run.ID, OutputName: "research_design"}
	saved, err := application.WorkflowFacade.SaveDeliverable(request)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := application.WorkflowFacade.SaveDeliverable(request)
	if err != nil || reused.Created || reused.Version.ID != saved.Version.ID {
		t.Fatalf("delivery not idempotent: %v", err)
	}
	for _, format := range []artifact.ExportFormat{artifact.ExportDOCX, artifact.ExportPDF} {
		exported, err := application.ArtifactFacade.CreateTaskArtifactExport(completed.Run.ResearchTaskID, artifact.ExportCommand{ProjectID: project.ID, VersionID: saved.Version.ID, Format: format, CitationStyle: artifact.CitationGB7714})
		if err != nil {
			t.Fatalf("%s export: %v", format, err)
		}
		if exported.Export.SizeBytes <= 0 || len(exported.Export.SHA256) != 64 || exported.Export.SourceSHA256 != saved.Version.SHA256 || exported.Export.ArtifactVersionID != saved.Version.ID || exported.Export.Format != format {
			t.Fatalf("export does not preserve a nonempty immutable source: %+v", exported.Export)
		}
	}
}

func closedLoopReviewExecution(t *testing.T, detail workflow.RunDetail, attempt int) workflow.AIExecution {
	t.Helper()
	for _, execution := range detail.AIExecutions {
		if execution.WorkflowStepID != "" && execution.Attempt == attempt {
			for _, step := range detail.Steps {
				if step.ID == execution.WorkflowStepID && step.NodeID == "independent_review" && execution.Status == "completed" {
					return execution
				}
			}
		}
	}
	t.Fatalf("completed independent review attempt %d was not found: %#v", attempt, detail.AIExecutions)
	return workflow.AIExecution{}
}

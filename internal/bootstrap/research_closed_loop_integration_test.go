package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestResearchClosedLoopRevisesMethodsAfterRestartAndExports(t *testing.T) {
	testResearchClosedLoopRevisesMethodsAfterRestartAndExports(t, "")
}

func TestResearchReviewRevisionRecoversTruncatedStreamAndExports(t *testing.T) {
	testResearchClosedLoopRevisesMethodsAfterRestartAndExports(t, "truncated")
}

func TestResearchReviewRevisionRecoversProviderOutageAndExports(t *testing.T) {
	testResearchClosedLoopRevisesMethodsAfterRestartAndExports(t, "service_unavailable")
}

func TestResearchReviewRevisionKeepsCorrectionsAfterFailureRetryAndRestart(t *testing.T) {
	testResearchClosedLoopRevisesMethodsAfterRestartAndExports(t, "invalid")
}

func testResearchClosedLoopRevisesMethodsAfterRestartAndExports(t *testing.T, fault string) {
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
	endpoint := server.URL
	var revisionRequests []string
	if fault != "" {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
			if err != nil {
				t.Error(err)
				return
			}
			prompt := workflowAITestPrompt(body)
			schema, err := workflowAITestSchema(prompt)
			if err != nil {
				t.Error(err)
				return
			}
			properties, _ := schema["properties"].(map[string]any)
			if _, method := properties["methodComponents"]; method && strings.Contains(prompt, "_reviewRevision") {
				mu.Lock()
				revisionRequests = append(revisionRequests, string(body))
				count := len(revisionRequests)
				mu.Unlock()
				if count == 1 {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"discarded repair draft\"}}]}\n\n")
					if fault == "truncated" {
						_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"`)
					} else if fault == "service_unavailable" {
						_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"upstream_error\",\"code\":\"upstream_error\",\"message\":\"The service is temporarily unavailable. Please retry later.\"}}\n\n")
					} else {
						_, _ = io.WriteString(w, `data: {"choices":broken}`)
					}
					return
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			server.Config.Handler.ServeHTTP(w, r)
		}))
		defer proxy.Close()
		endpoint = proxy.URL
	}
	root := t.TempDir()
	application, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, endpoint)
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
	wantMethodAttempt := 2
	if fault == "invalid" {
		broken := waitForWorkflowState(t, application, project.ID, failed.Run.ID, workflow.RunFailed, 15*time.Second)
		var methodStepID string
		for _, step := range broken.Steps {
			if step.NodeID == "method_selection" {
				if step.Status != workflow.StepFailed || step.Attempt != 2 || step.ErrorCode != "MODEL_STREAM_INVALID" {
					t.Fatalf("unexpected fault state: %+v", step)
				}
				methodStepID = step.ID
			}
		}
		if methodStepID == "" {
			t.Fatal("missing failed method stage")
		}
		if err := application.Close(); err != nil {
			t.Fatal(err)
		}
		application, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
		if err != nil {
			t.Fatal(err)
		}
		application.Startup(context.Background())
		if _, err := application.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: failed.Run.ID, StepID: methodStepID}); err != nil {
			t.Fatal(err)
		}
		wantMethodAttempt = 3
	}
	completed := waitForWorkflowState(t, application, project.ID, failed.Run.ID, workflow.RunCompleted, 20*time.Second)
	if completed.Run.ResearchTaskID != failed.Run.ResearchTaskID || completed.DeliveryAssessment == nil || completed.DeliveryAssessment.Status != "reviewed" || completed.DeliveryAssessment.Kind != "research_design" || len(completed.DeliveryAssessment.Checks) == 0 {
		t.Fatalf("closed-loop assessment=%+v", completed.DeliveryAssessment)
	}
	for _, step := range completed.Steps {
		if step.NodeID == "question_refinement" && (step.Attempt != 1 || string(step.Output) != questionHash) {
			t.Fatal("method revision changed the original research agreement")
		}
		if step.NodeID == "method_selection" && step.Attempt != wantMethodAttempt {
			t.Fatalf("method attempt=%d", step.Attempt)
		}
		if step.NodeID == "method_selection" && strings.Contains(string(step.Output), "discarded repair draft") {
			t.Fatal("failed draft was accepted as research output")
		}
	}
	mu.Lock()
	reviewCount, revisionCount := reviews, methodRevisions
	mu.Unlock()
	if reviewCount != 2 || revisionCount != 1 {
		t.Fatalf("reviews=%d methodRevisions=%d", reviewCount, revisionCount)
	}
	if fault != "" {
		mu.Lock()
		requests := append([]string(nil), revisionRequests...)
		mu.Unlock()
		if len(requests) != 2 || !strings.Contains(requests[1], "_reviewRevision") || (fault != "invalid" && requests[0] != requests[1]) {
			t.Fatal("recovery lost frozen review corrections, repeated stages or changed interrupted turn context")
		}
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

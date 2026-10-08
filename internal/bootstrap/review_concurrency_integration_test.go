package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestIndependentReviewConcurrencyRetryPreservesPriorStagesAndDelivery(t *testing.T) {
	// The protocol-specific nested Responses event is covered by the Agent HTTP
	// tests. This full application fixture tests the same code inside a real
	// review, SQLite persistence and delivery gate, not scientific model quality.
	fixture := newResearchStarterOutputTestServer(t, nil)
	var mu sync.Mutex
	reviewRequests := 0
	limited := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if _, review := properties["reviewedInputSha256"]; review {
			mu.Lock()
			reviewRequests++
			count := reviewRequests
			mu.Unlock()
			if count == 1 {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"error\":{\"code\":\"gateway_concurrency_limit\",\"message\":\"Concurrency limit exceeded for account, please retry later\"}}\n\n")
				close(limited)
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		fixture.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Review transient concurrency fixture"})
	if err != nil {
		t.Fatal(err)
	}
	starter, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: p.ID, ResearchIdea: "没有数据，先设计大学生睡眠日记研究", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, app, p.ID, starter.Run.ID, workflow.RunCompleted, 15*time.Second)
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: p.ID, RunID: planned.Run.ID, RouteID: "design_first"})
	if err != nil || adopted.Run == nil {
		t.Fatalf("adopt: %v", err)
	}
	select {
	case <-limited:
	case <-time.After(15 * time.Second):
		t.Fatal("review did not encounter fixture concurrency limit")
	}
	during, err := app.WorkflowFacade.GetRun(p.ID, adopted.Run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	prior := map[string]workflow.Step{}
	for _, step := range during.Steps {
		if step.Status == workflow.StepCompleted && step.NodeID != "independent_review" {
			prior[step.NodeID] = step
		}
	}
	if len(prior) < 3 || during.Run.Status != workflow.RunRunning {
		t.Fatal("upstream results missing or concurrency limit immediately failed run")
	}
	finished := waitForWorkflowState(t, app, p.ID, adopted.Run.Run.ID, workflow.RunCompleted, 25*time.Second)
	for _, step := range finished.Steps {
		if old, ok := prior[step.NodeID]; ok && (old.Attempt != step.Attempt || string(old.Output) != string(step.Output)) {
			t.Fatal("concurrency retry changed or reran upstream stage")
		}
	}
	review := closedLoopReviewExecution(t, finished, 1)
	mu.Lock()
	count := reviewRequests
	mu.Unlock()
	// This route reuses its frozen Skill snapshot: one accepted logical turn,
	// plus one rejected transport attempt, rather than a second Skill load.
	if review.Status != "completed" || review.Attempt != 1 || review.ModelTurns != 1 || count != 2 {
		t.Fatalf("review did not retry in same execution: requests=%d attempt=%d turns=%d status=%s", count, review.Attempt, review.ModelTurns, review.Status)
	}
	if finished.DeliveryAssessment == nil || finished.DeliveryAssessment.Status != "reviewed" {
		t.Fatalf("review did not reach unchanged delivery gate: %+v", finished.DeliveryAssessment)
	}
	var approved bool
	var output struct {
		Approved bool `json:"approved"`
	}
	if json.Unmarshal(review.Output, &output) == nil {
		approved = output.Approved
	}
	if !approved || strings.Contains(string(review.Output), "Concurrency limit") {
		t.Fatal("failed provider attempt was committed as a review")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reloaded, err := app.WorkflowFacade.GetRun(p.ID, finished.Run.ID)
	if err != nil || reloaded.Run.Status != workflow.RunCompleted || reloaded.DeliveryAssessment == nil || reloaded.DeliveryAssessment.Status != "reviewed" {
		t.Fatalf("restart lost completion: %v", err)
	}
	if restored := closedLoopReviewExecution(t, reloaded, 1); restored.OutputSHA256 != review.OutputSHA256 || string(restored.Output) != string(review.Output) {
		t.Fatal("restart changed accepted review")
	}
	t.Log("independent review waits and retries temporary concurrency in same execution; prior stages unchanged, unchanged delivery gate passes and restart preserves completion; fixed HTTP model, no live provider")
}

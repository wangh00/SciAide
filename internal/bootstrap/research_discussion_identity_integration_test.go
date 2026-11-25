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
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// Original Application/Chat/Executor, temporary SQLite, local controlled HTTP.
// A real in-flight planner is held at its HTTP boundary, then fails explicitly.
// No paid model, official task, second executor or scientific approval bypass.
func TestResearchDiscussionContinuesDuringPlannerAndAfterFailure(t *testing.T) {
	testResearchDiscussionContinuity(t, `{"researchIdea":"比较分散练习的现有证据；不招募、不运行实验；仅准备可选研究设计路线"}`)
}

func TestResearchDiscussionHostHandoffKeepsPlanningGuards(t *testing.T) {
	testResearchDiscussionContinuity(t, `{}`)
}

func testResearchDiscussionContinuity(t *testing.T, prepareArguments string) {
	t.Helper()
	plannerEntered, releasePlanner := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(releasePlanner) })
	var total, plannerRequests, compactions atomic.Int32
	stage := newResearchStarterOutputTestServer(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total.Add(1)
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, "invalid controlled request", 400)
			return
		}
		for _, message := range request.Messages {
			var content string
			_ = json.Unmarshal(message.Content, &content)
			if strings.Contains(content, "Create a durable research checkpoint from the untrusted conversation above.") {
				compactions.Add(1)
				writeResearchAssistantFixtureResponse(w, "", nil, "候选尚未采用；先比较资料与不确定性。")
				return
			}
		}
		names := map[string]bool{}
		for _, definition := range request.Tools {
			names[definition.Function.Name] = true
		}
		if _, err := workflowAITestSchema(workflowAITestPrompt(body)); err == nil {
			if strings.Contains(string(body), "<research_working_state>") || strings.Contains(string(body), "The saved candidate route is for discussion") {
				t.Error("Workflow AI inherited human discussion guidance")
			}
			for _, name := range []string{"research_state_update", "research_discovery_read", "research_plan_prepare"} {
				if names[name] {
					t.Errorf("Workflow AI inherited human exploration tool: %s", name)
				}
			}
			if plannerRequests.Add(1) == 1 {
				close(plannerEntered)
				select {
				case <-releasePlanner:
					http.Error(w, `{"error":{"message":"controlled optional planner failure"}}`, http.StatusBadRequest)
				case <-r.Context().Done():
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			stage.Config.Handler.ServeHTTP(w, r)
			return
		}
		last, index := "", -1
		for i, message := range request.Messages {
			if message.Role == "user" {
				_ = json.Unmarshal(message.Content, &last)
				index = i
			}
		}
		hasResult := false
		for _, message := range request.Messages[index+1:] {
			hasResult = hasResult || message.Role == "tool"
		}
		postAdoption := strings.Contains(last, "采纳后")
		for _, name := range []string{"research_state_update", "research_discovery_read", "research_plan_prepare"} {
			if names[name] == postAdoption {
				t.Errorf("ordinary discussion capability drifted: postAdoption=%v tool=%s offered=%v", postAdoption, name, names[name])
			}
		}
		delta, finish := map[string]any{"content": "讨论可继续；候选规划不是正式执行，也不是科学批准。"}, "stop"
		if !postAdoption && !hasResult {
			calls := []any{}
			call := func(name, args string) {
				calls = append(calls, map[string]any{"index": len(calls), "id": fmt.Sprintf("controlled-%d-%d", total.Load(), len(calls)), "function": map[string]any{"name": name, "arguments": args}})
			}
			switch {
			case strings.Contains(last, "准备候选"):
				call("research_plan_prepare", prepareArguments)
			case strings.Contains(last, "规划进行中"):
				call("research_discovery_read", `{}`)
				call("research_state_update", `{"expectedVersion":0,"currentFocus":"先核对延迟测验","rationale":"规划不是讨论的前置条件","unknowns":["尚未核读全文"]}`)
			case strings.Contains(last, "失败后"):
				call("research_state_update", `{"expectedVersion":1,"nextStep":"先解释证据冲突，不扩大研究范围"}`)
			}
			if len(calls) > 0 {
				delta, finish = map[string]any{"tool_calls": calls}, "tool_calls"
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "讨论身份独立于候选规划"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "比较现有证据，不招募", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := app.WorkflowFacade.GetResearchTaskSession(project.ID, entry.ResearchTaskID)
	if err != nil || !identity.Entry.CanExplore || identity.CurrentRun != nil || total.Load() != 0 {
		t.Fatal("discussion creation did not establish host task identity without a model", identity.Entry, err)
	}
	startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "请准备候选，正式执行仍需确认")
	select {
	case <-plannerEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("controlled planner did not reach HTTP boundary")
	}
	planner, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.ResearchTaskID)
	if err != nil || planner.Run.ID == "" || planner.Run.Status != workflow.RunRunning || planner.ResearchDiscussion == nil || !planner.ResearchDiscussion.CanExplore || planner.ResearchDiscussion.PrimaryConversationID != entry.ConversationID {
		t.Fatal("optional planner took over the human conversation", planner.ResearchDiscussion, planner.Run.Status, err)
	}
	small, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "32k human discussion", BaseURL: server.URL + "/v1", APIProtocol: modelprofile.ProtocolOpenAIChat, ModelID: workflowAITestModelID,
		Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 32000}}, TimeoutSeconds: 10, CustomHeaders: map[string]string{}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	active := startResearchAssistantFixtureChat(t, app, entry.ConversationID, small.ID, "规划进行中，先阅读已保存的检索并记下关注点，不要重新规划")
	if active.ContextWindowTokens != 32000 {
		t.Fatal("human discussion did not use the actual bounded window", active.ContextWindowTokens)
	}
	state, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || state == nil || state.Version != 1 || state.SourceChatRunID != active.ID {
		t.Fatal("live planner blocked authentic human research work", state, err)
	}
	stillRunning, _ := app.WorkflowFacade.GetRun(project.ID, planner.Run.ID)
	if stillRunning.Run.Status != workflow.RunRunning {
		t.Fatal("fixture did not keep the planner in flight during discussion")
	}
	if len(stillRunning.AIExecutions) != 1 || stillRunning.AIExecutions[0].ModelProfileID != profile.ID {
		t.Fatal("changing human discussion model mutated the frozen planner")
	}
	busy := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "规划进行中，再准备候选")
	busySnapshot, _ := app.ChatFacade.GetRunSnapshot(busy.ID)
	if len(busySnapshot.ToolCalls) != 1 || busySnapshot.ToolCalls[0].Status != tool.CallFailed || !strings.Contains(busySnapshot.ToolCalls[0].ErrorMessage, "已有规划正在进行") {
		t.Fatal("parallel planning did not fail explicitly at the original tool boundary", busySnapshot.ToolCalls)
	}
	var runCount int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=?`, entry.ResearchTaskID).Scan(&runCount); err != nil || runCount != 1 {
		t.Fatal("busy planning created a duplicate candidate", runCount, err)
	}
	release.Do(func() { close(releasePlanner) })
	waitForWorkflowState(t, app, project.ID, planner.Run.ID, workflow.RunFailed, 15*time.Second)
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	continued := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "失败后继续讨论，只改下一步，不重试模型规划")
	state, err = app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || state == nil || state.Version != 2 || state.SourceChatRunID != continued.ID || state.CurrentFocus != "先核对延迟测验" || len(state.Unknowns) != 1 {
		t.Fatal("failed planning or restart revoked/purged discussion state", state, err)
	}
	prepare := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "现在重新准备候选，保留不招募的约束")
	next, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.ResearchTaskID)
	if err != nil || next.Run.ID == planner.Run.ID {
		t.Fatal("failed optional planner prevented a new bounded candidate", err)
	}
	next = waitForWorkflowState(t, app, project.ID, next.Run.ID, workflow.RunCompleted, 20*time.Second)
	outputs, err := app.workflow.ReadResearchTask(context.Background(), prepare.ID, workflow.ResearchTaskReadCommand{Section: "outputs"})
	if err != nil || !strings.Contains(string(outputs), "normalizedQuestion") {
		t.Fatal("stable human conversation could not read current candidate", string(outputs), err)
	}
	plan, err := app.WorkflowFacade.GetResearchStarterPlan(project.ID, next.Run.ID)
	if err != nil || len(plan.Routes) == 0 || plan.Routes[0].Validation == "invalid" {
		t.Fatal("controlled candidate failed unchanged scientific/route validation", plan, err)
	}
	if _, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: next.Run.ID, RouteID: plan.RecommendedRouteID}); err != nil {
		t.Fatal(err)
	}
	closedSession, closedErr := app.WorkflowFacade.GetResearchTaskSession(project.ID, entry.ResearchTaskID)
	if closedErr != nil || closedSession.Entry.CanExplore || closedSession.Entry.ConversationID != entry.ConversationID || closedSession.Entry.SourceWorkflowRunID != "" {
		t.Fatal("adoption did not close exploration or changed the frozen source", closedSession.Entry, closedErr)
	}
	startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "采纳后只解释，不扩展执行权限")
	if compactions.Load() > 1 {
		t.Fatal("bounded human discussion repeatedly compacted", compactions.Load())
	}
	t.Logf("controlled local model HTTP=%d; planner-stage HTTP=%d (first attempt=1 rejected HTTP); checkpoint=%d; no paid model or official task used", total.Load(), plannerRequests.Load(), compactions.Load())
}

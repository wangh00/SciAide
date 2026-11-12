package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Production bootstrap, provider adapter, database and facade; only model HTTP
// is a controlled fixture. No user database or paid model request is touched.
func TestUserInputWaitResumeRestartAndPlanning(t *testing.T) {
	var requests atomic.Int32
	secondReplyStarted := make(chan struct{}, 1)
	releaseSecondReply := make(chan struct{})
	defer close(releaseSecondReply)
	stage := newResearchStarterOutputTestServer(t, nil)
	defer stage.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if schema, err := workflowAITestSchema(workflowAITestPrompt(body)); err == nil {
			var request struct {
				Tools      []struct{ Function struct{ Name string } }
				ToolChoice json.RawMessage `json:"tool_choice"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Error(err)
			}
			question := false
			for _, definition := range request.Tools {
				question = question || definition.Function.Name == "user_input_request"
			}
			properties, _ := schema["properties"].(map[string]any)
			_, review := properties["reviewedInputSha256"]
			// Requiring an action does not imply submission-only: current
			// review exploration offers both native completion and needed IO.
			submitting := len(request.Tools) > 0
			for _, d := range request.Tools {
				submitting = submitting && (d.Function.Name == "submit_stage_result" || d.Function.Name == "submit_stage_repair")
			}
			submitting = submitting && (string(request.ToolChoice) == `"required"` || len(request.ToolChoice) > 0 && request.ToolChoice[0] == '{')
			if question != (review && !submitting) {
				t.Errorf("incorrect frozen question scope: review=%v submitting=%v question=%v", review, submitting, question)
			}
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			stage.Config.Handler.ServeHTTP(w, r)
			return
		}
		requests.Add(1)
		var req struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		_ = json.Unmarshal(body, &req)
		answer, secondAnswer, dismissed, planned, proposed := false, false, false, false, false
		for _, m := range req.Messages {
			if m.Role == "tool" {
				answer = answer || strings.Contains(string(m.Content), "自己的方向")
				secondAnswer = secondAnswer || strings.Contains(string(m.Content), "第二次明确回答")
				dismissed = dismissed || strings.Contains(string(m.Content), "用户暂不回答")
				planned = planned || strings.Contains(string(m.Content), "Candidate route planning has started")
				proposed = proposed || strings.Contains(string(m.Content), "Pending revision proposal saved")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{}
		finish := "tool_calls"
		if dismissed {
			delta["content"] = "暂不回答也可以继续讨论，不自动选择方向。"
			finish = "stop"
		} else if secondAnswer {
			secondReplyStarted <- struct{}{}
			select {
			case <-releaseSecondReply:
			case <-r.Context().Done():
				return
			}
			delta["content"] = "两次回答均已收到，仍是同一轮对话。"
			finish = "stop"
		} else if answer && strings.Contains(string(body), "two-question-cycles") {
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "ask-second", "function": map[string]any{"name": "user_input_request", "arguments": `{"questions":[{"id":"scope","question":"具体研究范围？","required":true}]}`}}}
		} else if !answer {
			if !strings.Contains(string(body), "user_input_request") {
				t.Error("ordinary discussion lacks question tool")
			}
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "ask", "function": map[string]any{"name": "user_input_request", "arguments": `{"questions":[{"id":"goal","question":"你想得到什么？","required":true,"options":[{"id":"report","label":"报告","description":"汇总结果"},{"id":"plan","label":"方案","description":"先设计研究"}]}]}`}}}
		} else if strings.Contains(string(body), "prepare-after-answer") && strings.Contains(string(body), `"name":"research_plan_prepare"`) && !planned {
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "prepare", "function": map[string]any{"name": "research_plan_prepare", "arguments": `{"researchIdea":"自己的方向：先设计睡眠研究方案"}`}}}
		} else if strings.Contains(string(body), "revise-after-answer") && !proposed {
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "propose", "function": map[string]any{"name": "research_revision_propose", "arguments": `{"nodeId":"method_selection","summary":"按回答补充变量定义","changes":["保留原研究对象","明确变量定义"],"reason":"用户回答明确了方法调整范围"}`}}}
		} else {
			delta["content"] = "已收到自己的方向，继续原对话。"
			finish = "stop"
		}
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
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "问题夹具"})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(runID string, status chat.RunStatus) wailstransport.RunSnapshot {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			snap, e := app.ChatFacade.GetRunSnapshot(runID)
			if e != nil {
				t.Fatal(e)
			}
			if snap.Run.Status == status {
				return snap
			}
			if snap.Run.Status == chat.RunFailed {
				t.Fatalf("failed: %+v", snap.Run)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("wait timeout", runID, status)
		return wailstransport.RunSnapshot{}
	}
	start := func(conversationID, text string) chat.Run {
		client, _ := id.New()
		run, e := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: conversationID, Text: text, ClientMessageID: client, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
		if e != nil {
			t.Fatal(e)
		}
		return run
	}
	// Ordinary chat waits without an approval, timeout, model retry or new Run.
	conv, err := app.ConversationFacade.CreateConversation(wailstransport.CreateConversationRequest{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	run := start(conv.ID, "需求模糊，问我")
	snap := wait(run.ID, chat.RunWaitingUserInput)
	if snap.UserInput == nil || len(snap.PendingApprovals) != 0 {
		t.Fatal("question incorrectly projected", snap)
	}
	before := requests.Load()
	time.Sleep(150 * time.Millisecond)
	if requests.Load() != before {
		t.Fatal("model continued before user")
	}
	response := userinput.Response{ConversationID: conv.ID, RunID: run.ID, CallID: snap.UserInput.CallID, Answers: userinput.Answers{"goal": {Text: "自己的方向"}}}
	wrong := response
	wrong.ConversationID = "unrelated"
	if _, err = app.ChatFacade.RespondToUserInput(wrong); err == nil {
		t.Fatal("cross conversation accepted")
	}
	// Close/reopen preserves the human wait, unlike interrupted execution.
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	snap = wait(run.ID, chat.RunWaitingUserInput)
	if snap.UserInput == nil {
		t.Fatal("pending question lost on restart")
	}
	if _, err = app.ChatFacade.RespondToUserInput(response); err != nil {
		t.Fatal(err)
	}
	completed := wait(run.ID, chat.RunCompleted)
	if completed.Run.ModelTurns != 2 {
		t.Fatal("not same run", completed.Run)
	}
	count := requests.Load()
	if _, err = app.ChatFacade.RespondToUserInput(response); err != nil {
		t.Fatal("exact retry failed", err)
	}
	if requests.Load() != count {
		t.Fatal("retry called model")
	}
	response.Answers["goal"] = userinput.Answer{Text: "different"}
	if _, err = app.ChatFacade.RespondToUserInput(response); err == nil {
		t.Fatal("overwrote answer")
	}
	// A later greeting/question can ask again; cancelling invalidates its reply.
	later := start(conv.ID, "再问一次")
	pending := wait(later.ID, chat.RunWaitingUserInput)
	if err = app.ChatFacade.CancelRun(later.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: conv.ID, RunID: later.ID, CallID: pending.UserInput.CallID, Dismiss: true}); err == nil {
		t.Fatal("cancelled reply accepted")
	}
	// Explicit skipping returns an actual tool result, not an invented choice.
	skipped := start(conv.ID, "暂时不回答也继续讨论")
	pending = wait(skipped.ID, chat.RunWaitingUserInput)
	if _, err = app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: conv.ID, RunID: skipped.ID, CallID: pending.UserInput.CallID, Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	completed = wait(skipped.ID, chat.RunCompleted)
	if completed.Run.ModelTurns != 2 {
		t.Fatal("skip did not continue same run", completed.Run)
	}
	// Multiple questions in one Run have distinct identities. Retrying an
	// older answer while the latest continuation runs must not enqueue a replay.
	multi := start(conv.ID, "two-question-cycles")
	pending = wait(multi.ID, chat.RunWaitingUserInput)
	first := userinput.Response{ConversationID: conv.ID, RunID: multi.ID, CallID: pending.UserInput.CallID, Answers: userinput.Answers{"goal": {Text: "自己的方向"}}}
	if _, err = app.ChatFacade.RespondToUserInput(first); err != nil {
		t.Fatal(err)
	}
	pending = wait(multi.ID, chat.RunWaitingUserInput)
	if pending.UserInput.CallID == first.CallID {
		t.Fatal("second question reused first identity")
	}
	// After restart, no in-memory resume key for the first question remains.
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	pending = wait(multi.ID, chat.RunWaitingUserInput)
	second := userinput.Response{ConversationID: conv.ID, RunID: multi.ID, CallID: pending.UserInput.CallID, Answers: userinput.Answers{"scope": {Text: "第二次明确回答"}}}
	if _, err = app.ChatFacade.RespondToUserInput(second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondReplyStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("latest continuation did not start")
	}
	for _, retry := range []userinput.Response{first, second} {
		if _, err = app.ChatFacade.RespondToUserInput(retry); err != nil {
			t.Fatal("exact retry during later continuation", err)
		}
	}
	releaseSecondReply <- struct{}{}
	completed = wait(multi.ID, chat.RunCompleted)
	if completed.Run.ModelTurns != 3 {
		t.Fatal("multiple answers did not stay in same run", completed.Run)
	}
	count = requests.Load()
	time.Sleep(100 * time.Millisecond)
	if requests.Load() != count {
		t.Fatal("old answer scheduled an extra model continuation")
	}
	// Opening research: answer persists as genuine human context, then the same
	// model turn can invoke the existing planner, without bypassing ownership.
	entry, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "研究想法", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, DiscussionOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	research := start(entry.Run.ConversationID, "prepare-after-answer")
	pending = wait(research.ID, chat.RunWaitingUserInput)
	_, err = app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: research.ConversationID, RunID: research.ID, CallID: pending.UserInput.CallID, Answers: userinput.Answers{"goal": {Text: "自己的方向"}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(research.ID, chat.RunCompleted)
	latest, e := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.Run.ResearchTaskID)
	if e != nil || latest.Run.ID == entry.Run.ID {
		t.Fatal("answer could not prepare plan", e)
	}
	waitForWorkflowState(t, app, project.ID, latest.Run.ID, workflow.RunCompleted, 25*time.Second)
	var history string
	_ = app.store.DB().QueryRow(`SELECT inputs_json FROM workflow_runs WHERE id=?`, latest.Run.ID).Scan(&history)
	if !strings.Contains(history, "自己的方向") {
		t.Fatal("planner lost human answer")
	}
	// Reproduce post-delivery ordinary questioning in an adopted finished task.
	plan, err := app.WorkflowFacade.GetResearchStarterPlan(project.ID, latest.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: latest.Run.ID, RouteID: plan.Routes[0].RouteID})
	if err != nil || adopted.Run == nil {
		t.Fatal("adopt", err)
	}
	delivered := waitForWorkflowState(t, app, project.ID, adopted.Run.Run.ID, workflow.RunCompleted, 25*time.Second)
	post := start(delivered.Run.ConversationID, "交付后再问我")
	pending = wait(post.ID, chat.RunWaitingUserInput)
	if _, err = app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: post.ConversationID, RunID: post.ID, CallID: pending.UserInput.CallID, Answers: userinput.Answers{"goal": {Text: "自己的方向"}}}); err != nil {
		t.Fatal(err)
	}
	wait(post.ID, chat.RunCompleted)
	current, _ := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.Run.ResearchTaskID)
	if current.Run.ID != delivered.Run.ID {
		t.Fatal("answer automatically revised task")
	}
	// The answer can also feed a revision proposal; latest-human checks stay
	// exact, and confirmation is still required to execute it.
	revision := start(delivered.Run.ConversationID, "revise-after-answer")
	pending = wait(revision.ID, chat.RunWaitingUserInput)
	if _, err = app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: revision.ConversationID, RunID: revision.ID, CallID: pending.UserInput.CallID, Answers: userinput.Answers{"goal": {Text: "自己的方向"}}}); err != nil {
		t.Fatal(err)
	}
	wait(revision.ID, chat.RunCompleted)
	proposals, err := app.WorkflowFacade.ListResearchRevisionProposals(project.ID, delivered.Run.ID)
	if err != nil || len(proposals) != 1 || !proposals[0].CanConfirm {
		t.Fatalf("question reply cannot propose revision: %+v %v", proposals, err)
	}
	timeline, err := app.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: project.ID, TaskID: entry.Run.ResearchTaskID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	humanAnswer := false
	for _, e := range timeline.Entries {
		if e.Message != nil && e.Message.RunID == revision.ID && e.Message.Role == "user" && e.Message.ID != revision.UserMessageID {
			humanAnswer = true
		}
	}
	if !humanAnswer {
		t.Fatal("submitted answer missing from waterfall history")
	}
	// No FK damage or invented approval records.
	rows, err := app.store.DB().Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}

package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestDirectResearchTaskBudgetStopPreservesStateAndDoesNotReplay(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid controlled request", 400)
			return
		}
		lastUser, latest, hasResult := "", -1, false
		for i, message := range request.Messages {
			if message.Role == "user" {
				_ = json.Unmarshal(message.Content, &lastUser)
				latest = i
			}
		}
		for _, message := range request.Messages[latest+1:] {
			hasResult = hasResult || message.Role == "tool"
		}
		if strings.Contains(lastUser, "seed-state") && !hasResult {
			writeResearchTaskVerticalSliceResponse(w, "research_state_update", map[string]any{
				"expectedVersion": 0, "currentFocus": "保留这条已发布记录", "rationale": "这是停止前已确认存在的工作记录", "nextStep": "预算不足时停止，而不是重写历史",
			}, "")
			return
		}
		writeResearchTaskVerticalSliceResponse(w, "", nil, "受控记录已保存；没有生成候选或执行路线。")
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
	setWindow := func(window int) {
		if _, err := app.store.DB().Exec(`UPDATE model_profile_models SET context_window_tokens=?,auto_compact_token_limit=?,context_window_source='manual' WHERE profile_id=?`, window, window*9/10, profile.ID); err != nil {
			t.Fatal(err)
		}
	}
	setWindow(32000)
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "直接任务预算停止"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{
		ProjectID: project.ID, ResearchIdea: "预算停止不丢原记录", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "seed-state")
	state, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || state == nil || state.Version != 1 {
		t.Fatal("controlled state seed failed", state, err)
	}
	before := *state
	setWindow(16000)
	beforeRequests := requests.Load()
	messageID, _ := id.New()
	run, err := app.ChatFacade.StartChat(chat.StartCommand{
		ConversationID: entry.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
		ClientMessageID: messageID, Text: "this turn must stop before model HTTP when the real request cannot fit",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var snapshot wails.RunSnapshot
	for {
		snapshot, err = app.ChatFacade.GetRunSnapshot(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Run.Status == chat.RunCompleted || snapshot.Run.Status == chat.RunFailed || snapshot.Run.Status == chat.RunCancelled || snapshot.Run.Status == chat.RunInterrupted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("budget-stopped run did not converge", snapshot.Run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot.Run.Status != chat.RunFailed || snapshot.Run.ErrorCode != "CONTEXT_BUILD_FAILED" {
		t.Fatal("direct task did not stop at the final request budget", snapshot.Run)
	}
	if requests.Load() != beforeRequests {
		t.Fatal("budget stop issued model HTTP", requests.Load(), beforeRequests)
	}
	after, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || after == nil || !reflect.DeepEqual(before, *after) {
		t.Fatal("budget stop changed the published work record", after, err)
	}
	var runs int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=?`, entry.ResearchTaskID).Scan(&runs); err != nil || runs != 0 {
		t.Fatal("budget stop fabricated a Workflow", runs, err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopened, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || reopened == nil || !reflect.DeepEqual(before, *reopened) || requests.Load() != beforeRequests {
		t.Fatal("restart replayed or changed a budget-stopped direct task", reopened, requests.Load(), beforeRequests, err)
	}
}

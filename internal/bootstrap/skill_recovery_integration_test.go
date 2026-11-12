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
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Actual bootstrap, tool registry/executor, provider adapter, temporary SQLite,
// persistence and restart. HTTP simulates imperfect model calls; it is NOT a
// real-model quality claim and never runs a user's task/database.
func TestResearchSkillCorrectionsReachRealQuestionAndResumeSameRun(t *testing.T) {
	var requests atomic.Int32
	var app *Application
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
			return
		}
		lastResult := ""
		for _, m := range req.Messages {
			if m.Role == "tool" {
				var text string
				if err := json.Unmarshal(m.Content, &text); err != nil {
					t.Error(err)
				}
				lastResult = text
			}
		}
		hasTool := func(name string) bool {
			for _, item := range req.Tools {
				if item.Function.Name == name {
					return true
				}
			}
			return false
		}
		checkSnapshotCount := func(expected int) {
			var count int
			if e := app.store.DB().QueryRow(`SELECT COUNT(*) FROM run_dynamic_skills`).Scan(&count); e != nil || count != expected {
				t.Errorf("snapshot count=%d want=%d err=%v", count, expected, e)
			}
		}
		n := requests.Add(1)
		name, arguments, content := "skill_load", "", ""
		switch n {
		case 1:
			if !hasTool(name) || !hasTool("user_input_request") || !strings.Contains(string(body), "plain text cannot create one") {
				t.Error("missing actual tools/question guidance")
			}
			arguments = `{"category":"research","limit":7500,"name":"research","offset":0,"section":"section-0"}`
		case 2:
			if !strings.Contains(lastResult, "是分类") || !strings.Contains(lastResult, `"loaded":false`) || strings.Contains(lastResult, "research-workflows") || !strings.Contains(lastResult, "sleep-methods") {
				t.Errorf("invalid category correction: %s", lastResult)
			}
			checkSnapshotCount(0)
			// A model may still name an unavailable Skill from old context;
			// report why and preserve the conversation instead of hiding it.
			arguments = `{"name":"research-workflows","section":"section-0"}`
		case 3:
			if !strings.Contains(lastResult, "当前不可用") || !strings.Contains(lastResult, "对应工具尚未注册") || strings.Contains(lastResult, "工具执行失败") {
				t.Errorf("generic unavailable error: %s", lastResult)
			}
			checkSnapshotCount(0)
			arguments = `{"name":"sleep-methods","section":"section-0"}`
		case 4:
			if !strings.Contains(lastResult, "不存在章节") || !strings.Contains(lastResult, "section-1, section-2") {
				t.Errorf("invalid section lacks actual selector: %s", lastResult)
			}
			checkSnapshotCount(0)
			arguments = `{"name":"sleep-methods"}`
		case 5:
			if !strings.Contains(lastResult, `"loaded":true`) || !strings.Contains(lastResult, "讨论变量与设计，不虚构数据") {
				t.Errorf("valid instructions not loaded: %s", lastResult)
			}
			checkSnapshotCount(1)
			name = "user_input_request"
			arguments = `{"questions":[{"id":"goal","header":"产物","question":"你希望先获得研究方案还是现有文献梳理？","required":true,"options":[{"id":"design","label":"研究方案","description":"确定对象、变量与设计"},{"id":"literature","label":"文献梳理","description":"比较已有证据"}]}]}`
		case 6:
			if !strings.Contains(lastResult, "先做研究方案，不开展招募") {
				t.Errorf("human answer lost on restart: %s", lastResult)
			}
			checkSnapshotCount(1)
			content = "收到，先讨论研究方案，不开展招募；还未确认路线或执行研究。"
		case 7:
			content = "你好，可以继续讨论，不必加载 Skill 或重新规划。"
		default:
			t.Errorf("unexpected model retry %d", n)
			content = "unexpected"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{}
		finish := "stop"
		if content != "" {
			delta["content"] = content
		} else {
			if !hasTool(name) {
				t.Errorf("fixture tool %s was not offered", name)
			}
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": fmt.Sprintf("skill-recovery-%d", n), "function": map[string]any{"name": name, "arguments": arguments}}}
			finish = "tool_calls"
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
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "睡眠科研开局恢复夹具"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SkillFacade.WriteUserSkill("sleep-methods", "---\nname: sleep-methods\ndescription: 研究设计夹具\ncategory: research\n---\n# Overview\n讨论变量与设计，不虚构数据\n## Boundaries\n先明确产物，未确认路线不执行研究"); err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "睡眠与学习状态", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, DiscussionOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(id string, status chat.RunStatus) wailstransport.RunSnapshot {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			snap, e := app.ChatFacade.GetRunSnapshot(id)
			if e != nil {
				t.Fatal(e)
			}
			if snap.Run.Status == status {
				return snap
			}
			if snap.Run.Status == chat.RunFailed {
				t.Fatalf("conversation died: %+v", snap.Run)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("wait timeout", id, status)
		return wailstransport.RunSnapshot{}
	}
	clientID, _ := id.New()
	run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: entry.Run.ConversationID, Text: "我想研究大学生睡眠和学习状态的关系，但还没想好怎么做，你帮我看看。", ClientMessageID: clientID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	pending := wait(run.ID, chat.RunWaitingUserInput)
	if pending.UserInput == nil || requests.Load() != 5 || len(pending.PendingApprovals) != 0 {
		t.Fatalf("not a real question wait: %+v requests=%d", pending.UserInput, requests.Load())
	}
	// No retries/repair loops continue while waiting for the actual user.
	time.Sleep(150 * time.Millisecond)
	if requests.Load() != 5 {
		t.Fatal("question wait called model again")
	}
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	pending = wait(run.ID, chat.RunWaitingUserInput)
	if pending.UserInput == nil {
		t.Fatal("question lost on restart")
	}
	if _, err = app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: run.ConversationID, RunID: run.ID, CallID: pending.UserInput.CallID, Answers: userinput.Answers{"goal": {OptionIDs: []string{"design"}, Text: "先做研究方案，不开展招募"}}}); err != nil {
		t.Fatal(err)
	}
	completed := wait(run.ID, chat.RunCompleted)
	if completed.Run.ModelTurns != 6 || requests.Load() != 6 {
		t.Fatalf("unexpected retries/new run: %+v requests=%d", completed.Run, requests.Load())
	}
	current, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.Run.ResearchTaskID)
	if err != nil || current.Run.ID != entry.Run.ID {
		t.Fatal("answer adopted/prepared a route without model call", err)
	}
	calls, err := sqlite.NewToolRepository(app.store.DB()).ListByRun(context.Background(), run.ID)
	if err != nil || len(calls) != 5 {
		t.Fatalf("call persistence: %d %v", len(calls), err)
	}
	want := []tool.CallStatus{tool.CallCompleted, tool.CallFailed, tool.CallFailed, tool.CallCompleted, tool.CallCompleted}
	for i, call := range calls {
		if call.Status != want[i] || call.Result == nil || strings.Contains(call.Result.Text, "工具执行失败") {
			t.Fatalf("call %d: %+v", i, call)
		}
	}
	clientID, _ = id.New()
	greeting, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: entry.Run.ConversationID, Text: "你好", ClientMessageID: clientID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	completed = wait(greeting.ID, chat.RunCompleted)
	if completed.UserInput != nil || completed.Run.ModelTurns != 1 {
		t.Fatal("greeting forced another question or Skill load")
	}
}

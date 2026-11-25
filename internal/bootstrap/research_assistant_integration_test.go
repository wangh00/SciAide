package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/research/connectors"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// Exercise the original Application/Chat/Registry/Executor with controlled
// HTTP and a temporary SQLite root. This is not a real-model science test.
func TestResearchAssistantExploresRecordsAndChangesFocusWithoutRoute(t *testing.T) {
	const initialGoal = "比较分散练习与集中练习的现有证据，暂不生成完整执行路线"
	const firstFocus = "先核对延迟测验的证据边界"
	const changedFocus = "比较冲突是否来自任务和延迟时长不同"
	var mu sync.Mutex
	turns := map[string]int{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
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
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
			http.Error(w, "invalid controlled model request", 400)
			return
		}
		lastUser, latestIndex, all := "", -1, ""
		contents := make([]string, len(request.Messages))
		for i, message := range request.Messages {
			_ = json.Unmarshal(message.Content, &contents[i])
			all += contents[i] + "\n"
			if message.Role == "user" {
				lastUser, latestIndex = contents[i], i
			}
		}
		tools := map[string]bool{}
		for _, definition := range request.Tools {
			tools[definition.Function.Name] = true
		}
		key := "ordinary"
		for _, name := range []string{"先找现有证据", "新证据互相矛盾", "只解释术语"} {
			if strings.Contains(lastUser, name) {
				key = name
			}
		}
		mu.Lock()
		turns[key]++
		turn := turns[key]
		mu.Unlock()
		fail := func(reason string) {
			t.Error(reason)
			http.Error(w, reason, 400)
		}
		if key == "ordinary" {
			if tools["research_state_update"] || strings.Contains(all, "<research_working_state>") {
				fail("ordinary chat received another research task's state or write capability")
				return
			}
			writeResearchAssistantFixtureResponse(w, "", nil, "普通对话仍可直接解释，不创建科研状态。")
			return
		}
		if !tools["research_state_update"] || !tools["research_discovery_search"] {
			fail("research discussion lacks its real optional capabilities")
			return
		}
		hasLatestTool := func(text string) bool {
			for i := latestIndex + 1; i < len(request.Messages); i++ {
				if request.Messages[i].Role == "tool" && strings.Contains(contents[i], text) {
					return true
				}
			}
			return false
		}
		switch key {
		case "先找现有证据":
			switch turn {
			case 1:
				writeResearchAssistantFixtureResponse(w, "research_discovery_search", map[string]any{"query": "spaced practice university retention", "sourceIds": []string{"eric"}, "limit": 1}, "")
			case 2:
				if !hasLatestTool("Controlled retention abstract") || !hasLatestTool("untrusted") {
					fail("working note was not based on the actual unverified source response")
					return
				}
				writeResearchAssistantFixtureResponse(w, "research_state_update", map[string]any{"expectedVersion": 0, "currentFocus": firstFocus, "rationale": "当前只有一份摘要级发现，不能概括总体效应。", "nextStep": "核对是否确有延迟结局，再决定是否补查。", "unknowns": []string{"全文未核读", "单份记录不能代表总体证据"}}, "")
			case 3:
				if !hasLatestTool("pending_tool_completion") {
					fail("state update did not traverse the real tool executor")
					return
				}
				writeResearchAssistantFixtureResponse(w, "", nil, "先保留摘要级发现和未知项，不生成路线、不作科学批准。")
			default:
				fail("first exploration repeated an already completed action")
			}
		case "新证据互相矛盾":
			if turn == 1 && !strings.Contains(all, `"version":1`) {
				fail("next turn lost the saved host-bound working state")
				return
			}
			if turn == 1 {
				writeResearchAssistantFixtureResponse(w, "research_state_read", map[string]any{"expectedVersion": 1, "field": "currentFocus"}, "")
			} else if turn == 2 {
				if !hasLatestTool(firstFocus) {
					fail("current focus was not read through the bound state reader")
					return
				}
				writeResearchAssistantFixtureResponse(w, "research_state_update", map[string]any{"expectedVersion": 1, "currentFocus": changedFocus, "rationale": "用户提出了冲突证据，应先比较条件，而不是坚持原先的下一步。", "nextStep": "请用户指出冲突记录，尚不扩大检索或执行分析。"}, "")
			} else if turn == 3 {
				writeResearchAssistantFixtureResponse(w, "", nil, "已调整研究关注点；下一步只是建议，尚未执行。")
			} else {
				fail("focus adjustment started unnecessary research work")
			}
		case "只解释术语":
			if turn != 1 || !strings.Contains(all, `"version":2`) {
				fail("simple research discussion lost state or was forced through a workflow")
				return
			}
			writeResearchAssistantFixtureResponse(w, "", nil, "延迟测验是在学习结束一段时间后进行的测验；这里不改变研究记录。")
		}
	}))
	defer model.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/eric/" || !strings.Contains(r.URL.Query().Get("search"), "retention") {
			t.Error("unexpected scholarly source request", r.URL.Path)
			http.Error(w, "wrong source request", 400)
			return
		}
		fmt.Fprint(w, `{"response":{"numFound":1,"docs":[{"id":"EJ1234567","title":"Controlled retention abstract","description":"An unverified abstract on university students and delayed retention.","publicationdateyear":2025,"e_fulltextauth":0}]}}`)
	}))
	defer source.Close()
	endpoint, _ := url.Parse(source.URL)
	transport := &ericFixtureTransport{endpoint: endpoint, transport: http.DefaultTransport}
	root := t.TempDir()
	options := Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, ResearchConnectors: connectors.DefaultWithTransport(transport)}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, model.URL)
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "持续科研助手夹具"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: initialGoal, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID); err != nil || value != nil {
		t.Fatal("new discussion fabricated working state", value, err)
	}
	first := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "先找现有证据，只用ERIC，随后记下未知项，不规划。")
	state, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || state == nil || state.Version != 1 || state.CurrentFocus != firstFocus || state.SourceChatRunID != first.ID || state.Status != "working_note" {
		t.Fatal("completed tool did not publish its bound working note", state, err)
	}
	second := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "新证据互相矛盾，先改变关注点，不要继续原先下一步，也不要生成路线。")
	state, err = app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || state == nil || state.Version != 2 || state.CurrentFocus != changedFocus || state.SourceChatRunID != second.ID || state.SourceCallID == "" || !reflect.DeepEqual(state.Unknowns, []string{"全文未核读", "单份记录不能代表总体证据"}) {
		t.Fatal("research did not adapt inside the same task", state, err)
	}
	third := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "只解释术语：延迟测验是什么意思？不修改研究记录。")
	if first.ModelTurns != 3 || second.ModelTurns != 3 || third.ModelTurns != 1 || transport.calls.Load() != 1 {
		t.Fatal("working-state changes repeated source work or forced extra stages", first.ModelTurns, second.ModelTurns, third.ModelTurns, transport.calls.Load())
	}
	current, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.ResearchTaskID)
	if err == nil || current.Run.ID != "" || len(current.AIExecutions) != 0 {
		t.Fatal("working notes changed the goal/route or started a workflow", current, err)
	}
	var runs int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=?`, entry.ResearchTaskID).Scan(&runs); err != nil || runs != 0 {
		t.Fatal("exploration created a hidden planner or second executor", runs, err)
	}
	ordinary, err := app.ConversationFacade.CreateConversation(wails.CreateConversationRequest{ProjectID: project.ID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	startResearchAssistantFixtureChat(t, app, ordinary.ID, profile.ID, "普通聊天也能解释研究术语。")
	beforeRestart := *state
	mu.Lock()
	requestsBefore := 0
	for _, n := range turns {
		requestsBefore += n
	}
	mu.Unlock()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	app = reopened
	app.Startup(context.Background())
	state, err = app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || state == nil || !reflect.DeepEqual(beforeRestart, *state) {
		t.Fatal("restart lost or regenerated the working state", state, err)
	}
	mu.Lock()
	requestsAfter := 0
	for _, n := range turns {
		requestsAfter += n
	}
	mu.Unlock()
	if requestsAfter != requestsBefore || requestsAfter != 8 {
		t.Fatal("read-only restart executed research", requestsBefore, requestsAfter)
	}
}

func writeResearchAssistantFixtureResponse(w http.ResponseWriter, name string, arguments map[string]any, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	delta := map[string]any{"content": text}
	finish := "stop"
	if name != "" {
		encoded, _ := json.Marshal(arguments)
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "controlled-" + name, "function": map[string]any{"name": name, "arguments": string(encoded)}}}}
		finish = "tool_calls"
	}
	chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
}

func startResearchAssistantFixtureChat(t *testing.T, app *Application, conversationID, profileID, text string) chat.Run {
	t.Helper()
	messageID, _ := id.New()
	run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: conversationID, ModelProfileID: profileID, ModelID: workflowAITestModelID, ClientMessageID: messageID, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := app.ChatFacade.GetRunSnapshot(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Run.Status == chat.RunCompleted {
			for _, call := range snapshot.ToolCalls {
				if call.ToolName == workflow.ResearchStateUpdateTool && (call.Status != tool.CallCompleted || call.Result == nil || call.Result.Status != tool.ResultSuccess) {
					t.Fatal("working-state invocation was not successfully completed", call)
				}
			}
			return snapshot.Run
		}
		if snapshot.Run.Status == chat.RunFailed || snapshot.Run.Status == chat.RunCancelled || snapshot.Run.Status == chat.RunInterrupted {
			t.Fatal("controlled discussion failed", snapshot.Run.ErrorCode, snapshot.Run.ErrorMessage, snapshot.Run.ErrorDetails)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("controlled discussion did not converge")
	return run
}

func TestResearchAssistantRecordsStateAfterQuestionAnswerInSameRun(t *testing.T) {
	const answer = "先比较本科生的延迟测验，不做总体因果判断"
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		switch n {
		case 1:
			writeResearchAssistantFixtureResponse(w, "user_input_request", map[string]any{"questions": []any{map[string]any{"id": "scope", "question": "先比较哪类证据？", "required": true}}}, "")
		case 2:
			if !strings.Contains(string(body), answer) {
				t.Error("question answer was not returned to the same model run")
			}
			writeResearchAssistantFixtureResponse(w, "research_state_update", map[string]any{"expectedVersion": 0, "currentFocus": answer, "rationale": "用户刚通过问题卡明确了当前关注点。", "nextStep": "先比较条件差异，尚未执行。", "unknowns": []string{"原文尚待核读"}}, "")
		case 3:
			if !strings.Contains(string(body), "pending_tool_completion") {
				t.Error("a valid question continuation could not save its working state")
			}
			writeResearchAssistantFixtureResponse(w, "", nil, "已记录当前关注，保持原研究目标和执行边界。")
		default:
			t.Error("question continuation repeated an action or spawned planning")
			writeResearchAssistantFixtureResponse(w, "", nil, "unexpected extra request")
		}
	}))
	defer server.Close()
	app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "问题卡后的研究状态"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "先讨论证据边界，不生成路线", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	messageID, _ := id.New()
	run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: entry.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, ClientMessageID: messageID, Text: "请先问我一个必要问题。"})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(status chat.RunStatus) wails.RunSnapshot {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			snapshot, err := app.ChatFacade.GetRunSnapshot(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Run.Status == status {
				return snapshot
			}
			if snapshot.Run.Status == chat.RunFailed {
				t.Fatal("question continuation failed", snapshot.Run.ErrorCode, snapshot.Run.ErrorDetails)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("question continuation did not converge", status)
		return wails.RunSnapshot{}
	}
	pending := wait(chat.RunWaitingUserInput)
	if pending.UserInput == nil {
		t.Fatal("question card is missing")
	}
	if _, err := app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: entry.ConversationID, RunID: run.ID, CallID: pending.UserInput.CallID, Answers: userinput.Answers{"scope": {Text: answer}}}); err != nil {
		t.Fatal(err)
	}
	wait(chat.RunCompleted)
	state, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
	if err != nil || state == nil || state.CurrentFocus != answer || state.SourceChatRunID != run.ID || state.SourceUserMessageID == run.UserMessageID {
		t.Fatal("state must bind the real question answer, not the initial user message", state, err)
	}
	var latestUserID string
	if err := app.store.DB().QueryRow(`SELECT id FROM messages WHERE conversation_id=? AND role='user' ORDER BY rowid DESC LIMIT 1`, entry.ConversationID).Scan(&latestUserID); err != nil || state.SourceUserMessageID != latestUserID {
		t.Fatal("state answer identity mismatch", latestUserID, state.SourceUserMessageID, err)
	}
	mu.Lock()
	n := requests
	mu.Unlock()
	if n != 3 {
		t.Fatal("question answer unexpectedly invoked planning or repeated a tool", n)
	}
}

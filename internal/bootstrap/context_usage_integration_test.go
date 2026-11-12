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
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real adapters, bootstrap, tools, wait/resume and reopened SQLite; only the
// remote model is synthetic. No formal DB or commercial endpoint is touched.
func TestContextUsageRealProtocolsContinueAndRestoreQuestion(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				var req map[string]json.RawMessage
				if err := json.Unmarshal(data, &req); err != nil {
					t.Error(err)
					return
				}
				n := int(requests.Add(1))
				if n >= 3 && protocol != modelprofile.ProtocolOpenAIChat {
					// These full native groups would exceed the unchanged 180K
					// automatic threshold under the old character fallback.
					if len(data) <= 190000 || strings.Count(string(data), strings.Repeat("x", 95000)) != 2 {
						t.Error("native state was silently omitted, truncated or duplicated")
					}
				}
				name, args, input := "skill_load", `{}`, 10000
				switch n {
				case 1:
				case 2:
					input = 30000
				case 3:
					input, name = 55824, "user_input_request"
					args = `{"questions":[{"id":"scope","question":"这轮先做什么？","required":true,"options":[{"id":"design","label":"研究设计"},{"id":"review","label":"文献比较"}]}]}`
				case 4:
					input, name, args = 60000, "", ""
					if !strings.Contains(string(data), "先做研究设计") {
						t.Error("resumed question answer missing from actual request")
					}
				default:
					t.Errorf("unexpected model/compaction request %d", n)
					name, args = "", ""
				}
				emitContextUsageFixture(t, w, protocol, n, name, args, input)
			}))
			defer server.Close()
			root := t.TempDir()
			app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "budget fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 200000, AutoCompactTokenLimit: 180000}}, TimeoutSeconds: 10, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "context accounting fixture"})
			if err != nil {
				t.Fatal(err)
			}
			entry, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "大学生睡眠与学习状态", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, DiscussionOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			wait := func(runID string, status chat.RunStatus) wailstransport.RunSnapshot {
				t.Helper()
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					snap, e := app.ChatFacade.GetRunSnapshot(runID)
					if e != nil {
						t.Fatal(e)
					}
					if snap.Run.Status == status {
						return snap
					}
					if snap.Run.Status == chat.RunFailed {
						t.Fatal(snap.Run.ErrorCode, snap.Run.ErrorDetails)
					}
					time.Sleep(15 * time.Millisecond)
				}
				t.Fatal("wait timeout", status)
				return wailstransport.RunSnapshot{}
			}
			clientID, _ := id.New()
			run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: entry.Run.ConversationID, Text: "帮我讨论研究方法，需要时问我。", ClientMessageID: clientID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			pending := wait(run.ID, chat.RunWaitingUserInput)
			if requests.Load() != 3 || pending.UserInput == nil || len(pending.ToolCalls) != 3 || pending.Run.ContextBudgetTokens != 190000 || pending.Run.AutoCompactTokenLimit != 180000 {
				t.Fatal("wrong pending state/window", requests.Load(), pending.Run)
			}
			anchor, found, err := sqlite.NewRunRepository(app.store.DB()).LatestContextUsage(context.Background(), run.ID)
			if err != nil || !found || anchor.InputTokens != 55824 || anchor.TurnIndex != 3 {
				t.Fatal("actual protocol usage not saved", anchor, err)
			}
			var summaries int
			if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM model_request_usage WHERE run_id=? AND request_kind='compaction'`, run.ID).Scan(&summaries); err != nil || summaries != 0 {
				t.Fatal("premature compaction still occurred", summaries, err)
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			restored := wait(run.ID, chat.RunWaitingUserInput)
			loaded, found, err := sqlite.NewRunRepository(app.store.DB()).LatestContextUsage(context.Background(), run.ID)
			if err != nil || !found || loaded.TurnIndex != anchor.TurnIndex || requests.Load() != 3 || restored.UserInput.CallID != pending.UserInput.CallID {
				t.Fatal("restart lost accounting/question or re-ran tools", err)
			}
			if _, err := app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: run.ConversationID, RunID: run.ID, CallID: restored.UserInput.CallID, Answers: userinput.Answers{"scope": {OptionIDs: []string{"design"}, Text: "先做研究设计"}}}); err != nil {
				t.Fatal(err)
			}
			complete := wait(run.ID, chat.RunCompleted)
			if requests.Load() != 4 || len(complete.ToolCalls) != 3 || complete.Run.ErrorCode != "" || complete.Run.InputTokens != 155824 {
				t.Fatal("resume did not finish without duplicate calls", requests.Load(), complete.Run)
			}
		})
	}
}

func emitContextUsageFixture(t *testing.T, w http.ResponseWriter, protocol modelprofile.APIProtocol, n int, toolName, args string, input int) {
	emitContextUsageFixtureWithNativeSize(t, w, protocol, n, toolName, args, input, 95000)
}

func TestOrdinaryProtocolBudgetRecoveryUsesRealSQLiteQuestionAndRestart(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				n := int(requests.Add(1))
				switch n {
				case 1:
					// Tiny usage cannot discount newly generated native state: the
					// fourfold credit cap still forces independent evidence recovery.
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "skill_load", `{}`, 100, 280000)
				case 2:
					if strings.Contains(string(body), strings.Repeat("x", 1000)) || !strings.Contains(string(body), "fresh research tool exchange") || !strings.Contains(string(body), "Completed tool evidence") || !strings.Contains(string(body), "Skill categories") {
						t.Error("independent request lost result or replayed private state")
					}
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "user_input_request", `{"questions":[{"id":"choice","question":"继续哪种方案？","required":true,"options":[{"id":"keep","label":"保留研究构念"},{"id":"change","label":"讨论替代构念"}]}]}`, 20000, 0)
				case 3:
					if !strings.Contains(string(body), "原目标保持不变") || !strings.Contains(string(body), "末尾决策保持") || strings.Contains(string(body), strings.Repeat("x", 1000)) || !strings.Contains(string(body), "Skill categories") {
						t.Error("restart lost full human reply/evidence")
					}
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "", "", 21000, 0)
				default:
					t.Errorf("unexpected request/summary %d", n)
					emitContextUsageFixtureWithNativeSize(t, w, protocol, n, "", "", 21000, 0)
				}
			}))
			defer server.Close()
			root := t.TempDir()
			app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "ordinary recovery", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 200000, AutoCompactTokenLimit: 180000}}, TimeoutSeconds: 10, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "ordinary budget fixture"})
			if err != nil {
				t.Fatal(err)
			}
			entry, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "研究设计讨论", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, DiscussionOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			wait := func(runID string, status chat.RunStatus) wailstransport.RunSnapshot {
				t.Helper()
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					snap, e := app.ChatFacade.GetRunSnapshot(runID)
					if e != nil {
						t.Fatal(e)
					}
					if snap.Run.Status == status {
						return snap
					}
					if snap.Run.Status == chat.RunFailed {
						t.Fatal(snap.Run.ErrorCode, snap.Run.ErrorDetails)
					}
					time.Sleep(15 * time.Millisecond)
				}
				t.Fatal("wait timeout", status)
				return wailstransport.RunSnapshot{}
			}
			client, _ := id.New()
			run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: entry.Run.ConversationID, Text: "查资料但先不要启动返修。", ClientMessageID: client, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			pending := wait(run.ID, chat.RunWaitingUserInput)
			if pending.UserInput == nil || requests.Load() != 2 || len(pending.ToolCalls) != 2 || !pending.Run.ContextCompacted {
				t.Fatal("did not recover into durable question", pending.Run)
			}
			var before string
			if err := app.store.DB().QueryRow(`SELECT payload_json FROM provider_turn_items WHERE run_id=? AND turn_index=1 AND item_ordinal=0`, run.ID).Scan(&before); err != nil || !strings.Contains(before, strings.Repeat("x", 280000)) {
				t.Fatal("original native audit lost", err)
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			restored := wait(run.ID, chat.RunWaitingUserInput)
			if restored.UserInput.CallID != pending.UserInput.CallID || requests.Load() != 2 {
				t.Fatal("restart reexecuted operation")
			}
			answer := "原目标保持不变" + strings.Repeat("完整人类约束", 500) + "末尾决策保持"
			if _, err := app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: run.ConversationID, RunID: run.ID, CallID: restored.UserInput.CallID, Answers: userinput.Answers{"choice": {OptionIDs: []string{"keep"}, Text: answer}}}); err != nil {
				t.Fatal(err)
			}
			complete := wait(run.ID, chat.RunCompleted)
			if requests.Load() != 3 || len(complete.ToolCalls) != 2 || complete.Run.ContextBudgetTokens != 190000 || complete.Run.AutoCompactTokenLimit != 180000 {
				t.Fatal("completion reexecuted calls/changed window", complete.Run)
			}
			var after string
			if err := app.store.DB().QueryRow(`SELECT payload_json FROM provider_turn_items WHERE run_id=? AND turn_index=1 AND item_ordinal=0`, run.ID).Scan(&after); err != nil || after != before {
				t.Fatal("recovery changed immutable protocol", err)
			}
			current, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.Run.ResearchTaskID)
			if err != nil || current.Run.ID != entry.Run.ID {
				t.Fatal("discussion started revision", err)
			}
		})
	}
}

func emitContextUsageFixtureWithNativeSize(t *testing.T, w http.ResponseWriter, protocol modelprofile.APIProtocol, n int, toolName, args string, input, nativeSize int) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	text := "收到，可以继续讨论研究设计；没有开始执行路线。"
	switch protocol {
	case modelprofile.ProtocolOpenAIChat:
		delta, finish := map[string]any{"content": text}, "stop"
		if toolName != "" {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", n), "function": map[string]any{"name": toolName, "arguments": args}}}}
			finish = "tool_calls"
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": input, "completion_tokens": 20, "prompt_tokens_details": map[string]any{"cached_tokens": input - 100}}})
		io.WriteString(w, "data: [DONE]\n\n")
	case modelprofile.ProtocolOpenAIResponses:
		index := 0
		if n <= 2 && nativeSize > 0 {
			send(map[string]any{"type": "response.output_item.done", "output_index": index, "item": map[string]any{"type": "reasoning", "id": fmt.Sprintf("reason_%d", n), "encrypted_content": strings.Repeat("x", nativeSize)}})
			index++
		}
		item := map[string]any{"type": "message", "id": fmt.Sprintf("message_%d", n), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}
		if toolName != "" {
			item = map[string]any{"type": "function_call", "id": fmt.Sprintf("function_%d", n), "call_id": fmt.Sprintf("call_%d", n), "name": toolName, "arguments": args}
		}
		send(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "usage": map[string]any{"input_tokens": input, "output_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": input - 100}}}})
	case modelprofile.ProtocolAnthropic:
		send(map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 100, "cache_read_input_tokens": input - 100}}})
		index := 0
		if n <= 2 && nativeSize > 0 {
			send(map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "redacted_thinking", "data": strings.Repeat("x", nativeSize)}})
			send(map[string]any{"type": "content_block_stop", "index": index})
			index++
		}
		block, reason := map[string]any{"type": "text", "text": text}, "end_turn"
		if toolName != "" {
			var parsed any
			_ = json.Unmarshal([]byte(args), &parsed)
			block = map[string]any{"type": "tool_use", "id": fmt.Sprintf("call_%d", n), "name": toolName, "input": parsed}
			reason = "tool_use"
		}
		send(map[string]any{"type": "content_block_start", "index": index, "content_block": block})
		send(map[string]any{"type": "content_block_stop", "index": index})
		send(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}, "usage": map[string]any{"output_tokens": 20}})
		send(map[string]any{"type": "message_stop"})
	}
}

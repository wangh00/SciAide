package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Models are controlled HTTP fixtures; the adapters, bootstrap, SQLite,
// user-input wait/resume, facade and restart are real. Never use the formal DB
// or a commercial model. Optional capture contains visible assistant text only.
func TestQuestionHandoffReachesRealCardAcrossProtocols(t *testing.T) {
	const scopeChoiceIntro = "有两个选择会明显影响综述范围："
	intro := "这些是近年研究的初步线索，尚不能代替全文系统评价。\n\n如果你希望下一步做成更完整的文献综述（按年份、研究对象、实验设计把证据摊开，并标出研究缺口），我可以按这个方向准备研究方案。" + scopeChoiceIntro
	if path := os.Getenv("SCIAIDE_QUESTION_HANDOFF_CAPTURE"); path != "" {
		data, err := os.ReadFile(path)
		var captured struct {
			Text string `json:"text"`
		}
		if err != nil || json.Unmarshal(data, &captured) != nil || captured.Text == "" {
			t.Fatal("invalid visible-response fixture", err)
		}
		intro = captured.Text
	}
	body, found := strings.CutSuffix(intro, scopeChoiceIntro)
	if !found {
		body, _, found = strings.Cut(intro, "\n\n为了帮你继续细化")
	}
	body = strings.TrimSpace(body)
	if !found {
		t.Fatal("fixture must contain actual dangling handoff")
	}
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
				var tools []struct {
					Name     string `json:"name"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				}
				_ = json.Unmarshal(req["tools"], &tools)
				hasQuestion := false
				for _, d := range tools {
					hasQuestion = hasQuestion || d.Name == "user_input_request" || d.Function.Name == "user_input_request"
				}
				if !hasQuestion {
					t.Error("question tool missing on real wire")
				}
				n := requests.Add(1)
				text, toolName, args := intro, "", ""
				switch n {
				case 1:
					if !strings.Contains(string(data), "make the actual question tool call in the same response") {
						t.Error("model did not receive same-response question rule")
					}
				case 2:
					if len(tools) != 1 || !strings.Contains(string(data), "one bounded continuation") || strings.Contains(string(req["tool_choice"]), "user_input_request") {
						t.Errorf("repair is not bounded/optional: %s", data)
					}
					if protocol == modelprofile.ProtocolOpenAIResponses {
						var input []struct {
							Type    string `json:"type"`
							Role    string `json:"role"`
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						}
						_ = json.Unmarshal(req["input"], &input)
						last := input[len(input)-1]
						if last.Role != "system" || !strings.Contains(last.Content[0].Text, "one bounded continuation") {
							t.Error("continuation instruction must follow native final_answer on Responses wire")
						}
						count := 0
						for _, m := range input {
							if m.Role == "assistant" {
								for _, p := range m.Content {
									if p.Text == intro {
										count++
									}
								}
							}
						}
						if count != 1 {
							t.Errorf("native preceding text duplicated or lost: %d", count)
						}
					}
					text = ""
					toolName = "user_input_request"
					args = `{"questions":[{"id":"purpose","question":"希望最终产出什么？","required":true,"options":[{"id":"design","label":"研究设计"},{"id":"review","label":"文献梳理"}]},{"id":"materials","question":"目前有什么可用材料？","required":true,"options":[{"id":"survey","label":"可以收集问卷"},{"id":"data","label":"已有数据"}]}]}`
				case 3:
					if !strings.Contains(string(data), "课程论文方案") || !strings.Contains(string(data), "匿名问卷") || strings.Contains(string(data), "one bounded continuation") {
						t.Error("answers lost or continuation leaked into normal resumed context")
					}
					var wire any
					_ = json.Unmarshal(data, &wire)
					var countText func(any) int
					countText = func(value any) int {
						switch value := value.(type) {
						case string:
							if value == intro {
								return 1
							}
						case []any:
							count := 0
							for _, item := range value {
								count += countText(item)
							}
							return count
						case map[string]any:
							count := 0
							for _, item := range value {
								count += countText(item)
							}
							return count
						}
						return 0
					}
					if count := countText(wire); count != 1 {
						t.Errorf("resumed request lost or duplicated pre-question findings: %d copies", count)
					}
					text = "收到，继续细化课程论文的匿名问卷设计；没有采用路线或开始执行。"
				case 4:
					text = "你好，仍可自然对话。"
				default:
					t.Errorf("unexpected recovery loop/request %d", n)
					text = "unexpected"
				}
				emitQuestionHandoffFixture(t, w, protocol, int(n), text, toolName, args)
			}))
			defer server.Close()
			root := t.TempDir()
			app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "question handoff fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}}, TimeoutSeconds: 10, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "sleep handoff fixture"})
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
						t.Fatal(snap.Run)
					}
					time.Sleep(15 * time.Millisecond)
				}
				t.Fatal("wait timeout", runID, status)
				return wailstransport.RunSnapshot{}
			}
			clientID, _ := id.New()
			run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: entry.Run.ConversationID, Text: "我想研究大学生睡眠和学习状态的关系，但还没想好怎么做，你帮我看看。", ClientMessageID: clientID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			pending := wait(run.ID, chat.RunWaitingUserInput)
			if requests.Load() != 2 || pending.UserInput == nil || len(pending.UserInput.Questions) != 2 || len(pending.PendingApprovals) != 0 || len(pending.ToolCalls) != 1 {
				t.Fatal("not actual durable question card", pending.UserInput, requests.Load())
			}
			for _, m := range pending.Messages {
				if m.ID == pending.Run.AssistantMessageID && (len(m.Parts) != 1 || m.Parts[0].Text != body) {
					t.Fatal("explanation lost while waiting", m)
				}
			}
			time.Sleep(100 * time.Millisecond)
			if requests.Load() != 2 {
				t.Fatal("model called while waiting for human")
			}
			var original string
			expectedDraft := intro
			if protocol == modelprofile.ProtocolOpenAIResponses {
				expectedDraft = "正在核对文献摘要。\n\n" + intro
			}
			if err := app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=1`, run.ID).Scan(&original); err != nil || original != expectedDraft {
				t.Fatal("original provider intro not auditable", err)
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
				t.Fatal("wait changed or re-executed on restart")
			}
			response := userinput.Response{ConversationID: run.ConversationID, RunID: run.ID, CallID: restored.UserInput.CallID, Answers: userinput.Answers{"purpose": {OptionIDs: []string{"design"}, Text: "课程论文方案"}, "materials": {OptionIDs: []string{"survey"}, Text: "匿名问卷"}}}
			if _, err = app.ChatFacade.RespondToUserInput(response); err != nil {
				t.Fatal(err)
			}
			completed := wait(run.ID, chat.RunCompleted)
			if requests.Load() != 3 || completed.UserInput != nil || completed.Run.ErrorCode != "" {
				t.Fatal(completed.Run)
			}
			for _, m := range completed.Messages {
				if m.ID == completed.Run.AssistantMessageID && (!strings.HasPrefix(m.Parts[0].Text, body) || !strings.Contains(m.Parts[0].Text, "继续细化课程论文")) {
					t.Fatal("lost original explanation on resume", m)
				}
			}
			if _, err = app.ChatFacade.RespondToUserInput(response); err != nil {
				t.Fatal("idempotent retry", err)
			}
			current, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.Run.ResearchTaskID)
			if err != nil || current.Run.ID != entry.Run.ID {
				t.Fatal("answer executed/planned research", err, current)
			}
			greetingID, _ := id.New()
			greeting, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: run.ConversationID, Text: "你好", ClientMessageID: greetingID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			wait(greeting.ID, chat.RunCompleted)
			if requests.Load() != 4 {
				t.Fatal("routine greeting triggered recovery")
			}
		})
	}
}

func emitQuestionHandoffFixture(t *testing.T, w http.ResponseWriter, protocol modelprofile.APIProtocol, n int, text, toolName, args string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	switch protocol {
	case modelprofile.ProtocolOpenAIChat:
		delta := map[string]any{"content": text}
		finish := "stop"
		if toolName != "" {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", n), "function": map[string]any{"name": toolName, "arguments": args}}}}
			finish = "tool_calls"
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
		io.WriteString(w, "data: [DONE]\n\n")
	case modelprofile.ProtocolOpenAIResponses:
		item := map[string]any{"type": "message", "id": fmt.Sprintf("message_%d", n), "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": text}}}
		if toolName != "" {
			item = map[string]any{"type": "function_call", "id": fmt.Sprintf("function_%d", n), "call_id": fmt.Sprintf("call_%d", n), "name": toolName, "arguments": args}
		}
		index := 0
		if n == 1 {
			commentary := map[string]any{"type": "message", "id": "opening_progress", "role": "assistant", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": "正在核对文献摘要。"}}}
			send(map[string]any{"type": "response.output_item.done", "output_index": index, "item": commentary})
			index++
		}
		send(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
	case modelprofile.ProtocolAnthropic:
		send(map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 10}}})
		block := map[string]any{"type": "text", "text": text}
		reason := "end_turn"
		if toolName != "" {
			var input any
			_ = json.Unmarshal([]byte(args), &input)
			block = map[string]any{"type": "tool_use", "id": fmt.Sprintf("call_%d", n), "name": toolName, "input": input}
			reason = "tool_use"
		}
		send(map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
		send(map[string]any{"type": "content_block_stop", "index": 0})
		send(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}, "usage": map[string]any{"output_tokens": 20}})
		send(map[string]any{"type": "message_stop"})
	}
}

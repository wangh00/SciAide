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
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real proposal/approval/pause/resume/Executor/SQLite chain; only the model is
// a loopback fixture. Both tool orders use the same semantics and no clock.
func TestResearchStateQuestionBatchNeverUsesFutureAnswer(t *testing.T) {
	for _, questionFirst := range []bool{false, true} {
		order := "state-first"
		if questionFirst {
			order = "question-first"
		}
		t.Run(order, func(t *testing.T) {
			var requests atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				n := requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				stateArgs := `{"expectedVersion":0,"currentFocus":"旧提议不应写入","rationale":"回答之前提出","nextStep":"","unknowns":[]}`
				questionArgs := `{"questions":[{"id":"scope","question":"选择研究范围？","required":true}]}`
				stateCall := map[string]any{"id": "before-answer", "function": map[string]any{"name": "research_state_update", "arguments": stateArgs}}
				questionCall := map[string]any{"id": "question", "function": map[string]any{"name": "user_input_request", "arguments": questionArgs}}
				catalogCall := map[string]any{"id": "catalog", "function": map[string]any{"name": "research_catalog", "arguments": `{}`}}
				delta, finish := map[string]any{}, "tool_calls"
				switch n {
				case 1:
					ordered := []map[string]any{stateCall, questionCall, catalogCall}
					if questionFirst {
						ordered[0], ordered[1] = ordered[1], ordered[0]
					}
					items := make([]any, len(ordered))
					for i, item := range ordered {
						item["index"] = i
						items[i] = item
					}
					delta["tool_calls"] = items
				case 2:
					if !strings.Contains(string(body), "我的最新回答") {
						t.Error("the actual user answer did not reach the resumed model")
					}
					delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "after-answer", "function": map[string]any{"name": "research_state_update", "arguments": `{"expectedVersion":0,"currentFocus":"根据回答的新工作记录","rationale":"回答之后重新提出","nextStep":"","unknowns":[]}`}}}
				case 3:
					delta["content"], finish = "已按回答记录研究关注点。", "stop"
				default:
					t.Errorf("unexpected model request %d", n)
					delta["content"], finish = "停止。", "stop"
				}
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			}))
			defer model.Close()
			options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
			app, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile := saveWorkflowAITestProfile(t, app, model.URL)
			project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "同批问题与研究记录"})
			if err != nil {
				t.Fatal(err)
			}
			entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "讨论研究方向", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
			if err != nil {
				t.Fatal(err)
			}
			messageID, _ := id.New()
			run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: entry.ConversationID, Text: "请问我再继续", ClientMessageID: messageID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			wait := func(status chat.RunStatus) wails.RunSnapshot {
				t.Helper()
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					snapshot, err := app.ChatFacade.GetRunSnapshot(run.ID)
					if err != nil {
						t.Fatal(err)
					}
					if snapshot.Run.Status == status {
						return snapshot
					}
					if snapshot.Run.Status == chat.RunFailed {
						t.Fatalf("run failed: %+v", snapshot.Run)
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Fatal("waiting for run status", status)
				return wails.RunSnapshot{}
			}
			waiting := wait(chat.RunWaitingUserInput)
			if waiting.UserInput == nil {
				t.Fatal("missing user question")
			}
			if !questionFirst {
				if err := app.Close(); err != nil {
					t.Fatal(err)
				}
				app, err = New(options)
				if err != nil {
					t.Fatal(err)
				}
				app.Startup(context.Background())
				waiting = wait(chat.RunWaitingUserInput)
				if waiting.UserInput == nil || requests.Load() != 1 {
					t.Fatal("restart lost the pending question or resumed without an answer")
				}
			}
			if state, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID); err != nil || state != nil {
				t.Fatalf("question batch wrote before answer: %+v %v", state, err)
			}
			if _, err := app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: entry.ConversationID, RunID: run.ID, CallID: waiting.UserInput.CallID, Answers: userinput.Answers{"scope": {Text: "我的最新回答"}}}); err != nil {
				t.Fatal(err)
			}
			wait(chat.RunCompleted)
			var earlyID, earlyStatus, earlyText, newID, newStatus, catalogStatus, answerID string
			if err := app.store.DB().QueryRow(`SELECT tc.id,tc.status,COALESCE(tr.text_content,'') FROM tool_calls tc LEFT JOIN tool_results tr ON tr.tool_call_id=tc.id WHERE tc.run_id=? AND tc.provider_call_id='before-answer'`, run.ID).Scan(&earlyID, &earlyStatus, &earlyText); err != nil {
				t.Fatal(err)
			}
			if err := app.store.DB().QueryRow(`SELECT id,status FROM tool_calls WHERE run_id=? AND provider_call_id='after-answer'`, run.ID).Scan(&newID, &newStatus); err != nil {
				t.Fatal(err)
			}
			if err := app.store.DB().QueryRow(`SELECT status FROM tool_calls WHERE run_id=? AND provider_call_id='catalog'`, run.ID).Scan(&catalogStatus); err != nil {
				t.Fatal(err)
			}
			if err := app.store.DB().QueryRow(`SELECT id FROM messages WHERE conversation_id=? AND role='user' ORDER BY rowid DESC LIMIT 1`, entry.ConversationID).Scan(&answerID); err != nil {
				t.Fatal(err)
			}
			state, err := app.WorkflowFacade.GetResearchWorkingState(project.ID, entry.ResearchTaskID)
			if err != nil || state == nil || state.Version != 1 || state.CurrentFocus != "根据回答的新工作记录" || state.SourceCallID != newID || state.SourceUserMessageID != answerID {
				t.Fatalf("old proposal acquired future answer or fresh proposal failed: early=%s/%s/%q new=%s/%s state=%+v err=%v", earlyID, earlyStatus, earlyText, newID, newStatus, state, err)
			}
			if earlyStatus != "failed" || newStatus != "completed" || catalogStatus != "completed" || !strings.Contains(earlyText, "重新") {
				t.Fatalf("batch isolation/reproposal failure: old=%s %q new=%s unrelated=%s", earlyStatus, earlyText, newStatus, catalogStatus)
			}
		})
	}
}

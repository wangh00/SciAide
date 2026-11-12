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
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real bootstrap, HTTP adapter, SQLite and shared question facade. Only model
// replies are fixtures; no paid provider, production task or original DB.
func TestReviewQuestionResumesSameAttemptBeforeDirectRepair(t *testing.T) {
	for _, outcome := range []string{"answer", "defer", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			var questionRequests, ordinaryRequests, designs atomic.Int32
			var confirmed atomic.Bool
			stage := newResearchStarterOutputTestServer(t, func(out map[string]any, prompt string) {
				if _, design := out["status"]; design {
					designs.Add(1)
				}
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
				prompt := workflowAITestPrompt(body)
				schema, err := workflowAITestSchema(prompt)
				if err != nil {
					ordinaryRequests.Add(1)
					http.Error(w, "unexpected extra chat", 400)
					return
				}
				properties, _ := schema["properties"].(map[string]any)
				if _, review := properties["reviewedInputSha256"]; !review || confirmed.Load() {
					r.Body = io.NopCloser(bytes.NewReader(body))
					stage.Config.Handler.ServeHTTP(w, r)
					return
				}
				var request struct {
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
					Tools []struct{ Function struct{ Name string } }
				}
				_ = json.Unmarshal(body, &request)
				available, answered, dismissed := false, false, false
				for _, definition := range request.Tools {
					available = available || definition.Function.Name == "user_input_request"
				}
				for _, message := range request.Messages {
					if message.Role == "tool" {
						answered = answered || strings.Contains(string(message.Content), "仅成人样本")
						dismissed = dismissed || strings.Contains(string(message.Content), "用户暂不回答")
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if !answered && !dismissed {
					if !available || !strings.Contains(prompt, "this SAME review") {
						t.Error("new review lacks shared question capability or same-review guidance")
					}
					questionRequests.Add(1)
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
						"delta":         map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "review-scope-choice", "function": map[string]any{"name": "user_input_request", "arguments": `{"questions":[{"id":"scope","question":"样本范围含未成年人，是否限定成人？","required":true,"options":[{"id":"adults","label":"仅成人样本","description":"保留现有测量，调整招募范围"},{"id":"all","label":"保留全部年龄","description":"需要额外伦理与监护人安排"}]}]}`}}}},
						"finish_reason": "tool_calls",
					}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
					return
				}
				out := workflowAISchemaFixture(schema).(map[string]any)
				out["reviewedInputSha256"], out["approved"] = workflowAITestStageInputSHA256(prompt), false
				applyResearchAcceptanceFixture(out, prompt)
				out["methodIssues"] = []any{"当前设计需明确招募年龄范围"}
				out["requiredCorrections"] = []any{"将设计限定仅成人样本并保留已完成内容"}
				applyTrackedReviewFixture(out, prompt)
				plan := out["revisionPlan"].([]any)
				plan[0].(map[string]any)["nodeId"] = "research_design"
				plan[0].(map[string]any)["reason"] = "用户已在本次审查选择仅成人样本，修订招募范围"
				if dismissed {
					plan[0].(map[string]any)["needsUserInput"] = true
					plan[0].(map[string]any)["requiredInput"] = "招募年龄范围尚未决定，用户暂缓回答"
					plan[0].(map[string]any)["reason"] = "用户暂缓回答，不能代替用户选择成人范围"
				}
				for _, key := range []string{"unsupportedClaims", "citationIssues", "numericIssues", "methodIssues"} {
					delete(out, key)
				}
				encoded, _ := json.Marshal(out)
				if !emitRequiredPlannerResultFixture(t, w, modelprofile.ProtocolOpenAIChat, body, string(encoded)) {
					t.Error("current review source submission interface was not offered")
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
			profile := saveWorkflowAITestProfile(t, app, server.URL)
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "审查直接提问"})
			if err != nil {
				t.Fatal(err)
			}
			started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "设计睡眠研究及招募文案", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			planned := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
			adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planned.Run.ID, RouteID: "design_first"})
			if err != nil || adopted.Run == nil {
				t.Fatal(err)
			}
			formalID := adopted.Run.Run.ID
			waitQuestion := func() wailstransport.RunSnapshot {
				t.Helper()
				deadline := time.Now().Add(25 * time.Second)
				for time.Now().Before(deadline) {
					snapshot, e := app.ChatFacade.GetLatestRunSnapshot(adopted.Run.Run.ConversationID)
					if e != nil {
						t.Fatal(e)
					}
					if snapshot != nil && snapshot.Run.Status == chat.RunWaitingUserInput && snapshot.UserInput != nil {
						return *snapshot
					}
					detail, e := app.WorkflowFacade.GetRun(project.ID, formalID)
					if e != nil || detail.Run.Status.Terminal() {
						t.Fatalf("no question; workflow=%+v err=%v", detail.Run, e)
					}
					time.Sleep(25 * time.Millisecond)
				}
				t.Fatal("question wait timeout")
				return wailstransport.RunSnapshot{}
			}
			pending := waitQuestion()
			if !pending.WorkflowAI || len(pending.PendingApprovals) != 0 || questionRequests.Load() != 1 {
				t.Fatal("question not correctly projected", pending)
			}
			var executionID, stepID string
			var attempt int
			if err := app.store.DB().QueryRow(`SELECT e.id,e.workflow_step_id,e.attempt FROM workflow_ai_executions e JOIN workflow_ai_chat_runs b ON b.execution_id=e.id WHERE b.chat_run_id=?`, pending.Run.ID).Scan(&executionID, &stepID, &attempt); err != nil {
				t.Fatal(err)
			}
			response := userinput.Response{ConversationID: pending.Run.ConversationID, RunID: pending.Run.ID, CallID: pending.UserInput.CallID, Answers: userinput.Answers{"scope": {OptionIDs: []string{"adults"}}}}
			wrong := response
			wrong.ConversationID = "another-conversation"
			if _, err := app.ChatFacade.RespondToUserInput(wrong); err == nil {
				t.Fatal("cross conversation reply accepted")
			}
			if outcome == "cancel" {
				if _, err := app.WorkflowFacade.Cancel(project.ID, formalID); err != nil {
					t.Fatal(err)
				}
				waitForWorkflowState(t, app, project.ID, formalID, workflow.RunCancelled, 10*time.Second)
				if _, err := app.ChatFacade.RespondToUserInput(response); err == nil {
					t.Fatal("cancelled review accepted reply")
				}
				return
			}
			// A restart must keep this exact pending question and execution.
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			restored := waitQuestion()
			if restored.Run.ID != pending.Run.ID || restored.UserInput.CallID != response.CallID || questionRequests.Load() != 1 {
				t.Fatal("restart duplicated or lost question")
			}
			if outcome == "defer" {
				response.Dismiss, response.Answers = true, nil
			}
			if _, err := app.ChatFacade.RespondToUserInput(response); err != nil {
				t.Fatal(err)
			}
			failed := waitForWorkflowState(t, app, project.ID, formalID, workflow.RunFailed, 20*time.Second)
			if failed.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" {
				t.Fatal("review became program error", failed.Run.ErrorCode, failed.Run.ErrorMessage)
			}
			var reviewAttempts, executionCount, chatCount int
			_ = app.store.DB().QueryRow(`SELECT attempt FROM workflow_steps WHERE id=?`, stepID).Scan(&reviewAttempts)
			_ = app.store.DB().QueryRow(`SELECT count(*) FROM workflow_ai_executions WHERE workflow_step_id=? AND id=?`, stepID, executionID).Scan(&executionCount)
			_ = app.store.DB().QueryRow(`SELECT count(*) FROM workflow_ai_chat_runs WHERE execution_id=? AND chat_run_id=?`, executionID, pending.Run.ID).Scan(&chatCount)
			if reviewAttempts != attempt || executionCount != 1 || chatCount != 1 || designs.Load() != 1 || ordinaryRequests.Load() != 0 {
				t.Fatal("question triggered new attempt, ordinary chat or premature repair", reviewAttempts, executionCount, chatCount, designs.Load(), ordinaryRequests.Load())
			}
			if _, err := app.ChatFacade.RespondToUserInput(response); err != nil {
				t.Fatal("exact duplicate after stage completion failed", err)
			}
			recommendation := failed.ReviewRevisionRecommendation
			if outcome == "defer" {
				if recommendation == nil || recommendation.Status != "needs_input" || len(recommendation.RequiredInputs) != 1 {
					t.Fatal("deferred choice became execution authorization", recommendation)
				}
				return
			}
			if recommendation == nil || recommendation.Status != "recommended" || recommendation.NodeID != "research_design" {
				t.Fatal("answered choice did not become complete direct repair", recommendation)
			}
			var gate workflow.Step
			for _, step := range failed.Steps {
				if step.NodeID == "delivery_gate" {
					gate = step
				}
			}
			confirmed.Store(true)
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: formalID, StepID: gate.ID, UseRecommendation: true, ExpectedReviewSHA256: recommendation.ReviewOutputSHA256}); err != nil {
				t.Fatal(err)
			}
			completed := waitForWorkflowState(t, app, project.ID, formalID, workflow.RunCompleted, 25*time.Second)
			if designs.Load() != 2 || ordinaryRequests.Load() != 0 || questionRequests.Load() != 1 {
				t.Fatal("direct repair lost single confirmation chain")
			}
			for _, step := range completed.Steps {
				if step.NodeID == "research_design" && !strings.Contains(string(step.Input), "仅成人样本") {
					t.Fatal("answered choice was not delivered to responsible repair stage", string(step.Input))
				}
			}
			t.Log("same review/run/attempt across restart and answer; zero extra ordinary chats; original repair confirmation replays responsible suffix")
		})
	}
}

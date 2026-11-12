package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/modelcap"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Exercise the actual chat tool -> planner -> frozen execution -> HTTP path.
// MAX starts with the released v99 CHECK, then retries the same failed stage
// after the real startup migration. All providers and databases are fixtures.
func TestResearchReasoningPreferencesAndMaxUpgradeRetry(t *testing.T) {
	for _, level := range modelcap.AllReasoningLevels() {
		t.Run(string(level), func(t *testing.T) {
			var stageCalls atomic.Int32
			stage := newResearchStarterOutputTestServer(t, nil)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				var req struct {
					Effort string `json:"reasoning_effort"`
					Tools  []struct {
						Function struct{ Name string } `json:"function"`
					} `json:"tools"`
					Messages []struct{ Role string } `json:"messages"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					t.Error(err)
					return
				}
				if req.Effort != string(level) {
					t.Errorf("wire effort = %q, want %s", req.Effort, level)
				}
				discussion := false
				for _, tool := range req.Tools {
					discussion = discussion || tool.Function.Name == "research_plan_prepare"
				}
				if !discussion {
					stageCalls.Add(1)
					r.Body = io.NopCloser(bytes.NewReader(body))
					stage.Config.Handler.ServeHTTP(w, r)
					return
				}
				hasToolResult := false
				for _, message := range req.Messages {
					hasToolResult = hasToolResult || message.Role == "tool"
				}
				w.Header().Set("Content-Type", "text/event-stream")
				delta := map[string]any{"content": "已准备候选路线，等待你采用。"}
				finish := "stop"
				if !hasToolResult {
					delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "prepare", "function": map[string]any{"name": "research_plan_prepare", "arguments": `{"researchIdea":"大学生睡眠与课堂清醒程度，一次性问卷，课程论文研究方案、问卷初稿及分析计划；先设计路线，不招募"}`}}}}
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
			defer func() { app.Close() }()
			app.Startup(context.Background())
			profile := saveWorkflowAITestProfile(t, app, server.URL)
			if level == modelcap.ReasoningMax {
				// Restore only the original table CHECK in this empty isolated home.
				// No old migration file/checksum or production database is changed.
				sql, err := os.ReadFile(filepath.Join("..", "storage", "sqlite", "migrations", "000100_workflow_ai_reasoning_max.sql"))
				if err != nil {
					t.Fatal(err)
				}
				old := strings.Replace(string(sql), "'xhigh','max')", "'xhigh')", 1)
				if _, err := app.store.DB().Exec("PRAGMA foreign_keys=OFF; BEGIN; " + old + "; DELETE FROM schema_migrations WHERE version=100; COMMIT; PRAGMA foreign_keys=ON;"); err != nil {
					t.Fatal(err)
				}
			}
			p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "科研推理档位回归"})
			if err != nil {
				t.Fatal(err)
			}
			entry, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: p.ID, ResearchIdea: "想研究大学生睡眠", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, ReasoningLevel: level, DiscussionOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			client, _ := id.New()
			ordinary, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: entry.Run.ConversationID, Text: "一次性问卷，课堂清醒程度；先设计课程论文的方案、问卷初稿和分析计划，不招募", ClientMessageID: client, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, ReasoningLevel: level})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(15 * time.Second)
			for {
				snapshot, err := app.ChatFacade.GetRunSnapshot(ordinary.ID)
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.Run.Status == chat.RunCompleted {
					break
				}
				if snapshot.Run.Status == chat.RunFailed || time.Now().After(deadline) {
					t.Fatalf("discussion failed/timed out: %+v", snapshot.Run)
				}
				time.Sleep(20 * time.Millisecond)
			}
			planning, err := app.WorkflowFacade.GetCurrentResearchTaskRun(p.ID, entry.Run.ResearchTaskID)
			if err != nil || planning.Run.ID == entry.Run.ID {
				t.Fatal("discussion tool did not prepare same task", err)
			}
			if level == modelcap.ReasoningMax {
				failed := waitForWorkflowState(t, app, p.ID, planning.Run.ID, workflow.RunFailed, 15*time.Second)
				if len(failed.Steps) != 1 || failed.Steps[0].ErrorCode != "WORKFLOW_AI_START_FAILED" || !strings.Contains(failed.Steps[0].ErrorMessage, "reasoning_level IN") || len(failed.AIExecutions) != 0 || stageCalls.Load() != 0 {
					t.Fatalf("original failure not reproduced: %+v", failed)
				}
				var orphanChats int
				if err := app.store.DB().QueryRow(`SELECT count(*) FROM runs WHERE conversation_id=?`, planning.Run.ConversationID).Scan(&orphanChats); err != nil || orphanChats != 0 {
					t.Fatal("failed transaction leaked stage chat", orphanChats, err)
				}
				if err := app.Close(); err != nil {
					t.Fatal(err)
				}
				app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
				if err != nil {
					t.Fatal(err)
				}
				app.Startup(context.Background())
				if stageCalls.Load() != 0 {
					t.Fatal("startup automatically reran failed work")
				}
				if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: failed.Run.ID, StepID: failed.Steps[0].ID, Note: "升级后重试当前阶段"}); err != nil {
					t.Fatal(err)
				}
			}
			completed := waitForWorkflowState(t, app, p.ID, planning.Run.ID, workflow.RunCompleted, 20*time.Second)
			if completed.Run.ResearchTaskID != entry.Run.ResearchTaskID || len(completed.AIExecutions) != 1 || completed.AIExecutions[0].ReasoningLevel != level || completed.AIExecutions[0].Status != "completed" {
				t.Fatalf("planner effort/task not preserved: %+v", completed)
			}
			if level == modelcap.ReasoningMax && (completed.Steps[0].ID != planning.Steps[0].ID || completed.AIExecutions[0].Attempt != 2) {
				t.Fatal("retry replaced the original stage or lost attempt history")
			}
			plan, err := app.WorkflowFacade.GetResearchStarterPlan(p.ID, completed.Run.ID)
			if err != nil || len(plan.Routes) == 0 || plan.Routes[0].Validation == "invalid" {
				t.Fatal("candidate not usable", err)
			}
			var formal int
			if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE workflow_purpose='user_plan'`).Scan(&formal); err != nil || formal != 0 {
				t.Fatal("preparation executed candidate without adoption", formal, err)
			}
			adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: p.ID, RunID: completed.Run.ID, RouteID: plan.RecommendedRouteID})
			if err != nil || adopted.Run == nil {
				t.Fatal("explicit adoption failed", err)
			}
			result := waitForWorkflowState(t, app, p.ID, adopted.Run.Run.ID, workflow.RunCompleted, 25*time.Second)
			if len(result.AIExecutions) == 0 || result.Run.ResearchTaskID != entry.Run.ResearchTaskID {
				t.Fatal("adopted route did not execute same-task AI stages")
			}
			for _, execution := range result.AIExecutions {
				snapshot, err := app.ChatFacade.GetRunSnapshot(execution.ChatRunID)
				if err != nil || execution.ReasoningLevel != level || snapshot.Run.RequestedReasoningLevel != level || snapshot.Run.ResolvedReasoningLevel != level {
					t.Fatal("formal stage lost requested effort", err, execution)
				}
			}
			if level == modelcap.ReasoningMax {
				calls := stageCalls.Load()
				if err := app.Close(); err != nil {
					t.Fatal(err)
				}
				app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
				if err != nil {
					t.Fatal(err)
				}
				app.Startup(context.Background())
				restored, err := app.WorkflowFacade.GetRun(p.ID, result.Run.ID)
				if err != nil || len(restored.AIExecutions) != len(result.AIExecutions) || restored.Run.Status != workflow.RunCompleted || stageCalls.Load() != calls {
					t.Fatal("reopen lost/reran completed MAX stages", err)
				}
				for _, execution := range restored.AIExecutions {
					if execution.ReasoningLevel != modelcap.ReasoningMax {
						t.Fatal("reopen downgraded MAX")
					}
				}
			}
		})
	}
}

func TestWorkflowAIAnalysisAndAgentStagePersistMax(t *testing.T) {
	app, _, profile := newWorkflowAIRecoveryApplication(t, t.TempDir())
	defer app.Close()
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "MAX stage kinds"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []workflow.NodeKind{workflow.NodeAIAnalysis, workflow.NodeAgentStage} {
		saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: workflow.Definition{
			SchemaVersion: 1, Name: string(kind),
			Nodes: []workflow.Node{{ID: "analyze", Name: "Analyze", Kind: kind, Arguments: json.RawMessage(`{}`), PromptVersion: "max-v1", Prompt: "Analyze frozen input.", ReviewPolicy: workflow.AIReviewAuto,
				OutputSchema: json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`)}},
			Outputs: []workflow.Output{{Name: "result", Type: workflow.TypeObject, FromNode: "analyze", FromPort: "analysis", Required: true}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: profile.ID, ModelID: workflowAITestModelID, ReasoningLevel: modelcap.ReasoningMax})
		if err != nil {
			t.Fatal(err)
		}
		completed := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCompleted, 15*time.Second)
		if len(completed.AIExecutions) != 1 || completed.AIExecutions[0].ReasoningLevel != modelcap.ReasoningMax {
			t.Fatal("MAX stage not persisted", kind)
		}
	}
}

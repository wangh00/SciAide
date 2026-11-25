package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestDirectResearchTaskPreparesAndReadsFirstCandidate(t *testing.T) {
	const planningRequest = "请根据当前讨论准备候选路线；只规划，不采纳、不执行。"
	planner := newResearchStarterOutputTestServer(t, nil)
	defer planner.Close()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		if _, err := workflowAITestSchema(workflowAITestPrompt(body)); err == nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			planner.Config.Handler.ServeHTTP(w, r)
			return
		}
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
		if json.Unmarshal(body, &request) != nil {
			t.Error("invalid human discussion request")
			http.Error(w, "invalid request", 400)
			return
		}
		tools := map[string]bool{}
		for _, definition := range request.Tools {
			tools[definition.Function.Name] = true
		}
		lastUser, latest := "", -1
		contents := make([]string, len(request.Messages))
		for i, message := range request.Messages {
			_ = json.Unmarshal(message.Content, &contents[i])
			if message.Role == "user" {
				lastUser, latest = contents[i], i
			}
		}
		toolResult := false
		for _, message := range request.Messages[latest+1:] {
			toolResult = toolResult || message.Role == "tool"
		}
		if !tools["research_plan_prepare"] {
			t.Error("direct task cannot request an optional candidate")
			http.Error(w, "missing planning tool", 400)
			return
		}
		if strings.Contains(lastUser, "使用不存在材料") {
			if !toolResult {
				writeResearchTaskVerticalSliceResponse(w, "research_plan_prepare", map[string]any{"referenceAttachmentIds": []string{"not-in-this-task"}}, "")
				return
			}
			writeResearchTaskVerticalSliceResponse(w, "", nil, "材料不可用，未创建候选；仍可继续讨论。")
			return
		}
		if strings.Contains(lastUser, "只读当前候选") {
			if !tools["research_task_read"] {
				t.Error("direct task cannot read its actual candidate")
				http.Error(w, "missing candidate reader", 400)
				return
			}
			if !toolResult {
				writeResearchTaskVerticalSliceResponse(w, "research_task_read", map[string]any{"section": "inputs"}, "")
				return
			}
			joined := strings.Join(contents, "\n")
			if !strings.Contains(joined, planningRequest) || !strings.Contains(joined, "discussionRecord") {
				t.Error("candidate reader did not return the frozen direct discussion")
				http.Error(w, "wrong candidate inputs", 400)
				return
			}
			writeResearchTaskVerticalSliceResponse(w, "", nil, "已读取当前候选的冻结需求；候选仍未采纳。")
			return
		}
		if !toolResult {
			writeResearchTaskVerticalSliceResponse(w, "research_plan_prepare", map[string]any{}, "")
			return
		}
		joined := strings.Join(contents, "\n")
		if !strings.Contains(joined, `"executionStarted":false`) {
			t.Error("candidate preparation acknowledged execution")
			http.Error(w, "wrong candidate receipt", 400)
			return
		}
		writeResearchTaskVerticalSliceResponse(w, "", nil, "候选规划已启动；没有执行研究路线。")
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
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "直接任务首候选"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{
		ProjectID: project.ID, ResearchIdea: "未知材料不能创建任务", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
		ReferenceAttachmentIDs: []string{"not-in-project"},
	}); err == nil {
		t.Fatal("unknown initial reference created a direct task")
	}
	var taskCount int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM research_tasks WHERE project_id=?`, project.ID).Scan(&taskCount); err != nil || taskCount != 0 {
		t.Fatal("rejected initial reference left a partial task", taskCount, err)
	}
	path := filepath.Join(t.TempDir(), "direct-reference.md")
	if err := os.WriteFile(path, []byte("# Synthetic reference\nExplicitly selected for the direct task."), 0600); err != nil {
		t.Fatal(err)
	}
	batch, err := app.AttachmentFacade.ImportDocumentPaths(project.ID, []string{path})
	if err != nil || len(batch.Attachments) != 1 {
		t.Fatal(batch, err)
	}
	referenceID := batch.Attachments[0].ID
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{
		ProjectID: project.ID, ResearchIdea: "讨论后直接准备候选", ModelProfileID: profile.ID, ModelID: workflowAITestModelID,
		ReferenceAttachmentIDs: []string{referenceID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var runs int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=?`, entry.ResearchTaskID).Scan(&runs); err != nil || runs != 0 {
		t.Fatal("direct entry created a run before planning", runs, err)
	}
	invalid := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "使用不存在材料准备候选")
	invalidSnapshot, err := app.ChatFacade.GetRunSnapshot(invalid.ID)
	if err != nil {
		t.Fatal(err)
	}
	invalidRejected := false
	for _, call := range invalidSnapshot.ToolCalls {
		if call.ToolName == workflow.ResearchPlanPrepareTool && (call.Status == tool.CallCompleted || call.Status == tool.CallFailed) && call.Result != nil && call.Result.Status == tool.ResultError {
			invalidRejected = true
		}
	}
	if !invalidRejected {
		t.Fatal("unknown planning material was not rejected", invalidSnapshot.ToolCalls)
	}
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=?`, entry.ResearchTaskID).Scan(&runs); err != nil || runs != 0 {
		t.Fatal("rejected material left a partial candidate", runs, err)
	}
	session, err := app.WorkflowFacade.GetResearchTaskSession(project.ID, entry.ResearchTaskID)
	if err != nil || session.Entry.ResearchTaskID != entry.ResearchTaskID || !session.Entry.CanExplore || session.CurrentRun != nil || len(session.Entry.ReferenceAttachmentIDs) != 1 || session.Entry.ReferenceAttachmentIDs[0] != referenceID {
		t.Fatal("direct session fabricated a current Run", session, err)
	}
	prepare := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, planningRequest)
	snapshot, err := app.ChatFacade.GetRunSnapshot(prepare.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := ""
	for _, call := range snapshot.ToolCalls {
		if call.ToolName != workflow.ResearchPlanPrepareTool {
			continue
		}
		if call.Status != tool.CallCompleted || call.Result == nil || call.Result.Status != tool.ResultSuccess {
			t.Fatal("direct candidate preparation failed", call)
		}
		var receipt struct {
			RunID            string `json:"runId"`
			ExecutionStarted bool   `json:"executionStarted"`
		}
		if json.Unmarshal(call.Result.Structured, &receipt) != nil || receipt.RunID == "" || receipt.ExecutionStarted {
			t.Fatal("invalid direct candidate receipt", call.Result)
		}
		candidateID = receipt.RunID
	}
	if candidateID == "" || candidateID == entry.ResearchTaskID || prepare.ModelTurns != 2 {
		t.Fatal("candidate did not receive an independent execution identity", candidateID, prepare.ModelTurns)
	}
	var current workflow.RunDetail
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current, err = app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.ResearchTaskID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Run.ID == candidateID && current.Run.Status == workflow.RunCompleted {
			break
		}
		if current.Run.Status == workflow.RunFailed || current.Run.Status == workflow.RunCancelled || current.Run.Status == workflow.RunInterrupted {
			t.Fatal("controlled planner failed", current.Run.ErrorCode, current.Run.ErrorMessage)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if current.Run.ID != candidateID || current.Run.Status != workflow.RunCompleted {
		t.Fatal("direct candidate did not converge", current.Run.ID, current.Run.Status)
	}
	session, err = app.WorkflowFacade.GetResearchTaskSession(project.ID, entry.ResearchTaskID)
	if err != nil || session.CurrentRun == nil || session.CurrentRun.Run.ID != candidateID {
		t.Fatal("task session did not expose its real candidate", session, err)
	}
	var inputs struct {
		Starter workflow.ResearchStarterContext `json:"starter_context"`
	}
	if json.Unmarshal(current.Run.Inputs, &inputs) != nil || inputs.Starter.ResearchIdea != planningRequest || inputs.Starter.PriorStarterRunID != "" || inputs.Starter.DiscussionConversationID != entry.ConversationID || inputs.Starter.DiscussionUserMessageID != prepare.UserMessageID || inputs.Starter.DiscussionRecord == nil || len(inputs.Starter.SelectedMaterials) != 1 || inputs.Starter.SelectedMaterials[0].AttachmentID != referenceID {
		t.Fatal("first candidate did not preserve direct host-owned requirements", inputs.Starter)
	}
	if len(inputs.Starter.UserRequests) != 2 || inputs.Starter.UserRequests[0] != "使用不存在材料准备候选" || inputs.Starter.UserRequests[1] != planningRequest {
		t.Fatal("candidate lost actual history or inherited a fabricated prior request", inputs.Starter.UserRequests)
	}
	original, found, err := sqlite.NewWorkflowRuntimeRepository(app.store.DB()).ResearchDiscussionForConversation(context.Background(), entry.ConversationID)
	if err != nil || !found || original.SourceWorkflowRunID != "" || original.PrimaryConversationID != entry.ConversationID {
		t.Fatal("candidate rebound the direct primary conversation", original, found, err)
	}
	readRun := startResearchAssistantFixtureChat(t, app, entry.ConversationID, profile.ID, "只读当前候选的输入，不重新规划。")
	readSnapshot, err := app.ChatFacade.GetRunSnapshot(readRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	readOK := false
	for _, call := range readSnapshot.ToolCalls {
		if call.ToolName == workflow.ResearchTaskReadTool && call.Status == tool.CallCompleted && call.Result != nil && call.Result.Status == tool.ResultSuccess && strings.Contains(string(call.Result.Structured), planningRequest) {
			readOK = true
		}
	}
	if !readOK {
		t.Fatal("primary direct discussion could not read the first candidate", readSnapshot.ToolCalls)
	}
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=?`, entry.ResearchTaskID).Scan(&runs); err != nil || runs != 1 {
		t.Fatal("direct task created compatibility or duplicate runs", runs, err)
	}
	var formalRuns int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE research_task_id=? AND workflow_purpose<>'research_starter'`, entry.ResearchTaskID).Scan(&formalRuns); err != nil || formalRuns != 0 {
		t.Fatal("candidate preparation executed formal research", formalRuns, err)
	}
	beforeRestartID, beforeRestartHash, beforeRestartRequests := current.Run.ID, current.Run.InputsSHA256, requests.Load()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopenedSession, err := app.WorkflowFacade.GetResearchTaskSessionByConversation(entry.ConversationID)
	if err != nil || reopenedSession == nil || reopenedSession.CurrentRunUnavailable || reopenedSession.CurrentRun == nil {
		t.Fatal("conversation restore lost its direct task session", reopenedSession, err)
	}
	reopened := *reopenedSession.CurrentRun
	if reopened.Run.ID != beforeRestartID || reopened.Run.InputsSHA256 != beforeRestartHash || requests.Load() != beforeRestartRequests {
		t.Fatal("restart regenerated candidate or replayed work", reopened.Run.ID, requests.Load(), beforeRestartRequests, err)
	}
	if err := sqlite.NewResearchTaskRepository(app.store.DB()).Archive(context.Background(), project.ID, entry.ResearchTaskID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	archived, err := app.WorkflowFacade.GetResearchTaskSession(project.ID, entry.ResearchTaskID)
	if err != nil || archived.Entry.CanExplore || archived.CurrentRun == nil || archived.CurrentRun.Run.ID != candidateID {
		t.Fatal("archived task lost history or retained exploration authority", archived, err)
	}
}

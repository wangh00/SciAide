package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
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
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	projectapp "github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestResearchOpeningDiscussesThenPreparesWithoutExecuting(t *testing.T) {
	var discussionRequests atomic.Int32
	var frozenDiscussionReads atomic.Int32
	stage := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if strings.Contains(prompt, "discussionRecord") && !strings.Contains(prompt, "planning_discussion") {
			t.Error("planner lacks frozen discussion reader")
		}
	}, func(plan map[string]any) {
		missing := []any{}
		for i := 0; i < 30; i++ {
			missing = append(missing, strings.Repeat("m", 1800))
		}
		plan["missingInformation"] = missing
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
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
		_ = json.Unmarshal(body, &req)
		planning := ""
		for _, tool := range req.Tools {
			if tool.Function.Name == "research_plan_prepare" {
				planning = tool.Function.Name
			}
		}
		if planning == "" {
			if _, schemaErr := workflowAITestSchema(workflowAITestPrompt(body)); schemaErr != nil {
				w.Header().Set("Content-Type", "text/event-stream")
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "可以解释现有结果，不执行返修。"}, "finish_reason": "stop"}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
				return
			}
			// Actually read the immutable negotiation through the production tool
			// before returning the planner candidate; do not merely assert a prompt.
			if strings.Contains(string(body), "discussionRecord") && strings.Contains(string(body), `\"normalizedQuestion\"`) {
				hasRecord := false
				readName := ""
				for _, m := range req.Messages {
					if m.Role == "tool" && strings.Contains(string(m.Content), "planning_discussion") {
						hasRecord = true
						if !strings.Contains(string(m.Content), "不要进行招募") || !strings.Contains(string(m.Content), "要可执行方案") {
							t.Error("planner lost negotiated history page")
						}
					}
				}
				for _, candidate := range req.Tools {
					if candidate.Function.Name == "research_task_read" {
						readName = candidate.Function.Name
					}
				}
				if !hasRecord && readName != "" {
					frozenDiscussionReads.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "read-frozen-discussion", "function": map[string]any{"name": readName, "arguments": `{"section":"planning_discussion"}`}}}}, "finish_reason": "tool_calls"}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
					return
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			stage.Config.Handler.ServeHTTP(w, r)
			return
		}
		discussionRequests.Add(1)
		last := ""
		index := 0
		for i, m := range req.Messages {
			if m.Role == "user" {
				_ = json.Unmarshal(m.Content, &last)
				index = i
			}
		}
		toolResult := false
		for _, m := range req.Messages[index+1:] {
			toolResult = toolResult || m.Role == "tool"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var delta map[string]any
		finish := "stop"
		if strings.Contains(last, "要可执行方案") && !toolResult {
			if !strings.Contains(string(body), "不要进行招募") {
				t.Error("discussion lost earlier constraints across runs")
			}
			args := `{"researchIdea":"研究手机与睡眠的关系；不要进行招募，先要可执行方案"}`
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("prepare-%d", discussionRequests.Load()), "function": map[string]any{"name": planning, "arguments": args}}}}
			finish = "tool_calls"
		} else {
			delta = map[string]any{"content": "可以继续讨论，路线只会在采用后执行。"}
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
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "自然对话开局"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "我还没想好", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, DiscussionOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Run.Status != workflow.RunCompleted || len(entry.AIExecutions) != 0 || discussionRequests.Load() != 0 {
		t.Fatal("entry executed a model/stage", entry)
	}
	var chats int
	_ = app.store.DB().QueryRow(`SELECT count(*) FROM runs`).Scan(&chats)
	if chats != 0 {
		t.Fatal("entry spawned chat")
	}
	attachmentIDs := []string{}
	start := func(conv, text string) chat.Run {
		client, _ := id.New()
		run, e := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: conv, Text: text, AttachmentIDs: attachmentIDs, ClientMessageID: client, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
		if e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			snap, e := app.ChatFacade.GetRunSnapshot(run.ID)
			if e != nil {
				t.Fatal(e)
			}
			if snap.Run.Status == chat.RunCompleted {
				return snap.Run
			}
			if snap.Run.Status == chat.RunFailed {
				t.Fatalf("chat failed: %+v", snap.Run)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("chat wait timed out")
		return run
	}
	file := filepath.Join(root, "discussion.txt")
	if err := os.WriteFile(file, []byte("Discussion-only file: inspect without adopting."), 0600); err != nil {
		t.Fatal(err)
	}
	batch, err := app.AttachmentFacade.ImportConversationDocumentPaths(project.ID, entry.Run.ConversationID, []string{file})
	if err != nil || len(batch.Attachments) != 1 {
		t.Fatal("import", err, batch)
	}
	attachmentIDs = []string{batch.Attachments[0].ID}
	for _, text := range []string{"你好", "只是解释一下，不要进行招募"} {
		run := start(entry.Run.ConversationID, text)
		if run.ModelTurns != 1 {
			t.Fatal("ordinary discussion replanned")
		}
	}
	latest, _ := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.Run.ResearchTaskID)
	if latest.Run.ID != entry.Run.ID {
		t.Fatal("greeting created planner")
	}
	attachmentIDs = nil
	start(entry.Run.ConversationID, "要可执行方案，先帮我规划")
	latest, err = app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.Run.ResearchTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Run.ID == entry.Run.ID || latest.Run.ResearchTaskID != entry.Run.ResearchTaskID {
		t.Fatal("not same task")
	}
	planRun := waitForWorkflowState(t, app, project.ID, latest.Run.ID, workflow.RunCompleted, 20*time.Second)
	if frozenDiscussionReads.Load() == 0 {
		t.Fatal("planner never actually read the frozen negotiation")
	}
	plan, err := app.WorkflowFacade.GetResearchStarterPlan(project.ID, planRun.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) == 0 || plan.Routes[0].Validation == "invalid" {
		t.Fatal("valid plan missing", plan)
	}
	// A large valid candidate must not pin itself in the system context and
	// break a plain greeting when switching to a supported smaller model.
	small, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "Small discussion window", BaseURL: server.URL + "/v1", APIProtocol: modelprofile.ProtocolOpenAIChat, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 32768}}, TimeoutSeconds: 10, CustomHeaders: map[string]string{}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(planRun.Run.Outputs) < 50_000 {
		t.Fatal("large candidate fixture did not reproduce original budget")
	}
	previousProfile := profile
	profile = small
	if greeting := start(planRun.Run.ConversationID, "你好"); greeting.ModelTurns != 1 {
		t.Fatal("large candidate disrupted ordinary reply")
	}
	profile = previousProfile
	var formal int
	_ = app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs WHERE workflow_purpose='user_plan'`).Scan(&formal)
	if formal != 0 {
		t.Fatal("preparation executed formal route")
	}
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	// Restart restores the exact dialogue and candidate; no model rerun.
	plan, err = app.WorkflowFacade.GetResearchStarterPlan(project.ID, planRun.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := start(planRun.Run.ConversationID, "你好，先解释路线，不要开始")
	content, err := app.workflow.ReadResearchTask(context.Background(), ordinary.ID, workflow.ResearchTaskReadCommand{Section: "supplemental_content", AttachmentID: batch.Attachments[0].ID})
	if err != nil || !strings.Contains(string(content), "Discussion-only file") {
		t.Fatal("earlier private file unreadable", err, string(content))
	}
	other, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "另一个任务", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, DiscussionOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	foreign := start(other.Run.ConversationID, "你好")
	if _, err := app.workflow.ReadResearchTask(context.Background(), foreign.ID, workflow.ResearchTaskReadCommand{Section: "supplemental_content", AttachmentID: batch.Attachments[0].ID}); err == nil {
		t.Fatal("cross-task private attachment access")
	}
	// Build legacy-style unbound owners at INSERT, never bypass immutability.
	repo := sqlite.NewWorkflowRuntimeRepository(app.store.DB())
	var legacyConversations []string
	for i := 0; i < 2; i++ {
		run := entry.Run
		run.ID, _ = id.New()
		run.ResearchTaskID = ""
		run.ConversationID, _ = id.New()
		conv := conversation.Conversation{Title: "Legacy discussion", ReasoningLevel: "medium", ID: run.ConversationID, ProjectID: project.ID, PermissionMode: run.PermissionMode, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt}
		eventID, _ := id.New()
		if err := repo.CreateResearchRun(context.Background(), run, nil, workflow.RuntimeEvent{ID: eventID, WorkflowRunID: run.ID, Type: "workflow.created", Payload: json.RawMessage(`{}`), CreatedAt: run.CreatedAt}, conv); err != nil {
			t.Fatal(err)
		}
		start(conv.ID, fmt.Sprintf("legacy-%d", i))
		legacyConversations = append(legacyConversations, conv.ID)
	}
	history, err := repo.ResearchTaskDiscussionMessages(context.Background(), legacyConversations[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range history {
		if m.ConversationID != legacyConversations[1] {
			t.Fatal("empty task leaked history")
		}
	}

	plan, err = app.WorkflowFacade.GetResearchStarterPlan(project.ID, planRun.Run.ID)
	if err != nil || len(plan.Routes) == 0 || plan.Routes[0].Validation == "invalid" {
		t.Fatal("greeting unnecessarily invalidated plan", err)
	}
	start(planRun.Run.ConversationID, "要可执行方案，保留不要进行招募的要求")
	next, err := app.WorkflowFacade.GetCurrentResearchTaskRun(project.ID, entry.Run.ResearchTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planRun.Run.ID, RouteID: plan.RecommendedRouteID}); err == nil {
		t.Fatal("superseded candidate executed after a changed plan was prepared")
	}
	next = waitForWorkflowState(t, app, project.ID, next.Run.ID, workflow.RunCompleted, 20*time.Second)
	plan, err = app.WorkflowFacade.GetResearchStarterPlan(project.ID, next.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Archive restore remaps all discussion identities and keeps the candidate
	// usable without replaying the model or borrowing the original task.
	projects := projectapp.NewService(sqlite.NewProjectRepository(app.store.DB()), filepath.Join(root, "archive-workspaces"), filepath.Join(root, "archive-trash"))
	archives, err := projectarchive.NewService(sqlite.NewProjectArchiveRepository(app.store.DB()), projects, filepath.Join(root, "archive-workspaces"), filepath.Join(root, "archive-staging"), filepath.Join(root, "archive-trash"), "test")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := archives.Export(context.Background(), project.ID, filepath.Join(root, "opening.sciaide-project"))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := archives.Restore(context.Background(), projectarchive.RestoreCommand{Path: exported.Path})
	if err != nil {
		t.Fatal(err)
	}
	var restoredPlanID string
	if err := app.store.DB().QueryRow(`SELECT id FROM workflow_runs WHERE project_id=? AND creation_key LIKE 'research-discussion-plan:%' ORDER BY created_at DESC,id DESC LIMIT 1`, restored.Project.ID).Scan(&restoredPlanID); err != nil {
		t.Fatal(err)
	}
	restoredPlan, err := app.WorkflowFacade.GetResearchStarterPlan(restored.Project.ID, restoredPlanID)
	if err != nil || len(restoredPlan.Routes) == 0 || restoredPlan.Routes[0].Validation == "invalid" {
		t.Fatal("restored candidate invalid", err, restoredPlan)
	}
	restoredDetail, err := app.WorkflowFacade.GetRun(restored.Project.ID, restoredPlanID)
	if err != nil {
		t.Fatal(err)
	}
	// Restart/archive restoration preserves the full creation-event record,
	// with source identities remapped, without rerunning the planner.
	var restoredHistory struct {
		History []conversation.Message `json:"discussionHistory"`
	}
	restoredChat := start(restoredDetail.Run.ConversationID, "解释现有路线")
	readDiscussion := func(chatID string) string {
		var content strings.Builder
		command := workflow.ResearchTaskReadCommand{Section: "planning_discussion"}
		for {
			record, err := app.workflow.ReadResearchTask(context.Background(), chatID, command)
			var page struct {
				Content string `json:"content"`
				Next    int    `json:"nextOffset"`
				More    bool   `json:"hasMore"`
			}
			if err != nil || json.Unmarshal(record, &page) != nil {
				t.Fatal("frozen discussion unreadable", err)
			}
			content.WriteString(page.Content)
			if !page.More {
				break
			}
			if page.Next <= command.Offset {
				t.Fatal("paging stalled")
			}
			command.Offset = page.Next
		}
		return content.String()
	}
	if json.Unmarshal([]byte(readDiscussion(restoredChat.ID)), &restoredHistory) != nil || len(restoredHistory.History) == 0 {
		t.Fatal("restored frozen discussion incomplete")
	}
	for _, m := range restoredHistory.History {
		if m.ConversationID == entry.Run.ConversationID {
			t.Fatal("restored history borrows original conversation")
		}
		if m.Status != conversation.MessageComplete {
			t.Fatal("frozen negotiation included unfinished assistant")
		}
		for _, p := range m.Parts {
			if p.MessageID != m.ID {
				t.Fatal("restored part borrows old message")
			}
		}
	}

	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: next.Run.ID, RouteID: plan.RecommendedRouteID})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Run == nil || adopted.Run.Run.ResearchTaskID != entry.Run.ResearchTaskID {
		t.Fatal("adoption split the task")
	}
	completed := waitForWorkflowState(t, app, project.ID, adopted.Run.Run.ID, workflow.RunCompleted, 20*time.Second)
	finalChat := start(completed.Run.ConversationID, "解释报告，不要返修")
	if record := readDiscussion(finalChat.ID); !strings.Contains(record, "不要进行招募") || !strings.Contains(record, "要可执行方案") {
		t.Fatal("adoption lost frozen discussion")
	}
	page, err := app.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: project.ID, TaskID: entry.Run.ResearchTaskID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.LatestRunID != completed.Run.ID {
		t.Fatal("timeline lost current plan")
	}
	found := false
	for _, e := range page.Entries {
		if e.Message != nil {
			for _, p := range e.Message.Parts {
				found = found || strings.Contains(p.Text, "不要进行招募")
			}
		}
	}
	if !found {
		t.Fatal("human negotiation missing from same waterfall")
	}
}

func TestResearchVersion7StageContinuesAfterTaskReadUpgrade(t *testing.T) {
	testResearchLegacyStageContinuesAfterTaskReadUpgrade(t, "7")
}

func TestResearchVersion8StageContinuesAfterTaskReadUpgrade(t *testing.T) {
	testResearchLegacyStageContinuesAfterTaskReadUpgrade(t, "8")
}

func testResearchLegacyStageContinuesAfterTaskReadUpgrade(t *testing.T, oldVersion string) {
	t.Helper()
	ctx := context.Background()
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name       string          `json:"name"`
					Parameters json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
			return
		}
		name, read := "", false
		for _, d := range req.Tools {
			if d.Function.Name == "research_task_read" {
				name = d.Function.Name
				if (strings.Contains(string(d.Function.Parameters), "planning_discussion") != (oldVersion == "8")) || strings.Contains(string(d.Function.Parameters), `"messages"`) || strings.Contains(string(d.Function.Parameters), `"pointer"`) {
					t.Error("upgrade changed frozen legacy wire scope")
				}
			}
		}
		for _, m := range req.Messages {
			read = read || m.Role == "tool" && strings.Contains(string(m.Content), "stage_input")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{"content": "```json\n{\"summary\":\"Existing frozen task records remain readable.\"}\n```"}
		finish := "stop"
		if !read {
			if name == "" {
				t.Error("old stage lost task reader")
				return
			}
			reads.Add(1)
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "read-old-task", "function": map[string]any{"name": name, "arguments": `{"section":"stage_input"}`}}}}
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
	app.Startup(ctx)
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Old task-read contract"})
	if err != nil {
		t.Fatal(err)
	}
	defs, err := app.ToolFacade.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	oldRegistry := tool.NewRegistry()
	for _, d := range defs {
		if d.QualifiedName == workflow.ResearchTaskReadTool {
			var ok bool
			if oldVersion == "7" {
				d, ok = tool.ResearchTaskReadVersion7(d)
			} else {
				d, ok = tool.ResearchTaskReadVersion8(d)
			}
			if !ok {
				t.Fatal("no old contract")
			}
		}
		if err := oldRegistry.Register(ctx, starterUpgradeDefinitionTool{d}); err != nil {
			t.Fatal(err)
		}
	}
	definition := workflow.Definition{SchemaVersion: 1, Name: "Old frozen task read", Nodes: []workflow.Node{{ID: "read", Name: "Read existing input", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "old-read-v1", Prompt: "Read this stage_input before summarizing.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","minLength":1}}}`)}}, Outputs: []workflow.Output{{Name: "record", Type: workflow.TypeObject, FromNode: "read", FromPort: "analysis", Required: true}}}
	compiled, err := workflow.NewCompiler(oldRegistry).Compile(ctx, definition)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	saved, err := sqlite.NewWorkflowRepository(app.store.DB()).SaveVersion(ctx, workflow.SaveRecord{Workflow: workflow.Workflow{ID: "old-read-workflow", ProjectID: project.ID, Purpose: workflow.PurposeUserPlan, Name: definition.Name, CreatedAt: now, UpdatedAt: now}, Version: workflow.Version{ID: "old-read-version", WorkflowID: "old-read-workflow", Definition: definition, DefinitionSHA256: compiled.DefinitionSHA256, Compilation: compiled, CompilationSHA256: compiled.CompilationSHA256, CreatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(ctx)
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, WorkflowVersionID: saved.Version.ID, Inputs: json.RawMessage(`{}`), PermissionMode: conversation.PermissionFullAccess, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	if reads.Load() != 1 || completed.Run.CompilationSHA256 != compiled.CompilationSHA256 || completed.Run.Compilation.Nodes[0].AllowedTools[0].Version != oldVersion {
		t.Fatal("upgrade rewrote or failed old frozen route", reads.Load())
	}
	var failures int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id=? AND status<>'completed'`, completed.AIExecutions[0].ChatRunID).Scan(&failures); err != nil || failures != 0 {
		t.Fatal("old reader required failed tool calls", failures, err)
	}
}

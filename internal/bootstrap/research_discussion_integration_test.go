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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	projectapp "github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestResearchDiscussionNegotiatesThenConfirmsAfterRestart(t *testing.T) {
	var mu sync.Mutex
	proposalReply := make(chan struct{})
	releaseReply := make(chan struct{})
	var replyOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReply) }) }
	defer release()
	revisedStages := 0
	stageServer := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if strings.Contains(prompt, `"_userRevision"`) {
			mu.Lock()
			revisedStages++
			mu.Unlock()
			if !strings.Contains(prompt, "Keep the population") || !strings.Contains(prompt, "Clarify variable definitions") {
				t.Error("lost negotiated revision requirements")
			}
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
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
		_ = json.Unmarshal(body, &request)
		discussion := false
		for _, tool := range request.Tools {
			if tool.Function.Name == "research_task_read" {
				discussion = true
			}
		}
		if !discussion {
			r.Body = io.NopCloser(bytes.NewReader(body))
			stageServer.Config.Handler.ServeHTTP(w, r)
			return
		}
		lastUser := ""
		lastUserIndex := -1
		for i, m := range request.Messages {
			if m.Role == "user" {
				_ = json.Unmarshal(m.Content, &lastUser)
				lastUserIndex = i
			}
		}
		hasTool := false
		for _, m := range request.Messages[lastUserIndex+1:] {
			hasTool = hasTool || m.Role == "tool"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if hasTool {
			if strings.Contains(lastUser, "REVISION:") && !strings.Contains(lastUser, "REVISED") {
				replyOnce.Do(func() { close(proposalReply) })
				select {
				case <-releaseReply:
				case <-r.Context().Done():
					return
				}
			}
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "已核对当前任务。方案尚未执行，请确认卡片或继续讨论。"}, "finish_reason": "stop"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		name, args := "research_task_read", `{"section":"overview"}`
		if strings.Contains(lastUser, "REVISION") {
			node := "method_selection"
			if strings.Contains(lastUser, "REVISED") {
				node = "research_design"
			}
			name = "research_revision_propose"
			data, _ := json.Marshal(workflow.ProposeResearchRevisionCommand{NodeID: node, Summary: "Clarify the design", Changes: []string{"Keep the population", "Clarify variable definitions"}, Reason: "The design needs clearer variable definitions, not a new research direction"})
			args = string(data)
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("discussion-%d", len(request.Messages)), "function": map[string]any{"name": name, "arguments": args}}}}, "finish_reason": "tool_calls"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	defer release()
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Discussion revision fixture"})
	if err != nil {
		t.Fatal(err)
	}
	starter, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "Design a sleep diary study without data", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, app, project.ID, starter.Run.ID, workflow.RunCompleted, 15*time.Second)
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planned.Run.ID, RouteID: "design_first"})
	if err != nil || adopted.Run == nil {
		t.Fatalf("adopt=%+v err=%v", adopted, err)
	}
	completed := waitForWorkflowState(t, app, project.ID, adopted.Run.Run.ID, workflow.RunCompleted, 20*time.Second)
	readTimeline := func(projectID, taskID string) []workflow.ResearchTimelineEntry {
		t.Helper()
		var all []workflow.ResearchTimelineEntry
		cursor := ""
		for {
			page, err := app.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: projectID, TaskID: taskID, Before: cursor, Limit: 3})
			if err != nil {
				t.Fatal(err)
			}
			all = append(page.Entries, all...)
			if !page.HasMore {
				break
			}
			if cursor == page.NextBefore || page.NextBefore == "" {
				t.Fatal("timeline cursor stalled")
			}
			cursor = page.NextBefore
		}
		var last int64
		seen := map[string]bool{}
		for _, entry := range all {
			sequence, err := strconv.ParseInt(entry.Sequence, 10, 64)
			if err != nil || sequence <= last || seen[entry.ID] {
				t.Fatal("unstable timeline ordering", entry)
			}
			last, seen[entry.ID] = sequence, true
			if entry.Message != nil && entry.Message.Role == "user" && entry.Message.Internal {
				t.Fatal("internal input leaked into timeline")
			}
		}
		return all
	}
	initialTimeline := readTimeline(project.ID, completed.Run.ResearchTaskID)
	var originalDelivery workflow.ResearchTimelineEntry
	planningFound := false
	for _, entry := range initialTimeline {
		if entry.EventType == "workflow.completed" && entry.RunID == planned.Run.ID {
			planningFound = true
			if entry.Active {
				t.Fatal("adopted planning remained actionable")
			}
		}
		if entry.EventType == "workflow.completed" && entry.RunID == completed.Run.ID {
			originalDelivery = entry
		}
	}
	if !planningFound || !originalDelivery.Active || !strings.Contains(string(originalDelivery.Snapshot), "research_design") {
		t.Fatal("planning or completed delivery missing", initialTimeline)
	}
	if _, err := app.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: "another-project", TaskID: completed.Run.ResearchTaskID}); err == nil {
		t.Fatal("cross-project timeline exposed")
	}
	before := workflow.ResearchRevisionSnapshot(completed)
	startChat := func(text string) chat.Run {
		clientMessageID, err := id.New()
		if err != nil {
			t.Fatal(err)
		}
		value, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: completed.Run.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, Text: text, ClientMessageID: clientMessageID})
		if err != nil {
			t.Fatal(err)
		}
		if value.UserMessageID != clientMessageID {
			t.Fatal("client message ID not preserved")
		}
		return value
	}
	waitChat := func(value chat.Run) {
		for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			snapshot, err := app.ChatFacade.GetRunSnapshot(value.ID)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Run.Status == chat.RunCompleted {
				for _, call := range snapshot.ToolCalls {
					if call.Status != "completed" {
						t.Fatalf("tool failed: %+v", call)
					}
				}
				return
			}
			if snapshot.Run.Status == chat.RunFailed {
				t.Fatalf("chat failed: %+v", snapshot.Run)
			}
		}
		t.Fatal("discussion timed out")
	}
	refresh := func() workflow.RunDetail {
		d, e := app.WorkflowFacade.GetRun(project.ID, completed.Run.ID)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	consultation := startChat("How many stages does this task have?")
	waitChat(consultation)
	testDiscussionWorkspaceScope(t, app, project, consultation, completed)
	if d := refresh(); len(d.RevisionProposals) != 0 || workflow.ResearchRevisionSnapshot(d) != before {
		t.Fatal("consultation mutated task")
	}
	firstChat := startChat("REVISION: please clarify variable definitions")
	select {
	case <-proposalReply:
	case <-time.After(12 * time.Second):
		t.Fatal("proposal tool did not finish before reply")
	}
	generating := refresh()
	if len(generating.RevisionProposals) != 1 || generating.RevisionProposals[0].CanConfirm || generating.RevisionProposals[0].Status != "pending" {
		t.Fatalf("premature confirmation state: %+v", generating.RevisionProposals)
	}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: project.ID, RunID: completed.Run.ID, ProposalID: generating.RevisionProposals[0].ID, Confirmed: true}); err == nil {
		t.Fatal("proposal confirmed before source reply completed")
	}
	release()
	waitChat(firstChat)
	first := refresh()
	if len(first.RevisionProposals) != 1 || !first.RevisionProposals[0].CanConfirm || workflow.ResearchRevisionSnapshot(first) != before {
		t.Fatalf("proposal=%+v", first.RevisionProposals)
	}
	confirm := workflow.ConfirmResearchRevisionCommand{ProjectID: project.ID, RunID: completed.Run.ID, ProposalID: first.RevisionProposals[0].ID}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(confirm); err == nil {
		t.Fatal("implicit confirmation accepted")
	}
	secondChat := startChat("REVISED REVISION: only change the design wording, keep the population and the method")
	confirm.Confirmed = true
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(confirm); err == nil {
		t.Fatal("old proposal accepted after a new human turn")
	}
	waitChat(secondChat)
	second := refresh()
	discussionTimeline := readTimeline(project.ID, completed.Run.ResearchTaskID)
	var deliveryIndex, userIndex, assistantIndex, proposalIndex int
	for i, entry := range discussionTimeline {
		if entry.ID == originalDelivery.ID {
			deliveryIndex = i
		}
		if entry.Message != nil && entry.Message.ID == secondChat.UserMessageID {
			userIndex = i
		}
		if entry.Message != nil && entry.Message.ID == secondChat.AssistantMessageID {
			assistantIndex = i
		}
		if entry.Proposal != nil && entry.Proposal.ChatRunID == secondChat.ID {
			proposalIndex = i
		}
	}
	if !(deliveryIndex < userIndex && userIndex < assistantIndex && assistantIndex < proposalIndex) {
		t.Fatal("discussion or proposal reordered before delivery")
	}
	if len(second.RevisionProposals) != 2 || second.RevisionProposals[0].Status != "superseded" || second.RevisionProposals[1].NodeID != "research_design" {
		t.Fatalf("proposal replacement=%+v", second.RevisionProposals)
	}
	repository := sqlite.NewWorkflowRuntimeRepository(app.store.DB())
	for _, section := range []string{"messages", "attempts", "tool_calls", "revisions"} {
		if raw, err := repository.ReadResearchDiscussionRecords(context.Background(), completed.Run.ID, section, ""); err != nil || !json.Valid(raw) {
			t.Fatalf("section=%s err=%v", section, err)
		}
	}
	var history strings.Builder
	for offset := 0; ; {
		page, err := app.workflow.ReadResearchTask(context.Background(), secondChat.ID, workflow.ResearchTaskReadCommand{Section: "messages", Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		var part struct {
			Content string `json:"content"`
			Next    int    `json:"nextOffset"`
			More    bool   `json:"hasMore"`
		}
		if err := json.Unmarshal(page, &part); err != nil {
			t.Fatal(err)
		}
		history.WriteString(part.Content)
		if !part.More {
			break
		}
		if part.Next <= offset {
			t.Fatal("pagination stalled")
		}
		offset = part.Next
	}
	if !strings.Contains(history.String(), "Design a sleep diary") || !json.Valid([]byte(history.String())) {
		t.Fatal("paginated planning history missing or invalid")
	}
	if _, err := repository.ResolveResearchDiscussion(context.Background(), completed.AIExecutions[0].ChatRunID); err == nil {
		t.Fatal("stage AI can use discussion tools")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	restored := refresh()
	if !restored.RevisionProposals[1].CanConfirm {
		t.Fatal("pending proposal lost after restart")
	}
	confirm.ProposalID = restored.RevisionProposals[1].ID
	wrongProject := confirm
	wrongProject.ProjectID = "another-project"
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(wrongProject); err == nil {
		t.Fatal("cross-project proposal confirmed")
	}
	if _, err := app.WorkflowFacade.ListResearchRevisionProposals("another-project", completed.Run.ID); err == nil {
		t.Fatal("cross-project proposal list exposed")
	}
	if _, err := app.store.DB().Exec(`UPDATE research_revision_proposals SET proposal_json='{}' WHERE id=?`, confirm.ProposalID); err == nil {
		t.Fatal("frozen proposal mutated")
	}
	if _, err := app.store.DB().Exec(`UPDATE research_revision_proposals SET status='pending' WHERE id=?`, restored.RevisionProposals[0].ID); err == nil {
		t.Fatal("superseded proposal revived")
	}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(confirm); err != nil {
		t.Fatal(err)
	}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(confirm); err == nil {
		t.Fatal("double confirmation accepted")
	}
	revised := waitForWorkflowState(t, app, project.ID, completed.Run.ID, workflow.RunCompleted, 20*time.Second)
	revisedTimeline := readTimeline(project.ID, completed.Run.ResearchTaskID)
	deliveries, activeDeliveries := 0, 0
	for _, entry := range revisedTimeline {
		if entry.ID == originalDelivery.ID && (entry.Active || string(entry.Snapshot) != string(originalDelivery.Snapshot)) {
			t.Fatal("revision overwrote old delivery")
		}
		if entry.RunID == completed.Run.ID && entry.EventType == "workflow.completed" {
			deliveries++
			if entry.Active {
				activeDeliveries++
			}
		}
	}
	if deliveries != 2 || activeDeliveries != 1 {
		t.Fatal("delivery generations missing", deliveries, activeDeliveries)
	}
	newPage, err := app.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: project.ID, TaskID: completed.Run.ResearchTaskID, After: initialTimeline[len(initialTimeline)-1].Sequence, Limit: 2})
	if err != nil || !newPage.HasMore || len(newPage.Entries) != 2 {
		t.Fatal("forward timeline pagination failed", newPage, err)
	}
	for _, step := range revised.Steps {
		for _, old := range completed.Steps {
			if step.NodeID == old.NodeID {
				if step.NodeID == "research_design" || step.NodeID == "independent_review" || step.NodeID == "delivery_gate" {
					if step.Attempt != old.Attempt+1 {
						t.Fatalf("not rerun: %s", step.NodeID)
					}
				} else if step.Attempt != old.Attempt || string(step.Output) != string(old.Output) {
					t.Fatalf("unaffected stage changed: %s", step.NodeID)
				}
			}
		}
	}
	mu.Lock()
	count := revisedStages
	mu.Unlock()
	if count < 2 {
		t.Fatal("revision instructions did not reach design and review")
	}
	var oldSnapshot string
	if err := app.store.DB().QueryRow(`SELECT previous_snapshot_json FROM research_revision_proposals WHERE id=?`, confirm.ProposalID).Scan(&oldSnapshot); err != nil || !strings.Contains(oldSnapshot, string(completed.Run.ID)) {
		t.Fatal("old delivery snapshot lost", err)
	}
	if _, err := app.WorkflowFacade.SaveDeliverable(wailstransport.SaveWorkflowDeliverableRequest{ProjectID: project.ID, RunID: revised.Run.ID, OutputName: "research_design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.workflow.ProposeResearchRevision(context.Background(), secondChat.ID, "unused", workflow.ProposeResearchRevisionCommand{NodeID: "research_design", Summary: "x", Changes: []string{"x"}, Reason: "x"}); err == nil {
		t.Fatal("registered delivery remained revisable")
	}
	ordinary, err := app.ConversationFacade.CreateConversation(wailstransport.CreateConversationRequest{ProjectID: project.ID, Title: "unbound discussion", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.workflow.ReadResearchTask(context.Background(), ordinary.ID, workflow.ResearchTaskReadCommand{Section: "overview"}); err == nil {
		t.Fatal("unbound identity read research task")
	}
	projects := projectapp.NewService(sqlite.NewProjectRepository(app.store.DB()), filepath.Join(root, "archive-workspaces"), filepath.Join(root, "archive-trash"))
	archives, err := projectarchive.NewService(sqlite.NewProjectArchiveRepository(app.store.DB()), projects, filepath.Join(root, "archive-workspaces"), filepath.Join(root, "archive-staging"), filepath.Join(root, "archive-trash"), "test")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := archives.Export(context.Background(), project.ID, filepath.Join(root, "revision.sciaide-project"))
	if err != nil {
		t.Fatal(err)
	}
	restoredArchive, err := archives.Restore(context.Background(), projectarchive.RestoreCommand{Path: exported.Path})
	if err != nil {
		t.Fatal(err)
	}
	var restoredRun string
	if err := app.store.DB().QueryRow(`SELECT wr.id FROM workflow_runs wr JOIN research_revision_proposals p ON p.workflow_run_id=wr.id WHERE wr.project_id=? LIMIT 1`, restoredArchive.Project.ID).Scan(&restoredRun); err != nil {
		t.Fatal(err)
	}
	archiveProposals, err := app.WorkflowFacade.ListResearchRevisionProposals(restoredArchive.Project.ID, restoredRun)
	if err != nil || len(archiveProposals) != 2 {
		t.Fatalf("archived proposals=%+v err=%v", archiveProposals, err)
	}
	for _, p := range archiveProposals {
		if p.RunID != restoredRun || p.CanConfirm || p.ID == confirm.ProposalID {
			t.Fatal("unsafe restored proposal", p)
		}
	}
	var restoredTask string
	if err := app.store.DB().QueryRow(`SELECT research_task_id FROM workflow_runs WHERE id=?`, restoredRun).Scan(&restoredTask); err != nil {
		t.Fatal(err)
	}
	finalTimeline := readTimeline(project.ID, completed.Run.ResearchTaskID)
	archiveTimeline := readTimeline(restoredArchive.Project.ID, restoredTask)
	if len(finalTimeline) != len(archiveTimeline) {
		t.Fatal("archive lost timeline entries", len(finalTimeline), len(archiveTimeline))
	}
	for i, entry := range archiveTimeline {
		if entry.Kind != finalTimeline[i].Kind || entry.EventType != finalTimeline[i].EventType || entry.ID == finalTimeline[i].ID {
			t.Fatal("archive lost order or retained source identity", entry)
		}
	}
}

func testDiscussionWorkspaceScope(t *testing.T, app *Application, selected projectapp.Project, discussion chat.Run, completed workflow.RunDetail) {
	t.Helper()
	ctx := context.Background()
	repository := sqlite.NewWorkflowRuntimeRepository(app.store.DB())
	taskRoot, err := projectapp.ResearchTaskWorkspacePath(selected, completed.Run.ResearchTaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{discussion.ID, completed.AIExecutions[0].ChatRunID} {
		task, err := repository.ResearchTaskIDForSubject(ctx, tool.SubjectChatRun, runID)
		root, rootErr := repository.WorkspaceRootForSubject(ctx, tool.SubjectChatRun, runID)
		if err != nil || rootErr != nil || task != completed.Run.ResearchTaskID || filepath.Clean(root) != filepath.Clean(taskRoot) {
			t.Fatalf("wrong task scope: %q %q %v %v", task, root, err, rootErr)
		}
	}
	write := func(root, relative, text string) {
		t.Helper()
		name := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(taskRoot, "analysis-output/contrast.csv", "task contrast evidence")
	write(taskRoot, "research-inputs/input.csv", "task original input")
	write(selected.WorkspacePath, "analysis-output/contrast.csv", "project decoy")
	write(filepath.Join(filepath.Dir(taskRoot), "other-task"), "private.txt", "other task evidence")
	defs, err := app.ToolFacade.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	definitions := map[string]tool.Definition{}
	for _, def := range defs {
		definitions[def.QualifiedName] = def
	}
	service := tool.NewService(sqlite.NewToolRepository(app.store.DB()), tool.JSONSchemaValidator{})
	invoke := func(runID, name string, args map[string]any) tool.Execution {
		t.Helper()
		encoded, _ := json.Marshal(args)
		call, err := service.Propose(ctx, definitions[name], tool.CreateCommand{RunID: runID, ProviderCallID: fmt.Sprintf("scope-%d", time.Now().UnixNano()), Arguments: encoded})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.Start(ctx, call.ID); err != nil {
			t.Fatal(err)
		}
		result, err := app.tools.Execute(ctx, selected.ID, call.ID)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for relative, want := range map[string]string{"analysis-output/contrast.csv": "task contrast evidence", "research-inputs/input.csv": "task original input"} {
		result := invoke(discussion.ID, "builtin.workspace.read_text", map[string]any{"path": relative})
		if result.Result.Status != tool.ResultSuccess || result.Result.Text != want {
			t.Fatalf("read failed: %+v", result)
		}
	}
	listed := invoke(discussion.ID, "builtin.workspace.list", map[string]any{"path": "analysis-output"})
	if listed.Result.Status != tool.ResultSuccess || !strings.Contains(string(listed.Result.Structured), "contrast.csv") {
		t.Fatal("task directory missing", listed)
	}
	for _, relative := range []string{"../other-task/private.txt", filepath.Join(filepath.Dir(taskRoot), "other-task", "private.txt"), filepath.Join(selected.WorkspacePath, "analysis-output", "contrast.csv"), ".sciaide/project.json"} {
		result := invoke(discussion.ID, "builtin.workspace.read_text", map[string]any{"path": relative})
		if result.Result.Status != tool.ResultError {
			t.Fatalf("escaped task: %s %+v", relative, result)
		}
	}
	unbound, err := app.ConversationFacade.CreateConversation(wailstransport.CreateConversationRequest{ProjectID: selected.ID, Title: "Unbound scope fixture"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	freeRun := chat.Run{ID: "free-scope-run", ConversationID: unbound.ID, UserMessageID: "free-user", AssistantMessageID: "free-assistant", ModelProfileID: discussion.ModelProfileID, ModelID: discussion.ModelID, Status: chat.RunCompleted, CreatedAt: now, UpdatedAt: now, CompletedAt: &now}
	user := conversation.Message{ID: freeRun.UserMessageID, ConversationID: unbound.ID, RunID: freeRun.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}
	assistant := conversation.Message{ID: freeRun.AssistantMessageID, ConversationID: unbound.ID, RunID: freeRun.ID, Role: conversation.RoleAssistant, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewRunRepository(app.store.DB()).CreateWithMessages(ctx, freeRun, user, assistant); err != nil {
		t.Fatal(err)
	}
	root, err := repository.WorkspaceRootForSubject(ctx, tool.SubjectChatRun, freeRun.ID)
	if err != nil || root != "" {
		t.Fatal("ordinary chat inherited task", root, err)
	}
	result := invoke(freeRun.ID, "builtin.workspace.read_text", map[string]any{"path": "analysis-output/contrast.csv"})
	if result.Result.Status != tool.ResultSuccess || result.Result.Text != "project decoy" {
		t.Fatal("ordinary workspace changed", result)
	}
	result = invoke(freeRun.ID, "builtin.workspace.read_text", map[string]any{"path": filepath.Join(".sciaide", "tasks", completed.Run.ResearchTaskID, "analysis-output", "contrast.csv")})
	if result.Result.Status != tool.ResultError {
		t.Fatal("ordinary chat accessed private task", result)
	}
	if _, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: unbound.ID, ModelProfileID: discussion.ModelProfileID, ModelID: discussion.ModelID, Text: "must not replace another message", ClientMessageID: discussion.UserMessageID}); err == nil {
		t.Fatal("duplicate client identity overwrote an existing message")
	}
	var owner string
	if err := app.store.DB().QueryRow(`SELECT conversation_id FROM messages WHERE id=?`, discussion.UserMessageID).Scan(&owner); err != nil || owner != discussion.ConversationID {
		t.Fatal("message ownership changed", owner, err)
	}
}

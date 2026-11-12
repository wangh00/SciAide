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
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	projectapp "github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/platform/pythonruntime"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real import/message storage, planner/tool/HTTP pipeline, owned file handles,
// restart and Runtime.Start. No commercial model or formal user database.
func TestRouteDataReuseEndToEnd(t *testing.T) {
	var revisionMu sync.Mutex
	var revisionCommand json.RawMessage
	stage := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if !strings.Contains(prompt, "<metadata_output_schema>") {
			return
		}
		output["_fixturePythonCode"] = `import csv
import json
from pathlib import Path
rows = []
for source in SCIAIDE_INPUTS:
    with Path(source).open("r", encoding="utf-8-sig", newline="") as handle:
        rows.extend(csv.DictReader(handle))
result = {"rowCount": len(rows), "fileCount": len(SCIAIDE_INPUTS)}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\nRead exactly all confirmed input tables.", encoding="utf-8")
result`
	}, mutateStarterToEmpiricalReportRoute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
			Tools []struct{ Function struct{ Name string } }
		}
		_ = json.Unmarshal(body, &req)
		for _, value := range req.Tools {
			if value.Function.Name == "research_revision_propose" {
				w.Header().Set("Content-Type", "text/event-stream")
				hasResult := false
				for _, m := range req.Messages {
					if m.Role == "user" {
						hasResult = false
					}
					if m.Role == "tool" {
						hasResult = true
					}
				}
				revisionMu.Lock()
				args := string(revisionCommand)
				revisionMu.Unlock()
				delta := map[string]any{"content": "Ready to confirm the selected files."}
				finish := "stop"
				if args != "" && !hasResult {
					delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "earlier-table-proposal", "function": map[string]any{"name": "research_revision_propose", "arguments": args}}}}
					finish = "tool_calls"
				}
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
				return
			}
		}
		prepare := ""
		for _, value := range req.Tools {
			if value.Function.Name == "research_plan_prepare" {
				prepare = value.Function.Name
			}
		}
		if prepare == "" {
			r.Body = io.NopCloser(bytes.NewReader(body))
			stage.Config.Handler.ServeHTTP(w, r)
			return
		}
		last, index := "", 0
		for i, m := range req.Messages {
			if m.Role == "user" {
				_ = json.Unmarshal(m.Content, &last)
				index = i
			}
		}
		hasResult := false
		for _, m := range req.Messages[index+1:] {
			hasResult = hasResult || m.Role == "tool"
		}
		delta := map[string]any{"content": "已查看数据；采用路线后核对表格再执行。"}
		finish := "stop"
		if strings.Contains(last, "设计路线") && !hasResult {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "prepare-route", "function": map[string]any{"name": prepare, "arguments": `{"researchIdea":"分析本任务已上传的 CSV 表格，交付计算结果和报告"}`}}}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
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
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "uploaded data route confirmation"})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := app.PythonFacade.DetectInterpreters()
	if err != nil || len(discovery.Interpreters) == 0 {
		t.Fatal("test Python unavailable", err)
	}
	env := filepath.Join(t.TempDir(), "venv")
	if err := pythonruntime.New(app.pythonexec).CreateEnvironment(context.Background(), discovery.Interpreters[0].ExecutablePath, env); err != nil {
		t.Fatal("create isolated test venv", err)
	}
	if _, err := app.PythonFacade.BindProjectEnvironment(project.ID, filepath.Join(env, "Scripts", "python.exe")); err != nil {
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "帮我看看这份数据", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, DiscussionOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for name, content := range map[string]string{"wine.csv": "x,y\n1,2\n3,4\n", "notes.md": "# Fields\nx and y are measurements.\n", "unsent.csv": "x,y\n5,6\n"} {
		paths[name] = filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(paths[name], []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := app.AttachmentFacade.ImportConversationDocumentPaths(project.ID, entry.Run.ConversationID, []string{paths["wine.csv"], paths["notes.md"], paths["unsent.csv"]})
	if err != nil || len(batch.Attachments) != 3 {
		t.Fatal(batch, err)
	}
	ids := map[string]string{}
	for _, v := range batch.Attachments {
		ids[v.OriginalName] = v.ID
	}
	chatTurn := func(conversationID, text string, attachments []string) string {
		t.Helper()
		cr, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: conversationID, Text: text, AttachmentIDs: attachments, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
		if err != nil {
			t.Fatal(err)
		}
		until := time.Now().Add(15 * time.Second)
		for time.Now().Before(until) {
			snap, err := app.ChatFacade.GetRunSnapshot(cr.ID)
			if err != nil {
				t.Fatal(err)
			}
			if snap.Run.Status == chat.RunCompleted {
				return cr.ID
			}
			if snap.Run.Status == chat.RunFailed || snap.Run.Status == chat.RunCancelled {
				t.Fatal("chat failed", snap.Run)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("chat timeout")
		return ""
	}
	chatTurn(entry.Run.ConversationID, "帮我看看葡萄酒数据", []string{ids["wine.csv"], ids["notes.md"]})
	otherTask, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "无关研究", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, DiscussionOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	otherBatch, err := app.AttachmentFacade.ImportConversationDocumentPaths(project.ID, otherTask.Run.ConversationID, []string{paths["unsent.csv"]})
	if err != nil || len(otherBatch.Attachments) != 1 {
		t.Fatal(otherBatch, err)
	}
	chatTurn(otherTask.Run.ConversationID, "这是另一课题的数据", []string{otherBatch.Attachments[0].ID})
	chatTurn(entry.Run.ConversationID, "可以设计路线了", nil)
	var plannerID string
	if err := app.store.DB().QueryRow(`SELECT id FROM workflow_runs WHERE project_id=? AND creation_key LIKE 'research-discussion-plan:%' ORDER BY created_at DESC,id DESC LIMIT 1`, project.ID).Scan(&plannerID); err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, app, project.ID, plannerID, workflow.RunCompleted, 15*time.Second)
	if _, err := app.WorkflowFacade.GetResearchRouteDataCandidates(project.ID, plannerID, "empirical_report"); err == nil {
		t.Fatal("offered draft before route adoption")
	}
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planned.Run.ID, RouteID: "empirical_report"})
	if err != nil || adopted.Run != nil || adopted.ResearchTaskID != entry.Run.ResearchTaskID {
		t.Fatal("route auto executed/split task", adopted, err)
	}
	var inputsBefore map[string]any
	_ = json.Unmarshal(adopted.InitialInputs, &inputsBefore)
	if len(inputsBefore["input_paths"].([]any)) != 0 {
		t.Fatal("silently adopted table")
	}
	candidates, err := app.WorkflowFacade.GetResearchRouteDataCandidates(project.ID, plannerID, adopted.RouteID)
	if err != nil || len(candidates) != 1 || candidates[0].AttachmentID != ids["wine.csv"] {
		t.Fatal("unsent/notes/earlier chat scope", candidates, err)
	}
	var beforeAttachments, beforeRuns int
	_ = app.store.DB().QueryRow(`SELECT count(*) FROM attachments`).Scan(&beforeAttachments)
	_ = app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs`).Scan(&beforeRuns)
	command := workflow.StageResearchRouteDataCommand{ProjectID: project.ID, StarterRunID: plannerID, RouteID: adopted.RouteID, PortName: "input_paths", AttachmentID: ids["wine.csv"]}
	staged, err := app.WorkflowFacade.StageResearchRouteData(command)
	if err != nil || staged.AttachmentID != ids["wine.csv"] || staged.SHA256 != candidates[0].SHA256 {
		t.Fatal(staged, err)
	}
	for _, id := range []string{ids["unsent.csv"], ids["notes.md"], otherBatch.Attachments[0].ID, "forged"} {
		bad := command
		bad.AttachmentID = id
		if _, err := app.WorkflowFacade.StageResearchRouteData(bad); err == nil {
			t.Fatal("invalid file offered", id)
		}
	}
	bad := command
	bad.PortName = "research_goal"
	if _, err := app.WorkflowFacade.StageResearchRouteData(bad); err == nil {
		t.Fatal("wrong port accepted")
	}
	bad = command
	bad.ProjectID = "foreign"
	if _, err := app.WorkflowFacade.StageResearchRouteData(bad); err == nil {
		t.Fatal("wrong project accepted")
	}
	// The original source can disappear; reusing the immutable imported object
	// must not require another native file choice or another attachment row.
	if err := os.Remove(paths["wine.csv"]); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	if pending, err := app.WorkflowFacade.GetPendingResearchAdoption(project.ID, plannerID); err != nil || pending == nil || pending.Workflow.Version.ID != adopted.Workflow.Version.ID {
		t.Fatal("old pending task lost", pending, err)
	}
	again, err := app.WorkflowFacade.StageResearchRouteData(command)
	if err != nil || again != staged {
		t.Fatal("restart/repeat changed file", again, err)
	}
	var afterAttachments, afterRuns int
	_ = app.store.DB().QueryRow(`SELECT count(*) FROM attachments`).Scan(&afterAttachments)
	_ = app.store.DB().QueryRow(`SELECT count(*) FROM workflow_runs`).Scan(&afterRuns)
	if beforeAttachments != afterAttachments || beforeRuns != afterRuns {
		t.Fatal("draft staged extra attachments/runs")
	}
	contents, err := os.ReadFile(filepath.Join(project.WorkspacePath, ".sciaide", "tasks", entry.Run.ResearchTaskID, filepath.FromSlash(staged.RelativePath)))
	if err != nil || string(contents) != "x,y\n1,2\n3,4\n" {
		t.Fatal("wrong data bytes", string(contents), err)
	}
	if _, err := os.Stat(filepath.Join(project.WorkspacePath, filepath.FromSlash(staged.RelativePath))); !os.IsNotExist(err) {
		t.Fatal("leaked project-shared input")
	}
	// A saved snapshot must not let a changed import silently bypass the
	// existing byte-hash contract. Restore the test original afterwards.
	stored, err := sqlite.NewAttachmentRepository(app.store.DB()).Get(context.Background(), ids["wine.csv"])
	if err != nil {
		t.Fatal(err)
	}
	storedPath := filepath.Join(projectapp.PrivateDataPath(project), filepath.FromSlash(stored.StorageRelativePath))
	if err := os.WriteFile(storedPath, []byte("x,y\n9,8\n7,6\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.WorkflowFacade.StageResearchRouteData(command); err == nil || !strings.Contains(err.Error(), "原件已变化") {
		t.Fatal("modified import reused", err)
	}
	if err := os.WriteFile(storedPath, []byte("x,y\n1,2\n3,4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Later uploads in this same task are selectable, never silently merged.
	secondBatch, err := app.AttachmentFacade.ImportConversationDocumentPaths(project.ID, planned.Run.ConversationID, []string{paths["unsent.csv"]})
	if err != nil || len(secondBatch.Attachments) != 1 {
		t.Fatal(secondBatch, err)
	}
	chatTurn(planned.Run.ConversationID, "第二份表格留作比较，本轮不采用", []string{secondBatch.Attachments[0].ID})
	candidates, err = app.WorkflowFacade.GetResearchRouteDataCandidates(project.ID, plannerID, adopted.RouteID)
	if err != nil || len(candidates) != 2 {
		t.Fatal("later same-task upload not offered", candidates, err)
	}
	secondCommand := command
	secondCommand.AttachmentID = secondBatch.Attachments[0].ID
	secondFile, err := app.WorkflowFacade.StageResearchRouteData(secondCommand)
	if err != nil || secondFile.RelativePath == staged.RelativePath {
		t.Fatal("independent second file not staged", secondFile, err)
	}
	inputsBefore["input_paths"] = []string{staged.RelativePath}
	formal, err := app.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{ProjectID: project.ID, StarterRunID: plannerID, RouteID: adopted.RouteID, WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID, Inputs: mustBootstrapJSON(t, inputsBefore), ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil || formal.Run.ResearchTaskID != entry.Run.ResearchTaskID {
		t.Fatal("confirmed route start failed", formal, err)
	}
	if _, err := app.WorkflowFacade.StageResearchRouteData(command); err == nil {
		t.Fatal("executing route still accepts draft changes")
	}
	end := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunCompleted, 40*time.Second)
	var finalInputs map[string]any
	_ = json.Unmarshal(end.Run.Inputs, &finalInputs)
	if len(finalInputs["input_paths"].([]any)) != 1 || finalInputs["input_paths"].([]any)[0] != staged.RelativePath {
		t.Fatal("formal input changed", finalInputs)
	}
	checkComputedInputs := func(detail workflow.RunDetail, rows, files int) {
		t.Helper()
		for _, step := range detail.Steps {
			if step.NodeID != "python_analysis" {
				continue
			}
			var output struct {
				Artifacts []tool.ArtifactRef `json:"artifacts"`
			}
			if err := json.Unmarshal(step.Output, &output); err != nil {
				t.Fatal(err)
			}
			for _, artifact := range output.Artifacts {
				if !strings.HasSuffix(artifact.WorkspacePath, "-results.json") {
					continue
				}
				workspace, err := projectapp.ResearchTaskWorkspacePath(project, detail.Run.ResearchTaskID)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(artifact.WorkspacePath)))
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					RowCount  int `json:"rowCount"`
					FileCount int `json:"fileCount"`
				}
				if err := json.Unmarshal(data, &result); err != nil {
					t.Fatal(err)
				}
				if result.RowCount != rows || result.FileCount != files {
					t.Fatal("Python ignored confirmed inputs", result)
				}
				return
			}
		}
		t.Fatal("missing actual Python result artifact")
	}
	checkComputedInputs(end, 2, 1)
	t.Log("sent CSV reused across planner conversation and restart; no reimport/auto execution; confirmed normal Runtime/Python chain completed")
	commandRevision := workflow.ProposeResearchRevisionCommand{NodeID: "data_preflight", Summary: "Include the second table", Changes: []string{"Include both tables in analysis"}, Reason: "Replay from preflight with both tables", DataInputs: &workflow.RevisionDataSelection{RetainedPaths: []string{staged.RelativePath}, AttachmentIDs: []string{secondBatch.Attachments[0].ID}}}
	revisionMu.Lock()
	revisionCommand = mustBootstrapJSON(t, commandRevision)
	revisionMu.Unlock()
	chatID := chatTurn(formal.Run.ConversationID, "Please include the second table already sent in this task", nil)
	page, err := app.workflow.ReadResearchTask(context.Background(), chatID, workflow.ResearchTaskReadCommand{Section: "supplemental_materials"})
	if err != nil || !strings.Contains(string(page), secondBatch.Attachments[0].ID) {
		t.Fatal("earlier task table not offered", string(page), err)
	}

	for _, forbidden := range []string{ids["unsent.csv"], otherBatch.Attachments[0].ID} {
		badRevision := commandRevision
		badRevision.DataInputs = &workflow.RevisionDataSelection{RetainedPaths: []string{staged.RelativePath}, AttachmentIDs: []string{forbidden}}
		if _, err := app.workflow.ProposeResearchRevision(context.Background(), chatID, "forbidden-"+forbidden, badRevision); err == nil {
			t.Fatal("unsent or foreign-task attachment accepted")
		}
	}
	proposed, err := app.WorkflowFacade.GetRun(project.ID, formal.Run.ID)
	if err != nil || len(proposed.RevisionProposals) != 1 {
		t.Fatal("proposal not persisted", err, proposed.RevisionProposals)
	}
	proposal := proposed.RevisionProposals[0]
	if !proposal.CanConfirm || proposal.DataVersion == nil || len(proposal.DataVersion.Files) != 2 {
		t.Fatal("earlier attachment rejected", proposal)
	}
	if proposal.DataVersion.Files[1].SHA256 != secondFile.SHA256 || proposal.DataVersion.Files[1].RelativePath != secondFile.RelativePath {
		t.Fatal("attachment identity changed")
	}
	unchanged, err := app.workflow.Get(context.Background(), project.ID, formal.Run.ID)
	if err != nil || workflow.ResearchRevisionSnapshot(unchanged) != workflow.ResearchRevisionSnapshot(end) {
		t.Fatal("proposal executed before confirmation", err)
	}
	// Reopen before confirmation: ownership and staged hashes must remain durable.
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: project.ID, RunID: formal.Run.ID, ProposalID: proposal.ID, Confirmed: true}); err != nil {
		t.Fatal("confirmed earlier attachment rejected", err)
	}
	revised := waitForWorkflowState(t, app, project.ID, formal.Run.ID, workflow.RunCompleted, 40*time.Second)
	checkComputedInputs(revised, 3, 2)
	effective, err := workflow.ResearchEffectiveInputs(revised)
	if err != nil || !strings.Contains(string(effective), secondFile.RelativePath) || !strings.Contains(string(effective), staged.RelativePath) {
		t.Fatal("confirmed file set lost", string(effective), err)
	}
}

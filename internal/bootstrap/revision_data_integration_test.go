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
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/platform/pythonruntime"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestResearchDataUpdateConfirmsAfterRestartAndKeepsVersion(t *testing.T) {
	var mu sync.Mutex
	var selection workflow.RevisionDataSelection
	memory := &researchStageMemoryFixture{}
	stageServer := newResearchStarterOutputTestServer(t, func(out map[string]any, prompt string) {
		if _, ok := out["analysisInput"]; ok {
			code := `import csv
import json
from pathlib import Path
rows = []
for path in SCIAIDE_INPUTS:
    with Path(path).open("r", encoding="utf-8-sig", newline="") as handle:
        rows.extend(csv.DictReader(handle))
result = {"rowCount": len(rows), "total": sum(float(row["value"]) for row in rows)}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\nCount rows and sum value from exactly declared inputs.", encoding="utf-8")
result`
			// Exceed the comparison baseline budget so actual paging, rather
			// than its preview, is necessary to recover the complete source.
			code = strings.Repeat("# 保留原研究方法与参数；历史代码不是当前指令。\n", 400) + code
			out["_fixturePythonCode"] = memory.codeFor(prompt, code)
			if out["_fixturePythonCode"] == "" {
				t.Error("revised fixture did not use the code actually read from history")
			}
		}
	}, mutateStarterToEmpiricalReportRoute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
			return
		}
		last, text := -1, ""
		for i, m := range req.Messages {
			if m.Role == "user" {
				last = i
				_ = json.Unmarshal(m.Content, &text)
			}
		}
		if strings.Contains(text, "Create a durable research checkpoint") {
			// Exercise real context compaction without pretending this canned
			// summary contains full research evidence. Task records stay exact.
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# Research checkpoint\\nThe user is updating tables in the same research task. Confirmed file identities, computed results and complete revisions are in task records; consult data_inputs and stage_output. Preserve the original question. Do not treat this summary as numerical evidence.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		if !bytes.Contains(body, []byte(`"research_revision_propose"`)) {
			if memory.handle(t, w, body, workflowAITestPrompt(body)) {
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			stageServer.Config.Handler.ServeHTTP(w, r)
			return
		}
		tools := 0
		for _, m := range req.Messages[last+1:] {
			if m.Role == "tool" {
				tools++
			}
		}
		mu.Lock()
		dataSelection := selection
		mu.Unlock()
		wording := strings.Contains(text, "WORDING")
		steps := 3
		if wording {
			steps = 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if tools >= steps {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"方案尚未执行，请确认数据集合及重算范围。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		name := "research_task_read"
		var args any = workflow.ResearchTaskReadCommand{Section: "data_inputs"}
		if tools == 1 {
			args = workflow.ResearchTaskReadCommand{Section: "supplemental_content", AttachmentID: dataSelection.AttachmentIDs[0]}
		}
		if tools == 2 || wording {
			name = "research_revision_propose"
			command := workflow.ProposeResearchRevisionCommand{NodeID: "data_preflight", Summary: "Update the data and compare with the previous results", Changes: []string{"Use exactly the listed table set; concatenate compatible rows, count them and sum value", "Compare with previous results, preserving the research question"}, Reason: "Data changed; preflight, implementation and computation must use the new file set", DataInputs: &dataSelection}
			if wording {
				command.NodeID, command.Summary, command.DataInputs = "report_drafting", "Clarify report wording without recomputing", nil
				command.Changes = []string{"Clarify wording; preserve current computed data and results"}
			}
			args = command
		}
		encoded, _ := json.Marshal(args)
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("data-call-%d", len(req.Messages)), "function": map[string]any{"name": name, "arguments": string(encoded)}}}}, "finish_reason": "tool_calls"}}})
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
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Versioned data fixture"})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := app.PythonFacade.DetectInterpreters()
	if err != nil || len(discovery.Interpreters) == 0 {
		t.Skipf("no Python interpreter: %v", err)
	}
	env := filepath.Join(t.TempDir(), "python")
	if err := pythonruntime.New(app.pythonexec).CreateEnvironment(context.Background(), discovery.Interpreters[0].ExecutablePath, env); err != nil {
		t.Skipf("cannot create isolated Python: %v", err)
	}
	python := filepath.Join(env, "Scripts", "python.exe")
	if _, err := app.PythonFacade.BindProjectEnvironment(p.ID, python); err != nil {
		t.Fatal(err)
	}
	originalBytes := []byte("value\n1\n2\n")
	if err := os.WriteFile(filepath.Join(p.WorkspacePath, "initial.csv"), originalBytes, 0600); err != nil {
		t.Fatal(err)
	}
	starter, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: p.ID, ResearchIdea: "Analyze the supplied values and produce a report", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, app, p.ID, starter.Run.ID, workflow.RunCompleted, 20*time.Second)
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: p.ID, RunID: planned.Run.ID, RouteID: "empirical_report"})
	if err != nil {
		t.Fatal(err)
	}
	var inputs map[string]any
	if err := json.Unmarshal(adopted.InitialInputs, &inputs); err != nil {
		t.Fatal(err)
	}
	inputs["input_paths"] = []string{"initial.csv"}
	bound, _ := json.Marshal(inputs)
	started, err := app.WorkflowFacade.StartAdoptedResearchRoute(workflow.StartAdoptedResearchRouteCommand{ProjectID: p.ID, StarterRunID: adopted.StarterRunID, RouteID: adopted.RouteID, WorkflowID: adopted.Workflow.Workflow.ID, WorkflowVersionID: adopted.Workflow.Version.ID, Inputs: bound, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCompleted, 50*time.Second)
	workspace, err := project.ResearchTaskWorkspacePath(p, completed.Run.ResearchTaskID)
	if err != nil {
		t.Fatal(err)
	}
	step := func(d workflow.RunDetail, id string) workflow.Step {
		t.Helper()
		for _, s := range d.Steps {
			if s.NodeID == id {
				return s
			}
		}
		t.Fatal("missing step", id)
		return workflow.Step{}
	}
	checkResult := func(d workflow.RunDetail, rows, total float64) []tool.ArtifactRef {
		t.Helper()
		var out struct {
			Artifacts []tool.ArtifactRef `json:"artifacts"`
		}
		if err := json.Unmarshal(step(d, "python_analysis").Output, &out); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, a := range out.Artifacts {
			if !strings.HasSuffix(a.WorkspacePath, "-results.json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(workspace, a.WorkspacePath))
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]float64
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if result["rowCount"] != rows || result["total"] != total {
				t.Fatal("Python did not use confirmed data", result)
			}
			found = true
		}
		if !found {
			t.Fatal("no JSON artifact")
		}
		return out.Artifacts
	}
	oldArtifacts := checkResult(completed, 2, 3)
	originalInputs := string(completed.Run.Inputs)
	importTable := func(name, contents string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		batch, err := app.AttachmentFacade.ImportConversationDocumentPaths(p.ID, completed.Run.ConversationID, []string{file})
		if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 1 {
			t.Fatal(batch, err)
		}
		return batch.Attachments[0].ID
	}
	newID := importTable("updated.csv", "value\n3\n4\n5\n9\n")
	mu.Lock()
	selection = workflow.RevisionDataSelection{RetainedPaths: []string{}, AttachmentIDs: []string{newID}}
	mu.Unlock()
	chatRevision := func(text string, ids []string) (chat.Run, workflow.ResearchRevisionProposal) {
		t.Helper()
		run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: completed.Run.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, Text: text, AttachmentIDs: ids})
		if err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			snapshot, err := app.ChatFacade.GetRunSnapshot(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Run.Status == chat.RunCompleted {
				for _, call := range snapshot.ToolCalls {
					if call.Status != tool.CallCompleted {
						t.Fatal(call.ErrorMessage)
					}
				}
				break
			}
			if time.Now().After(end) || snapshot.Run.Status == chat.RunFailed {
				t.Fatal("discussion failed", snapshot.Run)
			}
		}
		d, err := app.WorkflowFacade.GetRun(p.ID, completed.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		proposal := d.RevisionProposals[len(d.RevisionProposals)-1]
		if !proposal.CanConfirm {
			t.Fatal("proposal not confirmable", proposal)
		}
		return run, proposal
	}
	chatRun, proposal := chatRevision("DATA UPDATE: replace the initial table with this updated table", []string{newID})
	d, err := app.WorkflowFacade.GetRun(p.ID, completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.ResearchRevisionSnapshot(d) != workflow.ResearchRevisionSnapshot(completed) || proposal.DataVersion == nil || len(proposal.DataVersion.Files) != 1 {
		t.Fatal("proposal altered delivery or lost frozen data")
	}
	checkResult(d, 2, 3)
	current, err := workflow.ResearchEffectiveInputs(d)
	if err != nil || string(current) != originalInputs {
		t.Fatal("unconfirmed data adopted", err)
	}
	unsentID := importTable("unsent.csv", "value\n99\n")
	for _, command := range []workflow.ProposeResearchRevisionCommand{
		{NodeID: "report_drafting", DataInputs: &workflow.RevisionDataSelection{RetainedPaths: []string{}, AttachmentIDs: []string{newID}}},
		{NodeID: "data_preflight", DataInputs: &workflow.RevisionDataSelection{RetainedPaths: []string{}, AttachmentIDs: []string{unsentID}}},
		{NodeID: "evidence_import", AttachmentIDs: []string{newID}},
	} {
		command.Summary, command.Changes, command.Reason = "Bad update", []string{"Update"}, "Fixture"
		if _, err := app.workflow.ProposeResearchRevision(context.Background(), chatRun.ID, "unused", command); err == nil {
			t.Fatal("invalid adoption allowed", command.NodeID)
		}
	}
	newPath := proposal.DataVersion.Files[0].RelativePath
	frozenPath := filepath.Join(workspace, newPath)
	frozenBytes, err := os.ReadFile(frozenPath)
	if err != nil {
		t.Fatal(err)
	}
	confirm := workflow.ConfirmResearchRevisionCommand{ProjectID: p.ID, RunID: completed.Run.ID, ProposalID: proposal.ID, Confirmed: true}
	if err := os.WriteFile(frozenPath, []byte("value\n999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(confirm); err == nil {
		t.Fatal("tampered data confirmed")
	}
	if err := os.WriteFile(frozenPath, frozenBytes, 0600); err != nil {
		t.Fatal(err)
	}
	d, err = app.WorkflowFacade.GetRun(p.ID, completed.Run.ID)
	if err != nil || workflow.ResearchRevisionSnapshot(d) != workflow.ResearchRevisionSnapshot(completed) {
		t.Fatal("failed confirmation discarded old delivery", err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(confirm); err != nil {
		t.Fatal(err)
	}
	updated := waitForWorkflowState(t, app, p.ID, completed.Run.ID, workflow.RunCompleted, 50*time.Second)
	checkResult(updated, 4, 21)
	if string(updated.Run.Inputs) != originalInputs || updated.Run.InputsSHA256 != completed.Run.InputsSHA256 {
		t.Fatal("original input identity mutated")
	}
	for _, id := range []string{"data_preflight", "python_analysis"} {
		if !strings.Contains(string(step(updated, id).Input), newPath) || step(updated, id).Attempt != 2 {
			t.Fatal("new version not bound", id)
		}
	}
	for _, id := range []string{"question_refinement", "method_selection", "data_environment"} {
		if step(updated, id).Attempt != step(completed, id).Attempt || string(step(updated, id).Output) != string(step(completed, id).Output) {
			t.Fatal("upstream context rerun", id)
		}
	}
	for _, id := range []string{"method_implementation", "result_interpretation", "report_drafting", "independent_review"} {
		if !strings.Contains(string(step(updated, id).Input), "researchDataVersion") {
			t.Fatal("stage lost data provenance", id)
		}
	}
	for _, a := range oldArtifacts {
		if _, err := os.Stat(filepath.Join(workspace, a.WorkspacePath)); err != nil {
			t.Fatal("old output lost", err)
		}
	}
	var orig struct {
		Paths []string `json:"input_paths"`
	}
	if err := json.Unmarshal(completed.Run.Inputs, &orig); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(workspace, orig.Paths[0]))
	if err != nil || !bytes.Equal(original, originalBytes) {
		t.Fatal("old input changed", err)
	}
	_, wording := chatRevision("WORDING: clarify report wording only, keep current results", nil)
	if wording.DataVersion != nil {
		t.Fatal("wording proposal claimed data update")
	}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: p.ID, RunID: completed.Run.ID, ProposalID: wording.ID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	worded := waitForWorkflowState(t, app, p.ID, completed.Run.ID, workflow.RunCompleted, 30*time.Second)
	checkResult(worded, 4, 21)
	if step(worded, "python_analysis").Attempt != 2 || string(step(worded, "python_analysis").Output) != string(step(updated, "python_analysis").Output) {
		t.Fatal("wording revision recomputed")
	}
	current, err = workflow.ResearchEffectiveInputs(worded)
	if err != nil || !strings.Contains(string(current), newPath) || strings.Contains(string(current), orig.Paths[0]) {
		t.Fatal("wording reverted data", err)
	}
	extraID := importTable("extra.csv", "value\n10\n20\n")
	mu.Lock()
	selection = workflow.RevisionDataSelection{RetainedPaths: []string{newPath}, AttachmentIDs: []string{extraID}}
	mu.Unlock()
	_, appended := chatRevision("DATA UPDATE: retain updated data and add the compatible extra rows", []string{extraID})
	if len(appended.DataVersion.Files) != 2 || appended.DataVersion.Files[0].AttachmentID != "" {
		t.Fatal("retained data was not explicit")
	}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: p.ID, RunID: completed.Run.ID, ProposalID: appended.ID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, p.ID, completed.Run.ID, workflow.RunCompleted, 50*time.Second)
	checkResult(final, 6, 51)
	memory.mu.Lock()
	for i, revision := range []workflow.ResearchRevisionProposal{proposal, appended} {
		if memory.pages[revision.ID] < 2 || memory.codes[revision.ID] == "" || memory.files[revision.ID] == "" {
			t.Error("stage did not read complete old code and historical artifact", revision.ID)
		}
		var historicalResult map[string]float64
		if err := json.Unmarshal([]byte(memory.files[revision.ID]), &historicalResult); err != nil || historicalResult["total"] != []float64{3, 21}[i] {
			t.Error("historical artifact was confused with current results", revision.ID, historicalResult, err)
		}
	}
	if memory.indexes != 2 {
		t.Error("unexpected number of completed historical-code reuses", memory.indexes)
	}
	memory.mu.Unlock()
	if string(step(final, "method_implementation").Output) != string(step(completed, "method_implementation").Output) {
		t.Fatal("historical code reuse unexpectedly changed the frozen method")
	}
	for _, execution := range final.AIExecutions {
		if execution.ChatRunID != "" {
			if _, err := app.workflow.ReadResearchTask(context.Background(), execution.ChatRunID, workflow.ResearchTaskReadCommand{Section: "overview"}); err == nil {
				t.Fatal("finished AI attempt still has live task-read binding")
			}
		}
	}
	repo := sqlite.NewWorkflowRuntimeRepository(app.store.DB())
	saved, err := repo.ReadResearchRevisionSnapshot(context.Background(), completed.Run.ID, appended.ID)
	if err != nil {
		t.Fatal(err)
	}
	var history struct {
		EffectiveInputs json.RawMessage `json:"effectiveInputs"`
	}
	if err := json.Unmarshal(saved, &history); err != nil || !strings.Contains(string(history.EffectiveInputs), newPath) {
		t.Fatal("history lost active data before next update", err)
	}
	page, err := app.workflow.ReadResearchTask(context.Background(), chatRun.ID, workflow.ResearchTaskReadCommand{Section: "data_inputs"})
	if err != nil || !strings.Contains(string(page), appended.ID) || !strings.Contains(string(page), proposal.ID) {
		t.Fatal("data version history missing", string(page), err)
	}
	archive, err := sqlite.NewProjectArchiveRepository(app.store.DB()).CreateSnapshot(context.Background(), p.ID, filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	archivedInputs := map[string]bool{}
	for _, file := range archive.Files {
		if file.Kind == projectarchive.FileWorkflowInput && file.TaskID == completed.Run.ResearchTaskID {
			archivedInputs[file.StorageRelativePath] = true
		}
	}
	for _, path := range []string{orig.Paths[0], newPath, appended.DataVersion.Files[1].RelativePath} {
		if !archivedInputs[path] {
			t.Fatal("project snapshot omitted data version", path)
		}
	}
	archives := projectArchiveServiceForTest(t, app, root)
	exported, err := archives.Export(context.Background(), p.ID, filepath.Join(root, "data-versions.sciaide-project"))
	if err != nil {
		t.Fatal(err)
	}
	imported, err := archives.Restore(context.Background(), projectarchive.RestoreCommand{Path: exported.Path})
	if err != nil {
		t.Fatal(err)
	}
	var restoredRunID string
	if err := app.store.DB().QueryRow(`SELECT wr.id FROM workflow_runs wr JOIN research_revision_proposals p ON p.workflow_run_id=wr.id WHERE wr.project_id=? LIMIT 1`, imported.Project.ID).Scan(&restoredRunID); err != nil {
		t.Fatal(err)
	}
	archiveRun, err := app.WorkflowFacade.GetRun(imported.Project.ID, restoredRunID)
	if err != nil {
		t.Fatal(err)
	}
	archiveInputs, err := workflow.ResearchEffectiveInputs(archiveRun)
	if err != nil || !strings.Contains(string(archiveInputs), newPath) || !strings.Contains(string(archiveInputs), appended.DataVersion.Files[1].RelativePath) {
		t.Fatal("archive restored initial data instead of current data", err)
	}
	// The existing archive design preserves historical review evidence with
	// original hashes while remapping live identities. Do not equate readable
	// restored data/results with fresh approval of the restored project.
	archiveWorkspace, err := project.ResearchTaskWorkspacePath(imported.Project, archiveRun.Run.ResearchTaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{orig.Paths[0], newPath, appended.DataVersion.Files[1].RelativePath} {
		old, err := os.ReadFile(filepath.Join(workspace, path))
		if err != nil {
			t.Fatal(err)
		}
		restoredBytes, err := os.ReadFile(filepath.Join(archiveWorkspace, path))
		if err != nil || !bytes.Equal(old, restoredBytes) {
			t.Fatal("archive did not restore version bytes", path, err)
		}
	}
	// Version identities must remain resolvable in the restored task, not
	// point back to the original project, even though the files share hashes.
	for _, event := range archiveRun.Events {
		if event.Type != "workflow.user_revision_queued" {
			continue
		}
		var saved struct {
			Proposal workflow.ResearchRevisionProposal `json:"proposal"`
		}
		if err := json.Unmarshal(event.Payload, &saved); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range archiveRun.RevisionProposals {
			found = found || p.ID == saved.Proposal.ID
		}
		if !found {
			t.Fatal("archive data history retained foreign proposal identity", saved.Proposal.ID)
		}
	}
	var restoredAttachmentID, owner, scope string
	if err := app.store.DB().QueryRow(`SELECT id,research_task_id,scope_kind FROM attachments WHERE project_id=? AND original_name='updated.csv'`, imported.Project.ID).Scan(&restoredAttachmentID, &owner, &scope); err != nil {
		t.Fatal(err)
	}
	if restoredAttachmentID == newID || owner != "conversation:"+archiveRun.Run.ConversationID || scope != "conversation" {
		t.Fatal("restored upload owner lost or broadened", owner, scope)
	}
	for _, a := range archiveRun.RevisionProposals {
		if a.DataVersion == nil {
			continue
		}
		for _, file := range a.DataVersion.Files {
			if file.AttachmentID == newID {
				t.Fatal("restored data points to original attachment")
			}
		}
	}
	var executions, uniqueExecutions int
	if err := app.store.DB().QueryRow(`SELECT COUNT(*),COUNT(DISTINCT execution_id) FROM python_kernel_executions WHERE project_id IN (?,?)`, p.ID, imported.Project.ID).Scan(&executions, &uniqueExecutions); err != nil || executions != uniqueExecutions || executions == 0 {
		t.Fatal("restored Python execution IDs collide", err)
	}
	var pythonOut struct {
		Structured struct {
			ExecutionID string `json:"executionId"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(step(archiveRun, "python_analysis").Output, &pythonOut); err != nil {
		t.Fatal(err)
	}
	var auditExecutionID string
	if err := app.store.DB().QueryRow(`SELECT execution_id FROM python_kernel_executions WHERE tool_call_id=?`, step(archiveRun, "python_analysis").ToolCallID).Scan(&auditExecutionID); err != nil || auditExecutionID != pythonOut.Structured.ExecutionID {
		t.Fatal("restored output and Python audit disagree", auditExecutionID, pythonOut.Structured.ExecutionID, err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	restored, err := app.WorkflowFacade.GetRun(p.ID, completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err = workflow.ResearchEffectiveInputs(restored)
	if err != nil || !strings.Contains(string(current), newPath) || !strings.Contains(string(current), appended.DataVersion.Files[1].RelativePath) {
		t.Fatal("restart reverted data", err)
	}
	checkResult(restored, 6, 51)
}

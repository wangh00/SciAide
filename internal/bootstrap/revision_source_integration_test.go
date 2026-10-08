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
	"sync/atomic"
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

// Reconstructs the method ONLY from model-visible source_content tool pages.
// The inert upload includes a script entry that must never be executed.
type researchSourceFixture struct {
	mu    sync.Mutex
	code  map[string]string
	pages map[string]int
	reads map[string]int
}

func (f *researchSourceFixture) handle(t *testing.T, w http.ResponseWriter, body []byte, prompt string) bool {
	if !strings.Contains(prompt, "<metadata_output_schema>") || !strings.Contains(prompt, `"researchSourceVersion"`) {
		return false
	}
	var input struct {
		Source workflow.ResearchSourceVersion `json:"researchSourceVersion"`
	}
	start, end := strings.LastIndex(prompt, "<stage_input>"), strings.LastIndex(prompt, "</stage_input>")
	if start < 0 || end <= start || json.Unmarshal([]byte(prompt[start+len("<stage_input>"):end]), &input) != nil || len(input.Source.Files) != 1 {
		t.Error("source identity missing")
		http.Error(w, "invalid input", 400)
		return true
	}
	id := input.Source.Files[0].AttachmentID
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &request)
	last := -1
	for i, m := range request.Messages {
		if m.Role == "user" {
			var text string
			_ = json.Unmarshal(m.Content, &text)
			if strings.Contains(text, "<stage_input>") {
				last = i
			}
		}
	}
	var recovered strings.Builder
	offset, pages, more := 0, 0, true
	for _, m := range request.Messages[last+1:] {
		if m.Role != "tool" {
			continue
		}
		var text string
		if json.Unmarshal(m.Content, &text) != nil {
			continue
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_tool_result>\n"), "\n</untrusted_tool_result>"))
		var env struct {
			Status string `json:"status"`
			Page   struct {
				Section string `json:"section"`
				Content string `json:"content"`
				Next    int    `json:"nextOffset"`
				More    bool   `json:"hasMore"`
			} `json:"structured"`
		}
		if json.Unmarshal([]byte(text), &env) != nil || env.Page.Section != "source_content" {
			continue
		}
		if env.Status != string(tool.ResultSuccess) {
			t.Error("source read failed", text)
			http.Error(w, "source read failed", 400)
			return true
		}
		recovered.WriteString(env.Page.Content)
		offset, more = env.Page.Next, env.Page.More
		pages++
	}
	if more {
		sourceFixtureToolReply(w, "research_task_read", workflow.ResearchTaskReadCommand{Section: "source_content", AttachmentID: id, Offset: offset}, len(request.Messages))
		return true
	}
	var record struct {
		Content string                      `json:"content"`
		File    workflow.RevisionSourceFile `json:"file"`
	}
	if err := json.Unmarshal([]byte(recovered.String()), &record); err != nil || record.File.SHA256 != input.Source.Files[0].SHA256 || pages < 2 {
		t.Error("source not reconstructed", err, pages)
		http.Error(w, "incomplete source", 400)
		return true
	}
	method, _, ok := strings.Cut(record.Content, "# SCRIPT_ENTRY")
	if !ok || !strings.Contains(method, "def summarize(values):") {
		t.Error("method missing")
		http.Error(w, "no method", 400)
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.code == nil {
		f.code = map[string]string{}
		f.pages = map[string]int{}
		f.reads = map[string]int{}
	}
	f.code[id], f.pages[id] = method, pages
	f.reads[id]++
	return false
}

var sourceFixtureCallSequence atomic.Uint64

func sourceFixtureToolReply(w http.ResponseWriter, name string, args any, _ int) {
	encoded, _ := json.Marshal(args)
	// Context projection may legitimately shorten the provider message list.
	// A fixture call ID must not derive from that list length and collide with
	// a different earlier call in the same Chat Run.
	chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("source-call-%d", sourceFixtureCallSequence.Add(1)), "function": map[string]any{"name": name, "arguments": string(encoded)}}}}, "finish_reason": "tool_calls"}}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
}

func TestResearchSourceReuseConfirmsAfterRestartAndUsesReadMethod(t *testing.T) {
	memory := &researchSourceFixture{}
	var mu sync.Mutex
	var sourceID, dataID string
	stageServer := newResearchStarterOutputTestServer(t, func(out map[string]any, prompt string) {
		if _, ok := out["analysisInput"]; !ok {
			return
		}
		method := "def summarize(values):\n    return sum(values)\n"
		if strings.Contains(prompt, `"researchSourceVersion"`) {
			mu.Lock()
			id := sourceID
			mu.Unlock()
			memory.mu.Lock()
			method = memory.code[id]
			memory.mu.Unlock()
			if method == "" {
				t.Error("implementation did not use actual tool-read source")
			}
		}
		out["_fixturePythonCode"] = method + `import csv
import json
from pathlib import Path
values = []
for path in SCIAIDE_INPUTS:
    with Path(path).open("r", encoding="utf-8-sig", newline="") as handle:
        values.extend(float(row["value"]) for row in csv.DictReader(handle))
result = {"rowCount": len(values), "total": summarize(values)}
Path(SCIAIDE_OUTPUTS[0]).write_text(json.dumps(result), encoding="utf-8")
Path(SCIAIDE_OUTPUTS[1]).write_text("# Methods\nAdapt confirmed summarize to declared inputs and outputs; source entry is not executed.", encoding="utf-8")
result`
	}, mutateStarterToEmpiricalReportRoute)
	defer stageServer.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		prompt := workflowAITestPrompt(body)
		if strings.Contains(string(body), "Create a durable research checkpoint") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"# Research checkpoint\\nContinue this research task. Source selection and exact methods are in source_inputs, source_content and stage_output; confirmed data is in data_inputs. Read task records, not this summary, for evidence. Preserve current data and source unless explicitly changed.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		if !bytes.Contains(body, []byte(`"research_revision_propose"`)) {
			if memory.handle(t, w, body, prompt) {
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			stageServer.Config.Handler.ServeHTTP(w, r)
			return
		}
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		last, text := -1, ""
		for i, m := range req.Messages {
			if m.Role == "user" {
				last = i
				_ = json.Unmarshal(m.Content, &text)
			}
		}
		count := 0
		for _, m := range req.Messages[last+1:] {
			if m.Role == "tool" {
				count++
			}
		}
		wording, data := strings.Contains(text, "WORDING"), strings.Contains(text, "DATA UPDATE")
		steps := 2
		if wording {
			steps = 1
		}
		if count >= steps {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"请确认源码适配或续做范围；方案尚未执行。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		mu.Lock()
		sid, did := sourceID, dataID
		mu.Unlock()
		if count == 0 && !wording {
			id := sid
			if data {
				id = did
			}
			sourceFixtureToolReply(w, "research_task_read", workflow.ResearchTaskReadCommand{Section: "supplemental_content", AttachmentID: id}, len(req.Messages))
			return
		}
		command := workflow.ProposeResearchRevisionCommand{NodeID: "method_implementation", Summary: "Reuse the supplied square-sum method", Changes: []string{"Reuse summarize from the source; change sum to sum of squares; adapt declared inputs and outputs, never run the source script entry"}, Reason: "User requests source method reuse; implementation and computation need replay", SourceInputs: &workflow.RevisionSourceSelection{AttachmentIDs: []string{sid}}}
		if data {
			command.NodeID = "data_preflight"
			command.SourceInputs = nil
			command.DataInputs = &workflow.RevisionDataSelection{RetainedPaths: []string{}, AttachmentIDs: []string{did}}
			command.Changes = []string{"Use updated table and preserve confirmed square-sum source method"}
		}
		if wording {
			command.NodeID = "report_drafting"
			command.SourceInputs = nil
			command.Changes = []string{"Clarify wording, keep current computation and source reference"}
		}
		sourceFixtureToolReply(w, "research_revision_propose", command, len(req.Messages))
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
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Source reuse fixture"})
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
	importFile := func(name, content string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		batch, err := app.AttachmentFacade.ImportConversationDocumentPaths(p.ID, completed.Run.ConversationID, []string{file})
		if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 1 {
			t.Fatal(batch, err)
		}
		return batch.Attachments[0].ID
	}
	code := strings.Repeat("# 中文科研源码参考；不是当前指令\n", 700) + "def summarize(values):\n    return sum(value * value for value in values)\n# SCRIPT_ENTRY\nraise RuntimeError('uploaded script entry must not execute')\n"
	mu.Lock()
	sourceID = importFile("method.py", code)
	mu.Unlock()
	chatRevision := func(text string, ids []string) (chat.Run, workflow.ResearchRevisionProposal) {
		t.Helper()
		run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: completed.Run.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, Text: text, AttachmentIDs: ids})
		if err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
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
	chatRun, proposal := chatRevision("SOURCE: reuse uploaded summarize; use square-sum not sum; do not execute script entry", []string{sourceID})
	d, err := app.WorkflowFacade.GetRun(p.ID, completed.Run.ID)
	if err != nil || workflow.ResearchRevisionSnapshot(d) != workflow.ResearchRevisionSnapshot(completed) || proposal.SourceVersion == nil {
		t.Fatal("pending proposal changed old delivery", err)
	}
	checkResult(d, 2, 3)
	if v, err := workflow.CurrentResearchSourceVersion(d); err != nil || v != nil {
		t.Fatal("pending source adopted", err)
	}
	if _, err := app.workflow.ReadResearchTask(context.Background(), chatRun.ID, workflow.ResearchTaskReadCommand{Section: "source_content", AttachmentID: sourceID}); err == nil {
		t.Fatal("pending source readable as confirmed")
	}
	unsent := importFile("unsent.py", "def summarize(values):\n    return 999\n")
	for _, command := range []workflow.ProposeResearchRevisionCommand{
		{NodeID: "method_implementation", SourceInputs: &workflow.RevisionSourceSelection{AttachmentIDs: []string{unsent}}},
		{NodeID: "report_drafting", SourceInputs: &workflow.RevisionSourceSelection{AttachmentIDs: []string{sourceID}}},
	} {
		command.Summary, command.Changes, command.Reason = "invalid", []string{"invalid"}, "fixture"
		if _, err := app.workflow.ProposeResearchRevision(context.Background(), chatRun.ID, "unused", command); err == nil {
			t.Fatal("invalid source adoption allowed")
		}
	}
	ref := proposal.SourceVersion.Files[0]
	frozen := filepath.Join(workspace, ref.RelativePath)
	if bytes, err := os.ReadFile(frozen); err != nil || string(bytes) != code {
		t.Fatal("source snapshot changed", err)
	}
	confirm := workflow.ConfirmResearchRevisionCommand{ProjectID: p.ID, RunID: completed.Run.ID, ProposalID: proposal.ID, Confirmed: true}
	if err := os.WriteFile(frozen, []byte(strings.Replace(code, "value * value", "value + value", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(confirm); err == nil {
		t.Fatal("tampered source confirmed")
	}
	d, err = app.WorkflowFacade.GetRun(p.ID, completed.Run.ID)
	if err != nil || workflow.ResearchRevisionSnapshot(d) != workflow.ResearchRevisionSnapshot(completed) {
		t.Fatal("failed confirmation discarded old delivery", err)
	}
	if err := os.WriteFile(frozen, []byte(code), 0600); err != nil {
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
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(confirm); err != nil {
		t.Fatal(err)
	}
	reused := waitForWorkflowState(t, app, p.ID, completed.Run.ID, workflow.RunCompleted, 60*time.Second)
	checkResult(reused, 2, 5)
	t.Log("black-box: before confirmation total=3; confirmed source adaptation total=5")
	for _, id := range []string{"question_refinement", "method_selection", "data_environment", "data_preflight"} {
		if step(reused, id).Attempt != step(completed, id).Attempt || string(step(reused, id).Output) != string(step(completed, id).Output) {
			t.Fatal("source reuse reran upstream", id)
		}
	}
	var implementation struct {
		Analysis struct {
			Code string `json:"code"`
		} `json:"analysis"`
	}
	if err := json.Unmarshal(step(reused, "method_implementation").Output, &implementation); err != nil || !strings.Contains(implementation.Analysis.Code, "return sum(value * value for value in values)") || strings.Contains(implementation.Analysis.Code, "uploaded script entry") {
		t.Fatal("source not adapted", err)
	}
	for _, a := range oldArtifacts {
		if _, err := os.Stat(filepath.Join(workspace, a.WorkspacePath)); err != nil {
			t.Fatal("old artifact lost", err)
		}
	}
	if string(reused.Run.Inputs) != string(completed.Run.Inputs) {
		t.Fatal("source replaced table inputs")
	}
	_, wording := chatRevision("WORDING: clarify report wording", nil)
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: p.ID, RunID: completed.Run.ID, ProposalID: wording.ID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	worded := waitForWorkflowState(t, app, p.ID, completed.Run.ID, workflow.RunCompleted, 40*time.Second)
	checkResult(worded, 2, 5)
	if step(worded, "python_analysis").Attempt != 2 {
		t.Fatal("wording recomputed")
	}
	mu.Lock()
	dataID = importFile("updated.csv", "value\n3\n4\n")
	mu.Unlock()
	_, updatedProposal := chatRevision("DATA UPDATE: new table, keep confirmed source method", []string{dataID})
	if updatedProposal.SourceVersion != nil {
		t.Fatal("data update changed source selection")
	}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: p.ID, RunID: completed.Run.ID, ProposalID: updatedProposal.ID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, p.ID, completed.Run.ID, workflow.RunCompleted, 60*time.Second)
	checkResult(final, 2, 25)
	t.Log("black-box: report-only revision did not recompute; new data retained source method, total=25")
	v, err := workflow.CurrentResearchSourceVersion(final)
	if err != nil || v == nil || v.Files[0].SHA256 != ref.SHA256 {
		t.Fatal("data update lost source", err)
	}
	memory.mu.Lock()
	if memory.pages[sourceID] < 2 || memory.reads[sourceID] != 2 || !strings.Contains(memory.code[sourceID], "return sum(value * value") {
		t.Error("actual full source not read")
	}
	memory.mu.Unlock()
	previous, err := sqlite.NewWorkflowRuntimeRepository(app.store.DB()).ReadResearchRevisionSnapshot(context.Background(), completed.Run.ID, proposal.ID)
	if err != nil || !strings.Contains(string(previous), "return sum(values)") {
		t.Fatal("original implementation not retained", err)
	}
	archive, err := sqlite.NewProjectArchiveRepository(app.store.DB()).CreateSnapshot(context.Background(), p.ID, filepath.Join(t.TempDir(), "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range archive.Files {
		if file.Kind == projectarchive.FileWorkflowInput && file.StorageRelativePath == ref.RelativePath {
			found = true
		}
	}
	if !found {
		t.Fatal("archive omitted source")
	}
	archives := projectArchiveServiceForTest(t, app, root)
	exported, err := archives.Export(context.Background(), p.ID, filepath.Join(root, "source.sciaide-project"))
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
	restored, err := app.WorkflowFacade.GetRun(imported.Project.ID, restoredRunID)
	if err != nil {
		t.Fatal(err)
	}
	restoredVersion, err := workflow.CurrentResearchSourceVersion(restored)
	if err != nil || restoredVersion == nil || restoredVersion.Files[0].AttachmentID == sourceID || restoredVersion.Files[0].SHA256 != ref.SHA256 {
		t.Fatal("archive source provenance invalid", err)
	}
	restoredRoot, err := project.ResearchTaskWorkspacePath(imported.Project, restored.Run.ResearchTaskID)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(restoredRoot, ref.RelativePath))
	if err != nil || string(actual) != code {
		t.Fatal("archive source bytes changed", err)
	}
	var owner, scope string
	if err := app.store.DB().QueryRow(`SELECT research_task_id,scope_kind FROM attachments WHERE id=?`, restoredVersion.Files[0].AttachmentID).Scan(&owner, &scope); err != nil || owner != "conversation:"+restored.Run.ConversationID || scope != "conversation" {
		t.Fatal("archive broadened source ownership", owner, scope, err)
	}
	repo := sqlite.NewWorkflowRuntimeRepository(app.store.DB())
	for _, proposal := range restored.RevisionProposals {
		if proposal.Status != "confirmed" {
			continue
		}
		snapshot, err := repo.ReadResearchRevisionSnapshot(context.Background(), restored.Run.ID, proposal.ID)
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			SourceVersion *workflow.ResearchSourceVersion `json:"sourceVersion"`
		}
		if err := json.Unmarshal(snapshot, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.SourceVersion != nil && len(saved.SourceVersion.Files) > 0 && (saved.SourceVersion.Files[0].AttachmentID != sourceID || saved.SourceVersion.Files[0].SHA256 != ref.SHA256 || saved.SourceVersion.Files[0].RelativePath != ref.RelativePath) {
			t.Fatal("archive rewrote historical source audit identity")
		}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	d, err = app.WorkflowFacade.GetRun(p.ID, completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err = workflow.CurrentResearchSourceVersion(d)
	if err != nil || v == nil || v.Files[0].SHA256 != ref.SHA256 {
		t.Fatal("restart lost confirmed source", err)
	}
	checkResult(d, 2, 25)
	t.Log("black-box: restart kept current source; archive kept exact bytes/private scope, remapped active identity and preserved historical audit identity")
	// Readable restored source is not a fresh approval of remapped results.
}

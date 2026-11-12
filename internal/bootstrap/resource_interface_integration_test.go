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

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestResearchResourceInterfaceReadsOnlyTaskDocumentWithoutGuessedLocators(t *testing.T) {
	base := newResearchStarterSkillTestServer(t)
	var documentReads atomic.Int32
	var documentSearches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			t.Error(err)
			http.Error(w, "bad body", 400)
			return
		}
		var req struct {
			Tools []struct {
				Function struct {
					Name, Description string
					Parameters        json.RawMessage
				}
			}
		}
		if err = json.Unmarshal(body, &req); err != nil {
			t.Error(err)
			http.Error(w, "bad JSON", 400)
			return
		}
		openName := ""
		searchName := ""
		allowed := map[string]bool{}
		searchAllowed := map[string]bool{}
		for _, d := range req.Tools {
			var schema struct{ Properties map[string]json.RawMessage }
			if err = json.Unmarshal(d.Function.Parameters, &schema); err != nil {
				t.Error(err)
				continue
			}
			for _, key := range []string{"path", "attachmentId", "name", "section"} {
				// task.read selects a finite host-bound task record, not a free
				// document locator. Its source_content selector can only read an
				// adopted Python reference; file reads still use issued actions.
				if d.Function.Name == "research_task_read" && (key == "section" || key == "attachmentId") {
					continue
				}
				if _, present := schema.Properties[key]; present {
					t.Errorf("free resource locator advertised: %s %s", d.Function.Name, key)
				}
			}
			if d.Function.Name == "research_task_read" {
				var section struct{ Enum []string }
				if err = json.Unmarshal(schema.Properties["section"], &section); err != nil || len(section.Enum) == 0 {
					t.Errorf("task records must use a finite selector: %v", err)
				}
				for _, name := range section.Enum {
					if strings.HasPrefix(name, "supplemental_") || name == "messages" || name == "tool_calls" || name == "attempts" || name == "revisions" {
						t.Errorf("planner exposes private/live discussion selector: %s", name)
					}
				}
			}
			if strings.Contains(d.Function.Description, "Operate on a host-issued resource action") {
				openName = d.Function.Name
				var id struct {
					Pattern string
					Enum    []string
				}
				if err = json.Unmarshal(schema.Properties["actionId"], &id); err != nil {
					t.Error(err)
				}
				if len(id.Enum) != 0 || id.Pattern != "^res_[a-f0-9]{32}$" {
					t.Error("resource action schema must be stable; issued IDs are checked by the host")
				}
			}
			if strings.Contains(d.Function.Description, "Search a host-issued document resource") {
				searchName = d.Function.Name
				var id struct {
					Pattern string
					Enum    []string
				}
				if err = json.Unmarshal(schema.Properties["actionId"], &id); err != nil {
					t.Error(err)
				}
				if len(id.Enum) != 0 || id.Pattern != "^res_[a-f0-9]{32}$" {
					t.Error("search schema must be stable")
				}
			}
		}
		prompt := workflowAITestPrompt(body)
		if strings.Contains(prompt, "private-other.csv") || strings.Contains(prompt, "other-task-private-marker") {
			t.Error("other task resources leaked into model context")
		}
		const tag = "<resource_actions>\n"
		at := strings.Index(prompt, tag)
		if at < 0 {
			t.Error("resource state missing")
			http.Error(w, "no resources", 400)
			return
		}
		at += len(tag)
		end := strings.Index(prompt[at:], "\n</resource_actions>")
		if end < 0 {
			t.Error("resource state malformed")
			http.Error(w, "bad resources", 400)
			return
		}
		var menu struct {
			Actions []resource.Option
			Facts   struct {
				InputState string `json:"inputState"`
			}
		}
		if err = json.Unmarshal([]byte(prompt[at:at+end]), &menu); err != nil {
			t.Error(err)
			return
		}
		for _, action := range menu.Actions {
			if action.Tool == openName {
				allowed[action.ID] = true
			}
			if action.Tool == searchName {
				searchAllowed[action.ID] = true
			}
		}
		if menu.Facts.InputState == "no_supplied_resources" {
			for _, a := range menu.Actions {
				if a.Kind == "directory" || a.Kind == "workspace_read" || a.Kind == "document_read" {
					t.Error("empty task exposes file operations")
				}
			}
		}
		for _, a := range menu.Actions {
			if a.Kind != "document_read" || !strings.Contains(a.Label, "seedlings.csv") || strings.Contains(prompt, "visible-resource-marker") {
				continue
			}
			if !allowed[a.ID] || openName == "" {
				t.Error("document operation absent from advertised enum")
				return
			}
			documentReads.Add(1)
			args, _ := json.Marshal(map[string]string{"actionId": a.ID})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "read-current-data", "function": map[string]any{"name": openName, "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		if strings.Contains(prompt, "visible-resource-marker") && !strings.Contains(prompt, `"matchCount":`) {
			for _, a := range menu.Actions {
				if a.Kind != "document_search" || !strings.Contains(a.Label, "seedlings.csv") {
					continue
				}
				if searchName == "" || !searchAllowed[a.ID] {
					t.Error("search action not advertised")
					return
				}
				documentSearches.Add(1)
				args, _ := json.Marshal(map[string]string{"actionId": a.ID, "query": "height"})
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "search-current-data", "function": map[string]any{"name": searchName, "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "资源对象接口"})
	if err != nil {
		t.Fatal(err)
	}
	start := func(idea string) workflow.RunDetail {
		t.Helper()
		r, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: p.ID, ResearchIdea: idea, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
		if err != nil {
			t.Fatal(err)
		}
		return waitForWorkflowState(t, app, p.ID, r.Run.ID, workflow.RunCompleted, 20*time.Second)
	}
	importFile := func(task, name, marker string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(file, []byte("light_hours,height,source\n8,12,"+marker+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		batch, err := app.AttachmentFacade.ImportTaskDocumentPaths(p.ID, task, []string{file})
		if err != nil || len(batch.Errors) > 0 || len(batch.Attachments) != 1 {
			t.Fatalf("import: %+v %v", batch, err)
		}
		return batch.Attachments[0].ID
	}
	other := start("另一个课题的研究设计")
	importFile(other.Run.ResearchTaskID, "private-other.csv", "other-task-private-marker")
	initial := start("我想研究光照时间与幼苗生长，已有模拟数据，需要分析和报告。")
	if documentReads.Load() != 0 {
		t.Fatal("read data before the task received it")
	}
	attachmentID := importFile(initial.Run.ResearchTaskID, "seedlings.csv", "visible-resource-marker")
	replanned, err := app.WorkflowFacade.ReplanResearchStarter(workflow.ReplanResearchStarterCommand{ProjectID: p.ID, StarterRunID: initial.Run.ID})
	if err != nil {
		t.Fatal(err)
	}
	end := waitForWorkflowState(t, app, p.ID, replanned.Run.ID, workflow.RunCompleted, 20*time.Second)
	if documentReads.Load() != 1 {
		t.Fatalf("read count=%d", documentReads.Load())
	}
	if documentSearches.Load() != 1 {
		t.Fatalf("search count=%d", documentSearches.Load())
	}
	var runID string
	if err = app.store.DB().QueryRow(`SELECT chat_run_id FROM workflow_ai_chat_runs WHERE execution_id=?`, end.AIExecutions[0].ID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	var failures int
	if err = app.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE run_id=? AND status<>'completed'`, runID).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("resource flow required failed calls: %d %v", failures, err)
	}
	rows, err := app.store.DB().Query(`SELECT c.arguments_json,r.structured_json,r.model_context_text FROM tool_calls c JOIN tool_results r ON r.tool_call_id=c.id WHERE c.run_id=? AND c.tool_name IN ('builtin.resource.open','builtin.resource.search')`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	readVerified := false
	searchVerified := false
	for rows.Next() {
		var argsRaw, raw, modelContext string
		if err = rows.Scan(&argsRaw, &raw, &modelContext); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(modelContext, `"sourceArguments"`) {
			t.Fatal("private adapter arguments leaked into model replay")
		}
		var args map[string]any
		json.Unmarshal([]byte(argsRaw), &args)
		if len(args) > 2 || args["actionId"] == nil || (len(args) == 2 && args["query"] != "height") {
			t.Fatalf("model supplied more than a selection: %s", argsRaw)
		}
		var result struct {
			SourceTool      string
			SourceArguments map[string]any
			SourceResult    json.RawMessage
		}
		if err = json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		if result.SourceTool == "builtin.document.read" {
			if result.SourceArguments["attachmentId"] != attachmentID || !strings.Contains(string(result.SourceResult), "visible-resource-marker") {
				t.Fatal("resolved the wrong document")
			}
			readVerified = true
		}
		if result.SourceTool == "builtin.document.search" {
			if result.SourceArguments["attachmentId"] != attachmentID || result.SourceArguments["query"] != "height" || !strings.Contains(modelContext, `"matchCount":1`) {
				t.Fatal("search did not resolve the issued document/query")
			}
			searchVerified = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !readVerified {
		t.Fatal("missing resolved-operation audit")
	}
	if !searchVerified {
		t.Fatal("missing resolved search audit")
	}
}

func TestResearchSubmissionInterfaceUsesFrozenSchemaAndRepairsWithoutExploration(t *testing.T) {
	var requests, submissions, exploration atomic.Int32
	var rejectedTurn, acceptedTurn int
	var rejected, accepted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			t.Error(err)
			return
		}
		var req struct {
			Tools []struct {
				Function struct {
					Name       string
					Parameters json.RawMessage
				}
			}
			ToolChoice json.RawMessage `json:"tool_choice"`
			Parallel   *bool           `json:"parallel_tool_calls"`
			Messages   []struct {
				Role      string
				ToolCalls []any `json:"tool_calls"`
			}
		}
		if err = json.Unmarshal(body, &req); err != nil {
			t.Error(err)
			return
		}
		n := requests.Add(1)
		name, args := "", ""
		controlsOnly := len(req.Tools) > 0
		for _, d := range req.Tools {
			controlsOnly = controlsOnly && (d.Function.Name == "submit_stage_result" || d.Function.Name == "submit_stage_repair")
		}
		if controlsOnly {
			// This fixture deliberately waits for the exclusive submission
			// phase; early native submission is covered by planner tests.
			k := submissions.Add(1)
			choiceRequired := string(req.ToolChoice) == `"required"`
			if req.Parallel == nil || *req.Parallel {
				t.Error("parallel stage controls must be disabled")
			}
			if !choiceRequired {
				var choice struct {
					Type     string
					Function struct{ Name string }
				}
				if json.Unmarshal(req.ToolChoice, &choice) != nil || choice.Type != "function" || choice.Function.Name != "submit_stage_result" {
					t.Error("stage action is not required")
				}
			}
			for _, m := range req.Messages {
				if m.Role == "tool" || len(m.ToolCalls) > 0 {
					t.Error("exploration protocol leaked into submission")
				}
			}
			var schema map[string]any
			var wire json.RawMessage
			for _, d := range req.Tools {
				if d.Function.Name == "submit_stage_result" {
					wire = d.Function.Parameters
				}
			}
			if err = json.Unmarshal(wire, &schema); err != nil {
				t.Error(err)
				return
			}
			frozen, schemaErr := workflowAITestSchema(workflowAITestPrompt(body))
			if schemaErr != nil || !bytes.Equal(mustBootstrapJSON(t, frozen), mustBootstrapJSON(t, schema)) {
				t.Error("native control differs from frozen stage schema", schemaErr)
			}
			plan := workflowAISchemaFixture(schema).(map[string]any)
			applyScientificWritingRouteFixture(plan)
			if k == 1 {
				plan["normalizedQuestion"] = map[string]any{"invalid": "shape"}
			}
			raw, _ := json.Marshal(plan)
			args = string(raw)
			name = "submit_stage_result"
			if k == 1 {
				rejected, rejectedTurn = args, int(n)
			} else {
				accepted, acceptedTurn = args, int(n)
				if !strings.Contains(string(body), "normalizedQuestion") {
					t.Error("missing repair detail")
				}
			}
		} else {
			exploration.Add(1)
			var enabled bool
			name, args, enabled, err = resourceFixtureSelection(body, "scientific-writing", false)
			if !enabled || err != nil {
				t.Errorf("resource selection: %v %v", enabled, err)
				http.Error(w, "resource unavailable", 400)
				return
			}
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("submission-flow-%d", n), "function": map[string]any{"name": name, "arguments": args}}}}, "finish_reason": "tool_calls"}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "独立阶段提交接口"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "设计幼苗光照实验，尚未采集数据。", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	end := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 25*time.Second)
	if exploration.Load() == 0 || requests.Load() != exploration.Load()+2 || submissions.Load() != 2 || acceptedTurn != rejectedTurn+1 {
		t.Fatalf("requests=%d exploration=%d submissions=%d rejection/acceptance turns=%d/%d", requests.Load(), exploration.Load(), submissions.Load(), rejectedTurn, acceptedTurn)
	}
	var runID string
	if err = app.store.DB().QueryRow(`SELECT chat_run_id FROM workflow_ai_chat_runs WHERE execution_id=?`, end.AIExecutions[0].ID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	var failures, controlCalls int
	if err = app.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE run_id=? AND status<>'completed'`, runID).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("tool failure: %d %v", failures, err)
	}
	if err = app.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE run_id=? AND tool_name='submit_stage_result'`, runID).Scan(&controlCalls); err != nil || controlCalls != 0 {
		t.Fatal("submission was dispatched as an IO tool", err)
	}
	for turn, want := range map[int]string{rejectedTurn: rejected, acceptedTurn: accepted} {
		var raw string
		if err = app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=?`, runID, turn).Scan(&raw); err != nil || raw != want {
			t.Fatalf("candidate audit mismatch turn %d: %v", turn, err)
		}
	}
}

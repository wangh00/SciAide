package bootstrap

import (
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

	"github.com/wangh00/SciAide/internal/app/tool"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestColdAdmissionFits24kWithoutUsageRetryOrLargerWindow(t *testing.T) {
	r := runResearchCapacityCase(t, researchCapacityCase{Name: "cold_admission_24000", Mode: "plain", Window: 24000})
	if r.Outcome != "selected_work_completed" || r.ModelHTTP != 1 || r.CheckpointHTTP != 0 || r.ProviderInputTokens != 0 || r.EffectiveBudget != 22800 {
		t.Fatal("a short 24k research discussion still cannot make its first request", r.Outcome, r.ModelHTTP, r.ErrorDetails)
	}
}

func TestColdAdmissionStillRejectsImpossibleInputWithSafeBreakdown(t *testing.T) {
	r := runResearchCapacityCase(t, researchCapacityCase{Name: "cold_impossible_input", Mode: "plain", Window: 24000, InputRunes: 40000})
	if r.Outcome != "capacity_stopped" || r.ModelHTTP != 0 || r.NotePublished || !strings.Contains(r.ErrorDetails, `"phase":"fixed_input"`) || !strings.Contains(r.ErrorDetails, `"deferred_builtin_count":`) || strings.Contains(r.ErrorDetails, "界界界") || !strings.Contains(r.ErrorMessage, "工具定义字符估算") || !strings.Contains(r.ErrorMessage, "未自动扩窗或重试") {
		t.Fatal("impossible input was admitted, echoed or lost budget source", r.Outcome, r.ModelHTTP, r.ErrorDetails)
	}
}

func TestColdAdmissionLoadsAndReadsOriginalWorkspaceThroughHostDiscovery(t *testing.T) {
	const text = "只读原始夹具 🧪 \"quoted\"\nKeep the exact bytes."
	var mu sync.Mutex
	requests := 0
	path := ""
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
		var latest struct {
			Status     string
			Structured json.RawMessage
		}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				var text string
				_ = json.Unmarshal(m.Content, &text)
				text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(text), "<untrusted_tool_result>"), "</untrusted_tool_result>"))
				_ = json.Unmarshal([]byte(text), &latest)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		requests++
		n := requests
		visible := func(name string) bool {
			for _, d := range req.Tools {
				if d.Function.Name == name {
					return true
				}
			}
			return false
		}
		respond := func(name string, args map[string]any) {
			rec := httptest.NewRecorder()
			writeResearchAssistantFixtureResponse(rec, name, args, "")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, strings.ReplaceAll(rec.Body.String(), "controlled-"+name, fmt.Sprintf("cold-workspace-%d", n)))
		}
		stop := func(reason string) {
			t.Error(reason)
			writeResearchAssistantFixtureResponse(w, "", nil, "停止，不猜路径或补造内容。")
		}
		switch n {
		case 1:
			if visible("workspace_list") || visible("workspace_read_text") {
				stop("optional definitions were not deferred on cold admission")
				return
			}
			respond("tools_search", map[string]any{"kind": "builtin", "query": "builtin.workspace.list", "limit": 1})
		case 2:
			if latest.Status != "success" || !visible("workspace_list") {
				stop("successful discovery did not activate exact next-turn definition")
				return
			}
			respond("workspace_list", map[string]any{"path": ".", "limit": 20})
		case 3:
			var p struct{ Entries []struct{ Name, Path string } }
			_ = json.Unmarshal(latest.Structured, &p)
			for _, e := range p.Entries {
				if e.Name == "cold-fixture.txt" {
					path = e.Path
				}
			}
			if path == "" {
				stop("original workspace did not return the file identity")
				return
			}
			respond("tools_search", map[string]any{"kind": "builtin", "query": "builtin.workspace.read_text", "limit": 1})
		case 4:
			if !visible("workspace_read_text") {
				stop("read definition missing after second discovery")
				return
			}
			respond("workspace_read_text", map[string]any{"path": path, "offset": 0, "maxBytes": 400})
		case 5:
			var p struct{ Content string }
			_ = json.Unmarshal(latest.Structured, &p)
			if latest.Status != "success" || p.Content != text {
				stop("original read returned changed/truncated bytes")
				return
			}
			writeResearchAssistantFixtureResponse(w, "", nil, "仅核读夹具，不是科学批准。")
		default:
			stop("cold discovery repeated work or checkpointed unexpectedly")
		}
	}))
	defer server.Close()
	app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	if _, err := app.store.DB().Exec(`UPDATE model_profile_models SET context_window_tokens=24000,auto_compact_token_limit=21600,context_window_source='manual' WHERE profile_id=?`, profile.ID); err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "冷启动只读回放"})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(project.WorkspacePath, "cold-fixture.txt")
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	conv, err := app.ConversationFacade.CreateConversation(wails.CreateConversationRequest{ProjectID: project.ID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ConversationFacade.SetPermissionMode(conv.ID, "full_access"); err != nil {
		t.Fatal(err)
	}
	run := startResearchAssistantFixtureChat(t, app, conv.ID, profile.ID, "cold-workspace-control：只读 cold-fixture.txt，不创建研究、材料或路线。")
	if run.ContextWindowTokens != 24000 || run.ContextBudgetTokens != 22800 || run.InputTokens != 0 {
		t.Fatal("cold read changed window or invented usage", run)
	}
	snapshot, err := app.ChatFacade.GetRunSnapshot(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range snapshot.ToolCalls {
		if c.Status != tool.CallCompleted || c.Result == nil || c.Result.Status != tool.ResultSuccess {
			t.Fatal("original executor did not finish safely", c)
		}
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != text {
		t.Fatal("readonly discovery mutated source", err)
	}
	for _, table := range []string{"research_tasks", "workflow_runs", "attachments", "knowledge_documents", "artifacts"} {
		var n int
		if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatal("cold read created unrelated task/material", table, n, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 5 {
		t.Fatal("unexpected model retry or repeated read", requests)
	}
	t.Log("24k/no usage: 5 localhost model HTTP, 2 definition loads + list + exact read; no checkpoint/retry/new task")
}

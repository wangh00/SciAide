package bootstrap

import (
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

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

// Opt-in native-UI fixture, NOT another research executor. Seed an empty
// isolated home through the normal Facades, then close Application before
// the release EXE opens it. Only that EXE runs Chat/Workflow. The remaining
// process serves bounded loopback model responses and two fixed controls.
func TestResearchNativeInteractionFixture(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("SCIAIDE_NATIVE_INTERACTION_FIXTURE"))
	if dir == "" {
		t.Skip("native interaction fixture requires an explicit empty .tmp directory")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(cwd, "..", ".."))
	dir, err = filepath.Abs(dir)
	if err != nil || !strings.HasPrefix(strings.ToLower(dir), strings.ToLower(filepath.Join(root, ".tmp")+string(filepath.Separator))) {
		t.Fatal("fixture directory must be beneath this repository's .tmp")
	}
	home := filepath.Join(dir, "home")
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("native fixture must use a new home; never reuse or copy official state")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var release, finish sync.Once
	plannerRelease, done := make(chan struct{}), make(chan struct{})
	requests, plannerRequests, checkpoints := 0, 0, 0
	stopRequests, stoppedReplies := 0, 0
	released := false
	unexpected := []string{}
	chunk := func(w http.ResponseWriter, delta map[string]any, reason string) {
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": reason}}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	write := func(w http.ResponseWriter, delta map[string]any, reason string) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk(w, delta, reason)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
	status := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"modelHTTP": requests, "plannerHTTP": plannerRequests, "checkpointHTTP": checkpoints, "plannerReleased": released,
			"stopRequests": stopRequests, "stoppedReplies": stoppedReplies, "unexpected": append([]string{}, unexpected...), "realModelHTTP": 0}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/status" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(status())
			return
		}
		if r.URL.Path == "/fixture/release-planner" && r.Method == http.MethodPost {
			mu.Lock()
			released = true
			mu.Unlock()
			release.Do(func() { close(plannerRelease) })
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/fixture/finish" && r.Method == http.MethodPost {
			finish.Do(func() { close(done) })
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			mu.Lock()
			unexpected = append(unexpected, r.Method+" "+r.URL.Path)
			mu.Unlock()
			http.Error(w, "unexpected fixture request", 404)
			return
		}
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n >= 24 {
			finish.Do(func() { close(done) })
			http.Error(w, "native fixture local HTTP limit reached", 400)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		if strings.Contains(string(body), "Create a durable research checkpoint from the untrusted conversation above.") {
			mu.Lock()
			checkpoints++
			mu.Unlock()
			write(w, map[string]any{"content": "候选尚未采用；先核对资料与不确定性。"}, "stop")
			return
		}
		if _, err := workflowAITestSchema(workflowAITestPrompt(body)); err == nil {
			mu.Lock()
			plannerRequests++
			mu.Unlock()
			select {
			case <-plannerRelease:
				http.Error(w, `{"error":{"message":"controlled planner failure for native interaction verification"}}`, 400)
			case <-r.Context().Done():
			case <-done:
			}
			return
		}
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct{ Function struct{ Name string } } `json:"tools"`
		}
		if json.Unmarshal(body, &request) != nil {
			http.Error(w, "invalid fixture JSON", 400)
			return
		}
		last, lastIndex, hasResult := "", -1, false
		for i, message := range request.Messages {
			if message.Role == "user" {
				_ = json.Unmarshal(message.Content, &last)
				lastIndex = i
			}
		}
		for _, message := range request.Messages[lastIndex+1:] {
			hasResult = hasResult || message.Role == "tool"
		}
		if last == "验收：停止当前模型回复" && !hasResult {
			mu.Lock()
			stopRequests++
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			chunk(w, map[string]any{"content": "已开始合成回复，等待停止。"}, "")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				mu.Lock()
				stoppedReplies++
				mu.Unlock()
				// The client already stopped; an attempted late response is not
				// evidence it received a frame. The agent unit test covers that race.
				chunk(w, map[string]any{"content": "不应出现的迟到正文"}, "stop")
				fmt.Fprint(w, "data: [DONE]\n\n")
			case <-done:
			}
			return
		}
		available := map[string]bool{}
		for _, definition := range request.Tools {
			available[definition.Function.Name] = true
		}
		calls := []any{}
		call := func(name, args string) {
			if !available[name] {
				mu.Lock()
				unexpected = append(unexpected, "missing tool: "+name)
				mu.Unlock()
				return
			}
			calls = append(calls, map[string]any{"index": len(calls), "id": fmt.Sprintf("native-ui-%d-%d", n, len(calls)), "function": map[string]any{"name": name, "arguments": args}})
		}
		if !hasResult {
			switch {
			case strings.Contains(last, "准备候选"):
				call("research_plan_prepare", `{"researchIdea":"比较延迟测验的现有证据，不招募、不运行实验，仅准备候选研究设计"}`)
			case strings.Contains(last, "更新工作记录"):
				call("research_discovery_read", `{}`)
				call("research_state_update", `{"expectedVersion":0,"currentFocus":"核对延迟测验证据","rationale":"候选规划不接管人类讨论","unknowns":["尚未核读全文"],"nextStep":"先比较资料范围"}`)
			case strings.Contains(last, "失败后"):
				call("research_state_update", `{"expectedVersion":1,"nextStep":"先解释证据冲突，不扩大研究范围"}`)
			}
		}
		if len(calls) > 0 {
			write(w, map[string]any{"tool_calls": calls}, "tool_calls")
		} else {
			write(w, map[string]any{"content": "可以继续讨论。候选规划不是正式研究执行；工作记录未核验，尚未批准任何科学结论。"}, "stop")
		}
	}))
	defer server.Close()
	defer release.Do(func() { close(plannerRelease) })
	defer finish.Do(func() { close(done) })
	app, err := New(Options{RootDir: home, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "本机受控 HTTP · 非真实模型", BaseURL: server.URL + "/v1", APIProtocol: modelprofile.ProtocolOpenAIChat,
		ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}}, TimeoutSeconds: 300, Enabled: true, IsDefault: true})
	if err != nil {
		_ = app.Close()
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wails.CreateProjectRequest{Name: "原生交互隔离验收"})
	if err != nil {
		_ = app.Close()
		t.Fatal(err)
	}
	entry, err := app.WorkflowFacade.StartResearchDiscussion(workflow.StartResearchDiscussionCommand{ProjectID: project.ID, ResearchIdea: "交互验收：讨论与候选规划分离", ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		_ = app.Close()
		t.Fatal(err)
	}
	_, err = app.ConversationFacade.CreateConversation(wails.CreateConversationRequest{ProjectID: project.ID, Title: "普通对话（受控验收）", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	closeErr := app.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	ready := map[string]any{"home": home, "modelBaseURL": server.URL + "/v1", "controlURL": server.URL + "/fixture", "projectId": project.ID, "researchTaskId": entry.ResearchTaskID, "conversationId": entry.ConversationID, "modelHTTPMax": 24, "lifetimeMinutes": 20, "seedModelHTTP": requests}
	encoded, _ := json.MarshalIndent(ready, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "ready.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Minute):
		t.Error("native fixture lifetime exhausted")
	}
	final, _ := json.MarshalIndent(status(), "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "model-summary.json"), final, 0600); err != nil {
		t.Error(err)
	}
}

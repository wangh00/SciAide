package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Real HTTP adapter, resource executor, frozen Skill snapshots, workflow/SQLite
// and restart. The model is a deterministic fixture, not scientific acceptance.
func TestStageSourceHandoffReachesNextStageBeforeRediscovery(t *testing.T) {
	base := newResearchStarterSkillTestServer(t)
	var mu sync.Mutex
	sectionCalls := 0
	seen := map[string][]workflow.StageMaterial{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		prompt := workflowAITestPrompt(body)
		schema, err := workflowAITestSchema(prompt)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid frozen schema", 400)
			return
		}
		properties, _ := schema["properties"].(map[string]any)
		_, method := properties["selectedSkills"]
		if _, starter := properties["normalizedQuestion"]; starter {
			method = false
		}
		if method && !strings.Contains(prompt, `"mode":"section"`) {
			start := strings.Index(prompt, "<resource_actions>\n")
			if start < 0 {
				t.Error("no issued resource menu")
				http.Error(w, "no menu", 400)
				return
			}
			start += len("<resource_actions>\n")
			end := strings.Index(prompt[start:], "\n</resource_actions>")
			var menu struct{ Actions []resource.Option }
			if end < 0 || json.Unmarshal([]byte(prompt[start:start+end]), &menu) != nil {
				t.Error("bad menu")
				http.Error(w, "bad menu", 400)
				return
			}
			for _, option := range menu.Actions {
				if strings.HasPrefix(option.Label, "读取 Skill 章节 · scientific-writing / ") {
					mu.Lock()
					sectionCalls++
					mu.Unlock()
					args, _ := json.Marshal(map[string]string{"actionId": option.ID})
					emitQuestionHandoffFixture(t, w, "openai_chat_completions", 1, "", option.Tool, string(args))
					return
				}
			}
			t.Error("header preload did not issue a section reader")
			http.Error(w, "missing section", 400)
			return
		}
		stage := ""
		if _, ok := properties["status"]; ok {
			stage = "design"
		}
		if _, ok := properties["reviewedInputSha256"]; ok {
			stage = "review"
		}
		if stage != "" {
			var request struct {
				Messages []struct {
					Role    string
					Content json.RawMessage
				}
			}
			_ = json.Unmarshal(body, &request)
			var sources []workflow.StageMaterial
			for _, message := range request.Messages {
				if message.Role != "user" {
					continue
				}
				var text string
				_ = json.Unmarshal(message.Content, &text)
				text = strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_conversation_content>\n"), "\n</untrusted_conversation_content>")
				if strings.HasPrefix(text, "Host source handoff:") {
					index := strings.Index(text, "\n[")
					if index < 0 || json.Unmarshal([]byte(text[index+1:]), &sources) != nil {
						t.Error("handoff is not complete source JSON")
					}
				}
			}
			if len(sources) != 1 || sources[0].SkillName != "scientific-writing" || sources[0].Content == "" || strings.Contains(sources[0].Content, "old-scoped-action") {
				t.Errorf("%s did not receive the original section before exploration: %+v", stage, sources)
			}
			mu.Lock()
			seen[stage] = sources
			mu.Unlock()
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	defer app.Close()
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "source handoff"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "设计一项大学生睡眠研究，不编造结果", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planner := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 30*time.Second)
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: project.ID, RunID: planner.Run.ID, RouteID: "design_first"})
	if err != nil || adopted.Run == nil {
		t.Fatal("adoption", err)
	}
	formal := waitForWorkflowState(t, app, project.ID, adopted.Run.Run.ID, workflow.RunCompleted, 30*time.Second)
	mu.Lock()
	defer mu.Unlock()
	if sectionCalls != 1 || len(seen) != 2 {
		t.Fatalf("section IO=%d received stages=%d", sectionCalls, len(seen))
	}
	if len(seen["design"]) == 0 || len(seen["review"]) == 0 {
		t.Fatal("source absent from downstream first request")
	}
	if seen["design"][0].Content != seen["review"][0].Content || seen["design"][0].SHA256 != seen["review"][0].SHA256 {
		t.Fatal("review did not receive the same original source")
	}
	for _, node := range formal.Run.Compilation.Nodes {
		if node.Kind == workflow.NodeAgentStage && node.StageMaterialsVersion != "source-v1" {
			t.Fatal("handoff version was not frozen")
		}
	}
	var count int
	if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM run_dynamic_skills WHERE run_id IN (SELECT b.chat_run_id FROM workflow_ai_chat_runs b JOIN workflow_ai_executions e ON e.id=b.execution_id WHERE e.workflow_run_id=?)`, formal.Run.ID).Scan(&count); err != nil || count < 3 {
		t.Fatal("handoff bypassed actual stage Skill loading", err, count)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Startup(context.Background())
	after, err := reopened.WorkflowFacade.GetRun(project.ID, formal.Run.ID)
	if err != nil || after.Run.Status != workflow.RunCompleted || fmt.Sprint(after.Run.CompilationSHA256) != fmt.Sprint(formal.Run.CompilationSHA256) {
		t.Fatal("restart changed the frozen route", err)
	}
}

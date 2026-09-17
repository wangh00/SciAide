package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestPlannerSkillValidationContinuesSameRunAndBoundsFailure(t *testing.T) {
	for _, correct := range []bool{true, false} {
		t.Run(fmt.Sprintf("correct_%t", correct), func(t *testing.T) {
			var requests, feedback atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
				var req struct {
					Tools []struct {
						Function struct{ Name, Description string }
					}
				}
				if err := json.Unmarshal(body, &req); err != nil {
					t.Error(err)
					http.Error(w, "bad request", 400)
					return
				}
				prompt := workflowAITestPrompt(body)
				schema, err := workflowAITestSchema(prompt)
				if err != nil {
					t.Error(err)
					http.Error(w, "missing schema", 400)
					return
				}
				n := requests.Add(1)
				sawFeedback := strings.Contains(string(body), "WORKFLOW_PLANNER_SKILLS_MISSING")
				if sawFeedback {
					feedback.Add(1)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if n == 1 || n == 3 && correct {
					if n == 3 && !sawFeedback {
						t.Error("missing host feedback before correction")
					}
					name := ""
					for _, tool := range req.Tools {
						if strings.Contains(tool.Function.Description, "Load specialized research instructions") {
							name = tool.Function.Name
						}
					}
					args := `{"category":"research","limit":5}`
					if n == 3 {
						args = `{"name":"scientific-writing"}`
					}
					if selected, arguments, enabled, selectErr := resourceFixtureSelection(body, "scientific-writing", n == 1); enabled {
						if selectErr != nil {
							t.Error(selectErr)
							return
						}
						name, args = selected, arguments
					}
					if name == "" {
						t.Error("missing Skill resource action")
						return
					}
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("skill-%d", n), "function": map[string]any{"name": name, "arguments": args}}}}, "finish_reason": "tool_calls"}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
					return
				}
				value := workflowAISchemaFixture(schema).(map[string]any)
				applyScientificWritingRouteFixture(value)
				data, _ := json.Marshal(value)
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "```json\n" + string(data) + "\n```"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 40}})
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
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "规划 Skill 真实加载核验"})
			if err != nil {
				t.Fatal(err)
			}
			started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "分析模拟数据，先读取必要 Skill", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			want := workflow.RunCompleted
			if !correct {
				want = workflow.RunFailed
			}
			end := waitForWorkflowState(t, app, project.ID, started.Run.ID, want, 20*time.Second)
			if feedback.Load() == 0 {
				t.Fatal("validator did not feed back to model")
			}
			if requests.Load() != 4 {
				t.Fatalf("unbounded or missing continuation: %d requests", requests.Load())
			}
			if len(end.AIExecutions) != 1 {
				t.Fatalf("restarted Chat Run instead of continuing it: %d", len(end.AIExecutions))
			}
			if correct {
				plan, err := app.WorkflowFacade.GetResearchStarterPlan(project.ID, end.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.LoadedSkills) != 1 || plan.LoadedSkills[0].Name != "scientific-writing" {
					t.Fatalf("false loaded projection: %#v", plan.LoadedSkills)
				}
				for _, route := range plan.Routes {
					if route.Validation == "invalid" {
						t.Fatalf("corrected route invalid: %s", route.ValidationError)
					}
				}
			} else if end.Run.ErrorCode != "WORKFLOW_PLANNER_SKILLS_INVALID" {
				t.Fatalf("wrong terminal error: %s %s", end.Run.ErrorCode, end.Run.ErrorMessage)
			}
		})
	}
}

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestPlannerSkillValidationContinuesSameRunAndBoundsFailure(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []struct{ correct, native bool }{{true, false}, {false, false}, {true, true}, {false, true}} {
			correct := mode.correct
			t.Run(fmt.Sprintf("%s/native_%t/correct_%t", protocol, mode.native, correct), func(t *testing.T) {
				var requests, feedback atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
					nativeBody := append([]byte(nil), body...)
					if !plannerActionRequiredOnWire(protocol, body) {
						t.Error("planner did not require an action while retaining Skill IO and submission")
					}
					body, viewErr := plannerSkillFixtureChatView(body)
					if viewErr != nil {
						t.Error(viewErr)
						http.Error(w, "invalid fixture request", 400)
						return
					}
					var req struct {
						Messages []struct {
							Role    string
							Content json.RawMessage
						}
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
					var actual any
					if json.Unmarshal(plannerSubmissionSchemaOnWire(protocol, nativeBody), &actual) != nil || !reflect.DeepEqual(actual, schema) {
						t.Error("native planner submission did not carry the complete frozen schema")
					}
					n := requests.Add(1)
					sawFeedback := strings.Contains(string(body), "WORKFLOW_PLANNER_SKILLS_MISSING")
					correctionText := ""
					if sawFeedback {
						feedback.Add(1)
						for _, message := range req.Messages {
							var text string
							if message.Role == "user" && json.Unmarshal(message.Content, &text) == nil && strings.Contains(text, "WORKFLOW_PLANNER_SKILLS_MISSING") {
								correctionText = text
								if strings.Contains(text, "builtin.skill.load") || !strings.Contains(text, "resource_open") {
									t.Error("native Skill feedback directs the model to an unoffered tool", text)
								}
							}
						}
					}
					if correct && n == 4 && sawFeedback {
						t.Error("successful persisted Skill loading left a stale missing-Skill instruction")
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
							if n == 3 {
								feedbackTool, feedbackArgs, feedbackErr := plannerSkillFixtureFeedbackAction(correctionText, "scientific-writing")
								var menuArgs, actualArgs struct{ ActionID string }
								menuErr := json.Unmarshal([]byte(arguments), &menuArgs)
								actualErr := json.Unmarshal([]byte(feedbackArgs), &actualArgs)
								if feedbackErr != nil || menuErr != nil || actualErr != nil || feedbackTool != selected || menuArgs.ActionID == "" || actualArgs.ActionID != menuArgs.ActionID {
									t.Errorf("feedback action does not match the offered Skill menu: %v", feedbackErr)
									http.Error(w, "invalid feedback action", 400)
									return
								}
								// Execute the feedback's action, not a separately selected
								// menu action that could mask incorrect feedback.
								name, args = feedbackTool, feedbackArgs
							}
						}
						if name == "" {
							t.Error("missing Skill resource action")
							return
						}
						emitQuestionHandoffFixture(t, w, protocol, int(n), "", name, args)
						return
					}
					value := workflowAISchemaFixture(schema).(map[string]any)
					applyScientificWritingRouteFixture(value)
					data, _ := json.Marshal(value)
					if mode.native {
						for _, definition := range req.Tools {
							if definition.Function.Name == "submit_stage_result" {
								emitQuestionHandoffFixture(t, w, protocol, int(n), "", definition.Function.Name, string(data))
								return
							}
						}
						t.Error("planner has no optional native submission control")
						http.Error(w, "missing planner submission control", 400)
						return
					}
					emitQuestionHandoffFixture(t, w, protocol, int(n), "```json\n"+string(data)+"\n```", "", "")
				}))
				defer server.Close()
				app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				app.Startup(context.Background())
				profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "planner feedback fixture", BaseURL: server.URL, APIProtocol: protocol, ModelID: workflowAITestModelID, TimeoutSeconds: 5, Enabled: true, IsDefault: true})
				if err != nil {
					t.Fatal(err)
				}
				project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "规划 Skill 真实加载核验"})
				if err != nil {
					t.Fatal(err)
				}
				started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "分析模拟数据，先读取必要 Skill", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
				if err != nil {
					t.Fatal(err)
				}
				want := workflow.RunCompleted
				if !correct || !mode.native {
					want = workflow.RunFailed
				}
				end := waitForWorkflowState(t, app, project.ID, started.Run.ID, want, 20*time.Second)
				if mode.native && feedback.Load() == 0 {
					t.Fatal("validator did not feed back to model")
				}
				wantRequests := int32(4)
				if !mode.native {
					wantRequests = 2
				}
				if requests.Load() != wantRequests {
					t.Fatalf("unbounded or missing continuation: %d requests", requests.Load())
				}
				if len(end.AIExecutions) != 1 {
					t.Fatalf("restarted Chat Run instead of continuing it: %d", len(end.AIExecutions))
				}
				if !mode.native {
					if end.Run.ErrorCode != "WORKFLOW_AI_NATIVE_ACTION_MISSING" || feedback.Load() != 0 {
						t.Fatal("missing native action was classified as content/Skill failure", end.Run.ErrorCode, feedback.Load())
					}
					journal, found, err := sqlite.NewRunRepository(app.store.DB()).LatestModelTurnJournal(context.Background(), end.AIExecutions[0].ChatRunID)
					if err != nil || !found || !strings.HasPrefix(journal.DraftText, "```json\n") {
						t.Fatal("native protocol error lost original text", err)
					}
					return
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
}

func plannerSkillFixtureFeedbackAction(text, skill string) (string, string, error) {
	const marker = "当前已签发的对应加载动作（只是选项，尚未执行）："
	_, encoded, found := strings.Cut(text, marker)
	if !found {
		return "", "", fmt.Errorf("feedback has no issued-action menu")
	}
	var actions []struct{ SkillName, Tool, ActionID string }
	if err := json.NewDecoder(strings.NewReader(encoded)).Decode(&actions); err != nil {
		return "", "", err
	}
	for _, action := range actions {
		if action.SkillName == skill {
			args, _ := json.Marshal(map[string]string{"actionId": action.ActionID})
			return action.Tool, string(args), nil
		}
	}
	return "", "", fmt.Errorf("feedback has no action for %s", skill)
}

// Translate envelope structure only for the existing fixture reader. Tool
// names, schemas and projected host text stay exactly as received over HTTP.
func plannerSkillFixtureChatView(body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var rawTools []map[string]json.RawMessage
	if err := json.Unmarshal(payload["tools"], &rawTools); err != nil {
		return nil, err
	}
	tools := []map[string]any{}
	for _, value := range rawTools {
		if fn := value["function"]; fn != nil {
			if err := json.Unmarshal(fn, &value); err != nil {
				return nil, err
			}
		}
		parameters := value["parameters"]
		if parameters == nil {
			parameters = value["input_schema"]
		}
		tools = append(tools, map[string]any{"function": map[string]any{"name": value["name"], "description": value["description"], "parameters": parameters}})
	}
	messages := []map[string]string{}
	for _, field := range []string{"system", "messages", "input", "instructions"} {
		var value any
		if len(payload[field]) == 0 {
			continue
		}
		if err := json.Unmarshal(payload[field], &value); err != nil {
			return nil, err
		}
		visitStageProtocolStrings(value, func(text string) {
			role := "system"
			if strings.Contains(text, "WORKFLOW_PLANNER_SKILLS_MISSING") {
				role = "user"
			}
			messages = append(messages, map[string]string{"role": role, "content": text})
		})
	}
	return json.Marshal(map[string]any{"tools": tools, "messages": messages})
}

func TestPlannerNativeSubmissionRefusalAndInvalidArgumentsAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, invalid := range []bool{false, true} {
			if invalid && protocol == modelprofile.ProtocolAnthropic {
				continue // Anthropic input is a JSON value, not an argument string.
			}
			t.Run(fmt.Sprintf("%s/invalid_arguments_%t", protocol, invalid), func(t *testing.T) {
				var requests atomic.Int32
				var servedArgs atomic.Value
				const refusal = "I cannot comply with this request."
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
					body, err := plannerSkillFixtureChatView(body)
					if err != nil {
						t.Error(err)
						return
					}
					if !strings.Contains(string(body), `submit_stage_result`) {
						t.Error("missing native planner control")
						return
					}
					schema, err := workflowAITestSchema(workflowAITestPrompt(body))
					if err != nil {
						t.Error(err)
						return
					}
					value := workflowAISchemaFixture(schema).(map[string]any)
					applyScientificWritingRouteFixture(value)
					data, _ := json.Marshal(value)
					args, text := string(data), refusal
					if invalid {
						args += "}"
						text = ""
					}
					servedArgs.Store(args)
					requests.Add(1)
					emitPlannerControlWithText(t, w, protocol, text, args)
				}))
				defer server.Close()
				app, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}})
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				app.Startup(context.Background())
				profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "planner native boundary fixture", BaseURL: server.URL, APIProtocol: protocol, ModelID: workflowAITestModelID, TimeoutSeconds: 5, Enabled: true, IsDefault: true})
				if err != nil {
					t.Fatal(err)
				}
				project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "规划原生控制边界"})
				if err != nil {
					t.Fatal(err)
				}
				started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: project.ID, ResearchIdea: "仅设计方案，不执行实验", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
				if err != nil {
					t.Fatal(err)
				}
				end := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 20*time.Second)
				want := "WORKFLOW_AI_MODEL_REFUSED"
				if invalid {
					want = "MODEL_TOOL_CALL_INVALID"
					if protocol == modelprofile.ProtocolOpenAIResponses {
						want = "MODEL_STREAM_INVALID"
					}
				}
				if end.Run.ErrorCode != want || requests.Load() != 1 || len(end.AIExecutions) != 1 {
					t.Fatal("native control bypassed refusal or repaired invalid argument bytes", end.Run.ErrorCode, requests.Load())
				}
				if !invalid {
					journal, found, err := sqlite.NewRunRepository(app.store.DB()).LatestModelTurnJournal(context.Background(), end.AIExecutions[0].ChatRunID)
					if err != nil || !found || journal.SubmissionCommentary != refusal || journal.DraftText != servedArgs.Load().(string) {
						t.Fatal("real adapter lost original refusal text or full argument bytes", err)
					}
				}
			})
		}
	}
}

func emitPlannerControlWithText(t *testing.T, w http.ResponseWriter, protocol modelprofile.APIProtocol, text, args string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	switch protocol {
	case modelprofile.ProtocolOpenAIChat:
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
		emitQuestionHandoffFixture(t, w, protocol, 1, "", "submit_stage_result", args)
	case modelprofile.ProtocolOpenAIResponses:
		if text != "" {
			send(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "message", "id": "visible", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}})
		}
		index := 0
		if text != "" {
			index = 1
		}
		send(map[string]any{"type": "response.output_item.done", "output_index": index, "item": map[string]any{"type": "function_call", "id": "control", "call_id": "call_control", "name": "submit_stage_result", "arguments": args}})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
	case modelprofile.ProtocolAnthropic:
		send(map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 10}}})
		send(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": text}})
		send(map[string]any{"type": "content_block_stop", "index": 0})
		var value any
		if err := json.Unmarshal([]byte(args), &value); err != nil {
			t.Fatal(err)
		}
		send(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": "call_control", "name": "submit_stage_result", "input": value}})
		send(map[string]any{"type": "content_block_stop", "index": 1})
		send(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 20}})
		send(map[string]any{"type": "message_stop"})
	}
}

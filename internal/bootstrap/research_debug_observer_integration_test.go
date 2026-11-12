package bootstrap

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/model/responses"
)

// These are local HTTP/temporary SQLite fixtures, NOT real model evidence.
// They deliberately pass through detached Chat execute/resume contexts.
func TestResearchDebugErrorObserverActualExecuteResumeAndWorkflow(t *testing.T) {
	for _, variant := range []string{"default_off", "initial", "resume", "workflow", "fault_default_off", "fault_initial", "fault_resume", "fault_workflow"} {
		t.Run(variant, func(t *testing.T) {
			mode := strings.TrimPrefix(variant, "fault_")
			malformed := strings.HasPrefix(variant, "fault_")
			var calls atomic.Int32
			modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				if mode == "resume" && n == 1 {
					emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIResponses, n, "", "user_input_request", `{"questions":[{"id":"scope","question":"Which scope?","required":true,"options":[{"id":"bounded","label":"Bounded"},{"id":"broad","label":"Broad"}]}]}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if malformed {
					fmt.Fprint(w, "data: {\"type\":\"error\",\"message\":\"PRIVATE_FIXTURE_STREAM\" broken}\n\n")
					return
				}
				fmt.Fprint(w, "data: {\"type\":\"error\",\"code\":\"invalid_tool_history\",\"message\":\"PRIVATE_FIXTURE_ERROR\"}\n\n")
			}))
			defer modelServer.Close()
			observed := make(chan responses.DebugProviderError, 4)
			faults := make(chan responses.DebugStreamFault, 4)
			f := newDebugFixture(t, modelServer.URL, modelprofile.ProtocolOpenAIResponses, 4, true, func(o *Options) {
				if mode != "default_off" {
					if malformed {
						o.DebugResponseStreamFaultObserver = func(e responses.DebugStreamFault) { faults <- e }
					} else {
						o.DebugResponseErrorObserver = func(e responses.DebugProviderError) { observed <- e }
					}
				}
			})
			assertObservation := func() {
				t.Helper()
				if malformed {
					select {
					case event := <-faults:
						data, err := base64.StdEncoding.DecodeString(event.DataBase64)
						wire, wireErr := base64.StdEncoding.DecodeString(event.WireTailBase64)
						if err != nil || wireErr != nil || !strings.Contains(string(data), "PRIVATE_FIXTURE_STREAM") || !strings.Contains(string(wire), "data: "+string(data)) || !event.EventComplete || event.JSONValid {
							t.Fatal("wrong private stream evidence")
						}
					case <-time.After(time.Second):
						t.Fatal("stream observer lost across detached execution")
					}
					return
				}
				select {
				case event := <-observed:
					if event.Code != "invalid_tool_history" || event.Message != "PRIVATE_FIXTURE_ERROR" {
						t.Fatal("wrong capture", event)
					}
				case <-time.After(time.Second):
					t.Fatal("observer lost across actual Chat runner context")
				}
			}
			var public any
			if mode == "workflow" {
				// A genuine workflow AI stage uses the same resolver, not a direct
				// adapter call or a mock RunExecutor.
				rid, _ := id.New()
				c, _ := f.create(t, rid, true)
				f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "chat_failed" })
				assertObservation() // Initial discussion, separate from the workflow error.
				saved, err := f.app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: c.ProjectID, Definition: workflow.Definition{SchemaVersion: 1, Name: "observer fixture", Nodes: []workflow.Node{{ID: "draft", Name: "draft", Kind: workflow.NodeAgentStage, ReviewPolicy: workflow.AIReviewAuto, Arguments: json.RawMessage(`{}`), PromptVersion: "debug-observer-fixture-v1", Prompt: "Return a summary.", OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`)}}, Outputs: []workflow.Output{{Name: "summary", Type: workflow.TypeObject, FromNode: "draft", FromPort: "analysis", Required: true}}}})
				if err != nil {
					t.Fatal(err)
				}
				started, err := f.app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: c.ProjectID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: f.profileID, ModelID: f.modelID, PermissionMode: conversation.PermissionFullAccess})
				if err != nil {
					t.Fatal(err)
				}
				done := waitForWorkflowState(t, f.app, c.ProjectID, started.Run.ID, workflow.RunFailed, 10*time.Second)
				want := "MODEL_REQUEST_REJECTED"
				if malformed {
					want = "MODEL_STREAM_INVALID"
				}
				if done.Run.ErrorCode != want || calls.Load() != 2 {
					t.Fatal("workflow error policy changed", done.Run.ErrorCode, calls.Load())
				}
				public = done
			} else {
				rid, _ := id.New()
				c, _ := f.create(t, rid, true)
				if mode == "resume" {
					pending := f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "waiting_user_input" })
					var chats []struct {
						UserInput struct{ RunID, CallID string }
					}
					_ = json.Unmarshal(pending["chats"], &chats)
					code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/answer", map[string]any{"runId": chats[0].UserInput.RunID, "callId": chats[0].UserInput.CallID, "answers": map[string]any{"scope": map[string]any{"optionIds": []string{"bounded"}}}})
					if code != 200 {
						t.Fatalf("answer failed: %s", result["error"])
					}
				}
				public = f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "chat_failed" })
			}
			data, _ := json.Marshal(public)
			if strings.Contains(string(data), "PRIVATE_FIXTURE_ERROR") || strings.Contains(string(data), "PRIVATE_FIXTURE_STREAM") || strings.Contains(string(data), base64.StdEncoding.EncodeToString([]byte("PRIVATE_FIXTURE_STREAM"))) {
				t.Fatal("private capture leaked into public snapshot")
			}
			if mode == "default_off" {
				if len(observed) != 0 || len(faults) != 0 || calls.Load() != 1 {
					t.Fatal("default opt-in/retry policy changed")
				}
				return
			}
			assertObservation()
			if len(observed) != 0 || len(faults) != 0 {
				t.Fatal("duplicate provider capture")
			}
		})
	}
}

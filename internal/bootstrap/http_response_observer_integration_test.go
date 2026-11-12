package bootstrap

import (
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

// Real Application/Facade + local HTTP/temporary SQLite, never paid-model evidence.
func TestResearchDebugHTTPObserverExecuteResumeWorkflowAndDefaultOff(t *testing.T) {
	for _, mode := range []string{"default_off", "initial", "resume", "workflow"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				w.Header().Set("X-Request-Id", "PRIVATE_HEADER_FIXTURE")
				w.Header().Set("Retry-After", "120")
				w.Header().Set("Set-Cookie", "DO_NOT_CAPTURE")
				if mode == "resume" && n == 1 {
					emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIResponses, n, "", "user_input_request", `{"questions":[{"id":"scope","question":"Which scope?","required":true,"options":[{"id":"bounded","label":"Bounded"},{"id":"broad","label":"Broad"}]}]}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"error\",\"code\":\"invalid_tool_history\",\"message\":\"fixture error\"}\n\n")
			}))
			defer server.Close()
			observed := make(chan responses.DebugHTTPResponse, 4)
			f := newDebugFixture(t, server.URL, modelprofile.ProtocolOpenAIResponses, 4, true, func(o *Options) {
				if mode != "default_off" {
					o.DebugResponseHTTPObserver = func(e responses.DebugHTTPResponse) { observed <- e }
				}
			})
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
			var public any = f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "chat_failed" })
			want := 1
			if mode == "resume" {
				want = 2
			}
			if mode == "workflow" {
				saved, err := f.app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: c.ProjectID, Definition: workflow.Definition{SchemaVersion: 1, Name: "HTTP observer fixture", Nodes: []workflow.Node{{ID: "draft", Name: "draft", Kind: workflow.NodeAgentStage, ReviewPolicy: workflow.AIReviewAuto, Arguments: json.RawMessage(`{}`), PromptVersion: "debug-observer-fixture-v1", Prompt: "Return a summary.", OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string"}}}`)}}, Outputs: []workflow.Output{{Name: "summary", Type: workflow.TypeObject, FromNode: "draft", FromPort: "analysis", Required: true}}}})
				if err != nil {
					t.Fatal(err)
				}
				started, err := f.app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: c.ProjectID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), ModelProfileID: f.profileID, ModelID: f.modelID, PermissionMode: conversation.PermissionFullAccess})
				if err != nil {
					t.Fatal(err)
				}
				done := waitForWorkflowState(t, f.app, c.ProjectID, started.Run.ID, workflow.RunFailed, 10*time.Second)
				if done.Run.ErrorCode != "MODEL_REQUEST_REJECTED" {
					t.Fatal("observation changed original workflow error", done.Run.ErrorCode)
				}
				public = done
				want = 2
			}
			if calls.Load() != int32(want) {
				t.Fatal("observation added requests", calls.Load(), want)
			}
			data, _ := json.Marshal(public)
			if strings.Contains(string(data), "PRIVATE_HEADER_FIXTURE") || strings.Contains(string(data), "DO_NOT_CAPTURE") {
				t.Fatal("private headers escaped into public snapshot")
			}
			if mode == "default_off" {
				if len(observed) != 0 {
					t.Fatal("capture enabled by default")
				}
				return
			}
			for i := 0; i < want; i++ {
				select {
				case event := <-observed:
					if event.HTTPStatus != 200 || event.Headers["X-Request-Id"] != "PRIVATE_HEADER_FIXTURE" || event.Headers["Retry-After"] != "120" || event.Headers["Set-Cookie"] != "" {
						t.Fatal("missing, unsafe or changed observation", event)
					}
				case <-time.After(time.Second):
					t.Fatal("observation lost across actual detached execution")
				}
			}
			if len(observed) != 0 {
				t.Fatal("duplicate HTTP capture")
			}
		})
	}
}

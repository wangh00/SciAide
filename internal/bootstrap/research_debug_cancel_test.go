package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/id"
)

func TestResearchDebugCancelledDiscussionIsNotFailure(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 1, "", "user_input_request", `{"questions":[{"id":"scope","question":"Choose the case scope","required":true,"options":[{"id":"bounded","label":"Bounded"},{"id":"wide","label":"Wide"}]}]}`)
	}))
	defer modelServer.Close()
	f := newDebugFixture(t, modelServer.URL, modelprofile.ProtocolOpenAIChat, 2, false)
	rid, _ := id.New()
	c, _ := f.create(t, rid, false)
	f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "waiting_user_input" })
	code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/cancel-chat", map[string]any{"runId": c.InitialRunID})
	if code != http.StatusOK {
		t.Fatal("cancel", code, string(result["error"]))
	}
	value := f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "chat_cancelled" })
	if string(value["discussionState"]) != `"cancelled"` {
		t.Fatal("explicit cancellation shown as failure")
	}
}

func TestResearchDebugPendingQuestionCancelRestartAndExplicitFollowup(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var calls atomic.Int32
			modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				if n == 1 {
					emitQuestionHandoffFixture(t, w, protocol, n, "", "user_input_request", `{"questions":[{"id":"scope","question":"Choose the case scope","required":true,"options":[{"id":"bounded","label":"Bounded"},{"id":"wide","label":"Wide"}]}]}`)
					return
				}
				emitQuestionHandoffFixture(t, w, protocol, n, "Explicit new discussion completed; no scientific claim.", "", "")
			}))
			defer modelServer.Close()
			f := newDebugFixture(t, modelServer.URL, protocol, 4, false)
			rid, _ := id.New()
			c, _ := f.create(t, rid, false)
			pending := f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "waiting_user_input" })
			var chats []struct {
				UserInput struct{ RunID, CallID string }
			}
			_ = json.Unmarshal(pending["chats"], &chats)
			if len(chats) == 0 || chats[0].UserInput.CallID == "" {
				t.Fatal("missing original question identity")
			}
			for i := 0; i < 2; i++ {
				code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/cancel-chat", map[string]any{"runId": c.InitialRunID})
				if code != http.StatusOK {
					t.Fatal("cancel", code, string(result["error"]))
				}
			}
			lateAnswer := map[string]any{"runId": chats[0].UserInput.RunID, "callId": chats[0].UserInput.CallID, "answers": map[string]any{"scope": map[string]any{"optionIds": []string{"bounded"}}}}
			code, rejected := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/answer", lateAnswer)
			if code == http.StatusOK {
				t.Fatal("late answer resumed a cancelled question")
			}
			var publicError struct{ Code string }
			if err := json.Unmarshal(rejected["error"], &publicError); err != nil || publicError.Code != "USER_INPUT_EXPIRED" {
				t.Fatal("expected state conflict, not internal failure", string(rejected["error"]), err)
			}
			_, desktopErr := f.app.ChatFacade.RespondToUserInput(userinput.Response{ConversationID: c.ConversationID, RunID: c.InitialRunID, CallID: chats[0].UserInput.CallID, Dismiss: true})
			if desktopErr == nil || !strings.Contains(desktopErr.Error(), "答案未提交") || apperr.Public(desktopErr).Code != "USER_INPUT_EXPIRED" {
				t.Fatal("desktop conflict must explain non-submission and retain the public code", desktopErr)
			}
			f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "chat_cancelled" })
			if err := f.server.Close(); err != nil {
				t.Fatal(err)
			}
			f = openDebugFixture(t, f.root, "", "", modelServer.URL, protocol, 4, false, true)
			f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "chat_cancelled" })
			if calls.Load() != 1 {
				t.Fatal("cancellation/late answer/restart called the model", calls.Load())
			}
			messageID, _ := id.New()
			code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/message", map[string]any{"text": "Explicit follow-up after cancellation; do not resume the old question.", "clientMessageId": messageID})
			if code != http.StatusOK {
				t.Fatal("explicit follow-up", code, string(result["error"]))
			}
			f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "discussion_idle" })
			if calls.Load() != 2 {
				t.Fatal("only the explicit new message may call the model", calls.Load())
			}
		})
	}
}

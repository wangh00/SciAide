package bootstrap

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/id"
)

func TestResearchDebugLatestDiscussionFailureDoesNotHideBehindPriorPlan(t *testing.T) {
	base := newDebugPlanningModel(t)
	defer base.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("TRIGGER_POST_PLAN_QUOTA")) {
			emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 1, "[error: usage_limit_reached]", "", "")
			return
		}
		response, e := http.Post(base.URL+r.URL.Path, "application/json", bytes.NewReader(body))
		if e != nil {
			t.Error(e)
			http.Error(w, "fixture failed", 500)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()
	f := newDebugFixture(t, proxy.URL, modelprofile.ProtocolOpenAIChat, 40, true)
	rid, _ := id.New()
	c, _ := f.create(t, rid, true)
	f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "plan_available" })
	code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/message", map[string]any{"text": "TRIGGER_POST_PLAN_QUOTA"})
	if code != 200 {
		t.Fatalf("follow-up failed before model: %s", result["error"])
	}
	snapshot := f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "chat_failed" })
	if string(snapshot["plan"]) == "null" || string(snapshot["workflowState"]) != `"plan_available"` || string(snapshot["discussionState"]) != `"failed"` {
		t.Fatal("discussion failure hid the frozen plan, or the old plan hid the new failure")
	}
}

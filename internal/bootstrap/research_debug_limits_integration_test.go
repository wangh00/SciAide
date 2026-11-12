package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/debugapi"
	"github.com/wangh00/SciAide/internal/id"
)

func TestResearchDebugLimitsUpdateKeepsActiveRequestAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-release:
					emitQuestionHandoffFixture(t, w, protocol, 2, "Completed the same paid request.", "", "")
				case <-r.Context().Done():
				}
			}))
			defer model.Close()
			f := newDebugFixture(t, model.URL, protocol, 2, false)
			var unblock sync.Once
			defer unblock.Do(func() { close(release) })
			code, status := f.request(t, "GET", "/v1/status", nil)
			var session struct {
				ID                    string
				Revision              uint64
				ExpiresAt, HardStopAt time.Time
			}
			if json.Unmarshal(status["session"], &session) != nil || code != 200 || session.Revision != 1 || session.ExpiresAt.IsZero() {
				t.Fatal("debug API does not expose the active time-window identity")
			}
			rid, _ := id.New()
			c, _ := f.create(t, rid, false)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("fixture request never started")
			}
			deadline := session.ExpiresAt.Add(time.Minute)
			body := map[string]any{"sessionId": session.ID, "expectedRevision": session.Revision, "maxRequests": 4, "expiresAt": deadline, "confirmed": true}
			code, updated := f.request(t, "POST", "/v1/limits", body)
			if code != 200 {
				t.Fatal("authorized update could not be applied without restart", string(updated["error"]))
			}
			var next struct {
				Revision  uint64
				ExpiresAt time.Time
			}
			var budget struct {
				Enabled     bool
				Limit, Used int
			}
			_ = json.Unmarshal(updated["session"], &next)
			_ = json.Unmarshal(updated["modelCalls"], &budget)
			if next.Revision != 2 || !next.ExpiresAt.Equal(deadline) || !budget.Enabled || budget.Limit != 4 || budget.Used != 1 || calls.Load() != 1 {
				t.Fatal("limit update reset use, cancelled or reissued the active request", next, budget)
			}
			code, stale := f.request(t, "POST", "/v1/limits", body)
			if code != 400 || !bytesContainCode(stale["error"], "DEBUG_LIMITS_CONFLICT") {
				t.Fatal("a stale approval could overwrite a newer grant", code)
			}
			data, err := os.ReadFile(filepath.Join(f.root, "debug-connection.json"))
			var descriptor struct{ ExpiresAt time.Time }
			if err != nil || json.Unmarshal(data, &descriptor) != nil || !descriptor.ExpiresAt.Equal(deadline) {
				t.Fatal("connection metadata did not reflect the new deadline", err)
			}
			unblock.Do(func() { close(release) })
			final := f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "discussion_idle" })
			var finalBudget struct{ Limit, Used int }
			_ = json.Unmarshal(final["modelCalls"], &finalBudget)
			if calls.Load() != 1 || finalBudget.Limit != 4 || finalBudget.Used != 1 {
				t.Fatal("updating debug limits renewed or double-charged the request")
			}
		})
	}
}

func bytesContainCode(raw json.RawMessage, code string) bool {
	var err struct{ Code string }
	return json.Unmarshal(raw, &err) == nil && err.Code == code
}

func TestResearchDebugLimitsPriorSessionCannotBeReplayedAfterRestart(t *testing.T) {
	root := t.TempDir()
	f := openDebugFixture(t, root, "", "", "http://127.0.0.1:1", modelprofile.ProtocolOpenAIResponses, 4, false, false)
	_, status := f.request(t, "GET", "/v1/status", nil)
	var old debugapi.SessionState
	_ = json.Unmarshal(status["session"], &old)
	body := map[string]any{"sessionId": old.ID, "expectedRevision": old.Revision, "maxRequests": 8, "expiresAt": old.ExpiresAt, "confirmed": true}
	_ = f.server.Close()
	f = openDebugFixture(t, root, "", "", "http://127.0.0.1:1", modelprofile.ProtocolOpenAIResponses, 4, false, false)
	_, status = f.request(t, "GET", "/v1/status", nil)
	var current debugapi.SessionState
	_ = json.Unmarshal(status["session"], &current)
	if current.ID == "" || current.ID == old.ID || current.Revision != old.Revision {
		t.Fatal("restart did not create a distinct authorization session")
	}
	before, err := os.ReadFile(filepath.Join(root, "model-budget.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	code, v := f.request(t, "POST", "/v1/limits", body)
	if code != 400 || !bytesContainCode(v["error"], "DEBUG_LIMITS_CONFLICT") {
		t.Fatal("prior-process grant was replayed with the new token", code)
	}
	after, _ := os.ReadFile(filepath.Join(root, "model-budget.json"))
	if string(before) != string(after) {
		t.Fatal("replayed old grant wrote durable budget")
	}
	body["sessionId"] = current.ID
	body["expiresAt"] = current.ExpiresAt
	code, v = f.request(t, "POST", "/v1/limits", body)
	var budget debugapi.BudgetState
	_ = json.Unmarshal(v["modelCalls"], &budget)
	if code != 200 || budget.Enabled || budget.Limit != 8 || budget.Used != 0 {
		t.Fatal("explicit current-session update enabled IO or reset use", code, budget)
	}
}

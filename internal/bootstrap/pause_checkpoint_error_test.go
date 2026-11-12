package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/id"
)

func TestPauseRunningReportsCheckpointBoundaryWithoutMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIResponses, 1, "Fixture discussion only.", "", "")
	}))
	defer server.Close()
	f := newDebugFixture(t, server.URL, modelprofile.ProtocolOpenAIResponses, 2, false)
	rid, _ := id.New()
	c, _ := f.create(t, rid, false)
	f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "discussion_idle" })
	// Establish just the guard's precondition in an owned temporary database;
	// no active model or user task is interrupted by this fixture.
	if _, err := f.app.store.DB().Exec(`UPDATE workflow_runs SET status='running',completed_at=NULL WHERE id=?`, c.StarterRunID); err != nil {
		t.Fatal(err)
	}
	code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/pause", map[string]any{"runId": c.StarterRunID})
	var failure struct {
		Code      string
		Retryable bool
	}
	if err := json.Unmarshal(result["error"], &failure); err != nil || code != http.StatusBadRequest || failure.Code != "WORKFLOW_PAUSE_REQUIRES_CHECKPOINT" || failure.Retryable {
		t.Fatal("checkpoint restriction returned a generic/retryable failure", code, string(result["error"]))
	}
	var status string
	if err := f.app.store.DB().QueryRow(`SELECT status FROM workflow_runs WHERE id=?`, c.StarterRunID).Scan(&status); err != nil || status != "running" {
		t.Fatal("pause cancelled or rewound current execution", status, err)
	}
}

package bootstrap

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
)

func TestResearchDebugDeliveredFollowupAndExplicitRevision(t *testing.T) {
	base := newDebugPlanningModel(t)
	defer base.Close()
	var followups atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"name":"research_revision_propose"`)) {
			followups.Add(1)
			if !bytes.Contains(body, []byte("Pending revision proposal saved")) {
				if !bytes.Contains(body, []byte("shorten-the-delivered-report")) {
					t.Error("follow-up lost current task text")
				}
				emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 1, "", "research_revision_propose", `{"nodeId":"research_design","summary":"Shorter reader-facing design","changes":["Keep the confirmed research requirements","Move detailed supporting tables into appendices"],"reason":"User requests concise main text without changing evidence or conclusions"}`)
			} else {
				emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 2, "Proposal saved; explicit confirmation is still required.", "", "")
			}
			return
		}
		response, err := http.Post(base.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture forwarding failed", 500)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer modelServer.Close()
	f := newDebugFixture(t, modelServer.URL, modelprofile.ProtocolOpenAIChat, 100, true)
	rid, _ := id.New()
	c, _ := f.create(t, rid, true)
	value := f.wait(t, c, func(v map[string]json.RawMessage) bool { return len(v["plan"]) > 0 && string(v["plan"]) != "null" })
	var plan workflow.ResearchStarterPlan
	var initial workflow.RunDetail
	_ = json.Unmarshal(value["plan"], &plan)
	_ = json.Unmarshal(value["workflow"], &initial)
	code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/adopt", map[string]any{"runId": initial.Run.ID, "routeId": plan.Routes[0].RouteID, "confirmExecution": true})
	if code != 200 {
		t.Fatal("adopt", code, string(result["error"]))
	}
	value = f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "workflow_completed" })
	var completed workflow.RunDetail
	_ = json.Unmarshal(value["workflow"], &completed)
	// The owned fixture has an older failed starter discussion. A newer human
	// follow-up must supersede it even after internal Workflow Runs are newer
	// still. A newest-Run-only filter incorrectly resurrects this stale failure.
	if _, err := f.app.store.DB().Exec(`UPDATE runs SET status='failed',error_code='OLD_STARTER_FAILURE',error_message='old fixture failure' WHERE id=?`, c.InitialRunID); err != nil {
		t.Fatal(err)
	}
	clientID, _ := id.New()
	code, result = f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/message", map[string]any{"text": "shorten-the-delivered-report; preserve all sources and scientific checks", "clientMessageId": clientID})
	if code != 200 {
		t.Fatal("message", code, string(result["error"]))
	}
	var conversationID string
	_ = json.Unmarshal(result["conversationId"], &conversationID)
	if conversationID != completed.Run.ConversationID || conversationID == c.ConversationID {
		t.Fatal("follow-up went back to the obsolete starter", conversationID)
	}
	value = f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "revision_available" })
	var offered workflow.RunDetail
	_ = json.Unmarshal(value["workflow"], &offered)
	if followups.Load() != 2 || offered.Run.Status != workflow.RunCompleted || len(offered.RevisionProposals) != 1 || !offered.RevisionProposals[0].CanConfirm {
		t.Fatal("proposal auto-executed/lost", followups.Load(), offered.Run.Status, len(offered.RevisionProposals))
	}
	proposal := offered.RevisionProposals[0]
	command := map[string]any{"runId": offered.Run.ID, "proposalId": proposal.ID, "confirmed": true}
	code, _ = f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/confirm-revision", command)
	if code == 200 {
		t.Fatal("revision executed without explicit execution acknowledgment")
	}
	command["confirmExecution"] = true
	command["projectId"] = "another-project"
	code, _ = f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/confirm-revision", command)
	if code == 200 {
		t.Fatal("cross-project confirmation accepted")
	}
	delete(command, "projectId")
	code, result = f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/confirm-revision", command)
	if code != 200 {
		t.Fatal("confirm", code, string(result["error"]))
	}
	value = f.wait(t, c, func(v map[string]json.RawMessage) bool {
		state := debugState(v)
		return state == "workflow_completed" || state == "workflow_failed"
	})
	var revised workflow.RunDetail
	_ = json.Unmarshal(value["workflow"], &revised)
	if revised.Run.ID != completed.Run.ID || revised.Run.Status != workflow.RunCompleted || len(revised.Run.Outputs) == 0 {
		t.Fatal("revision lost task or original gate", revised.Run.Status, revised.Run.ErrorCode)
	}
	for _, before := range completed.Steps {
		if before.NodeID == "research_design" {
			break
		}
		for _, after := range revised.Steps {
			if after.ID == before.ID && (after.Attempt != before.Attempt || string(after.Output) != string(before.Output)) {
				t.Fatal("report-only repair reran upstream", before.NodeID)
			}
		}
	}
}

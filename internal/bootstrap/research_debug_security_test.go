package bootstrap

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/debugapi"
	"github.com/wangh00/SciAide/internal/id"
)

func TestResearchDebugAuthBoundsAndCrossCaseActions(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 1, "plain fixture discussion", "", "")
	}))
	defer model.Close()
	f := newDebugFixture(t, model.URL, modelprofile.ProtocolOpenAIChat, 4, false)
	for _, mode := range []string{"no_token", "origin", "host", "content_type", "oversize", "unknown_field"} {
		t.Run(mode, func(t *testing.T) {
			method, path, body := "GET", "/v1/status", ""
			if mode == "content_type" || mode == "oversize" || mode == "unknown_field" {
				method, path = "POST", "/v1/cases"
				body = `{"unknown":true}`
			}
			if mode == "oversize" {
				body = `{"prompt":"` + strings.Repeat("x", (1<<20)+100) + `"}`
			}
			r, _ := http.NewRequest(method, f.server.URL()+path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+f.token)
			r.Header.Set("Content-Type", "application/json")
			switch mode {
			case "no_token":
				r.Header.Del("Authorization")
			case "origin":
				r.Header.Set("Origin", "https://untrusted.invalid")
			case "host":
				r.Host = "untrusted.invalid:1234"
			case "content_type":
				r.Header.Set("Content-Type", "text/plain")
			}
			response, e := http.DefaultClient.Do(r)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode < 400 {
				t.Fatal("unsafe request accepted", mode, response.StatusCode)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid HTTP request invoked model")
	}
	rid, _ := id.New()
	a, _ := f.create(t, rid, false)
	rid, _ = id.New()
	b, _ := f.create(t, rid, false)
	snap := f.wait(t, b, func(v map[string]json.RawMessage) bool {
		var chats []struct{ Run struct{ ID, Status string } }
		_ = json.Unmarshal(v["chats"], &chats)
		return len(chats) > 0 && chats[0].Run.Status == "completed"
	})
	var chats []struct{ Run struct{ ID string } }
	_ = json.Unmarshal(snap["chats"], &chats)
	code, _ := f.request(t, "POST", "/v1/cases/"+a.ID+"/actions/cancel-chat", map[string]any{"runId": chats[0].Run.ID})
	if code == 200 {
		t.Fatal("cross-case chat mutation allowed")
	}
	code, _ = f.request(t, "POST", "/v1/cases/"+a.ID+"/actions/retry", map[string]any{"runId": b.StarterRunID, "stepId": "none"})
	if code == 200 {
		t.Fatal("cross-case workflow mutation allowed")
	}
	code, _ = f.request(t, "POST", "/v1/cases/"+a.ID+"/actions/adopt", map[string]any{"runId": a.StarterRunID, "routeId": "none", "confirmExecution": true})
	if code == 200 {
		t.Fatal("adopt bypassed full-access server flag")
	}
	code, _ = f.request(t, "POST", "/v1/cases/"+a.ID+"/actions/start-route", map[string]any{"starterRunId": a.StarterRunID, "routeId": "none", "permissionMode": "full_access"})
	if code == 200 {
		t.Fatal("start route bypassed full-access flag")
	}
}

func TestResearchDebugWorkflowApprovalDenyWithoutExecutingPython(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 1, "fixture discussion", "", "")
	}))
	defer model.Close()
	f := newDebugFixture(t, model.URL, modelprofile.ProtocolOpenAIChat, 3, false)
	rid, _ := id.New()
	c, _ := f.create(t, rid, false)
	saved, e := f.app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: c.ProjectID, Definition: workflow.Definition{SchemaVersion: 1, Name: "pending approval", Nodes: []workflow.Node{{ID: "python", Name: "must never execute", Kind: workflow.NodeTool, ToolName: "builtin.python.kernel.execute", Arguments: json.RawMessage(`{"code":"raise RuntimeError('must not run')"}`)}}}})
	if e != nil {
		t.Fatal(e)
	}
	start, e := f.app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: c.ProjectID, ResearchTaskID: c.TaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{}`), PermissionMode: conversation.PermissionPlan, ModelProfileID: f.profileID, ModelID: f.modelID})
	if e != nil {
		t.Fatal(e)
	}
	detail := waitForWorkflowState(t, f.app, c.ProjectID, start.Run.ID, workflow.RunWaitingApproval, 10*time.Second)
	if len(detail.PendingApprovals) != 1 {
		t.Fatal("no actual workflow approval", detail.PendingApprovals)
	}
	code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/approve-workflow", map[string]any{"runId": start.Run.ID, "approvalId": detail.PendingApprovals[0].ID, "allow": false})
	if code != 200 {
		t.Fatalf("workflow approval could not be denied: %s", result["error"])
	}
	var statuses string
	_ = f.app.store.DB().QueryRow(`SELECT status FROM tool_calls WHERE workflow_run_id=? LIMIT 1`, start.Run.ID).Scan(&statuses)
	if statuses == "completed" || statuses == "running" {
		t.Fatal("denied Python executed", statuses)
	}
}

func TestResearchDebugSlowBodyShutdownKeepsRootLockedAndCannotInvokeModel(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer model.Close()
	f := newDebugFixture(t, model.URL, modelprofile.ProtocolOpenAIChat, 2, false)
	u, _ := url.Parse(f.server.URL())
	connection, e := net.Dial("tcp4", u.Host)
	if e != nil {
		t.Fatal(e)
	}
	defer connection.Close()
	_, e = fmt.Fprintf(connection, "POST /v1/cases HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 100000\r\n\r\n{", u.Host, f.token)
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	closed := make(chan struct{})
	go func() { _ = f.server.Close(); close(closed) }()
	time.Sleep(100 * time.Millisecond)
	if space, e := debugapi.OpenWorkspace(f.root, "", true, 2); e == nil {
		_ = space.Close()
		t.Fatal("root released while slow handler still active")
	}
	select {
	case <-closed:
	case <-time.After(8 * time.Second):
		t.Fatal("shutdown did not force-close and join slow handler")
	}
	if calls.Load() != 0 {
		t.Fatal("slow request reached model during shutdown")
	}
	if _, e = os.Stat(filepath.Join(f.root, "debug-connection.json")); !os.IsNotExist(e) {
		t.Fatal("live token descriptor retained after shutdown")
	}
	if e = f.app.store.DB().Ping(); e == nil {
		t.Fatal("application DB was not closed")
	}
	space, e := debugapi.OpenWorkspace(f.root, "", true, 2)
	if e != nil {
		t.Fatal("root not reusable after joined shutdown", e)
	}
	_ = space.Close()
}

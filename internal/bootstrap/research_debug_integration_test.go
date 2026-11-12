package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

type debugFixture struct {
	app                             *Application
	server                          *debugapi.Server
	root, token, profileID, modelID string
}

func newDebugFixture(t *testing.T, modelURL string, protocol modelprofile.APIProtocol, limit int, full bool, configures ...func(*Options)) *debugFixture {
	t.Helper()
	return openDebugFixture(t, t.TempDir(), "", "", modelURL, protocol, limit, full, true, configures...)
}

func openDebugFixture(t *testing.T, root, source, profileID, modelURL string, protocol modelprofile.APIProtocol, limit int, full, enabled bool, configures ...func(*Options)) *debugFixture {
	t.Helper()
	space, e := debugapi.OpenWorkspace(root, source, enabled, limit, full)
	if e != nil {
		t.Fatal(e)
	}
	options := Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, ModelRequestGate: space.Gate, ModelProfileOverlay: space.ProfileOverlay, DisableAuxiliaryModels: true}
	for _, configure := range configures {
		configure(&options)
	}
	app, e := New(options)
	if e != nil {
		_ = space.Close()
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	app.Startup(ctx)
	if source == "" {
		profile, e := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "debug fixture", BaseURL: modelURL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 272000}}, Enabled: true, IsDefault: true, TimeoutSeconds: 10})
		if e != nil {
			cancel()
			_ = app.Close()
			_ = space.Close()
			t.Fatal(e)
		}
		profileID = profile.ID
	}
	server, e := debugapi.Start(ctx, debugapi.Services{Projects: app.ProjectFacade, Conversations: app.ConversationFacade, Chat: app.ChatFacade, Workflow: app.WorkflowFacade, Models: app.ModelFacade, Permissions: app.PermissionFacade}, space, debugapi.Options{MaxDuration: time.Minute, AllowFullAccess: full, OnStop: func() { app.BeginShutdown(); cancel() }, OnClose: app.Close, OnError: func(e error) { t.Logf("local fixture error: %v", e) }})
	if e != nil {
		cancel()
		_ = app.Close()
		_ = space.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = server.Close() })
	data, e := os.ReadFile(filepath.Join(root, "debug-connection.json"))
	if e != nil {
		t.Fatal(e)
	}
	var connection struct{ Token string }
	_ = json.Unmarshal(data, &connection)
	return &debugFixture{app, server, root, connection.Token, profileID, workflowAITestModelID}
}
func (f *debugFixture) request(t *testing.T, method, path string, body any) (int, map[string]json.RawMessage) {
	t.Helper()
	data, _ := json.Marshal(body)
	r, e := http.NewRequest(method, f.server.URL()+path, bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+f.token)
	if method == "POST" {
		r.Header.Set("Content-Type", "application/json")
	}
	response, e := (&http.Client{Timeout: 8 * time.Second}).Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var result map[string]json.RawMessage
	_ = json.Unmarshal(raw, &result)
	if result == nil {
		t.Fatalf("unexpected non-JSON response: code=%d body=%s", response.StatusCode, raw)
	}
	return response.StatusCode, result
}
func (f *debugFixture) create(t *testing.T, requestID string, full bool) (debugapi.Case, map[string]any) {
	t.Helper()
	body := map[string]any{"name": "bounded flow", "prompt": "Test an isolated bounded research case; do not invent evidence.", "modelProfileId": f.profileID, "modelId": f.modelID, "requestId": requestID}
	if full {
		body["permissionMode"] = "full_access"
	}
	code, result := f.request(t, "POST", "/v1/cases", body)
	if code != 200 {
		t.Fatalf("create failed: %s", result["error"])
	}
	var c debugapi.Case
	_ = json.Unmarshal(result["case"], &c)
	return c, body
}
func (f *debugFixture) wait(t *testing.T, c debugapi.Case, predicate func(map[string]json.RawMessage) bool) map[string]json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last map[string]json.RawMessage
	for time.Now().Before(deadline) {
		code, result := f.request(t, "GET", "/v1/cases/"+c.ID, nil)
		if code != 200 {
			t.Fatalf("snapshot failed: %s", result["error"])
		}
		last = result
		if predicate(result) {
			return result
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("debug flow timed out, state=%s", last["state"])
	return nil
}
func debugState(value map[string]json.RawMessage) string {
	var s string
	_ = json.Unmarshal(value["state"], &s)
	return s
}

func TestResearchDebugHTTPQuotaAndQuestionAllProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []string{"quota", "question"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				var count atomic.Int32
				modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					n := int(count.Add(1))
					if mode == "quota" {
						emitQuestionHandoffFixture(t, w, protocol, n, "[error: usage_limit_reached]", "", "")
						return
					}
					if n == 1 {
						emitQuestionHandoffFixture(t, w, protocol, n, "", "user_input_request", `{"questions":[{"id":"scope","question":"Which scope?","required":true,"options":[{"id":"bounded","label":"Bounded"},{"id":"broad","label":"Broad"}]}]}`)
						return
					}
					if !strings.Contains(string(body), "bounded") {
						t.Error("answer lost before model resumption")
					}
					emitQuestionHandoffFixture(t, w, protocol, n, "Fixture finished; no scientific claim of real evidence.", "", "")
				}))
				defer modelServer.Close()
				f := newDebugFixture(t, modelServer.URL, protocol, 4, false)
				rid, _ := id.New()
				c, body := f.create(t, rid, false)
				if mode == "quota" {
					snapshot := f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "chat_failed" })
					var chats []struct {
						Run struct{ Status, ErrorCode string }
					}
					_ = json.Unmarshal(snapshot["chats"], &chats)
					if len(chats) != 1 || chats[0].Run.Status != "failed" || chats[0].Run.ErrorCode != "MODEL_REQUEST_REJECTED" || count.Load() != 1 {
						t.Fatal("quota marked successful/retried", chats, count.Load())
					}
				} else {
					snapshot := f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "waiting_user_input" })
					var chats []struct {
						UserInput struct{ RunID, CallID string }
					}
					_ = json.Unmarshal(snapshot["chats"], &chats)
					code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/answer", map[string]any{"runId": chats[0].UserInput.RunID, "callId": chats[0].UserInput.CallID, "answers": map[string]any{"scope": map[string]any{"optionIds": []string{"bounded"}}}})
					if code != 200 {
						t.Fatalf("answer failed: %s", result["error"])
					}
					f.wait(t, c, func(v map[string]json.RawMessage) bool {
						var list []struct{ Run struct{ Status string } }
						_ = json.Unmarshal(v["chats"], &list)
						return len(list) > 0 && list[0].Run.Status == "completed"
					})
					if count.Load() != 2 {
						t.Fatal("resumption restarted/duplicated model requests", count.Load())
					}
				}
				// Retrying the same create request cannot charge a second initial call.
				code, result := f.request(t, "POST", "/v1/cases", body)
				if code != 200 || string(result["replayed"]) != "true" {
					t.Fatal("idempotent case replay failed", code)
				}
				before := count.Load()
				time.Sleep(40 * time.Millisecond)
				if count.Load() != before {
					t.Fatal("replayed case called model")
				}
				body["prompt"] = "different"
				code, _ = f.request(t, "POST", "/v1/cases", body)
				if code == 200 {
					t.Fatal("idempotency key aliased different prompt")
				}
			})
		}
	}
}

func TestResearchDebugFullServerDefaultsResearchCasesToFullAccess(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 1, "fixture research discussion", "", "")
	}))
	defer model.Close()
	f := newDebugFixture(t, model.URL, modelprofile.ProtocolOpenAIChat, 3, true)
	rid, _ := id.New()
	c, _ := f.create(t, rid, false) // no explicit mode: server policy chooses the research default
	snapshot := f.wait(t, c, func(v map[string]json.RawMessage) bool {
		var chats []struct{ Run struct{ Status string } }
		_ = json.Unmarshal(v["chats"], &chats)
		return len(chats) > 0 && chats[0].Run.Status == "completed"
	})
	var detail workflow.RunDetail
	if err := json.Unmarshal(snapshot["workflow"], &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Run.PermissionMode != conversation.PermissionFullAccess {
		t.Fatalf("full-access debug server defaulted research case to %s", detail.Run.PermissionMode)
	}
}

func newDebugPlanningModel(t *testing.T, mutations ...func(map[string]any)) *httptest.Server {
	t.Helper()
	base := newResearchStarterSkillTestServer(t, mutations...)
	t.Cleanup(base.Close)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if _, e := workflowAITestSchema(workflowAITestPrompt(body)); e == nil {
			response, e := http.Post(base.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
			if e != nil {
				t.Error(e)
				http.Error(w, "fixture failure", 500)
				return
			}
			defer response.Body.Close()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
			return
		}
		if !bytes.Contains(body, []byte(`"role":"tool"`)) {
			emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 1, "", "research_plan_prepare", `{"researchIdea":"Research a bounded methodology design; no invented data."}`)
			return
		}
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 2, "The real planner returned a route; adopt explicitly.", "", "")
	}))
	return modelServer
}

func TestResearchDebugNaturalPlanningAdoptionAndWorkflow(t *testing.T) {
	modelServer := newDebugPlanningModel(t)
	defer modelServer.Close()
	f := newDebugFixture(t, modelServer.URL, modelprofile.ProtocolOpenAIChat, 80, true)
	rid, _ := id.New()
	c, _ := f.create(t, rid, true)
	value := f.wait(t, c, func(v map[string]json.RawMessage) bool { return string(v["plan"]) != "null" && len(v["plan"]) > 0 })
	var plan workflow.ResearchStarterPlan
	_ = json.Unmarshal(value["plan"], &plan)
	var current struct{ Run struct{ ID string } }
	_ = json.Unmarshal(value["workflow"], &current)
	if len(plan.Routes) == 0 {
		t.Fatal("no host-validated route")
	}
	code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/adopt", map[string]any{"runId": current.Run.ID, "routeId": plan.Routes[0].RouteID})
	if code == 200 {
		t.Fatal("route executed without explicit confirmation")
	}
	code, result = f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/adopt", map[string]any{"runId": current.Run.ID, "routeId": plan.Routes[0].RouteID, "confirmExecution": true})
	if code != 200 {
		t.Fatalf("adopt failed: %s", result["error"])
	}
	final := f.wait(t, c, func(v map[string]json.RawMessage) bool {
		state := debugState(v)
		return state == "workflow_completed" || state == "workflow_failed"
	})
	if debugState(final) != "workflow_completed" {
		var d workflow.RunDetail
		_ = json.Unmarshal(final["workflow"], &d)
		t.Fatalf("formal flow failed: %s %s", d.Run.ErrorCode, d.Run.ErrorMessage)
	}
	var d workflow.RunDetail
	_ = json.Unmarshal(final["workflow"], &d)
	if d.Run.ResearchTaskID != c.TaskID || len(d.AIExecutions) == 0 {
		t.Fatal("adoption lost original task or bypassed real AI stages")
	}
}

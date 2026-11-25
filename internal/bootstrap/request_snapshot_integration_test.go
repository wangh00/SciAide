package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/modelutil"
	w "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestRequestSnapshotsFollowOriginalToolLoopAndSQLiteRestart(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var mu sync.Mutex
			var wire [][]byte
			var records []modelutil.DebugRequestSnapshot
			server := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				wire = append(wire, body)
				n := len(wire)
				mu.Unlock()
				name, args := "", "{}"
				if n == 1 {
					name, args = "workspace_list", `{"path":".","limit":1}`
				}
				if n > 2 {
					t.Error("unexpected model retry")
				}
				emitContextUsageFixtureWithNativeSize(t, wr, protocol, n, name, args, 1000, 64)
			}))
			defer server.Close()
			observer, err := modelutil.NewRequestSnapshotObserver(func(v modelutil.DebugRequestSnapshot) { mu.Lock(); defer mu.Unlock(); records = append(records, v) })
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			admissions := 0
			app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, DisableAuxiliaryModels: true, DebugRequestSnapshotObserver: observer, ModelRequestGate: func(context.Context) error { mu.Lock(); admissions++; mu.Unlock(); return nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			app.Startup(context.Background())
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "snapshot fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: "fixture-model", Models: []modelprofile.ProfileModel{{ID: "fixture-model", Enabled: true, IsDefault: true, ContextWindowTokens: 64000, AutoCompactTokenLimit: 57600, ContextWindowSource: "manual"}}, Enabled: true, IsDefault: true, TimeoutSeconds: 10})
			if err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(w.CreateProjectRequest{Name: "isolated request inventory"})
			if err != nil {
				t.Fatal(err)
			}
			conv, err := app.ConversationFacade.CreateConversation(w.CreateConversationRequest{ProjectID: project.ID, ModelProfileID: profile.ID, ModelID: "fixture-model"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = app.ConversationFacade.SetPermissionMode(conv.ID, "full_access"); err != nil {
				t.Fatal(err)
			}
			run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: conv.ID, ModelProfileID: profile.ID, ModelID: "fixture-model", Text: "PRIVATE_USER_TEXT 中文 🧪: list this isolated directory once, then stop; do not create research or execute commands."})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				snapshot, e := app.ChatFacade.GetRunSnapshot(run.ID)
				if e != nil {
					t.Fatal(e)
				}
				if snapshot.Run.Status == chat.RunCompleted {
					if len(snapshot.ToolCalls) != 1 || snapshot.ToolCalls[0].Result == nil || snapshot.ToolCalls[0].Result.Status != tool.ResultSuccess {
						t.Fatal("original tool did not complete")
					}
					break
				}
				if snapshot.Run.Status == chat.RunFailed || time.Now().After(deadline) {
					t.Fatal("original tool loop stopped", snapshot.Run.Status, snapshot.Run.ErrorCode, snapshot.Run.ErrorMessage)
				}
				time.Sleep(10 * time.Millisecond)
			}
			mu.Lock()
			captured := append([]modelutil.DebugRequestSnapshot(nil), records...)
			bodies := append([][]byte(nil), wire...)
			calls := admissions
			mu.Unlock()
			if len(captured) != 2 || len(bodies) != 2 || calls != 2 {
				t.Fatal("request snapshots added, lost or merged actual HTTP", len(captured), len(bodies), calls)
			}
			for i, v := range captured {
				if v.RunID != run.ID || v.TurnIndex != i+1 || v.Kind != "conversation" || v.BodyBytes != len(bodies[i]) || v.Inspection != "complete" || v.HTTPStatus != 200 || v.ConfiguredBudget == nil || v.ConfiguredBudget.EffectiveTokens != 60800 {
					t.Fatal("final HTTP was not bound to the original Run/budget", v)
				}
				var body map[string]json.RawMessage
				_ = json.Unmarshal(bodies[i], &body)
				var definitions []json.RawMessage
				_ = json.Unmarshal(body["tools"], &definitions)
				if v.ToolCount != len(definitions) {
					t.Fatal("wire tool definitions mismatch")
				}
				encoded, _ := json.Marshal(v)
				if strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(string(encoded), root) || strings.Contains(string(encoded), server.URL) {
					t.Fatal("safe inventory leaked source")
				}
			}
			if captured[1].Delta == nil || captured[1].Delta.PreviousSequence != captured[0].Sequence || captured[1].Delta.InputCountDelta < 1 {
				t.Fatal("same-Run native/tool history growth was not compared", captured[1].Delta)
			}
			if err = app.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, DisableAuxiliaryModels: true})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.Startup(context.Background())
			after, err := reopened.ChatFacade.GetRunSnapshot(run.ID)
			if err != nil || after.Run.Status != chat.RunCompleted || len(after.ToolCalls) != 1 {
				t.Fatal("read-only restart changed original flow", err)
			}
			var usageRows int
			if err = reopened.store.DB().QueryRow(`SELECT COUNT(*) FROM model_request_usage WHERE run_id=?`, run.ID).Scan(&usageRows); err != nil || usageRows != 2 {
				t.Fatal("usage ledger mismatch", err, usageRows)
			}
			mu.Lock()
			afterCalls := len(wire)
			mu.Unlock()
			if afterCalls != 2 {
				t.Fatal("restart issued another HTTP")
			}
			t.Logf("%s: original tool loop 2 HTTP/2 snapshots, 1 tool; input items %d -> %d; original usage/restart retained", protocol, captured[0].InputCount, captured[1].InputCount)
		})
	}
}

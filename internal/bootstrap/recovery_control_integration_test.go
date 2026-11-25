package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/modelutil"
	w "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestRecoveryControlAuditPersistsAcrossCancelShutdownAndRestart(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, action := range []string{"cancel", "shutdown"} {
			t.Run(string(protocol)+"/"+action, func(t *testing.T) {
				started, disconnected := make(chan struct{}, 1), make(chan struct{}, 1)
				release := make(chan struct{})
				var received, admissions atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
					if received.Add(1) != 1 {
						http.Error(wr, "unexpected retry", http.StatusInternalServerError)
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					started <- struct{}{}
					select {
					case <-r.Context().Done():
						disconnected <- struct{}{}
					case <-release:
					}
				}))
				defer server.Close()
				defer close(release)
				gate := func(context.Context) error { admissions.Add(1); return nil }
				root := t.TempDir()
				var observed []modelutil.DebugRequestSnapshot
				observer, err := modelutil.NewRequestSnapshotObserver(func(v modelutil.DebugRequestSnapshot) { observed = append(observed, v) })
				if err != nil {
					t.Fatal(err)
				}
				app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, ModelRequestGate: gate, DisableAuxiliaryModels: true, DebugRequestSnapshotObserver: observer})
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				app.Startup(context.Background())
				profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{
					Name: "control audit fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol,
					ModelID: "fixture-model", Models: []modelprofile.ProfileModel{{ID: "fixture-model", Enabled: true, IsDefault: true, ContextWindowTokens: 64000}},
					Enabled: true, IsDefault: true, TimeoutSeconds: 20,
				})
				if err != nil {
					t.Fatal(err)
				}
				project, err := app.ProjectFacade.CreateProject(w.CreateProjectRequest{Name: "isolated cancellation audit"})
				if err != nil {
					t.Fatal(err)
				}
				conv, err := app.ConversationFacade.CreateConversation(w.CreateConversationRequest{ProjectID: project.ID, ModelProfileID: profile.ID, ModelID: "fixture-model"})
				if err != nil {
					t.Fatal(err)
				}
				run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: conv.ID, ModelProfileID: profile.ID, ModelID: "fixture-model", Text: "Synthetic fixture: wait for an explicit stop; do not execute any tools."})
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("original provider adapter never sent its request")
				}
				if action == "cancel" {
					if err = app.ChatFacade.CancelRun(run.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					app.BeginShutdown()
				}
				select {
				case <-disconnected:
				case <-time.After(5 * time.Second):
					t.Fatal("host control did not close the actual provider request")
				}
				if err = app.Close(); err != nil {
					t.Fatal(err)
				}
				if len(observed) != 1 {
					t.Fatal("snapshot lost/duplicated the real cancelled transport attempt", len(observed))
				}
				if v := observed[0]; v.RunID != run.ID || v.Outcome != "context_canceled" || v.Inspection != "complete" || v.HTTPStatus != 0 {
					t.Fatalf("cancelled transport snapshot: runMatches=%t outcome=%s inspection=%s status=%d", v.RunID == run.ID, v.Outcome, v.Inspection, v.HTTPStatus)
				}
				reopened, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}, ModelRequestGate: gate, DisableAuxiliaryModels: true})
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				reopened.Startup(context.Background())
				snapshot, err := reopened.ChatFacade.GetRunSnapshot(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if action == "cancel" && snapshot.Run.Status != chat.RunCancelled || action == "shutdown" && (snapshot.Run.Status != chat.RunInterrupted || snapshot.Run.ErrorCode != "APP_CLOSING") {
					t.Fatal("control changed the original terminal contract", snapshot.Run.Status, snapshot.Run.ErrorCode)
				}
				rows, err := reopened.store.DB().Query(`SELECT payload_json FROM run_events WHERE aggregate_id=? AND event_type='run.model_attempt_failed' ORDER BY sequence`, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				var audits []chat.ModelRecoveryFailure
				for rows.Next() {
					var raw []byte
					var audit chat.ModelRecoveryFailure
					if err = rows.Scan(&raw); err != nil {
						t.Fatal(err)
					}
					if err = json.Unmarshal(raw, &audit); err != nil {
						t.Fatal(err)
					}
					audits = append(audits, audit)
				}
				err = rows.Err()
				_ = rows.Close()
				if err != nil || len(audits) != 1 {
					t.Fatal("audit missing or duplicated across restart", len(audits), err)
				}
				audit := audits[0]
				_, raw, _ := strings.Cut(audit.Details, "\nProvider payload:\n")
				var fields map[string]any
				if json.Unmarshal([]byte(raw), &fields) != nil || audit.Code != "REQUEST_INTERRUPTED" || fields["source"] != "host_control" || fields["reason"] != "context_canceled" || strings.Contains(audit.Details, "jsonError") || strings.Contains(audit.Details, "payloadSha256") {
					t.Fatal("persisted cancellation pretended to be a provider decode failure", audit.Code, fields)
				}
				var retries, input int
				if err = reopened.store.DB().QueryRow(`SELECT COUNT(*) FROM run_events WHERE aggregate_id=? AND event_type='run.retrying'`, run.ID).Scan(&retries); err != nil {
					t.Fatal(err)
				}
				if err = reopened.store.DB().QueryRow(`SELECT COALESCE(SUM(input_tokens),0) FROM model_request_usage WHERE run_id=?`, run.ID).Scan(&input); err != nil {
					t.Fatal(err)
				}
				if admissions.Load() != 1 || received.Load() != 1 || retries != 0 || input != 0 || len(snapshot.ToolCalls) != 0 {
					t.Fatal("cancel/reopen reissued HTTP, invented usage or executed a tool", admissions.Load(), received.Load(), retries, input)
				}
				t.Logf("persisted audit: code=%s source=%s reason=%s; original run=%s/%s; 1 localhost HTTP, 0 retries/tools; reopened without another request", audit.Code, fields["source"], fields["reason"], snapshot.Run.Status, snapshot.Run.ErrorCode)
			})
		}
	}
}

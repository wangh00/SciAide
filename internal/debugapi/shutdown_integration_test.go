package debugapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/bootstrap"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

type debugQuietPublisher struct{}

func (debugQuietPublisher) Publish(context.Context, events.Envelope) {}

// Unlike the clock-level test, this observes the actual provider connection
// owned by a detached Chat Run, through each real protocol adapter.
func TestDebugStopCancelsRealProviderBeforeBlockedPersistenceDrains(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, trigger := range []string{"expiry", "close"} {
			for _, phase := range []string{"gate-budget", "limits-budget", "limits-descriptor"} {
				t.Run(string(protocol)+"/"+trigger+"/"+phase, func(t *testing.T) {
					started, providerCancelled := make(chan struct{}), make(chan struct{})
					var calls atomic.Int32
					model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if calls.Add(1) != 1 {
							t.Error("stop reissued a model request")
							http.Error(w, "unexpected request", 500)
							return
						}
						_, _ = io.Copy(io.Discard, r.Body)
						close(started)
						<-r.Context().Done()
						close(providerCancelled)
					}))
					defer model.Close()
					space, err := OpenWorkspace(t.TempDir(), "", true, 10)
					if err != nil {
						t.Fatal(err)
					}
					defer space.Close()
					clock := &grantClock{base: time.Now()}
					space.budget.clock = clock.now
					app, err := bootstrap.New(bootstrap.Options{RootDir: space.Root, EventPublisher: debugQuietPublisher{}, ModelRequestGate: space.Gate, DisableAuxiliaryModels: true})
					if err != nil {
						t.Fatal(err)
					}
					defer app.Close()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					app.Startup(ctx)
					profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "stop fixture", BaseURL: model.URL + "/v1", APIProtocol: protocol, ModelID: "fixture-model", Models: []modelprofile.ProfileModel{{ID: "fixture-model", Enabled: true, IsDefault: true, ContextWindowTokens: 272000}}, Enabled: true, IsDefault: true, TimeoutSeconds: 20})
					if err != nil {
						t.Fatal(err)
					}
					server, err := Start(ctx, Services{Projects: app.ProjectFacade, Conversations: app.ConversationFacade, Chat: app.ChatFacade, Workflow: app.WorkflowFacade, Models: app.ModelFacade, Permissions: app.PermissionFacade}, space, Options{MaxDuration: time.Minute, OnStop: func() { app.BeginShutdown(); cancel() }, OnClose: app.Close})
					if err != nil {
						t.Fatal(err)
					}
					defer server.Close()
					rid, _ := id.New()
					body, _ := json.Marshal(map[string]any{"name": "stop fixture", "prompt": "Plan a bounded study without inventing evidence.", "modelProfileId": profile.ID, "modelId": "fixture-model", "requestId": rid})
					req, _ := http.NewRequest("POST", server.URL()+"/v1/cases", bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Authorization", "Bearer "+server.token)
					response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if response.StatusCode != 200 {
						t.Fatal("case did not start", response.StatusCode)
					}
					select {
					case <-started:
					case <-time.After(5 * time.Second):
						t.Fatal("provider request never started")
					}
					entered, release := make(chan struct{}), make(chan struct{})
					var unblock sync.Once
					defer unblock.Do(func() { close(release) })
					space.budget.mu.Lock()
					space.budget.writeJSON = func(path string, data []byte) error {
						if (filepath.Base(path) == "debug-connection.json") == (phase == "limits-descriptor") {
							close(entered)
							<-release
						}
						return atomicJSON(path, data)
					}
					space.budget.mu.Unlock()
					written := make(chan struct{})
					go func() {
						if phase == "gate-budget" {
							_ = space.Gate(context.Background())
						} else {
							_, _ = server.updateLimits(LimitsCommand{SessionID: server.sessionID, ExpectedRevision: 1, MaxRequests: 20, ExpiresAt: clock.now().wall.Add(10 * time.Minute), Confirmed: true})
						}
						close(written)
					}()
					select {
					case <-entered:
					case <-time.After(2 * time.Second):
						t.Fatal("persistence did not block")
					}
					closed := make(chan struct{})
					go func() {
						if trigger == "expiry" {
							clock.advance(11*time.Minute, 0)
							server.expire()
						} else {
							_ = server.Close()
						}
						close(closed)
					}()
					select {
					case <-providerCancelled:
					case <-time.After(2 * time.Second):
						t.Fatal("real provider stream survived stop while fsync was blocked")
					}
					select {
					case <-server.Done():
						t.Fatal("workspace released before writer drain")
					default:
					}
					if second, err := OpenWorkspace(space.Root, "", false, 10); err == nil {
						_ = second.Close()
						t.Fatal("blocked writer lost exclusive workspace ownership")
					}
					unblock.Do(func() { close(release) })
					select {
					case <-written:
					case <-time.After(3 * time.Second):
						t.Fatal("writer failed to drain")
					}
					select {
					case <-closed:
					case <-time.After(3 * time.Second):
						t.Fatal("application failed to drain after cancellation")
					}
					if space.budget.snapshot().Used != map[bool]int{true: 2, false: 1}[phase == "gate-budget"] {
						t.Fatal("stop refunded or reissued a model request")
					}
					second, err := OpenWorkspace(space.Root, "", false, 10)
					if err != nil {
						t.Fatal("drained shutdown did not release workspace", err)
					}
					if second.budget.snapshot().Used != space.budget.snapshot().Used || calls.Load() != 1 {
						t.Fatal("reopening refunded use or repeated provider request")
					}
					_ = second.Close()
				})
			}
		}
	}
}

// Development entry, intentionally not started by the desktop application.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/wangh00/SciAide/internal/bootstrap"
	"github.com/wangh00/SciAide/internal/debugapi"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/model/responses"
)

type quietPublisher struct{}

func (quietPublisher) Publish(context.Context, events.Envelope) {}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "research-debug:", err)
		os.Exit(1)
	}
}
func run() error {
	root := flag.String("root", "", "required isolated, empty or debug-owned workspace (never formal data)")
	source := flag.String("inherit-models-from", "", "optional app data root: read-only model source; settings and credentials stay in memory")
	allow := flag.Bool("allow-model-calls", false, "explicitly permit possibly billable model calls")
	limit := flag.Int("max-requests", 12, "durable model HTTP request cap, including negotiation/retries/redirects; restarts do not refund")
	duration := flag.Duration("max-duration", time.Hour, "initial authorized window (max 1h); does not override an explicit deadline")
	deadlineText := flag.String("deadline", "", "optional absolute RFC3339 cutoff; reuse the same value across restarts")
	port := flag.Int("port", 0, "loopback port, 0 selects a random port")
	full := flag.Bool("allow-full-access", false, "allow explicitly requested full_access permission in isolated cases")
	upgradeFull := flag.Bool("upgrade-full-access", false, "explicitly upgrade an existing plan-owned debug root to full_access; requires --allow-full-access")
	captureErrors := flag.Bool("capture-provider-errors", false, "opt in to bounded local Responses error text, with configured key/header values redacted; never share the capture")
	captureFaults := flag.Bool("capture-stream-faults", false, "opt in to bounded sensitive malformed Responses event and wire-tail evidence; requires an access-restricted root (no ACL enforcement); never share the capture")
	captureHTTP := flag.Bool("capture-http-responses", false, "opt in to bounded private Responses status/allowlisted response-header evidence; requires an access-restricted root (no ACL enforcement); never share the capture")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	var deadline time.Time
	if *deadlineText != "" {
		var err error
		deadline, err = time.Parse(time.RFC3339, *deadlineText)
		if err != nil {
			return fmt.Errorf("deadline must be an RFC3339 timestamp")
		}
	}
	if err := debugapi.ValidateSessionWindow(context.Background(), *duration, deadline); err != nil {
		return err
	}
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("port must be 0..65535")
	}
	if *limit < 1 || *limit > 1000 {
		return fmt.Errorf("max-requests must be 1..1000")
	}
	if *upgradeFull && !*full {
		return fmt.Errorf("--upgrade-full-access requires --allow-full-access")
	}
	workspace, err := debugapi.OpenWorkspaceWithPolicy(*root, *source, *allow, *limit, *full, *upgradeFull)
	if err != nil {
		return err
	}
	defer workspace.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var observe func(responses.DebugProviderError)
	var observeFault func(responses.DebugStreamFault)
	var observeHTTP func(responses.DebugHTTPResponse)
	if *captureErrors {
		capture, e := openErrorCapture(workspace.Root)
		if e != nil {
			return e
		}
		defer capture.Close()
		observe = capture.record
	}
	if *captureFaults {
		capture, e := openStreamFaultCapture(workspace.Root)
		if e != nil {
			return e
		}
		defer capture.Close()
		observeFault = capture.recordStreamFault
	}
	if *captureHTTP {
		capture, e := openHTTPResponseCapture(workspace.Root)
		if e != nil {
			return e
		}
		defer capture.Close()
		observeHTTP = capture.recordHTTPResponse
	}
	app, err := bootstrap.New(bootstrap.Options{RootDir: workspace.Root, EventPublisher: quietPublisher{}, ModelRequestGate: workspace.Gate, ModelProfileOverlay: workspace.ProfileOverlay, DisableAuxiliaryModels: true, DebugResponseErrorObserver: observe, DebugResponseStreamFaultObserver: observeFault, DebugResponseHTTPObserver: observeHTTP})
	if err != nil {
		return err
	}
	defer app.Close()
	appCtx, stopApp := context.WithCancel(ctx)
	defer stopApp()
	app.Startup(appCtx)
	stopWork := func() { app.BeginShutdown(); stopApp() }
	server, err := debugapi.Start(ctx, debugapi.Services{Projects: app.ProjectFacade, Conversations: app.ConversationFacade, Chat: app.ChatFacade, Workflow: app.WorkflowFacade, Models: app.ModelFacade, Permissions: app.PermissionFacade}, workspace, debugapi.Options{Port: *port, MaxDuration: *duration, Deadline: deadline, AllowFullAccess: *full, OnStop: stopWork, OnClose: app.Close})
	if err != nil {
		return err
	}
	defer server.Close()
	// Never print tokens, keys, profiles or model response text. The descriptor
	// is local workspace state, not a log or public debug endpoint.
	fmt.Println("Research debug API:", server.URL())
	fmt.Println("Connection descriptor is in the isolated root: debug-connection.json (do not commit/share)")
	<-server.Done()
	return nil
}

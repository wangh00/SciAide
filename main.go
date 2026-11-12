package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/bootstrap"
	"github.com/wangh00/SciAide/internal/platform/desktopdiag"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	releaseInstance, primaryInstance, err := acquireApplicationInstance()
	if err != nil {
		reportStartupError(fmt.Errorf("acquire SciAide application instance: %w", err))
		os.Exit(1)
	}
	defer releaseInstance()
	if !primaryInstance {
		return
	}
	diagnostics, diagnosticErr := startDesktopDiagnostics()
	if diagnosticErr != nil {
		log.Printf("desktop diagnostics setup failed: %v", diagnosticErr)
	}
	// Do not defer Finish: panic unwinding must leave the runtime crash sink
	// installed until Go writes its fatal stack, even for a main-thread panic.
	err = runDesktop(diagnostics)
	diagnostics.Finish(err == nil)
	if err != nil {
		os.Exit(1)
	}
}

func runDesktop(diagnostics *desktopdiag.Session) error {
	application, err := bootstrap.New(bootstrap.Options{ConnectMCPOnStartup: true})
	if err != nil {
		diagnostics.Error("bootstrap", err)
		reportStartupError(fmt.Errorf("bootstrap SciAide: %w", err))
		return err
	}
	defer func() { diagnostics.Error("application_close", application.Close()) }()
	var recoveryMu sync.Mutex
	uiClosing := false
	stopUIRecovery := func() {}

	err = wails.Run(&options.App{
		Title:       "SciAide",
		Width:       1280,
		Height:      800,
		MinWidth:    960,
		MinHeight:   640,
		Frameless:   true,
		StartHidden: true,
		Logger:      desktopdiag.RuntimeLogger{Session: diagnostics},
		Windows: &windows.Options{
			Theme:                             windows.Light,
			DisableFramelessWindowDecorations: false,
			WindowClassName:                   singleInstanceWindowClass,
			WebviewUserDataPath:               webviewUserDataPath(os.Getenv("SCIAIDE_HOME")),
		},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		DragAndDrop: &options.DragAndDrop{EnableFileDrop: true},
		OnStartup: func(ctx context.Context) {
			application.Startup(ctx)
			applyInitialWindowSize(ctx)
			runtime.WindowShow(ctx)
			recoveryMu.Lock()
			if !uiClosing {
				stopUIRecovery = startUIRecovery(ctx, application.SystemFacade)
				diagnostics.Phase("running")
			}
			recoveryMu.Unlock()
		},
		OnBeforeClose: func(context.Context) bool { diagnostics.CloseRequested(); return false },
		OnShutdown: func(ctx context.Context) {
			diagnostics.Phase("shutdown_started")
			recoveryMu.Lock()
			uiClosing = true
			stop := stopUIRecovery
			recoveryMu.Unlock()
			stop()
			application.Shutdown(ctx)
			diagnostics.Phase("shutdown_completed")
		},
		Bind: []interface{}{
			application.SystemFacade,
			application.ProjectFacade,
			application.ProjectArchiveFacade,
			application.ConversationFacade,
			application.ModelFacade,
			application.ChatFacade,
			application.PermissionFacade,
			application.ToolFacade,
			application.MCPFacade,
			application.SkillFacade,
			application.AttachmentFacade,
			application.KnowledgeFacade,
			application.ArtifactFacade,
			application.ResearchFacade,
			application.PythonFacade,
			application.WorkflowFacade,
		},
	})
	diagnostics.Phase("event_loop_returned")
	if err != nil {
		// OnShutdown may already have closed the application logger.
		log.Printf("wails stopped: %v", err)
		diagnostics.Error("wails_run", err)
	}
	return err
}

// An explicitly isolated application home must also isolate browser state.
// The ordinary installation keeps Wails' existing default profile unchanged.
func webviewUserDataPath(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, "cache", "webview2")
}

const (
	singleInstanceMutexName   = `Local\SciAide.Desktop.SingleInstance.v1`
	singleInstanceWindowClass = "SciAideMainWindowV1"
	minimumWindowWidth        = 960
	minimumWindowHeight       = 640
	maximumWindowWidth        = 1920
	maximumWindowHeight       = 1200
)

func applyInitialWindowSize(ctx context.Context) {
	screens, err := runtime.ScreenGetAll(ctx)
	if err != nil || len(screens) == 0 {
		runtime.WindowCenter(ctx)
		return
	}
	selected := screens[0]
	for _, screen := range screens {
		if screen.IsCurrent {
			selected = screen
			break
		}
		if screen.IsPrimary {
			selected = screen
		}
	}
	width, height := selected.Size.Width, selected.Size.Height
	if width <= 0 || height <= 0 {
		width, height = selected.Width, selected.Height
	}
	windowWidth, windowHeight := initialWindowSize(width, height)
	runtime.WindowSetSize(ctx, windowWidth, windowHeight)
	runtime.WindowCenter(ctx)
}

func initialWindowSize(screenWidth, screenHeight int) (int, int) {
	if screenWidth <= 0 || screenHeight <= 0 {
		return 1280, 800
	}
	width := (screenWidth*2 + 1) / 3
	height := (screenHeight*20 + 13) / 27
	return min(max(width, minimumWindowWidth), maximumWindowWidth), min(max(height, minimumWindowHeight), maximumWindowHeight)
}

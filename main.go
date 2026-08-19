package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/bootstrap"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	application, err := bootstrap.New(bootstrap.Options{})
	if err != nil {
		log.Fatalf("bootstrap SciAide: %v", err)
	}
	defer application.Close()

	err = wails.Run(&options.App{
		Title:       "SciAide",
		Width:       1280,
		Height:      800,
		MinWidth:    960,
		MinHeight:   640,
		Frameless:   true,
		StartHidden: true,
		Windows: &windows.Options{
			Theme:                             windows.Light,
			DisableFramelessWindowDecorations: false,
		},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		DragAndDrop: &options.DragAndDrop{EnableFileDrop: true},
		OnStartup: func(ctx context.Context) {
			application.Startup(ctx)
			applyInitialWindowSize(ctx)
			runtime.WindowShow(ctx)
		},
		OnShutdown: application.Shutdown,
		Bind: []interface{}{
			application.SystemFacade,
			application.ProjectFacade,
			application.ConversationFacade,
			application.ModelFacade,
			application.ChatFacade,
			application.PermissionFacade,
			application.ToolFacade,
			application.MCPFacade,
			application.SkillFacade,
			application.AttachmentFacade,
			application.KnowledgeFacade,
		},
	})
	if err != nil {
		// OnShutdown may already have closed the application logger.
		log.Printf("wails stopped: %v", err)
	}
}

const (
	minimumWindowWidth  = 960
	minimumWindowHeight = 640
	maximumWindowWidth  = 1920
	maximumWindowHeight = 1200
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

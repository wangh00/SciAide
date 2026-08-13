package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
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
		Title:     "SciAide",
		Width:     1280,
		Height:    800,
		MinWidth:  960,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  application.Startup,
		OnShutdown: application.Shutdown,
		Bind: []interface{}{
			application.SystemFacade,
			application.ProjectFacade,
			application.ConversationFacade,
			application.ModelFacade,
			application.ChatFacade,
			application.PermissionFacade,
		},
	})
	if err != nil {
		// OnShutdown may already have closed the application logger.
		log.Printf("wails stopped: %v", err)
	}
}

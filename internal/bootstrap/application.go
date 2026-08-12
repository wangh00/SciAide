package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/observability"
	"github.com/wangh00/SciAide/internal/platform/appdirs"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

const Version = "0.1.0-dev"

type Options struct {
	RootDir string
}

type Application struct {
	Logger        *observability.Logger
	SystemFacade  *wailstransport.SystemFacade
	ProjectFacade *wailstransport.ProjectFacade

	lifecycle *wailstransport.LifecycleContext
	store     *sqlite.Store
	closeOnce sync.Once
	closeErr  error
}

func New(options Options) (*Application, error) {
	dirs, err := resolveDirs(options)
	if err != nil {
		return nil, err
	}
	if err := dirs.Ensure(); err != nil {
		return nil, err
	}

	logger, err := observability.NewLogger(dirs.Logs, slog.LevelInfo)
	if err != nil {
		return nil, fmt.Errorf("create logger: %w", err)
	}
	fail := func(err error) (*Application, error) {
		_ = logger.Close()
		return nil, err
	}

	store, err := sqlite.Open(context.Background(), filepath.Join(dirs.Data, "sciaide.db"))
	if err != nil {
		return fail(fmt.Errorf("open storage: %w", err))
	}
	lifecycle := wailstransport.NewLifecycleContext()
	projectService := project.NewService(sqlite.NewProjectRepository(store.DB()))
	return &Application{
		Logger:        logger,
		SystemFacade:  wailstransport.NewSystemFacade(Version),
		ProjectFacade: wailstransport.NewProjectFacade(lifecycle, projectService),
		lifecycle:     lifecycle,
		store:         store,
	}, nil
}

func (a *Application) Startup(ctx context.Context) {
	a.lifecycle.Set(ctx)
	a.Logger.InfoContext(ctx, "SciAide started", "version", Version)
}

func (a *Application) Shutdown(ctx context.Context) {
	a.Logger.InfoContext(ctx, "SciAide stopping")
	if err := a.Close(); err != nil {
		a.Logger.ErrorContext(ctx, "close SciAide", "error", err)
	}
}

func (a *Application) Close() error {
	a.closeOnce.Do(func() {
		if err := a.store.Close(); err != nil {
			a.closeErr = fmt.Errorf("close storage: %w", err)
		}
		if err := a.Logger.Close(); err != nil && a.closeErr == nil {
			a.closeErr = fmt.Errorf("close logger: %w", err)
		}
	})
	return a.closeErr
}

func resolveDirs(options Options) (appdirs.Dirs, error) {
	if options.RootDir != "" {
		return appdirs.ResolveUnder(options.RootDir), nil
	}
	if root := os.Getenv("SCIAIDE_HOME"); root != "" {
		return appdirs.ResolveUnder(root), nil
	}
	return appdirs.Resolve("SciAide")
}

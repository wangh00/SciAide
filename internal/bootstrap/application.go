package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model/gateway"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/observability"
	"github.com/wangh00/SciAide/internal/platform/appdirs"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

const Version = "0.2.0-dev"

type Options struct {
	RootDir string
}

type Application struct {
	Logger             *observability.Logger
	SystemFacade       *wailstransport.SystemFacade
	ProjectFacade      *wailstransport.ProjectFacade
	ConversationFacade *wailstransport.ConversationFacade
	ModelFacade        *wailstransport.ModelFacade
	ChatFacade         *wailstransport.ChatFacade
	PermissionFacade   *wailstransport.PermissionFacade

	lifecycle *wailstransport.LifecycleContext
	chat      *chat.Service
	store     *sqlite.Store
	closeOnce sync.Once
	closeErr  error
}

func New(options Options) (*Application, error) {
	dirs, err := resolveDirs(options)
	if err != nil {
		return nil, err
	}
	if options.RootDir == "" && os.Getenv("SCIAIDE_HOME") == "" {
		if _, err := appdirs.MigrateLegacy("SciAide", dirs); err != nil {
			return nil, fmt.Errorf("migrate legacy application data: %w", err)
		}
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
	projectService := project.NewService(sqlite.NewProjectRepository(store.DB()), dirs.Workspaces, dirs.Trash)
	if err := projectService.ReconcileWorkspacePaths(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("reconcile project workspaces: %w", err))
	}
	conversationRepository := sqlite.NewConversationRepository(store.DB())
	conversationService := conversation.NewService(conversationRepository)
	profileRepository := sqlite.NewModelProfileRepository(store.DB())
	secrets := secretstore.NewNative("SciAide")
	connectionTester := openai.New(modelprofile.Profile{TimeoutSeconds: 30}, nil)
	profileService := modelprofile.NewService(profileRepository, secrets, connectionTester)
	runRepository := sqlite.NewRunRepository(store.DB())
	toolRepository := sqlite.NewToolRepository(store.DB())
	permissionRepository := sqlite.NewPermissionRepository(store.DB())
	permissionEngine := permission.NewEngine(permissionRepository)
	toolService := tool.NewService(toolRepository, tool.JSONSchemaValidator{})
	approvalCoordinator := permission.NewCoordinator(permissionEngine, toolService, runRepository)
	publisher := wailstransport.NewEventPublisher(lifecycle)
	chatService := chat.NewService(runRepository, conversationRepository, runRepository, publisher, gateway.NewResolver(profileService))
	if expired, err := permissionEngine.Recover(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover pending approvals: %w", err))
	} else if expired > 0 {
		logger.Warn("expired pending approvals", "count", expired)
	}
	if interrupted, err := toolRepository.InterruptActive(context.Background(), time.Now().UTC()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover tool calls: %w", err))
	} else if interrupted > 0 {
		logger.Warn("interrupted unfinished tool calls", "count", interrupted)
	}
	if interrupted, err := chatService.Recover(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover chat runs: %w", err))
	} else if interrupted > 0 {
		logger.Warn("interrupted unfinished chat runs", "count", interrupted)
	}
	return &Application{
		Logger:             logger,
		SystemFacade:       wailstransport.NewSystemFacade(Version),
		ProjectFacade:      wailstransport.NewProjectFacade(lifecycle, projectService),
		ConversationFacade: wailstransport.NewConversationFacade(lifecycle, conversationService),
		ModelFacade:        wailstransport.NewModelFacade(lifecycle, profileService),
		ChatFacade:         wailstransport.NewChatFacade(lifecycle, chatService),
		PermissionFacade:   wailstransport.NewPermissionFacade(lifecycle, permissionEngine, approvalCoordinator),
		lifecycle:          lifecycle,
		chat:               chatService,
		store:              store,
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
		a.chat.Close()
		if _, err := sqlite.NewRunRepository(a.store.DB()).InterruptActive(context.Background(), time.Now().UTC()); err != nil {
			a.closeErr = fmt.Errorf("interrupt chat runs: %w", err)
		}
		if err := a.store.Close(); err != nil {
			if a.closeErr == nil {
				a.closeErr = fmt.Errorf("close storage: %w", err)
			}
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

package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/agent"
	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/embedding"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/mcpserver"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	mcpadapter "github.com/wangh00/SciAide/internal/mcp"
	"github.com/wangh00/SciAide/internal/model/gateway"
	"github.com/wangh00/SciAide/internal/observability"
	"github.com/wangh00/SciAide/internal/platform/appdirs"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"github.com/wangh00/SciAide/internal/skillpkg"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	"github.com/wangh00/SciAide/internal/tools/builtin"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

const Version = "0.3.0"

type Options struct {
	RootDir              string
	DisableBuiltinSkills bool
}

type Application struct {
	Logger             *observability.Logger
	SystemFacade       *wailstransport.SystemFacade
	ProjectFacade      *wailstransport.ProjectFacade
	ConversationFacade *wailstransport.ConversationFacade
	ModelFacade        *wailstransport.ModelFacade
	ChatFacade         *wailstransport.ChatFacade
	PermissionFacade   *wailstransport.PermissionFacade
	ToolFacade         *wailstransport.ToolFacade
	MCPFacade          *wailstransport.MCPFacade
	SkillFacade        *wailstransport.SkillFacade
	AttachmentFacade   *wailstransport.AttachmentFacade
	KnowledgeFacade    *wailstransport.KnowledgeFacade

	lifecycle     *wailstransport.LifecycleContext
	chat          *chat.Service
	knowledge     *knowledge.Service
	skills        *skill.Service
	tools         *tool.Executor
	mcp           *mcpadapter.Manager
	store         *sqlite.Store
	transientRoot string
	closeOnce     sync.Once
	closeErr      error
}

func New(options Options) (*Application, error) {
	transientRoot := ""
	if isBindingsBuild() && options.RootDir == "" {
		var err error
		transientRoot, err = os.MkdirTemp("", "sciaide-bindings-*")
		if err != nil {
			return nil, fmt.Errorf("create bindings-only data root: %w", err)
		}
		options.RootDir = transientRoot
		options.DisableBuiltinSkills = true
	}
	cleanupTransient := func() {
		if transientRoot != "" {
			_ = os.RemoveAll(transientRoot)
		}
	}
	dirs, err := resolveDirs(options)
	if err != nil {
		cleanupTransient()
		return nil, err
	}
	if options.RootDir == "" && os.Getenv("SCIAIDE_HOME") == "" {
		if _, err := appdirs.MigrateLegacy("SciAide", dirs); err != nil {
			return nil, fmt.Errorf("migrate legacy application data: %w", err)
		}
	}
	if err := dirs.Ensure(); err != nil {
		cleanupTransient()
		return nil, err
	}

	logger, err := observability.NewLogger(dirs.Logs, slog.LevelInfo)
	if err != nil {
		cleanupTransient()
		return nil, fmt.Errorf("create logger: %w", err)
	}
	fail := func(err error) (*Application, error) {
		_ = logger.Close()
		cleanupTransient()
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
	attachmentService := attachment.NewService(sqlite.NewAttachmentRepository(store.DB()), projectService)
	knowledgeService := knowledge.NewService(sqlite.NewKnowledgeRepository(store.DB()), projectService, attachmentService)
	conversationService := conversation.NewService(conversationRepository)
	contextCheckpointService := contextmemory.NewService(sqlite.NewContextCheckpointRepository(store.DB()))
	profileRepository := sqlite.NewModelProfileRepository(store.DB())
	secrets := secretstore.NewNative("SciAide")
	embeddingService := embedding.NewService(sqlite.NewEmbeddingRepository(store.DB()), secrets, embedding.NewHTTPClient())
	if err := knowledgeService.SetEmbeddingProvider(embeddingService); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure knowledge embeddings: %w", err))
	}
	connectionTester := gateway.NewConnectionTester()
	profileService := modelprofile.NewService(profileRepository, secrets, connectionTester)
	runRepository := sqlite.NewRunRepository(store.DB())
	toolRepository := sqlite.NewToolRepository(store.DB())
	permissionRepository := sqlite.NewPermissionRepository(store.DB())
	permissionEngine := permission.NewEngine(permissionRepository)
	toolService := tool.NewService(toolRepository, tool.JSONSchemaValidator{})
	toolRegistry := tool.NewRegistry()
	for _, builtinTool := range []tool.Tool{
		builtin.NewListWorkspace(projectService), builtin.NewReadText(projectService),
		builtin.NewListAttachments(attachmentService), builtin.NewInspectDocument(attachmentService),
		builtin.NewReadDocument(attachmentService), builtin.NewSearchDocument(attachmentService),
		builtin.NewSearchKnowledge(knowledgeService),
	} {
		if err := toolRegistry.Register(context.Background(), builtinTool); err != nil {
			_ = store.Close()
			return fail(fmt.Errorf("register builtin tool: %w", err))
		}
	}
	mcpManager := mcpadapter.NewManager(toolRegistry, logger.Logger)
	mcpService := mcpserver.NewService(sqlite.NewMCPServerRepository(store.DB()), mcpManager, secrets)
	mcpManager.SetRuntimeObserver(mcpService)
	mcpManager.SetSecretResolver(mcpService)
	if recovered, err := mcpService.RecoverRuntime(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover MCP runtime: %w", err))
	} else if recovered > 0 {
		logger.Warn("recovered stale MCP runtime states", "count", recovered)
	}
	skillService := skill.NewService(sqlite.NewSkillRepository(store.DB()), skillpkg.NewCatalog(dirs.Skills), registryToolAvailability{registry: toolRegistry}, Version)
	packageStore := skillpkg.NewFilePackageStore(dirs.Skills, filepath.Join(dirs.Cache, "skill-staging"), filepath.Join(dirs.Backups, "skills"))
	if err := skillService.SetPackageStore(packageStore); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure Skill package store: %w", err))
	}
	if !options.DisableBuiltinSkills {
		seeded, err := skillpkg.SeedBuiltins(context.Background(), skillService, dirs.Skills, filepath.Join(dirs.Cache, "builtin-skills"))
		if err != nil {
			_ = store.Close()
			return fail(fmt.Errorf("seed built-in Skills: %w", err))
		}
		if len(seeded.Installed) > 0 {
			logger.Info("installed built-in Skills", "count", len(seeded.Installed))
		}
		if len(seeded.Preserved) > 0 {
			logger.Warn("preserved user Skill packages that conflict with built-in versions", "count", len(seeded.Preserved))
		}
	}
	if err := toolRegistry.Register(context.Background(), builtin.NewReadSkillResource(skillService)); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("register Skill resource tool: %w", err))
	}
	if refreshed, err := skillService.Refresh(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("refresh Skill catalog: %w", err))
	} else if refreshed.Invalid > 0 || refreshed.Missing > 0 {
		logger.Warn("Skill catalog contains unavailable packages", "invalid", refreshed.Invalid, "missing", refreshed.Missing)
	}
	toolExecutor := tool.NewExecutor(toolRegistry, toolService, runRepository, tool.ExecutorOptions{})
	approvalCoordinator := permission.NewCoordinator(permissionEngine, toolService, runRepository)
	publisher := wailstransport.NewEventPublisher(lifecycle)
	chatService := chat.NewService(runRepository, conversationRepository, runRepository, publisher)
	if err := chatService.SetAttachmentResolver(attachmentService); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure chat attachments: %w", err))
	}
	terminator := chat.NewTerminator(runRepository, publisher)
	terminator.SetToolCanceller(toolExecutor.Cancel)
	if err := chatService.SetTerminator(terminator); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure run termination: %w", err))
	}
	if err := chatService.SetSnapshotToolCalls(toolService); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure chat snapshot: %w", err))
	}
	agentLoop := agent.NewLoop(runRepository, conversationRepository, toolService, toolRegistry, approvalCoordinator, toolExecutor, gateway.NewResolver(profileService), agent.NewEventObserver(chatService), agent.Options{Terminator: terminator, Checkpoints: contextCheckpointService, SkillContexts: skillService})
	if err := chatService.SetRunner(agent.NewRunner(agentLoop)); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure agent loop: %w", err))
	}
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
	if recovered, err := knowledgeService.Start(); err != nil {
		_ = mcpManager.Close()
		_ = store.Close()
		return fail(fmt.Errorf("recover knowledge indexing: %w", err))
	} else if recovered > 0 {
		logger.Warn("recovered unfinished knowledge indexing jobs", "count", recovered)
	}
	return &Application{
		Logger:             logger,
		SystemFacade:       wailstransport.NewSystemFacade(Version),
		ProjectFacade:      wailstransport.NewProjectFacade(lifecycle, projectService),
		ConversationFacade: wailstransport.NewConversationFacade(lifecycle, conversationService),
		ModelFacade:        wailstransport.NewModelFacade(lifecycle, profileService),
		ChatFacade:         wailstransport.NewChatFacade(lifecycle, chatService, permissionEngine),
		PermissionFacade:   wailstransport.NewPermissionFacade(lifecycle, permissionEngine, approvalCoordinator, chatService),
		ToolFacade:         wailstransport.NewToolFacade(lifecycle, toolExecutor, toolRegistry),
		MCPFacade:          wailstransport.NewMCPFacade(lifecycle, mcpService),
		SkillFacade:        wailstransport.NewSkillFacade(lifecycle, skillService),
		AttachmentFacade:   wailstransport.NewAttachmentFacade(lifecycle, attachmentService),
		KnowledgeFacade:    wailstransport.NewKnowledgeFacade(lifecycle, attachmentService, knowledgeService, embeddingService),
		lifecycle:          lifecycle,
		chat:               chatService,
		knowledge:          knowledgeService,
		skills:             skillService,
		tools:              toolExecutor,
		mcp:                mcpManager,
		store:              store,
		transientRoot:      transientRoot,
	}, nil
}

type registryToolAvailability struct{ registry *tool.MemoryRegistry }

func (a registryToolAvailability) AvailableToolNames(ctx context.Context) ([]string, error) {
	definitions, err := a.registry.Definitions(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(definitions))
	for index, definition := range definitions {
		result[index] = definition.QualifiedName
	}
	return result, nil
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
		a.knowledge.Close()
		if err := a.mcp.Close(); err != nil {
			a.closeErr = fmt.Errorf("close MCP connections: %w", err)
		}
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
		if a.transientRoot != "" {
			if err := os.RemoveAll(a.transientRoot); err != nil && a.closeErr == nil {
				a.closeErr = fmt.Errorf("remove bindings-only data root: %w", err)
			}
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

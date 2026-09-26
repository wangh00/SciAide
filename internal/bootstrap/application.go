package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/agent"
	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/browserenv"
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/embedding"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/mcpserver"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/multimodal"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/app/researchworkflow"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/websearch"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/app/workflowai"
	mcpadapter "github.com/wangh00/SciAide/internal/mcp"
	"github.com/wangh00/SciAide/internal/model/gateway"
	"github.com/wangh00/SciAide/internal/network"
	"github.com/wangh00/SciAide/internal/observability"
	"github.com/wangh00/SciAide/internal/opensciskill"
	"github.com/wangh00/SciAide/internal/platform/appdirs"
	"github.com/wangh00/SciAide/internal/platform/localexec"
	"github.com/wangh00/SciAide/internal/platform/pythonkernel"
	"github.com/wangh00/SciAide/internal/platform/pythonruntime"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"github.com/wangh00/SciAide/internal/research/connectors"
	researchevidence "github.com/wangh00/SciAide/internal/research/evidence"
	"github.com/wangh00/SciAide/internal/research/materializer"
	"github.com/wangh00/SciAide/internal/skillpkg"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	"github.com/wangh00/SciAide/internal/tools/builtin"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

const Version = "1.0.1"

type Options struct {
	RootDir            string
	ResearchConnectors []research.Connector
	EventPublisher     chat.Publisher
}

type Application struct {
	Logger               *observability.Logger
	SystemFacade         *wailstransport.SystemFacade
	ProjectFacade        *wailstransport.ProjectFacade
	ProjectArchiveFacade *wailstransport.ProjectArchiveFacade
	ConversationFacade   *wailstransport.ConversationFacade
	ModelFacade          *wailstransport.ModelFacade
	ChatFacade           *wailstransport.ChatFacade
	PermissionFacade     *wailstransport.PermissionFacade
	ToolFacade           *wailstransport.ToolFacade
	MCPFacade            *wailstransport.MCPFacade
	SkillFacade          *wailstransport.SkillFacade
	AttachmentFacade     *wailstransport.AttachmentFacade
	KnowledgeFacade      *wailstransport.KnowledgeFacade
	ArtifactFacade       *wailstransport.ArtifactFacade
	ResearchFacade       *wailstransport.ResearchFacade
	PythonFacade         *wailstransport.PythonFacade
	WorkflowFacade       *wailstransport.WorkflowFacade

	lifecycle     *wailstransport.LifecycleContext
	chat          *chat.Service
	knowledge     *knowledge.Service
	tools         *tool.Executor
	localexec     *localexec.Runner
	pythonexec    *localexec.Runner
	pythonkernel  *pythonenv.KernelService
	workflow      *workflow.RuntimeService
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
	if retired, err := retireBuiltinSkillPackages(context.Background(), store.DB(), dirs.Skills, filepath.Join(dirs.Backups, "skills")); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("retire legacy built-in Skills: %w", err))
	} else if retired > 0 {
		logger.Info("archived retired built-in Skill packages", "count", retired)
	}
	lifecycle := wailstransport.NewLifecycleContext()
	projectService := project.NewService(sqlite.NewProjectRepository(store.DB()), dirs.Workspaces, dirs.Trash)
	if err := projectService.ReconcileWorkspacePaths(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("reconcile project workspaces: %w", err))
	}
	projectArchiveService, err := projectarchive.NewService(
		sqlite.NewProjectArchiveRepository(store.DB()), projectService, dirs.Workspaces,
		filepath.Join(dirs.Cache, "project-archives"), dirs.Trash, Version,
	)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure project archives: %w", err))
	}
	if recovered, err := projectArchiveService.Recover(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover project archives: %w", err))
	} else if recovered.StagingDirectoriesRemoved > 0 || recovered.OrphanWorkspacesArchived > 0 || recovered.PublishedMarkersRemoved > 0 {
		logger.Warn("recovered interrupted project archive operations",
			"stagingDirectories", recovered.StagingDirectoriesRemoved,
			"orphanWorkspaces", recovered.OrphanWorkspacesArchived,
			"publishedMarkers", recovered.PublishedMarkersRemoved)
	}
	conversationRepository := sqlite.NewConversationRepository(store.DB())
	attachmentService := attachment.NewService(sqlite.NewAttachmentRepository(store.DB()), projectService)
	attachmentService.SetConversationValidator(conversationRepository)
	artifactService := artifact.NewService(sqlite.NewArtifactRepository(store.DB()), projectService)
	knowledgeService := knowledge.NewService(sqlite.NewKnowledgeRepository(store.DB()), projectService, attachmentService)
	conversationService := conversation.NewService(conversationRepository)
	contextCheckpointService := contextmemory.NewService(sqlite.NewContextCheckpointRepository(store.DB()))
	profileRepository := sqlite.NewModelProfileRepository(store.DB())
	secrets := secretstore.NewNative("SciAide")
	networkService, err := network.New(context.Background(), secrets)
	if err != nil {
		_ = store.Close()
		return fail(err)
	}
	network.Activate(networkService)
	webSearchService := websearch.New(secrets)
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
	researchConnectors := options.ResearchConnectors
	if researchConnectors == nil {
		researchConnectors = connectors.Default()
	}
	researchService, err := research.NewService(researchConnectors)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure research Connectors: %w", err))
	}
	researchDiscovery, err := research.NewDiscoveryService(researchService, sqlite.NewResearchRepository(store.DB()), projectService)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure research discovery: %w", err))
	}
	researchMaterializer := materializer.New()
	if err := researchDiscovery.SetImportPipeline(researchMaterializer, attachmentService, knowledgeService); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure research import pipeline: %w", err))
	}
	evidenceVerifier, err := researchevidence.New(knowledgeService)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure research evidence verification: %w", err))
	}
	researchBibliography, err := research.NewBibliographyService(sqlite.NewResearchRepository(store.DB()), projectService, evidenceVerifier)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure research bibliography: %w", err))
	}
	localProcessRunner := localexec.NewRunner(localexec.Options{Recorder: sqlite.NewProcessExecutionRepository(store.DB())})
	pythonProcessRunner := localexec.NewRunner(localexec.Options{MaxOutputBytes: 512 * 1024})
	pythonEnvironmentService, err := pythonenv.NewService(
		sqlite.NewPythonEnvironmentRepository(store.DB()), projectService,
		pythonruntime.New(pythonProcessRunner), dirs.PythonEnvs,
	)
	if err != nil {
		_ = pythonProcessRunner.Close()
		_ = localProcessRunner.Close()
		_ = store.Close()
		return fail(fmt.Errorf("configure project Python environments: %w", err))
	}
	pythonKernelService, err := pythonenv.NewKernelService(projectService, pythonEnvironmentService, pythonkernel.New(pythonProcessRunner))
	if err != nil {
		_ = pythonProcessRunner.Close()
		_ = localProcessRunner.Close()
		_ = store.Close()
		return fail(fmt.Errorf("configure project Python Kernel: %w", err))
	}
	browserService := browserenv.New(projectService, pythonEnvironmentService, pythonProcessRunner)
	researchMaterializer.SetBrowser(browserService)
	pythonKernelService.SetAuditRepository(sqlite.NewPythonKernelRepository(store.DB()))
	pythonEnvironmentService.SetKernelStopper(pythonKernelService.Stop)
	if recovered, err := pythonKernelService.Recover(context.Background()); err != nil {
		_ = pythonKernelService.Close()
		_ = pythonProcessRunner.Close()
		_ = localProcessRunner.Close()
		_ = store.Close()
		return fail(fmt.Errorf("recover project Python Kernel staging: %w", err))
	} else if recovered.TemporaryPathsRemoved > 0 {
		logger.Warn("recovered project Python Kernel staging", "temporaryPaths", recovered.TemporaryPathsRemoved)
	}
	if recovered, err := pythonEnvironmentService.Recover(context.Background()); err != nil {
		_ = pythonKernelService.Close()
		_ = pythonProcessRunner.Close()
		_ = localProcessRunner.Close()
		_ = store.Close()
		return fail(fmt.Errorf("recover project Python environments: %w", err))
	} else if recovered.EnvironmentsRecovered > 0 || recovered.OperationsInterrupted > 0 || recovered.TemporaryPathsRemoved > 0 {
		logger.Warn("recovered project Python environments", "environments", recovered.EnvironmentsRecovered,
			"operations", recovered.OperationsInterrupted, "temporaryPaths", recovered.TemporaryPathsRemoved)
	}
	researchWorkflowService, err := researchworkflow.New(researchDiscovery, knowledgeService, researchBibliography, pythonEnvironmentService, toolService, artifactService)
	if err != nil {
		_ = pythonKernelService.Close()
		_ = pythonProcessRunner.Close()
		_ = localProcessRunner.Close()
		_ = store.Close()
		return fail(fmt.Errorf("configure research Workflow coordination: %w", err))
	}
	for _, builtinTool := range []tool.Tool{
		builtin.NewWebSearch(webSearchService), builtin.NewWebOpen(webSearchService), builtin.NewBrowserOpen(browserService),
		builtin.NewListWorkspace(projectService), builtin.NewReadText(projectService),
		builtin.NewShellExecute(projectService, localProcessRunner), builtin.NewPythonExecute(projectService, localProcessRunner),
		builtin.NewPythonKernelExecute(pythonKernelService), builtin.NewPythonKernelManage(pythonKernelService),
		builtin.NewPythonEnvironmentInstall(pythonEnvironmentService),
		builtin.NewListAttachments(attachmentService), builtin.NewInspectDocument(attachmentService),
		builtin.NewReadDocument(attachmentService), builtin.NewSearchDocument(attachmentService),
		builtin.NewSearchKnowledge(knowledgeService),
		builtin.NewResearchCatalog(researchService), builtin.NewResearchSearch(researchService),
		builtin.NewResearchFetch(researchService),
		builtin.NewResearchFullTextRead(researchDiscovery),
		builtin.NewResearchWorkflowSearch(researchWorkflowService), builtin.NewResearchWorkflowImport(researchWorkflowService),
		builtin.NewResearchWorkflowSync(researchWorkflowService), builtin.NewResearchWorkflowPython(researchWorkflowService),
		builtin.NewResearchWorkflowPreparePython(researchWorkflowService),
		builtin.NewResearchWorkflowReviewGate(), builtin.NewResearchWorkflowReport(researchWorkflowService),
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
	dynamicSkills, err := opensciskill.NewService(store.DB(), projectService, dirs.Data)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure OpenScience Skill catalog: %w", err))
	}
	if err := dynamicSkills.SetToolRegistry(toolRegistry); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("bind Skill capability audit to Tool registry: %w", err))
	}
	if err := toolRegistry.Register(context.Background(), builtin.NewSkillLoad(dynamicSkills, toolRegistry)); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("register Skill loader tool: %w", err))
	}
	if err := toolRegistry.Register(context.Background(), builtin.NewReadSkillResource(dynamicSkills)); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("register Skill resource tool: %w", err))
	}
	if err := toolRegistry.Register(context.Background(), builtin.NewListSkillResources(dynamicSkills)); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("register Skill resource listing tool: %w", err))
	}
	if err := toolRegistry.Register(context.Background(), builtin.NewMaterializeSkillResource(dynamicSkills, projectService)); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("register Skill resource materialization tool: %w", err))
	}
	workflowService, err := workflow.NewService(
		sqlite.NewWorkflowRepository(store.DB()), projectService, workflow.NewCompiler(toolRegistry),
	)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure Workflows: %w", err))
	}
	workflowRuntimeRepository := sqlite.NewWorkflowRuntimeRepository(store.DB())
	resourceActions := builtin.NewResourceActions(sqlite.NewModelResourceRepository(store.DB()), toolRegistry, tool.CompositeProjectResolver{Runs: runRepository, Workflows: workflowRuntimeRepository}, dynamicSkills)
	for _, resourceTool := range []tool.Tool{resourceActions.OpenTool(), resourceActions.SearchTool()} {
		if err := toolRegistry.Register(context.Background(), resourceTool); err != nil {
			_ = store.Close()
			return fail(fmt.Errorf("register resource interface: %w", err))
		}
	}
	researchTaskService := researchtask.NewService(sqlite.NewResearchTaskRepository(store.DB()))
	attachmentService.SetTaskValidator(researchTaskService)
	artifactService.SetTaskValidator(researchTaskService)
	knowledgeService.SetTaskValidator(researchTaskService)
	researchDiscovery.SetTaskValidator(researchTaskService)
	researchBibliography.SetTaskValidator(researchTaskService)
	researchWorkflowService.SetTaskValidator(researchTaskService)
	researchWorkflowService.SetMaterialLoader(attachmentService)
	toolExecutor := tool.NewExecutor(toolRegistry, toolService, tool.CompositeProjectResolver{Runs: runRepository, Workflows: workflowRuntimeRepository}, tool.ExecutorOptions{
		OnInvocationError: func(call tool.Call, err error) {
			logger.Error("tool invocation failed", "toolCallId", call.ID, "tool", call.ToolName, "error", err)
		},
		OnArtifactRegistrationError: func(callID string, err error) {
			logger.Warn("tool completed but Artifact registration was deferred", "toolCallId", callID, "error", err)
		},
	})
	if err := toolExecutor.SetArtifactRegistrar(artifactService); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure tool Artifact registration: %w", err))
	}
	workflowRuntime, err := workflow.NewRuntimeService(workflowRuntimeRepository, sqlite.NewWorkflowRepository(store.DB()), projectService, toolRegistry, toolService, permissionEngine, toolExecutor)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure Workflow Runtime: %w", err))
	}
	workflowRuntime.SetTaskValidator(researchTaskService)
	workflowRuntime.SetMaterialLoader(attachmentService)
	for _, value := range builtin.NewResearchDiscussionTools(workflowRuntime) {
		if err := toolRegistry.Register(context.Background(), value); err != nil {
			_ = store.Close()
			return fail(fmt.Errorf("register research discussion tool: %w", err))
		}
	}
	if err := workflowRuntime.SetSkillLoader(dynamicSkills); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("bind Workflow Skill snapshots: %w", err))
	}
	// Resource managers use durable task metadata rather than reconstructing
	// labels from ephemeral Workflow Run history.
	// The facade is already allocated below; binding is completed after the
	// application value is constructed.
	workflowRuntime.SetProcessRuntimeReader(localProcessRunner)
	workflowRuntime.AddProcessRuntimeReader(pythonProcessRunner)
	workflowStarter, err := workflow.NewStarterService(workflowService, workflowRuntime, projectService, attachmentService, knowledgeService, dynamicSkills)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure research starter: %w", err))
	}
	approvalCoordinator := permission.NewCoordinator(permissionEngine, toolService, runRepository)
	var publisher chat.Publisher = options.EventPublisher
	if publisher == nil {
		publisher = wailstransport.NewEventPublisher(lifecycle)
	}
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
	if err := chatService.SetSnapshotRunSteps(runRepository); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure chat run steps: %w", err))
	}
	modelResolver := gateway.NewResolver(profileService)
	discoveryScope := func(ctx context.Context, runID string) (map[string]bool, error) {
		run, err := runRepository.Get(ctx, runID)
		if err != nil {
			return nil, err
		}
		contract, frozen, err := runRepository.WorkflowAIContract(ctx, runID)
		if err != nil {
			return nil, err
		}
		workflowAI, err := runRepository.IsWorkflowAIRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		if workflowAI && !frozen {
			return nil, fmt.Errorf("MCP discovery requires the frozen workflow tool scope")
		}
		guidance, bound, err := workflowRuntime.GuidanceForConversation(ctx, run.ConversationID)
		if err != nil {
			return nil, err
		}
		var allowed map[string]bool
		if frozen {
			var names []string
			if err := json.Unmarshal(contract.AllowedTools, &names); err != nil {
				return nil, err
			}
			allowed = map[string]bool{}
			for _, name := range names {
				allowed[name] = true
			}
		}
		if bound {
			next := map[string]bool{}
			for _, name := range guidance.AllowedToolNames {
				if allowed == nil || allowed[name] {
					next[name] = true
				}
			}
			allowed = next
		}
		return allowed, nil
	}
	for _, discoveryTool := range []tool.Tool{builtin.NewMCPList(toolRegistry, mcpService, discoveryScope), builtin.NewToolsSearch(toolRegistry, mcpService, discoveryScope)} {
		if err := toolRegistry.Register(context.Background(), discoveryTool); err != nil {
			_ = store.Close()
			return fail(fmt.Errorf("register MCP discovery: %w", err))
		}
	}
	multimodalService := multimodal.NewService(sqlite.NewVisionFallbackRepository(store.DB()), secrets, multimodal.NewProtocolResolver())
	historicalSkills := skill.NewHistoricalRunContexts(sqlite.NewSkillRepository(store.DB()))
	agentLoop := agent.NewLoop(runRepository, conversationRepository, toolService, toolRegistry, approvalCoordinator, toolExecutor, modelResolver, agent.NewEventObserver(chatService), agent.Options{Terminator: terminator, Checkpoints: contextCheckpointService, SkillContexts: historicalSkills, SkillRouter: dynamicSkills, Images: attachmentService, Multimodal: multimodalService, Research: workflowRuntime, Resources: resourceActions})
	agentLoop.SetBrowserAvailability(browserService.Available)
	if err := chatService.SetRunner(agent.NewRunner(agentLoop)); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure agent loop: %w", err))
	}
	workflowAIBridge, err := workflowai.New(chatService, conversationRepository, permissionEngine)
	if err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("configure Workflow AI bridge: %w", err))
	}
	workflowAIBridge.SetProcessRuntimeReader(localProcessRunner)
	workflowRuntime.SetLiteratureCandidateReader(researchWorkflowService)
	workflowAIBridge.AddProcessRuntimeReader(pythonProcessRunner)
	if err := workflowRuntime.SetAIStageExecutor(workflowAIBridge); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("bind Workflow AI bridge: %w", err))
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
	if interrupted, err := sqlite.NewProcessExecutionRepository(store.DB()).InterruptActive(context.Background(), time.Now().UTC()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover local process execution audits: %w", err))
	} else if interrupted > 0 {
		logger.Warn("interrupted unfinished local process execution audits", "count", interrupted)
	}
	if interrupted, err := chatService.Recover(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover chat runs: %w", err))
	} else if interrupted > 0 {
		logger.Warn("interrupted unfinished chat runs", "count", interrupted)
	}
	// Workflow AI nodes project durable Chat Run outcomes. Recover Chat first so
	// an unfinished model/tool turn is terminal before Workflow recovery decides
	// whether the stage can be committed or must wait for an explicit retry.
	if recovered, err := workflowRuntime.Recover(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover Workflow Runs: %w", err))
	} else if recovered > 0 {
		logger.Warn("recovered Workflow Runs", "count", recovered)
	}
	if recovered, err := artifactService.Recover(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover Artifact storage: %w", err))
	} else if recovered.TemporaryFilesRemoved > 0 || recovered.OrphanObjectsRemoved > 0 || recovered.ToolArtifactsRecovered > 0 || recovered.ToolArtifactsFailed > 0 {
		logger.Warn("recovered Artifact storage", "temporaryFiles", recovered.TemporaryFilesRemoved, "orphanObjects", recovered.OrphanObjectsRemoved, "toolArtifacts", recovered.ToolArtifactsRecovered, "toolArtifactFailures", recovered.ToolArtifactsFailed)
	}
	if recovered, err := researchDiscovery.Recover(context.Background()); err != nil {
		_ = store.Close()
		return fail(fmt.Errorf("recover research imports: %w", err))
	} else if recovered > 0 {
		logger.Warn("recovered unfinished research imports", "count", recovered)
	}
	if recovered, err := knowledgeService.Start(); err != nil {
		_ = mcpManager.Close()
		_ = store.Close()
		return fail(fmt.Errorf("recover knowledge indexing: %w", err))
	} else if recovered > 0 {
		logger.Warn("recovered unfinished knowledge indexing jobs", "count", recovered)
	}
	application := &Application{
		Logger:               logger,
		SystemFacade:         wailstransport.NewSystemFacade(Version),
		ProjectFacade:        wailstransport.NewProjectFacade(lifecycle, projectService, pythonEnvironmentService),
		ProjectArchiveFacade: wailstransport.NewProjectArchiveFacade(lifecycle, projectArchiveService, projectService),
		ConversationFacade:   wailstransport.NewConversationFacade(lifecycle, conversationService),
		ModelFacade:          wailstransport.NewModelFacade(lifecycle, profileService, multimodalService),
		ChatFacade:           wailstransport.NewChatFacade(lifecycle, chatService, permissionEngine, agentLoop),
		PermissionFacade:     wailstransport.NewPermissionFacade(lifecycle, permissionEngine, approvalCoordinator, chatService),
		ToolFacade:           wailstransport.NewToolFacade(lifecycle, toolExecutor, toolRegistry),
		MCPFacade:            wailstransport.NewMCPFacade(lifecycle, mcpService),
		SkillFacade:          wailstransport.NewSkillFacade(lifecycle, dynamicSkills),
		AttachmentFacade:     wailstransport.NewAttachmentFacade(lifecycle, attachmentService),
		KnowledgeFacade:      wailstransport.NewKnowledgeFacade(lifecycle, attachmentService, knowledgeService, embeddingService),
		ArtifactFacade:       wailstransport.NewArtifactFacade(lifecycle, artifactService),
		ResearchFacade:       wailstransport.NewResearchFacade(lifecycle, researchDiscovery, researchBibliography),
		PythonFacade:         wailstransport.NewPythonFacade(lifecycle, pythonEnvironmentService, pythonKernelService),
		WorkflowFacade:       wailstransport.NewWorkflowFacade(lifecycle, workflowService, workflowRuntime, workflowStarter, artifactService, projectService),
		lifecycle:            lifecycle,
		chat:                 chatService,
		knowledge:            knowledgeService,
		tools:                toolExecutor,
		localexec:            localProcessRunner,
		pythonexec:           pythonProcessRunner,
		pythonkernel:         pythonKernelService,
		workflow:             workflowRuntime,
		mcp:                  mcpManager,
		store:                store,
		transientRoot:        transientRoot,
	}
	application.WorkflowFacade.SetResearchTaskService(researchTaskService)
	application.ModelFacade.SetWebSearch(webSearchService)
	application.PythonFacade.SetBrowser(browserService)
	return application, nil
}

func retireBuiltinSkillPackages(ctx context.Context, db *sql.DB, skillsRoot, backupRoot string) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT package_rel_path FROM retired_builtin_skill_packages ORDER BY package_rel_path`)
	if err != nil {
		return 0, err
	}
	paths := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()
			return 0, err
		}
		paths = append(paths, value)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	archived := 0
	for _, value := range paths {
		moved, err := skillpkg.ArchiveRetiredPackage(skillsRoot, backupRoot, value)
		if err != nil {
			return archived, err
		}
		if moved {
			archived++
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM retired_builtin_skill_packages WHERE package_rel_path=?`, value); err != nil {
			return archived, err
		}
	}
	return archived, nil
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
		a.workflow.BeginShutdown()
		a.localexec.BeginShutdown()
		a.pythonexec.BeginShutdown()
		a.chat.Close()
		if err := a.pythonkernel.Close(); err != nil {
			a.closeErr = fmt.Errorf("close project Python Kernels: %w", err)
		}
		if err := a.localexec.Close(); err != nil {
			a.closeErr = fmt.Errorf("close local process runner: %w", err)
		}
		if err := a.pythonexec.Close(); err != nil && a.closeErr == nil {
			a.closeErr = fmt.Errorf("close Python environment process runner: %w", err)
		}
		a.knowledge.Close()
		if err := a.mcp.Close(); err != nil {
			a.closeErr = fmt.Errorf("close MCP connections: %w", err)
		}
		// Workflow drivers can still persist their final cancellation state while
		// process runners, Kernels, and MCP transports are being closed above.
		a.workflow.Wait()
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

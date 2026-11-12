package debugapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/id"
	w "github.com/wangh00/SciAide/internal/transport/wails"
)

type Services struct {
	Projects      *w.ProjectFacade
	Conversations *w.ConversationFacade
	Chat          *w.ChatFacade
	Workflow      *w.WorkflowFacade
	Models        *w.ModelFacade
	Permissions   *w.PermissionFacade
}

type Options struct {
	Port        int
	MaxDuration time.Duration
	// Optional absolute cutoff. Reuse it on a restart instead of silently
	// obtaining a fresh duration. It must fit within MaxDuration and one hour.
	Deadline        time.Time
	AllowFullAccess bool
	OnClose         func() error
	// Signal actual detached Chat/Workflow/process owners, not just a UI
	// lifecycle context. Must not wait for workers or call full application Close.
	OnStop context.CancelFunc
	// Optional in-process test observer; never serialized or logged by default.
	OnError func(error)
}

type Case struct {
	RequestID              string `json:"requestId"`
	RequestSHA256          string `json:"requestSha256"`
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	ProjectID              string `json:"projectId"`
	TaskID                 string `json:"taskId"`
	ConversationID         string `json:"conversationId"`
	StarterRunID           string `json:"starterRunId"`
	ModelProfileID         string `json:"modelProfileId"`
	ModelID                string `json:"modelId"`
	InitialClientMessageID string `json:"initialClientMessageId"`
	InitialRunID           string `json:"initialRunId,omitempty"`
}

type Server struct {
	services       Services
	workspace      *Workspace
	options        Options
	http           *http.Server
	listener       net.Listener
	token          string
	url            string
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	cases          map[string]Case
	once           sync.Once
	done           chan struct{}
	admission      sync.Mutex
	closing        bool
	handlers       sync.WaitGroup
	limitsMu       sync.Mutex
	grant          *sessionGrant
	sessionID      string
	limitsRevision uint64
	deadlineTimer  *time.Timer
	timerMu        sync.Mutex
}

// Only the developer runner calls Start. No desktop/Wails startup path does.
func Start(parent context.Context, services Services, workspace *Workspace, options Options) (*Server, error) {
	if workspace == nil || workspace.budget == nil || options.OnStop == nil || options.OnClose == nil {
		return nil, fmt.Errorf("owned workspace and application lifecycle callbacks are required")
	}
	if workspace.AllowFullAccess != options.AllowFullAccess {
		return nil, fmt.Errorf("permission policy must match the owned workspace")
	}
	if options.Port < 0 || options.Port > 65535 {
		return nil, fmt.Errorf("port must be 0..65535")
	}
	if options.MaxDuration <= 0 || options.MaxDuration > time.Hour {
		return nil, fmt.Errorf("max-duration must be >0 and <=1h")
	}
	if services.Projects == nil || services.Chat == nil || services.Conversations == nil || services.Workflow == nil || services.Models == nil || services.Permissions == nil {
		return nil, fmt.Errorf("incomplete real application services")
	}
	now := sessionNow()
	expiresAt, hardStopAt, err := sessionWindow(parent, now, options.MaxDuration, options.Deadline)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(parent, hardStopAt.mono)
	s := &Server{services: services, workspace: workspace, options: options, ctx: ctx, cancel: cancel, cases: map[string]Case{}, done: make(chan struct{})}
	s.grant = &sessionGrant{expiresAt: expiresAt, hardStopAt: hardStopAt, ctx: ctx, cancel: cancel, onStop: options.OnStop, clock: workspace.budget.now}
	s.limitsRevision = 1
	s.sessionID, err = id.New()
	if err != nil {
		cancel()
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(workspace.Root, "cases.json"))
	if err == nil {
		err = json.Unmarshal(data, &s.cases)
	} else if os.IsNotExist(err) {
		err = nil
	}
	if err != nil || s.cases == nil {
		cancel()
		return nil, fmt.Errorf("invalid saved case manifest")
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		cancel()
		return nil, err
	}
	s.token = hex.EncodeToString(key)
	s.listener, err = net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", options.Port))
	if err != nil {
		cancel()
		return nil, err
	}
	s.url = "http://" + s.listener.Addr().String()
	s.http = &http.Server{Handler: s, BaseContext: func(net.Listener) context.Context { return s.ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	workspace.budget.mu.Lock()
	workspace.budget.grant = s.grant
	workspace.budget.mu.Unlock()
	if err = s.writeConnectionDescriptor(expiresAt.wall, workspace.budget.snapshot()); err != nil {
		_ = s.listener.Close()
		cancel()
		return nil, err
	}
	s.timerMu.Lock()
	s.deadlineTimer = time.AfterFunc(deadlineDelay(expiresAt.remaining(sessionNow())), s.expire)
	s.timerMu.Unlock()
	go func() { _ = s.http.Serve(s.listener); _ = s.Close() }()
	go func() { <-ctx.Done(); _ = s.Close() }()
	return s, nil
}

func (s *Server) URL() string           { return s.url }
func (s *Server) Done() <-chan struct{} { return s.done }
func (s *Server) Close() error {
	// Cancellation must never wait for budget/descriptor fsync or handlers.
	s.grant.stop()
	s.once.Do(func() {
		s.admission.Lock()
		s.closing = true
		s.admission.Unlock()
		s.cancel()
		s.timerMu.Lock()
		if s.deadlineTimer != nil {
			s.deadlineTimer.Stop()
		}
		s.timerMu.Unlock()
		// Block admission first, then wait for handlers and real workers before
		// releasing the root lock. No late model call can escape shutdown.
		s.workspace.budget.mu.Lock()
		s.workspace.budget.state.Enabled = false
		s.workspace.budget.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.http.Shutdown(ctx); err != nil {
			_ = s.http.Close()
		}
		s.handlers.Wait()
		if s.options.OnClose != nil {
			_ = s.options.OnClose()
		}
		_ = os.Remove(filepath.Join(s.workspace.Root, "debug-connection.json"))
		_ = s.workspace.Close()
		close(s.done)
	})
	return nil
}

func (s *Server) ServeHTTP(out http.ResponseWriter, r *http.Request) {
	// Serialize Add against the closing flag; Wait cannot miss a late handler.
	s.admission.Lock()
	if s.closing {
		s.admission.Unlock()
		http.Error(out, "debug session closing", http.StatusServiceUnavailable)
		return
	}
	s.handlers.Add(1)
	s.admission.Unlock()
	defer s.handlers.Done()
	out.Header().Set("Cache-Control", "no-store")
	out.Header().Set("X-Content-Type-Options", "nosniff")
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil || host != "127.0.0.1" || r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(out, "local non-browser clients only", http.StatusForbidden)
		return
	}
	want := "Bearer " + s.token
	actual := r.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(want), []byte(actual)) != 1 {
		http.Error(out, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.ctx.Err() != nil || s.sessionExpired() {
		go s.Close()
		http.Error(out, "debug session expired", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(out, r.Body, 1<<20)
	if r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
		http.Error(out, "application/json required", http.StatusUnsupportedMediaType)
		return
	}
	value, callErr := s.dispatch(r)
	out.Header().Set("Content-Type", "application/json; charset=utf-8")
	if callErr != nil {
		if s.options.OnError != nil {
			s.options.OnError(callErr)
		}
		out.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(out).Encode(map[string]any{"error": apperr.Public(callErr), "result": value})
		return
	}
	_ = json.NewEncoder(out).Encode(value)
}

func decode(r *http.Request, target any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return &apperr.Error{Code: "DEBUG_INVALID_JSON", UserMessage: "调试请求 JSON、字段或大小无效。"}
	}
	if err := r.Context().Err(); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return &apperr.Error{Code: "DEBUG_INVALID_JSON", UserMessage: "调试请求只能包含一个 JSON 对象。"}
	}
	return nil
}

// Only call with developer-authored validation messages, never upstream errors
// or arbitrary request contents; provider failures retain the normal redaction.
func invalidRequest(message string) error {
	return &apperr.Error{Code: "DEBUG_REQUEST_INVALID", UserMessage: message}
}

func (s *Server) dispatch(r *http.Request) (any, error) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if path == "v1/status" && r.Method == http.MethodGet {
		limits := s.limitsSnapshot()
		return map[string]any{"kind": "sciaide-research-debug", "apiVersion": 1, "modelCalls": limits.ModelCalls, "session": limits.Session, "allowFullAccess": s.options.AllowFullAccess, "operations": []string{"message", "answer", "adopt", "start-route", "retry", "pause", "resume", "cancel", "cancel-chat", "decide", "approve", "approve-workflow", "clarify", "replan", "limits"}}, nil
	}
	if path == "v1/limits" && r.Method == http.MethodPost {
		var cmd LimitsCommand
		if err := decode(r, &cmd); err != nil {
			return nil, err
		}
		return s.updateLimits(cmd)
	}
	if path == "v1/shutdown" && r.Method == http.MethodPost {
		var empty struct{}
		if err := decode(r, &empty); err != nil {
			return nil, err
		}
		go s.Close()
		return map[string]bool{"stopping": true}, nil
	}
	if path == "v1/models" && r.Method == http.MethodGet {
		profiles, err := s.services.Models.ListModelProfiles()
		if err != nil {
			return nil, err
		}
		result := []any{}
		for _, p := range profiles {
			result = append(result, map[string]any{"id": p.ID, "name": p.Name, "protocol": p.APIProtocol, "enabled": p.Enabled, "isDefault": p.IsDefault, "modelId": p.ModelID, "models": p.Models})
		}
		return result, nil
	}
	if path == "v1/cases" {
		if r.Method == http.MethodPost {
			return s.createCase(r)
		}
		if r.Method == http.MethodGet {
			s.mu.Lock()
			defer s.mu.Unlock()
			values := []Case{}
			for _, c := range s.cases {
				values = append(values, c)
			}
			return values, nil
		}
	}
	if len(parts) >= 3 && parts[0] == "v1" && parts[1] == "cases" {
		s.mu.Lock()
		c, found := s.cases[parts[2]]
		s.mu.Unlock()
		if !found {
			return nil, invalidRequest("unknown owned debug case")
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			return s.snapshot(c)
		}
		if len(parts) == 5 && parts[3] == "actions" && r.Method == http.MethodPost {
			return s.action(c, parts[4], r)
		}
	}
	return nil, &apperr.Error{Code: "DEBUG_NOT_FOUND", UserMessage: "未知调试接口或请求方法。"}
}

func (s *Server) createCase(r *http.Request) (any, error) {
	var cmd struct {
		Name, Prompt, ModelProfileID, ModelID, RequestID string
		PermissionMode                                   conversation.PermissionMode
	}
	if err := decode(r, &cmd); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cmd.Prompt) == "" || len([]rune(cmd.Prompt)) > 10000 || len([]rune(cmd.Name)) > 120 {
		return nil, invalidRequest("case requires a 1..10000 character prompt and name <=120 characters")
	}
	if cmd.PermissionMode == "" {
		// Research cases follow the production research entry point: a debug
		// server that explicitly opted into full access defaults new cases to
		// full access. Without that opt-in, keep the conservative Plan mode.
		if s.options.AllowFullAccess {
			cmd.PermissionMode = conversation.PermissionFullAccess
		} else {
			cmd.PermissionMode = conversation.PermissionPlan
		}
	}
	if strings.TrimSpace(cmd.RequestID) == "" || len(cmd.RequestID) > 128 {
		return nil, &apperr.Error{Code: "DEBUG_REQUEST_ID_REQUIRED", UserMessage: "请在调用前生成并保存 1–128 字符的 requestId；网络中断后以原标识和完全相同的输入重放。"}
	}
	if !cmd.PermissionMode.Valid() || cmd.PermissionMode == conversation.PermissionFullAccess && !s.options.AllowFullAccess {
		return nil, invalidRequest("full_access requires the server's explicit --allow-full-access flag")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, _ := json.Marshal(cmd)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
	for _, prior := range s.cases {
		if prior.RequestID == cmd.RequestID {
			if prior.RequestSHA256 != fingerprint {
				return nil, invalidRequest("requestId already belongs to a different case request")
			}
			return s.initializeCase(prior, cmd.Prompt, true)
		}
	}
	if err := s.validateModel(cmd.ModelProfileID, cmd.ModelID); err != nil {
		return nil, err
	}
	if len(s.cases) >= 20 {
		return nil, invalidRequest("this isolated workspace already owns 20 cases; use a new isolated root")
	}
	caseID, err := id.New()
	if err != nil {
		return nil, err
	}
	p, err := s.services.Projects.CreateProject(w.CreateProjectRequest{Name: "[debug] " + cmd.Name})
	if err != nil {
		return nil, err
	}
	entry, err := s.services.Workflow.StartResearch(workflow.StartResearchCommand{ProjectID: p.ID, ResearchIdea: cmd.Prompt, ModelProfileID: cmd.ModelProfileID, ModelID: cmd.ModelID, DiscussionOnly: true, PermissionMode: cmd.PermissionMode})
	if err != nil {
		return nil, err
	}
	clientID, err := id.New()
	if err != nil {
		return nil, err
	}
	c := Case{RequestID: cmd.RequestID, RequestSHA256: fingerprint, ID: caseID, Name: cmd.Name, ProjectID: p.ID, TaskID: entry.Run.ResearchTaskID, ConversationID: entry.Run.ConversationID, StarterRunID: entry.Run.ID, ModelProfileID: cmd.ModelProfileID, ModelID: cmd.ModelID, InitialClientMessageID: clientID}
	s.cases[c.ID] = c
	data, _ := json.Marshal(s.cases)
	if err = atomicJSON(filepath.Join(s.workspace.Root, "cases.json"), data); err != nil {
		delete(s.cases, c.ID)
		return nil, err
	}
	return s.initializeCase(c, cmd.Prompt, false)
}

// Caller holds s.mu: a lost response or process interruption cannot create a
// second initial Chat Run. Startup alone never resumes pending initialization.
func (s *Server) initializeCase(c Case, prompt string, replayed bool) (any, error) {
	result := map[string]any{"case": c, "replayed": replayed, "initialState": "pending"}
	if c.InitialClientMessageID == "" {
		return result, &apperr.Error{Code: "DEBUG_LEGACY_CASE", UserMessage: "旧调试案例缺少持久初始化标识，请保留记录并使用新的隔离目录。"}
	}
	var run chat.Run
	var err error
	if c.InitialRunID != "" {
		result["initialState"] = "started"
		return result, nil
	}
	runID, err := s.workspace.initialChatRun(s.ctx, c)
	if err != nil {
		return result, err
	}
	if runID != "" {
		snap, e := s.services.Chat.GetRunSnapshot(runID)
		if e != nil {
			return result, e
		}
		if snap.Run.ConversationID != c.ConversationID || snap.Run.UserMessageID != c.InitialClientMessageID {
			return result, invalidRequest("initial run identity does not match its debug case")
		}
		run = snap.Run
	}
	if run.ID == "" {
		if err = s.validateModel(c.ModelProfileID, c.ModelID); err != nil {
			return result, err
		}
		run, err = s.services.Chat.StartChat(chat.StartCommand{ConversationID: c.ConversationID, ModelProfileID: c.ModelProfileID, ModelID: c.ModelID, Text: prompt, ClientMessageID: c.InitialClientMessageID})
		if err != nil {
			return result, &apperr.Error{Code: "DEBUG_CASE_INITIALIZATION_PENDING", UserMessage: "案例已保留，但初始对话未确认启动。请保留原 requestId 和相同输入显式重放创建请求；不会自动重新调用模型。", Cause: err}
		}
	}
	c.InitialRunID = run.ID
	s.cases[c.ID] = c
	data, _ := json.Marshal(s.cases)
	if err = atomicJSON(filepath.Join(s.workspace.Root, "cases.json"), data); err != nil {
		// Keep the in-memory marker too; a restart resolves the stable message
		// identity from the app's atomic run/messages transaction instead.
		return result, &apperr.Error{Code: "DEBUG_CASE_INITIALIZATION_PENDING", UserMessage: "初始对话已创建，但调试索引未确认保存；请用相同 requestId 显式重放，勿更换标识。", Cause: err}
	}
	result["case"], result["chatRun"], result["initialState"] = c, run, "started"
	return result, nil
}

func (s *Server) validateModel(profileID, modelID string) error {
	if !s.workspace.budget.snapshot().Enabled {
		return &apperr.Error{Code: "DEBUG_MODEL_DISABLED", UserMessage: "请显式启用模型调用后再创建试跑案例，可能产生费用。"}
	}
	// The live read-only source overlay also prepares the isolated FK stub.
	// Calling /models first must never be a hidden prerequisite for /cases.
	profiles, err := s.services.Models.ListModelProfiles()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if p.ID == profileID && p.Enabled {
			for _, m := range p.Models {
				if m.ID == modelID && m.Enabled {
					return nil
				}
			}
		}
	}
	return &apperr.Error{Code: "DEBUG_MODEL_UNAVAILABLE", UserMessage: "指定的连接或模型不存在、已禁用或不可选。"}
}

// Keep the immutable initial discussion identity for initialization replay,
// while follow-ups use the task's adopted Workflow conversation. A planning
// AI conversation is never substituted for the human starter discussion.
func discussionConversation(c Case, current workflow.RunDetail) string {
	if current.Run.WorkflowPurpose != workflow.PurposeResearchStarter && current.Run.ConversationID != "" {
		return current.Run.ConversationID
	}
	return c.ConversationID
}

func (s *Server) snapshot(c Case) (any, error) {
	current, err := s.services.Workflow.GetCurrentResearchTaskRun(c.ProjectID, c.TaskID)
	if err != nil {
		return nil, err
	}
	discussionID := discussionConversation(c, current)
	messages, err := s.services.Conversations.ListMessages(discussionID)
	if err != nil {
		return nil, err
	}
	latest, err := s.services.Chat.GetLatestOrdinaryRunSnapshot(discussionID)
	if err != nil {
		return nil, err
	}
	if discussionID != c.ConversationID {
		initial, e := s.services.Chat.GetLatestOrdinaryRunSnapshot(c.ConversationID)
		if e != nil {
			return nil, e
		}
		// A route can be adopted before its original discussion finishes.
		// Do not hide a late failure there until a genuinely newer human turn
		// exists in the adopted conversation.
		if initial != nil && (latest == nil || initial.Run.CreatedAt.After(latest.Run.CreatedAt)) {
			latest = initial
		}
	}
	chats := []w.RunSnapshot{}
	if latest != nil {
		chats = append(chats, *latest)
	}
	for _, execution := range current.AIExecutions {
		if execution.ChatRunID != "" {
			snap, e := s.services.Chat.GetRunSnapshot(execution.ChatRunID)
			if e != nil {
				return nil, e
			}
			chats = append(chats, snap)
		}
	}
	var plan *workflow.ResearchStarterPlan
	if current.Run.Status == workflow.RunCompleted && current.Run.WorkflowPurpose == workflow.PurposeResearchStarter {
		if p, e := s.services.Workflow.GetResearchStarterPlan(c.ProjectID, current.Run.ID); e == nil {
			plan = &p
		}
	}
	history, err := s.services.Workflow.ListProjectRuns(c.ProjectID, 100)
	if err != nil {
		return nil, err
	}
	// A completed discussion-only starter is NOT completed research. Do not
	// let the harness repeat the HTTP-200 / "completed" false-success bug.
	state := "discussion"
	if latest != nil && latest.Run.Status == chat.RunCompleted {
		state = "discussion_idle"
	}
	if current.Run.ID != c.StarterRunID {
		state = "planning_" + string(current.Run.Status)
	}
	if plan != nil {
		state = "plan_available"
	}
	if current.Run.WorkflowPurpose != workflow.PurposeResearchStarter {
		state = "workflow_" + string(current.Run.Status)
	}
	if current.Run.ErrorCode != "" {
		state = "workflow_failed"
	}
	workflowState := state
	discussionState := "not_started"
	if latest != nil {
		discussionState = string(latest.Run.Status)
		if current.Run.Status.Terminal() {
			if latest.Run.Status == chat.RunRunning || latest.Run.Status == chat.RunQueued {
				state = "discussion"
			} else if latest.Run.Status == chat.RunCompleted {
				for _, proposal := range current.RevisionProposals {
					if proposal.Status == "pending" && proposal.CanConfirm {
						state = "revision_available"
						break
					}
				}
			}
		}
	}
	if latest != nil {
		switch latest.Run.Status {
		case chat.RunFailed:
			state = "chat_failed"
		case chat.RunCancelled:
			state = "chat_cancelled"
		case chat.RunInterrupted:
			state = "chat_interrupted"
		}
	}
	for _, snap := range chats {
		if snap.UserInput != nil {
			state = "waiting_user_input"
		}
		if len(snap.PendingApprovals) > 0 {
			state = "waiting_approval"
		}
	}
	if len(current.PendingApprovals) > 0 {
		state = "waiting_approval"
	}
	if c.InitialRunID == "" {
		state = "initialization_pending"
	}
	limits := s.limitsSnapshot()
	return map[string]any{"case": c, "activeConversationId": discussionID, "state": state, "workflowState": workflowState, "discussionState": discussionState, "workflow": current, "history": history, "plan": plan, "messages": messages, "chats": chats, "modelCalls": limits.ModelCalls, "session": limits.Session}, nil
}

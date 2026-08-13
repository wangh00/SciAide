package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/model"
)

type RunExecutor interface {
	Execute(ctx context.Context, runID string)
	ResumeExecute(ctx context.Context, runID string)
}

type StartCommand struct {
	ConversationID string `json:"conversationId"`
	ModelProfileID string `json:"modelProfileId"`
	ModelID        string `json:"modelId"`
	Text           string `json:"text"`
}

const maxUserMessageChars = 100_000

// Snapshot is the durable UI recovery view. Events improve latency, but this
// snapshot remains the source of truth after lost or out-of-order UI events.
type Snapshot struct {
	Run       Run                    `json:"run"`
	Messages  []conversation.Message `json:"messages"`
	ToolCalls []tool.Call            `json:"toolCalls"`
}

type ToolCallReader interface {
	ListByRun(ctx context.Context, runID string) ([]tool.Call, error)
}

type Service struct {
	runs          Repository
	conversations ConversationRepository
	events        EventRepository
	publisher     Publisher
	terminator    *Terminator
	toolCalls     ToolCallReader
	now           func() time.Time

	mu            sync.Mutex
	runner        RunExecutor
	active        map[string]context.CancelFunc
	pendingResume map[string]bool
	lastResumeKey map[string]string
	closing       bool
	wg            sync.WaitGroup
}

func (s *Service) SetSnapshotToolCalls(toolCalls ToolCallReader) error {
	if toolCalls == nil {
		return fmt.Errorf("chat snapshot tool call reader is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolCalls != nil {
		return fmt.Errorf("chat snapshot tool call reader is already configured")
	}
	s.toolCalls = toolCalls
	return nil
}

func (s *Service) SetTerminator(terminator *Terminator) error {
	if terminator == nil {
		return fmt.Errorf("chat run terminator is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminator != nil {
		return fmt.Errorf("chat run terminator is already configured")
	}
	s.terminator = terminator
	return nil
}

func NewService(runs Repository, conversations ConversationRepository, eventRepository EventRepository, publisher Publisher) *Service {
	return &Service{runs: runs, conversations: conversations, events: eventRepository, publisher: publisher, now: func() time.Time { return time.Now().UTC() }, active: make(map[string]context.CancelFunc), pendingResume: make(map[string]bool), lastResumeKey: make(map[string]string)}
}

// SetRunner completes bootstrap's dependency cycle: Chat creates the durable
// Run, while AgentLoop depends on Chat's typed Run repository and observer.
// It must be called once during bootstrap before Start.
func (s *Service) SetRunner(runner RunExecutor) error {
	if runner == nil {
		return fmt.Errorf("chat run executor is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runner != nil {
		return fmt.Errorf("chat run executor is already configured")
	}
	s.runner = runner
	return nil
}

func (s *Service) Recover(ctx context.Context) (int64, error) {
	return s.runs.InterruptActive(ctx, s.now())
}

func (s *Service) Start(ctx context.Context, cmd StartCommand) (Run, error) {
	return s.start(ctx, cmd, "")
}

func (s *Service) start(ctx context.Context, cmd StartCommand, replacedRunID string) (Run, error) {
	cmd.ConversationID = strings.TrimSpace(cmd.ConversationID)
	cmd.ModelProfileID = strings.TrimSpace(cmd.ModelProfileID)
	cmd.ModelID = strings.TrimSpace(cmd.ModelID)
	cmd.Text = strings.TrimSpace(cmd.Text)
	if cmd.ConversationID == "" || cmd.ModelProfileID == "" || cmd.ModelID == "" || cmd.Text == "" {
		return Run{}, fmt.Errorf("conversation, model profile, model and message text are required")
	}
	if len([]rune(cmd.Text)) > maxUserMessageChars {
		return Run{}, fmt.Errorf("message is too long")
	}
	if s.currentRunner() == nil {
		return Run{}, fmt.Errorf("chat run executor is not configured")
	}
	selectedConversation, err := s.conversations.GetConversation(ctx, cmd.ConversationID)
	if err != nil {
		return Run{}, fmt.Errorf("load conversation: %w", err)
	}
	if !selectedConversation.PermissionMode.Valid() {
		return Run{}, fmt.Errorf("conversation has an invalid permission mode")
	}
	if previous, exists, latestErr := s.runs.LatestForConversation(ctx, cmd.ConversationID); latestErr != nil {
		return Run{}, latestErr
	} else if exists {
		if !isTerminal(previous.Status) {
			return Run{}, fmt.Errorf("conversation already has an active run")
		}
		if replacedRunID != "" && previous.ID != replacedRunID {
			return Run{}, fmt.Errorf("conversation changed before the replacement run could start")
		}
	} else if replacedRunID != "" {
		return Run{}, fmt.Errorf("cancelled run is no longer the latest conversation run")
	}
	runID, err := id.New()
	if err != nil {
		return Run{}, err
	}
	userID, err := id.New()
	if err != nil {
		return Run{}, err
	}
	userPartID, err := id.New()
	if err != nil {
		return Run{}, err
	}
	assistantID, err := id.New()
	if err != nil {
		return Run{}, err
	}
	assistantPartID, err := id.New()
	if err != nil {
		return Run{}, err
	}
	now := s.now()
	user := conversation.Message{ID: userID, ConversationID: cmd.ConversationID, RunID: runID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now,
		Parts: []conversation.MessagePart{{ID: userPartID, MessageID: userID, Ordinal: 0, Type: "text", Text: cmd.Text, CreatedAt: now}}}
	assistant := conversation.Message{ID: assistantID, ConversationID: cmd.ConversationID, RunID: runID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now.Add(time.Nanosecond), UpdatedAt: now,
		Parts: []conversation.MessagePart{{ID: assistantPartID, MessageID: assistantID, Ordinal: 0, Type: "text", CreatedAt: now.Add(time.Nanosecond)}}}
	run := Run{ID: runID, ConversationID: cmd.ConversationID, UserMessageID: userID, AssistantMessageID: assistantID, ModelProfileID: cmd.ModelProfileID, ModelID: cmd.ModelID, PermissionMode: selectedConversation.PermissionMode, Status: RunQueued, CreatedAt: now, UpdatedAt: now}
	if err := s.runs.CreateWithMessages(ctx, run, user, assistant); err != nil {
		return Run{}, fmt.Errorf("create chat run: %w", err)
	}
	if err := s.launch(run.ID, false); err != nil {
		return Run{}, err
	}
	return run, nil
}

// Steer intentionally means "cancel current work, preserve its partial text,
// then start a fresh durable Run with the new user instruction". This keeps
// one execution owner per conversation and gives the user a predictable
// interrupt-and-continue interaction without mutating an in-flight request.
func (s *Service) Steer(ctx context.Context, activeRunID string, cmd StartCommand) (Run, error) {
	activeRunID = strings.TrimSpace(activeRunID)
	if activeRunID == "" {
		return Run{}, fmt.Errorf("active run id is required")
	}
	active, err := s.runs.Get(ctx, activeRunID)
	if err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(cmd.ConversationID) != active.ConversationID {
		return Run{}, fmt.Errorf("steer target does not match the active conversation")
	}
	if isTerminal(active.Status) {
		return Run{}, fmt.Errorf("run is no longer active")
	}
	if latest, exists, latestErr := s.runs.LatestForConversation(ctx, active.ConversationID); latestErr != nil {
		return Run{}, latestErr
	} else if !exists || latest.ID != active.ID || isTerminal(latest.Status) {
		return Run{}, fmt.Errorf("run is no longer the active conversation run")
	}
	if err := s.Cancel(ctx, active.ID); err != nil {
		return Run{}, err
	}
	if err := s.waitInactive(ctx, active.ID, 5*time.Second); err != nil {
		return Run{}, err
	}
	return s.start(ctx, cmd, active.ID)
}

func (s *Service) waitInactive(ctx context.Context, runID string, maximum time.Duration) error {
	timer := time.NewTimer(maximum)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		active := s.active[runID] != nil
		s.mu.Unlock()
		if !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("current run did not stop in time; please retry")
		case <-ticker.C:
		}
	}
}

// Resume schedules a Run only after PermissionCoordinator has atomically moved
// it back to running. approvalID is the idempotency key for one approval cycle:
// duplicate UI replies coalesce, while a later approval may resume the same Run.
func (s *Service) Resume(_ context.Context, runID, approvalID string) error {
	runID, approvalID = strings.TrimSpace(runID), strings.TrimSpace(approvalID)
	if runID == "" || approvalID == "" {
		return fmt.Errorf("run id and approval id are required")
	}
	run, err := s.runs.Get(context.Background(), runID)
	if err != nil {
		return err
	}
	if run.Status != RunRunning {
		return fmt.Errorf("run is not ready to resume")
	}
	s.mu.Lock()
	if s.lastResumeKey[run.ID] == approvalID {
		s.mu.Unlock()
		return nil
	}
	if s.active[run.ID] != nil {
		s.pendingResume[run.ID] = true
		s.lastResumeKey[run.ID] = approvalID
		s.mu.Unlock()
		return nil
	}
	err = s.launchLocked(run.ID, true)
	if err == nil {
		s.lastResumeKey[run.ID] = approvalID
	}
	s.mu.Unlock()
	return err
}

func (s *Service) Cancel(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id is required")
	}
	s.mu.Lock()
	cancel := s.active[runID]
	terminator := s.terminator
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return err
	}
	if isTerminal(run.Status) {
		return nil
	}
	if terminator == nil {
		if cancel != nil {
			return nil
		}
		return fmt.Errorf("run is not active")
	}
	if _, err = terminator.Cancel(ctx, run.ID); err != nil {
		return err
	}
	return nil
}

func (s *Service) Snapshot(ctx context.Context, runID string) (Snapshot, error) {
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return Snapshot{}, err
	}
	messages, err := s.conversations.ListMessages(ctx, run.ConversationID, 200)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Run: run, Messages: messages, ToolCalls: []tool.Call{}}
	s.mu.Lock()
	toolCalls := s.toolCalls
	s.mu.Unlock()
	if toolCalls != nil {
		snapshot.ToolCalls, err = toolCalls.ListByRun(ctx, run.ID)
		if err != nil {
			return Snapshot{}, err
		}
	}
	return snapshot, nil
}

func (s *Service) LatestSnapshot(ctx context.Context, conversationID string) (*Snapshot, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, fmt.Errorf("conversation id is required")
	}
	run, exists, err := s.runs.LatestForConversation(ctx, conversationID)
	if err != nil || !exists {
		return nil, err
	}
	snapshot, err := s.Snapshot(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Service) UsageStatistics(ctx context.Context, modelProfileID string) (UsageStatistics, error) {
	modelProfileID = strings.TrimSpace(modelProfileID)
	if modelProfileID == "" {
		return UsageStatistics{}, fmt.Errorf("model profile id is required")
	}
	return s.runs.UsageStatistics(ctx, modelProfileID)
}

func (s *Service) Close() {
	s.mu.Lock()
	s.closing = true
	clear(s.pendingResume)
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) launch(runID string, resume bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launchLocked(runID, resume)
}

func (s *Service) launchLocked(runID string, resume bool) error {
	if s.closing {
		return fmt.Errorf("chat service is closing")
	}
	if s.runner == nil {
		return fmt.Errorf("chat run executor is not configured")
	}
	if _, exists := s.active[runID]; exists {
		if resume {
			if !s.pendingResume[runID] {
				s.pendingResume[runID] = true
			}
			return nil
		}
		return fmt.Errorf("run is already active")
	}
	runner := s.runner
	runCtx, cancel := context.WithCancel(context.Background())
	s.active[runID] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.finishActive(runID)
		if resume {
			runner.ResumeExecute(runCtx, runID)
			return
		}
		runner.Execute(runCtx, runID)
	}()
	return nil
}

func (s *Service) currentRunner() RunExecutor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runner
}

func (s *Service) emit(ctx context.Context, runID, eventType string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	eventID, err := id.New()
	if err != nil {
		return
	}
	envelope := events.New(eventID, runID, "run", eventType, 0, data)
	envelope, err = s.events.AppendNext(ctx, envelope)
	if err != nil {
		return
	}
	if s.publisher != nil {
		s.publisher.Publish(ctx, envelope)
	}
}

func (s *Service) finishActive(runID string) {
	s.mu.Lock()
	delete(s.active, runID)
	resume := s.pendingResume[runID] && !s.closing
	delete(s.pendingResume, runID)
	s.mu.Unlock()
	if resume {
		s.mu.Lock()
		_ = s.launchLocked(runID, true)
		s.mu.Unlock()
	}
}

func isTerminal(status RunStatus) bool {
	return status == RunCompleted || status == RunFailed || status == RunCancelled || status == RunInterrupted
}

func (s *Service) PublishRunEvent(runID, eventType string, payload any) {
	s.emit(context.Background(), runID, eventType, payload)
}

// Kept package-local for focused context-window regression tests. Production
// requests are built by agent.ContextBuilder.
func buildRequest(messages []conversation.Message, excludedMessageID string, maxChars int) model.ChatRequest {
	reversed := make([]model.Message, 0, len(messages))
	used := 0
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.ID == excludedMessageID {
			continue
		}
		var text strings.Builder
		for _, part := range message.Parts {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
		if text.Len() == 0 {
			continue
		}
		length := len([]rune(text.String()))
		if used > 0 && used+length > maxChars {
			break
		}
		reversed = append(reversed, model.Message{Role: model.Role(message.Role), Content: text.String()})
		used += length
	}
	request := model.ChatRequest{Messages: make([]model.Message, len(reversed))}
	for index := range reversed {
		request.Messages[len(reversed)-1-index] = reversed[index]
	}
	return request
}

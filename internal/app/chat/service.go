package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
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
	Run      Run                    `json:"run"`
	Messages []conversation.Message `json:"messages"`
}

type Service struct {
	runs          Repository
	conversations ConversationRepository
	events        EventRepository
	publisher     Publisher
	now           func() time.Time

	mu            sync.Mutex
	runner        RunExecutor
	active        map[string]context.CancelFunc
	pendingResume map[string]bool
	closing       bool
	wg            sync.WaitGroup
}

func NewService(runs Repository, conversations ConversationRepository, eventRepository EventRepository, publisher Publisher) *Service {
	return &Service{runs: runs, conversations: conversations, events: eventRepository, publisher: publisher, now: func() time.Time { return time.Now().UTC() }, active: make(map[string]context.CancelFunc), pendingResume: make(map[string]bool)}
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
	run := Run{ID: runID, ConversationID: cmd.ConversationID, UserMessageID: userID, AssistantMessageID: assistantID, ModelProfileID: cmd.ModelProfileID, ModelID: cmd.ModelID, Status: RunQueued, CreatedAt: now, UpdatedAt: now}
	if err := s.runs.CreateWithMessages(ctx, run, user, assistant); err != nil {
		return Run{}, fmt.Errorf("create chat run: %w", err)
	}
	if err := s.launch(run.ID, false); err != nil {
		return Run{}, err
	}
	return run, nil
}

// Resume schedules a Run only after PermissionCoordinator has atomically moved
// it back to running. It never changes approval or ToolCall state itself.
func (s *Service) Resume(_ context.Context, runID string) error {
	run, err := s.runs.Get(context.Background(), strings.TrimSpace(runID))
	if err != nil {
		return err
	}
	if run.Status != RunRunning {
		return fmt.Errorf("run is not ready to resume")
	}
	return s.launch(run.ID, true)
}

func (s *Service) Cancel(ctx context.Context, runID string) error {
	s.mu.Lock()
	cancel := s.active[runID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		return nil
	}
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return err
	}
	if isTerminal(run.Status) {
		return nil
	}
	return fmt.Errorf("run is not active")
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
	return Snapshot{Run: run, Messages: messages}, nil
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
	if s.closing {
		s.mu.Unlock()
		return fmt.Errorf("chat service is closing")
	}
	if s.runner == nil {
		s.mu.Unlock()
		return fmt.Errorf("chat run executor is not configured")
	}
	if _, exists := s.active[runID]; exists {
		if resume {
			s.pendingResume[runID] = true
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		return fmt.Errorf("run is already active")
	}
	runner := s.runner
	runCtx, cancel := context.WithCancel(context.Background())
	s.active[runID] = cancel
	s.wg.Add(1)
	s.mu.Unlock()
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
	if !resume {
		delete(s.pendingResume, runID)
	}
	s.mu.Unlock()
	if resume {
		if err := s.launch(runID, true); err == nil {
			s.mu.Lock()
			delete(s.pendingResume, runID)
			s.mu.Unlock()
		}
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

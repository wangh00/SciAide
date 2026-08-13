package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/model"
)

type ModelResolver interface {
	Resolve(ctx context.Context, profileID, modelID string) (model.ChatModel, error)
}

type StartCommand struct {
	ConversationID string `json:"conversationId"`
	ModelProfileID string `json:"modelProfileId"`
	ModelID        string `json:"modelId"`
	Text           string `json:"text"`
}

const (
	maxUserMessageChars = 100_000
	maxContextChars     = 120_000
)

type Snapshot struct {
	Run      Run                    `json:"run"`
	Messages []conversation.Message `json:"messages"`
}

type Service struct {
	runs          Repository
	conversations ConversationRepository
	events        EventRepository
	publisher     Publisher
	models        ModelResolver
	now           func() time.Time

	mu     sync.Mutex
	active map[string]context.CancelFunc
	wg     sync.WaitGroup
}

func NewService(runs Repository, conversations ConversationRepository, eventRepository EventRepository, publisher Publisher, models ModelResolver) *Service {
	return &Service{runs: runs, conversations: conversations, events: eventRepository, publisher: publisher, models: models, now: func() time.Time { return time.Now().UTC() }, active: make(map[string]context.CancelFunc)}
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
	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.active[run.ID] = cancel
	s.mu.Unlock()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); defer s.removeActive(run.ID); s.execute(runCtx, run) }()
	return run, nil
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
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) execute(ctx context.Context, run Run) {
	sequence := int64(0)
	emit := func(eventType string, payload any) {
		sequence++
		s.emit(context.Background(), run.ID, sequence, eventType, payload)
	}
	now := s.now()
	run.Status = RunRunning
	run.StartedAt = &now
	run.UpdatedAt = now
	if err := s.runs.Update(context.Background(), run); err != nil {
		return
	}
	emit("run.started", map[string]any{"runId": run.ID, "status": run.Status})
	emit("content.started", map[string]any{"runId": run.ID, "messageId": run.AssistantMessageID})

	messages, err := s.conversations.ListMessages(ctx, run.ConversationID, 200)
	if err != nil {
		s.fail(run, "CONTEXT_LOAD_FAILED", "无法加载会话上下文。", "", emit)
		return
	}
	request := buildRequest(messages, run.AssistantMessageID, maxContextChars)
	chatModel, err := s.models.Resolve(ctx, run.ModelProfileID, run.ModelID)
	if err != nil {
		s.finishError(run, err, "", emit)
		return
	}
	stream, err := chatModel.Stream(ctx, request)
	if err != nil {
		s.finishError(run, err, "", emit)
		return
	}
	defer stream.Close()

	var text, pending string
	lastPersist, lastEmit := s.now(), s.now()
	for {
		event, recvErr := stream.Recv()
		if event.Type == model.EventTextDelta && event.Text != "" {
			text += event.Text
			pending += event.Text
			now = s.now()
			if len(pending) >= 64 || now.Sub(lastEmit) >= 35*time.Millisecond {
				emit("content.delta", map[string]any{"messageId": run.AssistantMessageID, "delta": pending})
				pending = ""
				lastEmit = now
			}
			if len(text) >= 256 || now.Sub(lastPersist) >= 200*time.Millisecond {
				_ = s.conversations.UpdateMessageText(context.Background(), run.AssistantMessageID, conversation.MessageStreaming, text, now)
				lastPersist = now
			}
		}
		if event.Type == model.EventUsage && event.Usage != nil {
			run.InputTokens = event.Usage.InputTokens
			run.OutputTokens = event.Usage.OutputTokens
			emit("usage.updated", event.Usage)
		}
		if event.FinishReason != "" {
			run.FinishReason = event.FinishReason
		}
		if recvErr != nil {
			if errors.Is(recvErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				if pending != "" {
					emit("content.delta", map[string]any{"messageId": run.AssistantMessageID, "delta": pending})
				}
				s.cancelled(run, text, emit)
				return
			}
			if errors.Is(recvErr, io.EOF) {
				break
			}
			s.finishError(run, recvErr, text, emit)
			return
		}
		if event.Type == model.EventDone {
			break
		}
	}
	if pending != "" {
		emit("content.delta", map[string]any{"messageId": run.AssistantMessageID, "delta": pending})
	}
	now = s.now()
	if err := s.conversations.UpdateMessageText(context.Background(), run.AssistantMessageID, conversation.MessageComplete, text, now); err != nil {
		s.fail(run, "MESSAGE_SAVE_FAILED", "回答已生成，但保存失败。", text, emit)
		return
	}
	run.Status = RunCompleted
	run.UpdatedAt = now
	run.CompletedAt = &now
	if err := s.runs.Update(context.Background(), run); err != nil {
		return
	}
	emit("content.completed", map[string]any{"messageId": run.AssistantMessageID, "text": text})
	emit("run.completed", map[string]any{"run": run})
}

func (s *Service) cancelled(run Run, text string, emit func(string, any)) {
	now := s.now()
	_ = s.conversations.UpdateMessageText(context.Background(), run.AssistantMessageID, conversation.MessageIncomplete, text, now)
	run.Status = RunCancelled
	run.ErrorCode = "RUN_CANCELLED"
	run.ErrorMessage = "已停止生成"
	run.UpdatedAt = now
	run.CompletedAt = &now
	_ = s.runs.Update(context.Background(), run)
	emit("run.cancelled", map[string]any{"run": run})
}

func (s *Service) finishError(run Run, err error, text string, emit func(string, any)) {
	public := apperr.Public(err)
	s.fail(run, public.Code, public.Message, text, emit)
}

func (s *Service) fail(run Run, code, message, text string, emit func(string, any)) {
	now := s.now()
	status := conversation.MessageFailed
	if text != "" {
		status = conversation.MessageIncomplete
	}
	_ = s.conversations.UpdateMessageText(context.Background(), run.AssistantMessageID, status, text, now)
	run.Status = RunFailed
	run.ErrorCode = code
	run.ErrorMessage = message
	run.UpdatedAt = now
	run.CompletedAt = &now
	_ = s.runs.Update(context.Background(), run)
	emit("run.failed", map[string]any{"run": run})
}

func (s *Service) emit(ctx context.Context, runID string, sequence int64, eventType string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	eventID, err := id.New()
	if err != nil {
		return
	}
	envelope := events.New(eventID, runID, "run", eventType, sequence, data)
	envelope, err = s.events.AppendNext(ctx, envelope)
	if err != nil {
		return
	}
	if s.publisher != nil {
		s.publisher.Publish(ctx, envelope)
	}
}

func (s *Service) removeActive(runID string) { s.mu.Lock(); delete(s.active, runID); s.mu.Unlock() }
func isTerminal(status RunStatus) bool {
	return status == RunCompleted || status == RunFailed || status == RunCancelled || status == RunInterrupted
}
func messageText(message conversation.Message) string {
	var builder strings.Builder
	for _, part := range message.Parts {
		if part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func buildRequest(messages []conversation.Message, excludedMessageID string, maxChars int) model.ChatRequest {
	reversed := make([]model.Message, 0, len(messages))
	used := 0
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.ID == excludedMessageID {
			continue
		}
		text := messageText(message)
		if text == "" {
			continue
		}
		length := len([]rune(text))
		if used > 0 && used+length > maxChars {
			break
		}
		reversed = append(reversed, model.Message{Role: model.Role(message.Role), Content: text})
		used += length
	}
	request := model.ChatRequest{Messages: make([]model.Message, len(reversed))}
	for index := range reversed {
		request.Messages[len(reversed)-1-index] = reversed[index]
	}
	return request
}

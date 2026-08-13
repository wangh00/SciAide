package chat

import (
	"context"
	"errors"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/events"
)

var ErrModelTurnBudgetExceeded = errors.New("model turn budget exceeded")

type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunCompleted       RunStatus = "completed"
	RunFailed          RunStatus = "failed"
	RunCancelled       RunStatus = "cancelled"
	RunInterrupted     RunStatus = "interrupted"
)

type Run struct {
	ID                 string                      `json:"id"`
	ConversationID     string                      `json:"conversationId"`
	UserMessageID      string                      `json:"userMessageId"`
	AssistantMessageID string                      `json:"assistantMessageId,omitempty"`
	ModelProfileID     string                      `json:"modelProfileId"`
	ModelID            string                      `json:"modelId"`
	PermissionMode     conversation.PermissionMode `json:"permissionMode"`
	Status             RunStatus                   `json:"status"`
	ErrorCode          string                      `json:"errorCode,omitempty"`
	ErrorMessage       string                      `json:"errorMessage,omitempty"`
	InputTokens        int                         `json:"inputTokens"`
	OutputTokens       int                         `json:"outputTokens"`
	ModelTurns         int                         `json:"modelTurns"`
	FinishReason       string                      `json:"finishReason,omitempty"`
	CreatedAt          time.Time                   `json:"createdAt"`
	StartedAt          *time.Time                  `json:"startedAt,omitempty"`
	CompletedAt        *time.Time                  `json:"completedAt,omitempty"`
	UpdatedAt          time.Time                   `json:"updatedAt"`
}

type Repository interface {
	CreateWithMessages(ctx context.Context, value Run, userMessage, assistantMessage conversation.Message) error
	Get(ctx context.Context, id string) (Run, error)
	LatestForConversation(ctx context.Context, conversationID string) (Run, bool, error)
	Update(ctx context.Context, value Run) error
	IncrementModelTurns(ctx context.Context, runID string, maximum int, at time.Time) (Run, error)
	CancelRun(ctx context.Context, runID, errorCode, errorMessage string, at time.Time, event events.Envelope) (Run, bool, error)
	InterruptActive(ctx context.Context, at time.Time) (int64, error)
}

type ConversationRepository interface {
	GetConversation(ctx context.Context, id string) (conversation.Conversation, error)
	UpdateMessageText(ctx context.Context, messageID string, status conversation.MessageStatus, text string, updatedAt time.Time) error
	ListMessages(ctx context.Context, conversationID string, limit int) ([]conversation.Message, error)
}

type EventRepository interface {
	AppendNext(ctx context.Context, event events.Envelope) (events.Envelope, error)
}

type Publisher interface {
	Publish(ctx context.Context, event events.Envelope)
}

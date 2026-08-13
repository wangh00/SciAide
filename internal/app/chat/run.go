package chat

import (
	"context"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/events"
)

type RunStatus string

const (
	RunQueued      RunStatus = "queued"
	RunRunning     RunStatus = "running"
	RunCompleted   RunStatus = "completed"
	RunFailed      RunStatus = "failed"
	RunCancelled   RunStatus = "cancelled"
	RunInterrupted RunStatus = "interrupted"
)

type Run struct {
	ID                 string     `json:"id"`
	ConversationID     string     `json:"conversationId"`
	UserMessageID      string     `json:"userMessageId"`
	AssistantMessageID string     `json:"assistantMessageId,omitempty"`
	ModelProfileID     string     `json:"modelProfileId"`
	ModelID            string     `json:"modelId"`
	Status             RunStatus  `json:"status"`
	ErrorCode          string     `json:"errorCode,omitempty"`
	ErrorMessage       string     `json:"errorMessage,omitempty"`
	InputTokens        int        `json:"inputTokens"`
	OutputTokens       int        `json:"outputTokens"`
	FinishReason       string     `json:"finishReason,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	StartedAt          *time.Time `json:"startedAt,omitempty"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type Repository interface {
	CreateWithMessages(ctx context.Context, value Run, userMessage, assistantMessage conversation.Message) error
	Get(ctx context.Context, id string) (Run, error)
	Update(ctx context.Context, value Run) error
	InterruptActive(ctx context.Context, at time.Time) (int64, error)
}

type ConversationRepository interface {
	UpdateMessageText(ctx context.Context, messageID string, status conversation.MessageStatus, text string, updatedAt time.Time) error
	ListMessages(ctx context.Context, conversationID string, limit int) ([]conversation.Message, error)
}

type EventRepository interface {
	Append(ctx context.Context, event events.Envelope) error
}

type Publisher interface {
	Publish(ctx context.Context, event events.Envelope)
}

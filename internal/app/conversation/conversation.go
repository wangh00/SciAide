package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/id"
)

type Conversation struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type MessageStatus string

const (
	MessageComplete   MessageStatus = "complete"
	MessageStreaming  MessageStatus = "streaming"
	MessageIncomplete MessageStatus = "incomplete"
	MessageFailed     MessageStatus = "failed"
)

type Message struct {
	ID             string        `json:"id"`
	ConversationID string        `json:"conversationId"`
	RunID          string        `json:"runId,omitempty"`
	Role           Role          `json:"role"`
	Status         MessageStatus `json:"status"`
	Parts          []MessagePart `json:"parts"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

type MessagePart struct {
	ID        string          `json:"id"`
	MessageID string          `json:"messageId"`
	Ordinal   int             `json:"ordinal"`
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Repository interface {
	CreateConversation(ctx context.Context, value Conversation) error
	GetConversation(ctx context.Context, id string) (Conversation, error)
	ListConversations(ctx context.Context, projectID string) ([]Conversation, error)
	CreateMessage(ctx context.Context, value Message) error
	UpdateMessageText(ctx context.Context, messageID string, status MessageStatus, text string, updatedAt time.Time) error
	ListMessages(ctx context.Context, conversationID string, limit int) ([]Message, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Create(ctx context.Context, projectID, title string) (Conversation, error) {
	projectID = strings.TrimSpace(projectID)
	title = strings.TrimSpace(title)
	if projectID == "" || title == "" {
		return Conversation{}, fmt.Errorf("project id and conversation title are required")
	}
	conversationID, err := id.New()
	if err != nil {
		return Conversation{}, err
	}
	now := s.now()
	value := Conversation{ID: conversationID, ProjectID: projectID, Title: title, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.CreateConversation(ctx, value); err != nil {
		return Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return value, nil
}

func (s *Service) List(ctx context.Context, projectID string) ([]Conversation, error) {
	return s.repository.ListConversations(ctx, projectID)
}

func (s *Service) Messages(ctx context.Context, conversationID string) ([]Message, error) {
	return s.repository.ListMessages(ctx, conversationID, 200)
}

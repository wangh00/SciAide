package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type Conversation struct {
	ID             string                  `json:"id"`
	ProjectID      string                  `json:"projectId"`
	Title          string                  `json:"title"`
	ModelProfileID string                  `json:"modelProfileId"`
	ModelID        string                  `json:"modelId"`
	PermissionMode PermissionMode          `json:"permissionMode"`
	ReasoningLevel modelcap.ReasoningLevel `json:"reasoningLevel"`
	CreatedAt      time.Time               `json:"createdAt"`
	UpdatedAt      time.Time               `json:"updatedAt"`
}

// PermissionMode is a conversation-level user choice. Plan permits only
// low-risk idempotent reads already confined to the current Workspace and asks
// for external reads, writes and other tool calls. Full Access automatically
// authorizes registered tools while keeping schema, workspace, timeout and
// cancellation boundaries intact.
type PermissionMode string

const (
	PermissionPlan       PermissionMode = "plan"
	PermissionFullAccess PermissionMode = "full_access"
)

func (m PermissionMode) Valid() bool {
	return m == PermissionPlan || m == PermissionFullAccess
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
	Citations      []Citation    `json:"citations"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

type Citation struct {
	ID             string    `json:"id"`
	MessageID      string    `json:"messageId"`
	RunID          string    `json:"runId"`
	ToolCallID     string    `json:"toolCallId"`
	ProjectID      string    `json:"projectId"`
	Reference      string    `json:"reference"`
	Ordinal        int       `json:"ordinal"`
	IndexVersionID string    `json:"indexVersionId"`
	DocumentID     string    `json:"documentId"`
	AttachmentID   string    `json:"attachmentId"`
	ChunkID        string    `json:"chunkId"`
	SourceName     string    `json:"sourceName"`
	MIMEType       string    `json:"mimeType,omitempty"`
	Locator        string    `json:"locator"`
	Title          string    `json:"title,omitempty"`
	Quote          string    `json:"quote"`
	QuoteSHA256    string    `json:"quoteSha256"`
	SourceStart    int       `json:"sourceStart"`
	SourceEnd      int       `json:"sourceEnd"`
	CreatedAt      time.Time `json:"createdAt"`
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
	UpdateModelSelection(ctx context.Context, conversationID, modelProfileID, modelID string, updatedAt time.Time) error
	UpdatePermissionMode(ctx context.Context, conversationID string, mode PermissionMode, updatedAt time.Time) error
	UpdateReasoningLevel(ctx context.Context, conversationID string, level modelcap.ReasoningLevel, updatedAt time.Time) error
	DeleteConversation(ctx context.Context, id string) error
}

func (s *Service) Remove(ctx context.Context, conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fmt.Errorf("conversation id is required")
	}
	if err := s.repository.DeleteConversation(ctx, conversationID); err != nil {
		return fmt.Errorf("remove conversation: %w", err)
	}
	return nil
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
	value := Conversation{ID: conversationID, ProjectID: projectID, Title: title, PermissionMode: PermissionPlan, ReasoningLevel: modelcap.ReasoningMedium, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.CreateConversation(ctx, value); err != nil {
		return Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return value, nil
}

func (s *Service) SetReasoningLevel(ctx context.Context, conversationID string, level modelcap.ReasoningLevel) (Conversation, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" || !level.Valid() {
		return Conversation{}, fmt.Errorf("conversation id and valid reasoning level are required")
	}
	now := s.now()
	if err := s.repository.UpdateReasoningLevel(ctx, conversationID, level, now); err != nil {
		return Conversation{}, fmt.Errorf("update conversation reasoning level: %w", err)
	}
	return s.repository.GetConversation(ctx, conversationID)
}

func (s *Service) SetModelSelection(ctx context.Context, conversationID, modelProfileID, modelID string) (Conversation, error) {
	conversationID = strings.TrimSpace(conversationID)
	modelProfileID = strings.TrimSpace(modelProfileID)
	modelID = strings.TrimSpace(modelID)
	if conversationID == "" || modelProfileID == "" || modelID == "" {
		return Conversation{}, fmt.Errorf("conversation, model profile and model are required")
	}
	now := s.now()
	if err := s.repository.UpdateModelSelection(ctx, conversationID, modelProfileID, modelID, now); err != nil {
		return Conversation{}, fmt.Errorf("update conversation model selection: %w", err)
	}
	return s.repository.GetConversation(ctx, conversationID)
}

func (s *Service) List(ctx context.Context, projectID string) ([]Conversation, error) {
	return s.repository.ListConversations(ctx, projectID)
}

func (s *Service) Messages(ctx context.Context, conversationID string) ([]Message, error) {
	return s.repository.ListMessages(ctx, conversationID, 200)
}

func (s *Service) Get(ctx context.Context, conversationID string) (Conversation, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return Conversation{}, fmt.Errorf("conversation id is required")
	}
	return s.repository.GetConversation(ctx, conversationID)
}

func (s *Service) SetPermissionMode(ctx context.Context, conversationID string, mode PermissionMode) (Conversation, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" || !mode.Valid() {
		return Conversation{}, fmt.Errorf("conversation id and valid permission mode are required")
	}
	now := s.now()
	if err := s.repository.UpdatePermissionMode(ctx, conversationID, mode, now); err != nil {
		return Conversation{}, fmt.Errorf("update conversation permission mode: %w", err)
	}
	return s.repository.GetConversation(ctx, conversationID)
}

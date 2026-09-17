package attachment

import (
	"context"
	"time"

	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/model"
)

type Status string

type ScopeKind string

const (
	ScopeTask          ScopeKind = "task"
	ScopeProjectShared ScopeKind = "project_shared"
	ScopeLegacyProject ScopeKind = "legacy_project"
	// ScopeConversation is used for files uploaded directly in a chat. These
	// files are available only to that conversation and are never implicit
	// inputs of a research task or the project knowledge base.
	ScopeConversation ScopeKind = "conversation"
)

// SourceKind describes how an attachment entered SciAide. It is separate
// from ScopeKind: a task-owned file may be a user import or a materialized
// literature source, while conversation uploads remain ephemeral.
type SourceKind string

const (
	SourceUnknown        SourceKind = "unknown"
	SourceUserImport     SourceKind = "user_import"
	SourceResearchImport SourceKind = "research_import"
	SourceConversation   SourceKind = "conversation_upload"
)

const (
	StatusParsing Status = "parsing"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
)

type Attachment struct {
	ID                  string            `json:"id"`
	ProjectID           string            `json:"projectId"`
	ScopeKind           ScopeKind         `json:"scopeKind"`
	ResearchTaskID      string            `json:"researchTaskId,omitempty"`
	SourceKind          SourceKind        `json:"sourceKind"`
	OriginalName        string            `json:"originalName"`
	MIMEType            string            `json:"mimeType"`
	Format              document.Format   `json:"format"`
	SizeBytes           int64             `json:"sizeBytes"`
	SHA256              string            `json:"sha256"`
	StorageRelativePath string            `json:"-"`
	CacheRelativePath   string            `json:"-"`
	Status              Status            `json:"status"`
	UnitCount           int               `json:"unitCount"`
	ExtractedRunes      int               `json:"extractedRunes"`
	Truncated           bool              `json:"truncated"`
	ParseMetadata       map[string]string `json:"parseMetadata,omitempty"`
	ErrorMessage        string            `json:"errorMessage,omitempty"`
	CreatedAt           time.Time         `json:"createdAt"`
	UpdatedAt           time.Time         `json:"updatedAt"`
}

type MessageReference struct {
	AttachmentID string          `json:"attachmentId"`
	OriginalName string          `json:"originalName"`
	MIMEType     string          `json:"mimeType"`
	Format       document.Format `json:"format"`
	SizeBytes    int64           `json:"sizeBytes"`
	UnitCount    int             `json:"unitCount"`
	Truncated    bool            `json:"truncated"`
}

type ImportError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type ImportBatch struct {
	Attachments []Attachment  `json:"attachments"`
	Errors      []ImportError `json:"errors"`
}

type Repository interface {
	Create(ctx context.Context, value Attachment) error
	Get(ctx context.Context, id string) (Attachment, error)
	FindByHash(ctx context.Context, projectID, sha256 string) (Attachment, bool, error)
	ListByProject(ctx context.Context, projectID string) ([]Attachment, error)
	UpdateParse(ctx context.Context, value Attachment) error
}

// ScopedHashRepository prevents identical bytes imported into two research
// tasks from resolving to the first task's attachment record. Implementations
// may share the immutable object bytes while keeping ownership rows separate.
type ScopedHashRepository interface {
	FindByHashInScope(ctx context.Context, projectID, sha256 string, scopeKind ScopeKind, researchTaskID string) (Attachment, bool, error)
}

type ConversationAttachmentLoader interface {
	ListForConversation(ctx context.Context, projectID, conversationID string) ([]Attachment, error)
}

type ConversationAttachmentResolver interface {
	ResolveForConversation(ctx context.Context, projectID, conversationID string, ids []string) ([]MessageReference, error)
}

type ConversationImageResolver interface {
	ResolveImageForConversation(ctx context.Context, projectID, conversationID, attachmentID string) (model.ContentPart, error)
}

type TaskAttachmentParser interface {
	ParsedForTask(ctx context.Context, projectID, taskID, attachmentID string) (Attachment, document.Parsed, error)
}

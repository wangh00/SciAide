package attachment

import (
	"context"
	"time"

	"github.com/wangh00/SciAide/internal/document"
)

type Status string

const (
	StatusParsing Status = "parsing"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
)

type Attachment struct {
	ID                  string          `json:"id"`
	ProjectID           string          `json:"projectId"`
	OriginalName        string          `json:"originalName"`
	MIMEType            string          `json:"mimeType"`
	Format              document.Format `json:"format"`
	SizeBytes           int64           `json:"sizeBytes"`
	SHA256              string          `json:"sha256"`
	StorageRelativePath string          `json:"-"`
	CacheRelativePath   string          `json:"-"`
	Status              Status          `json:"status"`
	UnitCount           int             `json:"unitCount"`
	ExtractedRunes      int             `json:"extractedRunes"`
	Truncated           bool            `json:"truncated"`
	ErrorMessage        string          `json:"errorMessage,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
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

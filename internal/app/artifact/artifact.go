// Package artifact manages immutable, project-scoped research outputs.
package artifact

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type Kind string

const (
	KindDocument Kind = "document"
	KindData     Kind = "data"
	KindImage    Kind = "image"
	KindCode     Kind = "code"
	KindOther    Kind = "other"
)

type Status string

type ScopeKind string

const (
	ScopeTask          ScopeKind = "task"
	ScopeProjectShared ScopeKind = "project_shared"
	ScopeLegacyProject ScopeKind = "legacy_project"
)

const (
	StatusActive  Status = "active"
	StatusTrashed Status = "trashed"
)

type SourceKind string

const (
	SourceAssistantMessage SourceKind = "assistant_message"
	SourceWorkspaceFile    SourceKind = "workspace_file"
	SourceTool             SourceKind = "tool"
)

type Artifact struct {
	ID               string     `json:"id"`
	ProjectID        string     `json:"projectId"`
	ScopeKind        ScopeKind  `json:"scopeKind"`
	ResearchTaskID   string     `json:"researchTaskId,omitempty"`
	Name             string     `json:"name"`
	Kind             Kind       `json:"kind"`
	Status           Status     `json:"status"`
	CurrentVersionID string     `json:"currentVersionId"`
	CurrentVersion   *Version   `json:"currentVersion,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	TrashedAt        *time.Time `json:"trashedAt,omitempty"`
}

type Version struct {
	ID            string     `json:"id"`
	ArtifactID    string     `json:"artifactId"`
	BlobID        string     `json:"-"`
	VersionNumber int        `json:"versionNumber"`
	FileName      string     `json:"fileName"`
	MIMEType      string     `json:"mimeType"`
	SizeBytes     int64      `json:"sizeBytes"`
	SHA256        string     `json:"sha256"`
	SourceKind    SourceKind `json:"sourceKind"`
	SourceKey     string     `json:"-"`
	Provenance    Provenance `json:"provenance"`
	Lineage       []Lineage  `json:"lineage"`
	Citations     []Citation `json:"citations"`
	Exports       []Export   `json:"exports"`
	CreatedAt     time.Time  `json:"createdAt"`
}

type ExportFormat string

const (
	ExportDOCX ExportFormat = "docx"
	ExportPDF  ExportFormat = "pdf"
)

type CitationStyle string

const (
	CitationGB7714 CitationStyle = "gb_t_7714_2015"
	CitationAPA7   CitationStyle = "apa_7"
)

// Export is an immutable, derived rendering of one immutable ArtifactVersion.
// It is deliberately separate from Version so exporting can never replace or
// silently advance the source Artifact.
type Export struct {
	ID                string        `json:"id"`
	ProjectID         string        `json:"projectId"`
	ArtifactVersionID string        `json:"artifactVersionId"`
	BlobID            string        `json:"-"`
	Format            ExportFormat  `json:"format"`
	CitationStyle     CitationStyle `json:"citationStyle"`
	GeneratorVersion  string        `json:"generatorVersion"`
	FileName          string        `json:"fileName"`
	MIMEType          string        `json:"mimeType"`
	SizeBytes         int64         `json:"sizeBytes"`
	SHA256            string        `json:"sha256"`
	SourceSHA256      string        `json:"sourceSha256"`
	CreatedAt         time.Time     `json:"createdAt"`
}

type ExportCommand struct {
	ProjectID     string        `json:"projectId"`
	VersionID     string        `json:"versionId"`
	Format        ExportFormat  `json:"format"`
	CitationStyle CitationStyle `json:"citationStyle"`
}

type ExportResult struct {
	Export  Export `json:"export"`
	Created bool   `json:"created"`
}

type WorkflowReportCommand struct {
	ProjectID            string
	WorkflowRunID        string
	ResearchTaskID       string
	ToolCallID           string
	ToolName             string
	ToolVersion          string
	OperationKey         string
	Name                 string
	Markdown             string
	Citations            []Citation
	SourceToolCallIDs    []string
	SourceWorkspaceFiles []string
}

type WorkflowReportResult struct {
	Artifact Artifact `json:"artifact"`
	Version  Version  `json:"version"`
	DOCX     Export   `json:"docx"`
	PDF      Export   `json:"pdf"`
	Created  bool     `json:"created"`
}

// WorkflowDeliverableCommand freezes a deterministic, host-rendered output
// from a completed Workflow Run. Callers must derive Markdown from the
// persisted Run output instead of accepting editable client content.
type WorkflowDeliverableCommand struct {
	ProjectID      string
	WorkflowRunID  string
	ResearchTaskID string
	OutputName     string
	OutputSHA256   string
	Name           string
	Markdown       string
	Citations      []Citation
}

type Provenance struct {
	SchemaVersion         int                  `json:"schemaVersion"`
	ProjectID             string               `json:"projectId"`
	SourceKind            SourceKind           `json:"sourceKind"`
	ConversationID        string               `json:"conversationId,omitempty"`
	ConversationTitle     string               `json:"conversationTitle,omitempty"`
	RunID                 string               `json:"runId,omitempty"`
	WorkflowRunID         string               `json:"workflowRunId,omitempty"`
	MessageID             string               `json:"messageId,omitempty"`
	ToolCallID            string               `json:"toolCallId,omitempty"`
	ToolName              string               `json:"toolName,omitempty"`
	ToolVersion           string               `json:"toolVersion,omitempty"`
	ModelProfileID        string               `json:"modelProfileId,omitempty"`
	ModelProfileName      string               `json:"modelProfileName,omitempty"`
	ModelID               string               `json:"modelId,omitempty"`
	APIProtocol           modelcap.APIProtocol `json:"apiProtocol,omitempty"`
	WorkspaceRelativePath string               `json:"workspaceRelativePath,omitempty"`
	WorkspaceModifiedAt   *time.Time           `json:"workspaceModifiedAt,omitempty"`
	Skills                []SkillSnapshot      `json:"skills"`
	Extra                 map[string]string    `json:"extra,omitempty"`
}

type SkillSnapshot struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	ContentHash string `json:"contentHash"`
	PackageHash string `json:"packageHash"`
	Origin      string `json:"origin,omitempty"`
	Dynamic     bool   `json:"dynamic,omitempty"`
}

type Lineage struct {
	ID                      string          `json:"id"`
	ArtifactVersionID       string          `json:"artifactVersionId"`
	Ordinal                 int             `json:"ordinal"`
	RelationKind            string          `json:"relationKind"`
	SourceIDSnapshot        string          `json:"sourceIdSnapshot"`
	SourceRunID             string          `json:"sourceRunId,omitempty"`
	SourceWorkflowRunID     string          `json:"sourceWorkflowRunId,omitempty"`
	SourceMessageID         string          `json:"sourceMessageId,omitempty"`
	SourceToolCallID        string          `json:"sourceToolCallId,omitempty"`
	SourceArtifactVersionID string          `json:"sourceArtifactVersionId,omitempty"`
	Label                   string          `json:"label,omitempty"`
	Metadata                json.RawMessage `json:"metadata,omitempty"`
	CreatedAt               time.Time       `json:"createdAt"`
}

type Citation struct {
	ID                       string          `json:"id"`
	ArtifactVersionID        string          `json:"artifactVersionId"`
	Ordinal                  int             `json:"ordinal"`
	Reference                string          `json:"reference"`
	SourceRunIDSnapshot      string          `json:"sourceRunId,omitempty"`
	SourceMessageIDSnapshot  string          `json:"sourceMessageId,omitempty"`
	SourceToolCallIDSnapshot string          `json:"sourceToolCallId,omitempty"`
	IndexVersionID           string          `json:"indexVersionId,omitempty"`
	DocumentID               string          `json:"documentId,omitempty"`
	AttachmentID             string          `json:"attachmentId,omitempty"`
	ChunkID                  string          `json:"chunkId,omitempty"`
	SourceName               string          `json:"sourceName"`
	MIMEType                 string          `json:"mimeType,omitempty"`
	Locator                  string          `json:"locator,omitempty"`
	Title                    string          `json:"title,omitempty"`
	Quote                    string          `json:"quote"`
	QuoteSHA256              string          `json:"quoteSha256"`
	SourceStart              int             `json:"sourceStart"`
	SourceEnd                int             `json:"sourceEnd"`
	BibliographyIDSnapshot   string          `json:"bibliographyId,omitempty"`
	BibliographySnapshot     json.RawMessage `json:"bibliography,omitempty"`
	EvidenceLevel            string          `json:"evidenceLevel,omitempty"`
	CreatedAt                time.Time       `json:"createdAt"`
}

type Detail struct {
	Artifact Artifact  `json:"artifact"`
	Versions []Version `json:"versions"`
}

type SaveResult struct {
	Artifact Artifact `json:"artifact"`
	Version  Version  `json:"version"`
	Created  bool     `json:"created"`
}

type Preview struct {
	VersionID string             `json:"versionId"`
	Kind      string             `json:"kind"`
	Text      string             `json:"text,omitempty"`
	Data      string             `json:"data,omitempty"`
	MIMEType  string             `json:"mimeType"`
	Truncated bool               `json:"truncated"`
	Document  *StructuredPreview `json:"document,omitempty"`
}

type PreviewBlock struct {
	Kind    string     `json:"kind"`
	Level   int        `json:"level,omitempty"`
	Text    string     `json:"text,omitempty"`
	Locator string     `json:"locator,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
}

type StructuredPreview struct {
	Title    string            `json:"title,omitempty"`
	Format   string            `json:"format"`
	Blocks   []PreviewBlock    `json:"blocks"`
	Metadata map[string]string `json:"metadata"`
}

type IntegrityStatus string

const (
	IntegrityVerified IntegrityStatus = "verified"
	IntegrityMissing  IntegrityStatus = "missing"
	IntegrityMismatch IntegrityStatus = "mismatch"
)

type IntegrityResult struct {
	ArtifactID     string          `json:"artifactId"`
	VersionID      string          `json:"versionId"`
	Status         IntegrityStatus `json:"status"`
	ExpectedSize   int64           `json:"expectedSize"`
	ActualSize     int64           `json:"actualSize"`
	ExpectedSHA256 string          `json:"expectedSha256"`
	ActualSHA256   string          `json:"actualSha256,omitempty"`
	Message        string          `json:"message,omitempty"`
	CheckedAt      time.Time       `json:"checkedAt"`
}

type RegisterWorkspaceCommand struct {
	ProjectID      string    `json:"projectId"`
	Path           string    `json:"path"`
	ArtifactID     string    `json:"artifactId,omitempty"`
	Name           string    `json:"name,omitempty"`
	ScopeKind      ScopeKind `json:"scopeKind,omitempty"`
	ResearchTaskID string    `json:"researchTaskId,omitempty"`
}

type BlobRecord struct {
	ID                  string
	ProjectID           string
	SHA256              string
	SizeBytes           int64
	MIMEType            string
	StorageRelativePath string
	CreatedAt           time.Time
}

type CreateVersionRecord struct {
	ArtifactID string
	ProjectID  string
	Name       string
	Kind       Kind
	Blob       BlobRecord
	Version    Version
}

type CreateExportRecord struct {
	Export Export
	Blob   BlobRecord
}

type AssistantSource struct {
	ProjectID         string
	ConversationID    string
	ConversationTitle string
	MessageID         string
	RunID             string
	ResearchTaskID    string
	Text              string
	ModelProfileID    string
	ModelProfileName  string
	ModelID           string
	APIProtocol       modelcap.APIProtocol
	Skills            []SkillSnapshot
	Citations         []Citation
}

type ToolSource struct {
	ProjectID         string
	ConversationID    string
	ConversationTitle string
	RunID             string
	ResearchTaskID    string
	SubjectKind       tool.SubjectKind
	CallID            string
	ToolName          string
	ToolVersion       string
	ModelProfileID    string
	ModelProfileName  string
	ModelID           string
	APIProtocol       modelcap.APIProtocol
	Skills            []SkillSnapshot
	Permissions       []tool.PermissionRequirement
	Artifacts         []tool.ArtifactRef
	Citations         []tool.CitationRef
}

type Repository interface {
	CreateVersion(ctx context.Context, record CreateVersionRecord) (SaveResult, error)
	List(ctx context.Context, projectID string, includeTrashed bool) ([]Artifact, error)
	Get(ctx context.Context, projectID, artifactID string) (Detail, error)
	GetVersion(ctx context.Context, projectID, versionID string) (Version, BlobRecord, error)
	CreateExport(ctx context.Context, record CreateExportRecord) (ExportResult, error)
	GetExport(ctx context.Context, projectID, exportID string) (Export, BlobRecord, error)
	Rename(ctx context.Context, projectID, artifactID, name string, at time.Time) (Artifact, error)
	SetStatus(ctx context.Context, projectID, artifactID string, status Status, at time.Time) (Artifact, error)
	AssistantSource(ctx context.Context, projectID, messageID string) (AssistantSource, error)
	ToolSource(ctx context.Context, callID string) (ToolSource, error)
	RecoverableToolCallIDs(ctx context.Context) ([]string, error)
	BlobPaths(ctx context.Context, projectID string) (map[string]struct{}, error)
}

package knowledge

import (
	"context"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/embedding"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
)

const (
	IndexSchemaVersion = 3
	ChunkingVersion    = "bounded-unit-v2"
	SearchKind         = "lexical_v1" // Kept for migration-24 compatibility.
	RetrievalEngine    = "fts5_bm25_v1"
	HybridBM25Only     = "bm25_only_v1"
	HybridRRF          = "rrf_v1"
)

type IndexSpec struct {
	EmbeddingModel       string
	EmbeddingDimensions  int
	EmbeddingFingerprint string
	HybridStrategy       string
}

func DefaultIndexSpec() IndexSpec { return IndexSpec{HybridStrategy: HybridBM25Only} }

func IndexSpecForEmbedding(value embedding.Identity) IndexSpec {
	return IndexSpec{EmbeddingModel: value.ModelID, EmbeddingDimensions: value.Dimensions, EmbeddingFingerprint: value.Fingerprint, HybridStrategy: HybridRRF}
}

type DocumentStatus string

const (
	DocumentPending  DocumentStatus = "pending"
	DocumentIndexing DocumentStatus = "indexing"
	DocumentReady    DocumentStatus = "ready"
	DocumentFailed   DocumentStatus = "failed"
)

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

type IndexStatus string

const (
	IndexBuilding IndexStatus = "building"
	IndexReady    IndexStatus = "ready"
	IndexRetired  IndexStatus = "retired"
	IndexFailed   IndexStatus = "failed"
)

type Document struct {
	ID                  string         `json:"id"`
	ProjectID           string         `json:"projectId"`
	AttachmentID        string         `json:"attachmentId"`
	IndexVersionID      string         `json:"indexVersionId"`
	Title               string         `json:"title"`
	AttachmentSHA256    string         `json:"attachmentSha256"`
	Status              DocumentStatus `json:"status"`
	ParserSchemaVersion int            `json:"parserSchemaVersion"`
	ChunkingVersion     string         `json:"chunkingVersion"`
	ChunkCount          int            `json:"chunkCount"`
	ErrorMessage        string         `json:"errorMessage,omitempty"`
	CreatedAt           time.Time      `json:"createdAt"`
	IndexedAt           *time.Time     `json:"indexedAt,omitempty"`
	UpdatedAt           time.Time      `json:"updatedAt"`
}

type ImportJob struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"projectId"`
	DocumentID     string     `json:"documentId"`
	AttachmentID   string     `json:"attachmentId"`
	IndexVersionID string     `json:"indexVersionId"`
	Status         JobStatus  `json:"status"`
	Stage          string     `json:"stage"`
	AttemptCount   int        `json:"attemptCount"`
	ErrorMessage   string     `json:"errorMessage,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type IndexVersion struct {
	ID                   string      `json:"id"`
	ProjectID            string      `json:"projectId"`
	VersionNumber        int         `json:"versionNumber"`
	SchemaVersion        int         `json:"schemaVersion"`
	ParserSchemaVersion  int         `json:"parserSchemaVersion"`
	ChunkingVersion      string      `json:"chunkingVersion"`
	SearchKind           string      `json:"searchKind"`
	RetrievalEngine      string      `json:"retrievalEngine"`
	EmbeddingModel       string      `json:"embeddingModel,omitempty"`
	EmbeddingDimensions  int         `json:"embeddingDimensions,omitempty"`
	EmbeddingFingerprint string      `json:"-"`
	HybridStrategy       string      `json:"hybridStrategy"`
	StorageRelativePath  string      `json:"-"`
	Status               IndexStatus `json:"status"`
	ErrorMessage         string      `json:"errorMessage,omitempty"`
	CreatedAt            time.Time   `json:"createdAt"`
	ActivatedAt          *time.Time  `json:"activatedAt,omitempty"`
	UpdatedAt            time.Time   `json:"updatedAt"`
}

type Work struct {
	Document Document
	Job      ImportJob
	Version  IndexVersion
}

type ProjectStatus struct {
	Documents   int `json:"documents"`
	Ready       int `json:"ready"`
	Pending     int `json:"pending"`
	Indexing    int `json:"indexing"`
	Failed      int `json:"failed"`
	QueuedJobs  int `json:"queuedJobs"`
	RunningJobs int `json:"runningJobs"`
}

type Chunk struct {
	ID            string
	DocumentID    string
	AttachmentID  string
	Ordinal       int
	UnitIndex     int
	Kind          string
	Locator       string
	Title         string
	Content       string
	ContentSHA256 string
	SourceStart   int
	SourceEnd     int
}

type Match struct {
	ChunkID        string          `json:"-"`
	IndexVersionID string          `json:"indexVersionId"`
	DocumentID     string          `json:"documentId"`
	AttachmentID   string          `json:"attachmentId"`
	Name           string          `json:"name"`
	MIMEType       string          `json:"mimeType"`
	Format         document.Format `json:"format"`
	Locator        string          `json:"locator"`
	Title          string          `json:"title,omitempty"`
	Snippet        string          `json:"snippet"`
	Score          float64         `json:"score"`
	Rank           int             `json:"rank"`
	MatchedTerms   []string        `json:"matchedTerms,omitempty"`
	LexicalRank    int             `json:"lexicalRank,omitempty"`
	SemanticRank   int             `json:"semanticRank,omitempty"`
	SemanticScore  float64         `json:"semanticScore,omitempty"`
	SourceStart    int             `json:"sourceStart"`
	SourceEnd      int             `json:"sourceEnd"`
}

type SearchResult struct {
	Query            string        `json:"query"`
	Matches          []Match       `json:"matches"`
	TotalMatches     int           `json:"totalMatches"`
	Status           ProjectStatus `json:"status"`
	RetrievalMode    string        `json:"retrievalMode"`
	EmbeddingWarning string        `json:"embeddingWarning,omitempty"`
}

type SearchOptions struct {
	Query       string
	Limit       int
	DocumentIDs []string
	Formats     []document.Format
}

type Repository interface {
	EnsureVersion(ctx context.Context, projectID string, spec IndexSpec, at time.Time) (IndexVersion, error)
	ReadyVersion(ctx context.Context, projectID string) (IndexVersion, bool, error)
	ActiveVersions(ctx context.Context, projectID string) ([]IndexVersion, error)
	CanActivate(ctx context.Context, projectID, versionID string, expectedDocuments int) (bool, error)
	MarkVersionReady(ctx context.Context, versionID, projectID string, at time.Time) error
	Enqueue(ctx context.Context, value attachment.Attachment, version IndexVersion, force bool, at time.Time) (ImportJob, bool, error)
	ListDocuments(ctx context.Context, projectID string) ([]Document, error)
	GetDocument(ctx context.Context, projectID, documentID string) (Document, bool, error)
	RemoveDocument(ctx context.Context, projectID, documentID string) (bool, error)
	Recover(ctx context.Context, at time.Time) (int64, error)
	ClaimNext(ctx context.Context, projectID string, at time.Time) (Work, bool, error)
	UpdateStage(ctx context.Context, work Work, stage string, at time.Time) error
	Complete(ctx context.Context, work Work, chunkCount int, at time.Time) error
	Fail(ctx context.Context, work Work, message string, at time.Time) error
	Requeue(ctx context.Context, work Work, at time.Time) error
	ProjectStatus(ctx context.Context, projectID string) (ProjectStatus, error)
}

type EmbeddingProvider interface {
	Current(ctx context.Context) (embedding.Identity, bool, error)
	Embed(ctx context.Context, expected embedding.Identity, inputs []string) ([][]float32, error)
}

type AttachmentLoader interface {
	List(ctx context.Context, projectID string) ([]attachment.Attachment, error)
	Parsed(ctx context.Context, projectID, attachmentID string) (attachment.Attachment, document.Parsed, error)
}

type ProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

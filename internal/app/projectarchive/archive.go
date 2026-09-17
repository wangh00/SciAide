// Package projectarchive implements versioned, key-free SciAide project
// archives. Archive payloads are untrusted until their manifest, SQLite
// snapshot, referenced objects, and project graph have all been verified.
package projectarchive

import (
	"context"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
)

const (
	SchemaVersion = 1
	Extension     = ".sciaide-project"
)

type FileKind string

const (
	FileAttachment     FileKind = "attachment"
	FileDocumentCache  FileKind = "document_cache"
	FileKnowledgeIndex FileKind = "knowledge_index"
	FileArtifactObject FileKind = "artifact_object"
	FileWorkflowInput  FileKind = "workflow_input"
)

type ProjectSnapshot struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type DatabaseEntry struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type FileEntry struct {
	Path                string   `json:"path"`
	StorageRelativePath string   `json:"storageRelativePath"`
	Kind                FileKind `json:"kind"`
	// TaskID is set for task-private Workflow input snapshots. The relative
	// path remains relative to that task's private workspace, while the archive
	// path includes the owner to keep equal filenames from different tasks
	// distinct and restorable.
	TaskID    string `json:"taskId,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type SkillBinding struct {
	SkillID     string `json:"skillId"`
	Version     string `json:"version"`
	ContentHash string `json:"contentHash"`
	PackageHash string `json:"packageHash"`
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
}

type ArchiveStats struct {
	Conversations  int `json:"conversations"`
	Runs           int `json:"runs"`
	Attachments    int `json:"attachments"`
	Documents      int `json:"knowledgeDocuments"`
	Artifacts      int `json:"artifacts"`
	Workflows      int `json:"workflows"`
	WorkflowRuns   int `json:"workflowRuns"`
	Candidates     int `json:"researchCandidates"`
	Bibliographies int `json:"bibliographies"`
	Evidence       int `json:"evidenceEntries"`
}

type Manifest struct {
	SchemaVersion         int             `json:"schemaVersion"`
	Application           string          `json:"application"`
	ApplicationVersion    string          `json:"applicationVersion"`
	DatabaseSchemaVersion int             `json:"databaseSchemaVersion"`
	CreatedAt             time.Time       `json:"createdAt"`
	SourceProject         ProjectSnapshot `json:"sourceProject"`
	Database              DatabaseEntry   `json:"database"`
	Files                 []FileEntry     `json:"files"`
	SkillBindings         []SkillBinding  `json:"skillBindings"`
	Stats                 ArchiveStats    `json:"stats"`
	Excluded              []string        `json:"excluded"`
}

type FileSource struct {
	StorageRelativePath string
	SourcePath          string
	Kind                FileKind
	TaskID              string
	Required            bool
	ExpectedSize        int64
	ExpectedSHA256      string
}

type Snapshot struct {
	Project               project.Project
	DatabaseSchemaVersion int
	Files                 []FileSource
	SkillBindings         []SkillBinding
	Stats                 ArchiveStats
	HistoricalProfiles    int
}

type IndexRewrite struct {
	StorageRelativePath string
	OldProjectID        string
	NewProjectID        string
	OldIndexVersionID   string
	NewIndexVersionID   string
	DocumentIDs         map[string]string
	AttachmentIDs       map[string]string
}

type RewritePlan struct {
	Indexes            []IndexRewrite
	HistoricalProfiles int
	TaskIDs            map[string]string
}

type MergeReport struct {
	RestoredSkillBindings int
	MissingSkillBindings  []SkillBinding
}

type Repository interface {
	CreateSnapshot(ctx context.Context, projectID, destination string) (Snapshot, error)
	ValidateSnapshot(ctx context.Context, path, sourceProjectID string, databaseSchemaVersion int) (Snapshot, error)
	RewriteSnapshot(ctx context.Context, path string, restored project.Project) (RewritePlan, error)
	RewriteKnowledgeIndexes(ctx context.Context, privateRoot string, plan RewritePlan) error
	MergeSnapshot(ctx context.Context, path string, bindings []SkillBinding) (MergeReport, error)
	ProjectExists(ctx context.Context, projectID string) (bool, error)
}

type ProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

type ExportResult struct {
	Path      string   `json:"path"`
	SHA256    string   `json:"sha256"`
	SizeBytes int64    `json:"sizeBytes"`
	FileCount int      `json:"fileCount"`
	Manifest  Manifest `json:"manifest"`
}

type RestoreCommand struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

type RestoreReport struct {
	Project                 project.Project `json:"project"`
	SourceProjectID         string          `json:"sourceProjectId"`
	FilesRestored           int             `json:"filesRestored"`
	BytesRestored           int64           `json:"bytesRestored"`
	HistoricalProfiles      int             `json:"historicalProfiles"`
	RestoredSkillBindings   int             `json:"restoredSkillBindings"`
	MissingSkillBindings    []SkillBinding  `json:"missingSkillBindings"`
	SecretsRequireRebinding bool            `json:"secretsRequireRebinding"`
	Excluded                []string        `json:"excluded"`
}

type RecoveryResult struct {
	StagingDirectoriesRemoved int `json:"stagingDirectoriesRemoved"`
	OrphanWorkspacesArchived  int `json:"orphanWorkspacesArchived"`
	PublishedMarkersRemoved   int `json:"publishedMarkersRemoved"`
}

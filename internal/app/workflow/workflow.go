// Package workflow defines versioned, statically compiled research workflows.
// A workflow is data until a separately created WorkflowRun executes its
// frozen compilation through the normal Tool and permission pipeline.
package workflow

import (
	"context"
	"encoding/json"
	"time"
)

const SchemaVersion = 1

type Purpose string

const (
	PurposeUserPlan        Purpose = "user_plan"
	PurposeResearchStarter Purpose = "research_starter"
)

func (value Purpose) Valid() bool {
	return value == PurposeUserPlan || value == PurposeResearchStarter
}

type NodeKind string

const (
	NodeTool               NodeKind = "tool"
	NodeShell              NodeKind = "shell"
	NodePython             NodeKind = "python"
	NodeHumanConfirmation  NodeKind = "human_confirmation"
	NodeCandidateSelection NodeKind = "candidate_selection"
	NodeCitationSelection  NodeKind = "citation_selection"
	NodeAIAnalysis         NodeKind = "ai_analysis"
	NodeAgentStage         NodeKind = "agent_stage"
)

type AIReviewPolicy string

const (
	AIReviewHuman AIReviewPolicy = "human"
	AIReviewAuto  AIReviewPolicy = "auto"
)

type DataType string

const (
	TypeAny       DataType = "any"
	TypeString    DataType = "string"
	TypeNumber    DataType = "number"
	TypeInteger   DataType = "integer"
	TypeBoolean   DataType = "boolean"
	TypeObject    DataType = "object"
	TypeArray     DataType = "array"
	TypeArtifacts DataType = "artifacts"
	TypeCitations DataType = "citations"
)

type Port struct {
	Name        string          `json:"name"`
	Type        DataType        `json:"type"`
	Description string          `json:"description,omitempty"`
	FileKind    string          `json:"fileKind,omitempty"`
	Control     string          `json:"control,omitempty"`
	MinItems    int             `json:"minItems,omitempty"`
	MaxItems    int             `json:"maxItems,omitempty"`
	Required    bool            `json:"required"`
	Default     json.RawMessage `json:"default,omitempty"`
}

type Node struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Kind          NodeKind        `json:"kind"`
	ToolName      string          `json:"toolName,omitempty"`
	Arguments     json.RawMessage `json:"arguments"`
	Prompt        string          `json:"prompt,omitempty"`
	PromptVersion string          `json:"promptVersion,omitempty"`
	AllowedTools  []string        `json:"allowedTools,omitempty"`
	SkillRouting  bool            `json:"skillRouting,omitempty"`
	ReviewPolicy  AIReviewPolicy  `json:"reviewPolicy,omitempty"`
	OutputSchema  json.RawMessage `json:"outputSchema,omitempty"`
}

type Edge struct {
	FromNode string `json:"fromNode"`
	FromPort string `json:"fromPort"`
	ToNode   string `json:"toNode"`
	ToPort   string `json:"toPort"`
}

type Output struct {
	Name        string   `json:"name"`
	Type        DataType `json:"type"`
	FromNode    string   `json:"fromNode"`
	FromPort    string   `json:"fromPort"`
	Required    bool     `json:"required"`
	Description string   `json:"description,omitempty"`
}

type Definition struct {
	SchemaVersion int      `json:"schemaVersion"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Inputs        []Port   `json:"inputs"`
	Nodes         []Node   `json:"nodes"`
	Edges         []Edge   `json:"edges"`
	Outputs       []Output `json:"outputs"`
}

type Template struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Definition  Definition `json:"definition"`
}

type Diagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type ToolSnapshot struct {
	QualifiedName string          `json:"qualifiedName"`
	Version       string          `json:"version"`
	Risk          string          `json:"risk"`
	Permissions   json.RawMessage `json:"permissions"`
	Idempotent    bool            `json:"idempotent"`
	InputSchema   json.RawMessage `json:"inputSchema"`
	OutputSchema  json.RawMessage `json:"outputSchema,omitempty"`
}

type CompiledNode struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Kind               NodeKind        `json:"kind"`
	Tool               *ToolSnapshot   `json:"tool,omitempty"`
	Arguments          json.RawMessage `json:"arguments"`
	Prompt             string          `json:"prompt,omitempty"`
	PromptVersion      string          `json:"promptVersion,omitempty"`
	AllowedTools       []ToolSnapshot  `json:"allowedTools,omitempty"`
	SkillRouting       bool            `json:"skillRouting,omitempty"`
	ReviewPolicy       AIReviewPolicy  `json:"reviewPolicy,omitempty"`
	OutputSchema       json.RawMessage `json:"outputSchema,omitempty"`
	OutputSchemaSHA256 string          `json:"outputSchemaSha256,omitempty"`
	Dependencies       []string        `json:"dependencies"`
	SideEffect         bool            `json:"sideEffect"`
}

type Compilation struct {
	SchemaVersion     int            `json:"schemaVersion"`
	CompilerVersion   string         `json:"compilerVersion"`
	DefinitionSHA256  string         `json:"definitionSha256"`
	CompilationSHA256 string         `json:"compilationSha256"`
	Order             []string       `json:"order"`
	Nodes             []CompiledNode `json:"nodes"`
	Edges             []Edge         `json:"edges"`
	Inputs            []Port         `json:"inputs"`
	Outputs           []Output       `json:"outputs"`
	Diagnostics       []Diagnostic   `json:"diagnostics"`
}

type PreviewNode struct {
	Ordinal     int             `json:"ordinal"`
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Kind        NodeKind        `json:"kind"`
	ToolName    string          `json:"toolName,omitempty"`
	ToolVersion string          `json:"toolVersion,omitempty"`
	Risk        string          `json:"risk,omitempty"`
	Permissions json.RawMessage `json:"permissions"`
	Idempotent  bool            `json:"idempotent"`
	SideEffect  bool            `json:"sideEffect"`
	Summary     string          `json:"summary"`
}

type Preview struct {
	Valid             bool          `json:"valid"`
	DefinitionSHA256  string        `json:"definitionSha256,omitempty"`
	CompilationSHA256 string        `json:"compilationSha256,omitempty"`
	Diagnostics       []Diagnostic  `json:"diagnostics"`
	Nodes             []PreviewNode `json:"nodes"`
	EdgeCount         int           `json:"edgeCount"`
	InputCount        int           `json:"inputCount"`
	OutputCount       int           `json:"outputCount"`
}

type Workflow struct {
	ID                      string    `json:"id"`
	ProjectID               string    `json:"projectId"`
	Purpose                 Purpose   `json:"purpose"`
	Name                    string    `json:"name"`
	Description             string    `json:"description,omitempty"`
	CurrentVersionID        string    `json:"currentVersionId"`
	CurrentDefinitionSHA256 string    `json:"currentDefinitionSha256,omitempty"`
	Version                 int       `json:"version"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

type Version struct {
	ID                string      `json:"id"`
	WorkflowID        string      `json:"workflowId"`
	Version           int         `json:"version"`
	Definition        Definition  `json:"definition"`
	DefinitionSHA256  string      `json:"definitionSha256"`
	Compilation       Compilation `json:"compilation"`
	CompilationSHA256 string      `json:"compilationSha256"`
	RuntimeInputs     []Port      `json:"runtimeInputs,omitempty"`
	CreatedAt         time.Time   `json:"createdAt"`
}

type Detail struct {
	Workflow Workflow  `json:"workflow"`
	Versions []Version `json:"versions"`
}

type SaveCommand struct {
	ProjectID                string     `json:"projectId"`
	WorkflowID               string     `json:"workflowId,omitempty"`
	ExpectedCurrentVersionID string     `json:"expectedCurrentVersionId,omitempty"`
	Definition               Definition `json:"definition"`
}

type SaveResult struct {
	Workflow Workflow `json:"workflow"`
	Version  Version  `json:"version"`
	Created  bool     `json:"created"`
}

type SaveRecord struct {
	Workflow                 Workflow
	Version                  Version
	ExpectedCurrentVersionID string
}

type Repository interface {
	SaveVersion(ctx context.Context, record SaveRecord) (SaveResult, error)
	List(ctx context.Context, projectID string) ([]Workflow, error)
	Get(ctx context.Context, projectID, workflowID string) (Detail, error)
	Delete(ctx context.Context, projectID, workflowID string) error
}

package pythonenv

import (
	"context"
	"errors"
	"time"
)

type State string

type EnvironmentKind string

const (
	StateAbsent   State = "absent"
	StateCreating State = "creating"
	StateReady    State = "ready"
	StateBroken   State = "broken"
	StateDeleting State = "deleting"
)

const (
	KindLegacyManaged    EnvironmentKind = "legacy_managed"
	KindWorkspaceManaged EnvironmentKind = "workspace_managed"
	KindExternal         EnvironmentKind = "external"
)

type Interpreter struct {
	ExecutablePath   string `json:"executablePath"`
	Version          string `json:"version"`
	Architecture     string `json:"architecture"`
	Implementation   string `json:"implementation"`
	Prefix           string `json:"prefix"`
	BasePrefix       string `json:"basePrefix"`
	ExecutableSHA256 string `json:"executableSha256"`
	HasVenv          bool   `json:"hasVenv"`
	HasPip           bool   `json:"hasPip"`
}

type Discovery struct {
	Status       string        `json:"status"`
	Message      string        `json:"message"`
	Interpreters []Interpreter `json:"interpreters"`
}

type Environment struct {
	ID                     string          `json:"id"`
	ProjectID              string          `json:"projectId"`
	State                  State           `json:"state"`
	Kind                   EnvironmentKind `json:"environmentKind"`
	BaseExecutablePath     string          `json:"baseExecutablePath"`
	BaseExecutableVersion  string          `json:"baseExecutableVersion"`
	BaseExecutableSHA256   string          `json:"baseExecutableSha256"`
	Architecture           string          `json:"architecture"`
	Implementation         string          `json:"implementation"`
	EnvironmentPythonPath  string          `json:"environmentPythonPath"`
	EnvironmentFingerprint string          `json:"environmentFingerprint"`
	Lock                   []string        `json:"lock"`
	FreezeSHA256           string          `json:"freezeSha256"`
	CreatedAt              time.Time       `json:"createdAt"`
	UpdatedAt              time.Time       `json:"updatedAt"`
	LastVerifiedAt         *time.Time      `json:"lastVerifiedAt,omitempty"`
	ErrorMessage           string          `json:"errorMessage,omitempty"`
}

type Operation struct {
	ID                string     `json:"id"`
	ProjectID         string     `json:"projectId"`
	EnvironmentID     string     `json:"environmentId"`
	Kind              string     `json:"kind"`
	State             string     `json:"state"`
	RequestJSON       string     `json:"requestJson"`
	BeforeFingerprint string     `json:"beforeFingerprint"`
	AfterFingerprint  string     `json:"afterFingerprint"`
	StartedAt         time.Time  `json:"startedAt"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
	ErrorMessage      string     `json:"errorMessage,omitempty"`
}

var ErrEnvironmentNotFound = errors.New("project Python environment not found")

type Repository interface {
	Get(ctx context.Context, projectID string) (Environment, error)
	List(ctx context.Context) ([]Environment, error)
	ListRunningOperations(ctx context.Context) ([]Operation, error)
	Save(ctx context.Context, value Environment) error
	Delete(ctx context.Context, projectID string) error
	CreateOperation(ctx context.Context, value Operation) error
	FinishOperation(ctx context.Context, operationID, state, afterFingerprint, errorMessage string, completedAt time.Time) error
	CompleteOperation(ctx context.Context, value Environment, operationID, state, afterFingerprint, errorMessage string, completedAt time.Time) error
}

type Runtime interface {
	Discover(ctx context.Context, preferredPath string) (Discovery, error)
	Probe(ctx context.Context, executablePath string) (Interpreter, error)
	CreateEnvironment(ctx context.Context, baseExecutablePath, destination string) error
	FinalizeEnvironment(ctx context.Context, environmentPythonPath, previousRoot string) error
	ValidateEnvironmentScripts(ctx context.Context, environmentPythonPath string) error
	InstallPackages(ctx context.Context, environmentPythonPath string, packages []string) error
	Freeze(ctx context.Context, environmentPythonPath string) ([]string, error)
}

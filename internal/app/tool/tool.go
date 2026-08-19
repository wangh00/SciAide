// Package tool defines SciAide's provider-independent tool protocol. Model,
// builtin, MCP and Skill adapters must all cross this boundary before a tool is
// allowed to execute.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RiskLevel string

const (
	RiskLow         RiskLevel = "low"
	RiskModerate    RiskLevel = "moderate"
	RiskHigh        RiskLevel = "high"
	RiskDestructive RiskLevel = "destructive"
)

type PermissionKind string

const (
	PermissionToolInvoke         PermissionKind = "tool.invoke"
	PermissionWorkspaceRead      PermissionKind = "workspace.read"
	PermissionWorkspaceWrite     PermissionKind = "workspace.write"
	PermissionFilesystemExternal PermissionKind = "filesystem.external"
	PermissionNetworkDomain      PermissionKind = "network.domain"
	PermissionProcessExecute     PermissionKind = "process.execute"
	PermissionDestructive        PermissionKind = "destructive"
	PermissionSecretUse          PermissionKind = "secret.use"
)

type PermissionRequirement struct {
	Kind     PermissionKind `json:"kind"`
	Resource string         `json:"resource,omitempty"`
}

type Definition struct {
	QualifiedName string                  `json:"qualifiedName"`
	Description   string                  `json:"description"`
	InputSchema   json.RawMessage         `json:"inputSchema"`
	OutputSchema  json.RawMessage         `json:"outputSchema,omitempty"`
	Risk          RiskLevel               `json:"risk"`
	Permissions   []PermissionRequirement `json:"permissions"`
	Idempotent    bool                    `json:"idempotent"`
	Version       string                  `json:"version"`
}

type Invocation struct {
	CallID    string          `json:"callId"`
	RunID     string          `json:"runId"`
	ProjectID string          `json:"projectId"`
	Arguments json.RawMessage `json:"arguments"`
}

type Tool interface {
	Definition(ctx context.Context) (Definition, error)
	Invoke(ctx context.Context, invocation Invocation) (Result, error)
}

type Registry interface {
	Definitions(ctx context.Context) ([]Definition, error)
	Definition(ctx context.Context, qualifiedName string) (Definition, error)
	Resolve(ctx context.Context, qualifiedName string) (Tool, error)
}

type MutableRegistry interface {
	Registry
	Register(ctx context.Context, value Tool) error
	ReplaceNamespace(ctx context.Context, prefix string, values []Tool) error
}

type CallStatus string

const (
	CallPending          CallStatus = "pending"
	CallAwaitingApproval CallStatus = "awaiting_approval"
	CallRunning          CallStatus = "running"
	CallCompleted        CallStatus = "completed"
	CallFailed           CallStatus = "failed"
	CallDenied           CallStatus = "denied"
	CallCancelled        CallStatus = "cancelled"
	CallInterrupted      CallStatus = "interrupted"
)

type Call struct {
	ID             string                  `json:"id"`
	RunID          string                  `json:"runId"`
	ProviderCallID string                  `json:"providerCallId"`
	ToolName       string                  `json:"toolName"`
	ToolVersion    string                  `json:"toolVersion"`
	Arguments      json.RawMessage         `json:"arguments"`
	Status         CallStatus              `json:"status"`
	Risk           RiskLevel               `json:"risk"`
	Permissions    []PermissionRequirement `json:"permissions"`
	Idempotent     bool                    `json:"idempotent"`
	IdempotencyKey string                  `json:"idempotencyKey,omitempty"`
	ErrorCode      string                  `json:"errorCode,omitempty"`
	ErrorMessage   string                  `json:"errorMessage,omitempty"`
	Result         *Result                 `json:"result,omitempty"`
	CreatedAt      time.Time               `json:"createdAt"`
	StartedAt      *time.Time              `json:"startedAt,omitempty"`
	CompletedAt    *time.Time              `json:"completedAt,omitempty"`
	UpdatedAt      time.Time               `json:"updatedAt"`
}

type ResultStatus string

const (
	ResultSuccess   ResultStatus = "success"
	ResultError     ResultStatus = "error"
	ResultDenied    ResultStatus = "denied"
	ResultCancelled ResultStatus = "cancelled"
)

type ArtifactRef struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

type CitationRef struct {
	ID             string `json:"id"`
	Kind           string `json:"kind,omitempty"`
	Reference      string `json:"reference,omitempty"`
	ProjectID      string `json:"projectId,omitempty"`
	IndexVersionID string `json:"indexVersionId,omitempty"`
	DocumentID     string `json:"documentId,omitempty"`
	AttachmentID   string `json:"attachmentId,omitempty"`
	ChunkID        string `json:"chunkId,omitempty"`
	SourceName     string `json:"sourceName,omitempty"`
	MIMEType       string `json:"mimeType,omitempty"`
	Locator        string `json:"locator,omitempty"`
	Title          string `json:"title,omitempty"`
	Quote          string `json:"quote,omitempty"`
	QuoteSHA256    string `json:"quoteSha256,omitempty"`
	SourceStart    int    `json:"sourceStart,omitempty"`
	SourceEnd      int    `json:"sourceEnd,omitempty"`
}

type ResultMeta struct {
	DurationMillis int64 `json:"durationMillis,omitempty"`
	OriginalBytes  int64 `json:"originalBytes,omitempty"`
}

const (
	ErrorCodeInvocationFailed = "TOOL_INVOCATION_FAILED"
	ErrorCodePanic            = "TOOL_PANIC"
	ErrorCodeTimeout          = "TOOL_TIMEOUT"
	ErrorCodeCancelled        = "TOOL_CANCELLED"
	ErrorCodeResultInvalid    = "TOOL_RESULT_INVALID"
	ErrorCodeResultTooLarge   = "TOOL_RESULT_TOO_LARGE"
)

type Result struct {
	Status     ResultStatus    `json:"status"`
	Text       string          `json:"text,omitempty"`
	Structured json.RawMessage `json:"structured,omitempty"`
	Artifacts  []ArtifactRef   `json:"artifacts"`
	Citations  []CitationRef   `json:"citations"`
	Truncated  bool            `json:"truncated"`
	Meta       ResultMeta      `json:"meta"`
	CreatedAt  time.Time       `json:"createdAt"`
}

var ErrTransitionConflict = errors.New("tool call transition conflict")

func (status CallStatus) Terminal() bool {
	switch status {
	case CallCompleted, CallFailed, CallDenied, CallCancelled, CallInterrupted:
		return true
	default:
		return false
	}
}

func CanTransition(from, to CallStatus) bool {
	switch from {
	case CallPending:
		return to == CallAwaitingApproval || to == CallRunning || to == CallFailed || to == CallDenied || to == CallCancelled || to == CallInterrupted
	case CallAwaitingApproval:
		return to == CallRunning || to == CallDenied || to == CallCancelled || to == CallInterrupted
	case CallRunning:
		return to == CallCompleted || to == CallFailed || to == CallCancelled || to == CallInterrupted
	default:
		return false
	}
}

func ValidateDefinition(value Definition) error {
	if err := validateQualifiedName(value.QualifiedName); err != nil {
		return err
	}
	qualifiedName := strings.TrimSpace(value.QualifiedName)
	if strings.TrimSpace(value.Description) == "" || strings.TrimSpace(value.Version) == "" {
		return fmt.Errorf("tool description and version are required")
	}
	if !validRisk(value.Risk) {
		return fmt.Errorf("invalid tool risk level %q", value.Risk)
	}
	if len(value.InputSchema) > 256*1024 || len(value.OutputSchema) > 256*1024 {
		return fmt.Errorf("tool schema exceeds size limit")
	}
	if err := validateJSONObject("input schema", value.InputSchema); err != nil {
		return err
	}
	if len(value.OutputSchema) > 0 {
		if err := validateJSONObject("output schema", value.OutputSchema); err != nil {
			return err
		}
	}
	seen := make(map[string]struct{}, len(value.Permissions))
	if len(value.Permissions) > 32 {
		return fmt.Errorf("tool has too many permission requirements")
	}
	for _, requirement := range value.Permissions {
		if !validPermission(requirement.Kind) {
			return fmt.Errorf("invalid permission requirement %q", requirement.Kind)
		}
		resource := strings.TrimSpace(requirement.Resource)
		if len(resource) > 4096 || strings.IndexByte(resource, 0) >= 0 {
			return fmt.Errorf("permission resource is invalid")
		}
		if requirement.Kind == PermissionToolInvoke && resource != "" && resource != qualifiedName {
			return fmt.Errorf("tool.invoke resource must match the qualified tool name")
		}
		key := string(requirement.Kind) + "\x00" + resource
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate permission requirement %q", requirement.Kind)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// SnapshotDefinition normalizes and deep-copies the trusted definition that is
// persisted with a ToolCall. Model-provided data must never be used to fill
// these security fields.
func SnapshotDefinition(value Definition) Definition {
	value.QualifiedName = strings.TrimSpace(value.QualifiedName)
	value.Description = strings.TrimSpace(value.Description)
	value.Version = strings.TrimSpace(value.Version)
	value.InputSchema = append(json.RawMessage(nil), value.InputSchema...)
	value.OutputSchema = append(json.RawMessage(nil), value.OutputSchema...)
	value.Permissions = snapshotPermissions(value.QualifiedName, value.Permissions)
	return value
}

func snapshotPermissions(toolName string, values []PermissionRequirement) []PermissionRequirement {
	if len(values) == 0 {
		return []PermissionRequirement{}
	}
	result := make([]PermissionRequirement, len(values))
	for index, value := range values {
		value.Resource = strings.TrimSpace(value.Resource)
		if value.Kind == PermissionToolInvoke && value.Resource == "" {
			value.Resource = toolName
		}
		result[index] = value
	}
	return result
}

func ValidateArguments(value json.RawMessage) error {
	if len(value) > 256*1024 {
		return fmt.Errorf("tool arguments exceed size limit")
	}
	return validateJSONObject("tool arguments", value)
}

func ValidateResult(value Result) error {
	switch value.Status {
	case ResultSuccess, ResultError, ResultDenied, ResultCancelled:
	default:
		return fmt.Errorf("invalid tool result status %q", value.Status)
	}
	if len(value.Structured) > 0 && !json.Valid(value.Structured) {
		return fmt.Errorf("tool structured result must be valid JSON")
	}
	if value.Meta.DurationMillis < 0 || value.Meta.OriginalBytes < 0 {
		return fmt.Errorf("tool result metadata must not be negative")
	}
	return nil
}

func TerminalStatusForResult(status ResultStatus) (CallStatus, error) {
	switch status {
	case ResultSuccess:
		return CallCompleted, nil
	case ResultError:
		return CallFailed, nil
	case ResultDenied:
		return CallDenied, nil
	case ResultCancelled:
		return CallCancelled, nil
	default:
		return "", fmt.Errorf("invalid tool result status %q", status)
	}
}

func validateQualifiedName(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 160 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
		return fmt.Errorf("invalid qualified tool name")
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return fmt.Errorf("invalid qualified tool name")
	}
	return nil
}

func validateJSONObject(label string, value json.RawMessage) error {
	if len(value) == 0 || !json.Valid(value) {
		return fmt.Errorf("%s must be valid JSON", label)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return fmt.Errorf("%s must be a JSON object", label)
	}
	return nil
}

func validRisk(value RiskLevel) bool {
	return value == RiskLow || value == RiskModerate || value == RiskHigh || value == RiskDestructive
}

func validPermission(value PermissionKind) bool {
	switch value {
	case PermissionToolInvoke, PermissionWorkspaceRead, PermissionWorkspaceWrite, PermissionFilesystemExternal, PermissionNetworkDomain, PermissionProcessExecute, PermissionDestructive, PermissionSecretUse:
		return true
	default:
		return false
	}
}

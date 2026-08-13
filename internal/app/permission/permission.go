// Package permission owns tool policy decisions, user approvals and scoped
// grants. It deliberately depends on tool definition snapshots, not model data.
package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

type Scope string

const (
	ScopeCall    Scope = "call"
	ScopeRun     Scope = "run"
	ScopeProject Scope = "project"
)

type ApprovalStatus string

const (
	ApprovalPending ApprovalStatus = "pending"
	ApprovalGranted ApprovalStatus = "granted"
	ApprovalDenied  ApprovalStatus = "denied"
	ApprovalExpired ApprovalStatus = "expired"
)

type SubjectType string

const (
	SubjectUser SubjectType = "user"
)

type Grant struct {
	ID             string              `json:"id"`
	ProjectID      string              `json:"projectId"`
	RunID          string              `json:"runId,omitempty"`
	ToolName       string              `json:"toolName"`
	PermissionKind tool.PermissionKind `json:"permissionKind"`
	Resource       string              `json:"resource,omitempty"`
	Scope          Scope               `json:"scope"`
	GrantedBy      SubjectType         `json:"grantedBy"`
	CreatedAt      time.Time           `json:"createdAt"`
	ExpiresAt      *time.Time          `json:"expiresAt,omitempty"`
	RevokedAt      *time.Time          `json:"revokedAt,omitempty"`
}

type Approval struct {
	ID             string              `json:"id"`
	RunID          string              `json:"runId"`
	ToolCallID     string              `json:"toolCallId"`
	ProjectID      string              `json:"projectId"`
	ToolName       string              `json:"toolName"`
	ToolVersion    string              `json:"toolVersion"`
	PermissionKind tool.PermissionKind `json:"permissionKind"`
	Resource       string              `json:"resource,omitempty"`
	Risk           tool.RiskLevel      `json:"risk"`
	Status         ApprovalStatus      `json:"status"`
	RequestedScope Scope               `json:"requestedScope"`
	ResolvedScope  Scope               `json:"resolvedScope,omitempty"`
	Reason         string              `json:"reason"`
	CreatedAt      time.Time           `json:"createdAt"`
	ResolvedAt     *time.Time          `json:"resolvedAt,omitempty"`
}

type EvaluationRequest struct {
	ProjectID string    `json:"projectId"`
	RunID     string    `json:"runId"`
	Call      tool.Call `json:"call"`
}

type Evaluation struct {
	Decision Decision                     `json:"decision"`
	Reason   string                       `json:"reason"`
	Missing  []tool.PermissionRequirement `json:"missing"`
	GrantIDs []string                     `json:"grantIds"`
}

type ResolveCommand struct {
	ApprovalID string `json:"approvalId"`
	Allow      bool   `json:"allow"`
	Scope      Scope  `json:"scope"`
}

var ErrApprovalConflict = errors.New("approval transition conflict")

type Repository interface {
	ListActiveGrants(ctx context.Context, projectID, runID, toolName string, at time.Time) ([]Grant, error)
	ListGrantedApprovals(ctx context.Context, toolCallID string) ([]Approval, error)
	CreateApprovalWithEvent(ctx context.Context, value Approval, event events.Envelope) error
	GetApproval(ctx context.Context, id string) (Approval, error)
	ListApprovalsByRun(ctx context.Context, runID string) ([]Approval, error)
	ListPendingApprovals(ctx context.Context, runID string) ([]Approval, error)
	ListGrantsByProject(ctx context.Context, projectID string) ([]Grant, error)
	GetGrant(ctx context.Context, id string) (Grant, error)
	ResolveApprovalWithGrantAndEvent(ctx context.Context, approvalID string, expected, next ApprovalStatus, scope Scope, grant *Grant, at time.Time, event events.Envelope) error
	RevokeGrantWithEvent(ctx context.Context, id string, at time.Time, event events.Envelope) error
	ExpirePending(ctx context.Context, at time.Time) (int64, error)
}

type Engine struct {
	repository Repository
	now        func() time.Time
}

func NewEngine(repository Repository) *Engine {
	return &Engine{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func (e *Engine) Evaluate(ctx context.Context, request EvaluationRequest) (Evaluation, error) {
	request.ProjectID, request.RunID = strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.RunID)
	if request.ProjectID == "" || request.RunID == "" || strings.TrimSpace(request.Call.ID) == "" {
		return Evaluation{}, fmt.Errorf("project, run and tool call are required")
	}
	if request.Call.RunID != request.RunID {
		return Evaluation{}, fmt.Errorf("tool call does not belong to run")
	}
	request.Call.ToolName = strings.TrimSpace(request.Call.ToolName)
	if request.Call.ToolName == "" {
		return Evaluation{}, fmt.Errorf("tool call name is required")
	}
	if request.Call.Status != tool.CallPending && request.Call.Status != tool.CallAwaitingApproval {
		return Evaluation{}, fmt.Errorf("tool call is not eligible for policy evaluation")
	}
	requirements := cloneRequirements(request.Call.Permissions)
	if (request.Call.Risk == tool.RiskModerate || request.Call.Risk == tool.RiskHigh || request.Call.Risk == tool.RiskDestructive) && !containsPermission(requirements, tool.PermissionToolInvoke) {
		requirements = append([]tool.PermissionRequirement{{Kind: tool.PermissionToolInvoke, Resource: request.Call.ToolName}}, requirements...)
	}
	approvals, err := e.repository.ListGrantedApprovals(ctx, request.Call.ID)
	if err != nil {
		return Evaluation{}, fmt.Errorf("list granted approvals: %w", err)
	}
	grants, err := e.repository.ListActiveGrants(ctx, request.ProjectID, request.RunID, request.Call.ToolName, e.now())
	if err != nil {
		return Evaluation{}, fmt.Errorf("list permission grants: %w", err)
	}
	missing := make([]tool.PermissionRequirement, 0)
	used := make([]string, 0)
	for _, requirement := range requirements {
		requirement.Resource = strings.TrimSpace(requirement.Resource)
		if approvalCovers(approvals, requirement) {
			continue
		}
		if !persistentGrantAllowed(request.Call, requirement) {
			missing = append(missing, requirement)
			continue
		}
		grantID := matchingGrant(grants, request, requirement)
		if grantID == "" {
			missing = append(missing, requirement)
		} else {
			used = append(used, grantID)
		}
	}
	if len(missing) == 0 {
		return Evaluation{Decision: DecisionAllow, Reason: "调用所需权限已由有效授权覆盖。", Missing: []tool.PermissionRequirement{}, GrantIDs: used}, nil
	}
	return Evaluation{Decision: DecisionAsk, Reason: "调用需要用户确认尚未授权的权限范围。", Missing: missing, GrantIDs: used}, nil
}

func persistentGrantAllowed(call tool.Call, requirement tool.PermissionRequirement) bool {
	if call.Risk == tool.RiskHigh || call.Risk == tool.RiskDestructive {
		return false
	}
	switch requirement.Kind {
	case tool.PermissionFilesystemExternal, tool.PermissionProcessExecute, tool.PermissionDestructive, tool.PermissionSecretUse:
		return false
	default:
		return true
	}
}

func (e *Engine) RequestApproval(ctx context.Context, request EvaluationRequest, evaluation Evaluation) (Approval, error) {
	if evaluation.Decision != DecisionAsk || len(evaluation.Missing) == 0 {
		return Approval{}, fmt.Errorf("evaluation does not require approval")
	}
	requirement := evaluation.Missing[0]
	requirement.Resource = strings.TrimSpace(requirement.Resource)
	if !callRequires(request.Call, requirement) {
		return Approval{}, fmt.Errorf("approval requirement is not part of the tool call snapshot")
	}
	approvalID, err := id.New()
	if err != nil {
		return Approval{}, err
	}
	now := e.now()
	value := Approval{ID: approvalID, RunID: request.RunID, ToolCallID: request.Call.ID, ProjectID: request.ProjectID, ToolName: request.Call.ToolName, ToolVersion: request.Call.ToolVersion, PermissionKind: requirement.Kind, Resource: strings.TrimSpace(requirement.Resource), Risk: request.Call.Risk, Status: ApprovalPending, RequestedScope: maximumScope(request.Call, requirement), Reason: evaluation.Reason, CreatedAt: now}
	event, err := approvalEvent(value.RunID, "approval.requested", map[string]any{"approval": value})
	if err != nil {
		return Approval{}, err
	}
	if err := e.repository.CreateApprovalWithEvent(ctx, value, event); err != nil {
		return Approval{}, fmt.Errorf("create approval: %w", err)
	}
	return value, nil
}

func (e *Engine) Resolve(ctx context.Context, command ResolveCommand) (Approval, *Grant, error) {
	value, err := e.repository.GetApproval(ctx, strings.TrimSpace(command.ApprovalID))
	if err != nil {
		return Approval{}, nil, err
	}
	if value.Status != ApprovalPending {
		return Approval{}, nil, ErrApprovalConflict
	}
	now := e.now()
	next := ApprovalDenied
	scope := ScopeCall
	var grant *Grant
	if command.Allow {
		next = ApprovalGranted
		scope = command.Scope
		if err := validateResolvedScope(value, scope); err != nil {
			return Approval{}, nil, err
		}
		if scope != ScopeCall {
			grantID, err := id.New()
			if err != nil {
				return Approval{}, nil, err
			}
			grant = &Grant{ID: grantID, ProjectID: value.ProjectID, ToolName: value.ToolName, PermissionKind: value.PermissionKind, Resource: value.Resource, Scope: scope, GrantedBy: SubjectUser, CreatedAt: now}
			if scope == ScopeRun {
				grant.RunID = value.RunID
			}
		}
	}
	projected := value
	projected.Status, projected.ResolvedScope, projected.ResolvedAt = next, scope, &now
	eventType := "approval.denied"
	if next == ApprovalGranted {
		eventType = "approval.granted"
	}
	event, err := approvalEvent(value.RunID, eventType, map[string]any{"approval": projected, "grant": grant})
	if err != nil {
		return Approval{}, nil, err
	}
	if err := e.repository.ResolveApprovalWithGrantAndEvent(ctx, value.ID, ApprovalPending, next, scope, grant, now, event); err != nil {
		return Approval{}, nil, err
	}
	loaded, err := e.repository.GetApproval(ctx, value.ID)
	return loaded, grant, err
}

func (e *Engine) ListByRun(ctx context.Context, runID string) ([]Approval, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("run id is required")
	}
	return e.repository.ListApprovalsByRun(ctx, runID)
}

func (e *Engine) ListPending(ctx context.Context, runID string) ([]Approval, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("run id is required")
	}
	return e.repository.ListPendingApprovals(ctx, runID)
}

func (e *Engine) Get(ctx context.Context, approvalID string) (Approval, error) {
	approvalID = strings.TrimSpace(approvalID)
	if approvalID == "" {
		return Approval{}, fmt.Errorf("approval id is required")
	}
	return e.repository.GetApproval(ctx, approvalID)
}

func (e *Engine) ListGrants(ctx context.Context, projectID string) ([]Grant, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	return e.repository.ListGrantsByProject(ctx, projectID)
}

func (e *Engine) Revoke(ctx context.Context, grantID string) error {
	grantID = strings.TrimSpace(grantID)
	if grantID == "" {
		return fmt.Errorf("permission grant id is required")
	}
	grant, err := e.repository.GetGrant(ctx, grantID)
	if err != nil {
		return err
	}
	at := e.now()
	aggregateID, aggregateType := grant.ProjectID, "project"
	if grant.Scope == ScopeRun {
		aggregateID, aggregateType = grant.RunID, "run"
	}
	event, err := permissionEvent(aggregateID, aggregateType, "permission.grant_revoked", map[string]any{"grantId": grant.ID, "revokedAt": at})
	if err != nil {
		return err
	}
	return e.repository.RevokeGrantWithEvent(ctx, grantID, at, event)
}

func (e *Engine) Recover(ctx context.Context) (int64, error) {
	return e.repository.ExpirePending(ctx, e.now())
}

func maximumScope(call tool.Call, requirement tool.PermissionRequirement) Scope {
	if call.Risk == tool.RiskHigh || call.Risk == tool.RiskDestructive || requirement.Kind == tool.PermissionDestructive || requirement.Kind == tool.PermissionFilesystemExternal || requirement.Kind == tool.PermissionProcessExecute || requirement.Kind == tool.PermissionSecretUse {
		return ScopeCall
	}
	return ScopeProject
}

func validateResolvedScope(value Approval, scope Scope) error {
	if scope != ScopeCall && scope != ScopeRun && scope != ScopeProject {
		return fmt.Errorf("invalid approval scope %q", scope)
	}
	if value.RequestedScope == ScopeCall && scope != ScopeCall {
		return fmt.Errorf("this permission can only be allowed once")
	}
	if value.RequestedScope == ScopeRun && scope == ScopeProject {
		return fmt.Errorf("approval scope exceeds requested scope")
	}
	return nil
}

func matchingGrant(grants []Grant, request EvaluationRequest, requirement tool.PermissionRequirement) string {
	for _, grant := range grants {
		if grant.ToolName != request.Call.ToolName || grant.PermissionKind != requirement.Kind || grant.Resource != strings.TrimSpace(requirement.Resource) {
			continue
		}
		if grant.Scope == ScopeRun && grant.RunID != request.RunID {
			continue
		}
		if grant.Scope != ScopeRun && grant.Scope != ScopeProject {
			continue
		}
		return grant.ID
	}
	return ""
}

func approvalCovers(values []Approval, requirement tool.PermissionRequirement) bool {
	for _, value := range values {
		if value.Status == ApprovalGranted && value.PermissionKind == requirement.Kind && value.Resource == strings.TrimSpace(requirement.Resource) {
			return true
		}
	}
	return false
}

func containsPermission(values []tool.PermissionRequirement, target tool.PermissionKind) bool {
	for _, value := range values {
		if value.Kind == target {
			return true
		}
	}
	return false
}
func cloneRequirements(values []tool.PermissionRequirement) []tool.PermissionRequirement {
	if len(values) == 0 {
		return []tool.PermissionRequirement{}
	}
	return append([]tool.PermissionRequirement(nil), values...)
}

func callRequires(call tool.Call, target tool.PermissionRequirement) bool {
	if target.Kind == tool.PermissionToolInvoke && (call.Risk == tool.RiskModerate || call.Risk == tool.RiskHigh || call.Risk == tool.RiskDestructive) {
		return target.Resource == strings.TrimSpace(call.ToolName)
	}
	for _, requirement := range call.Permissions {
		if requirement.Kind == target.Kind && strings.TrimSpace(requirement.Resource) == target.Resource {
			return true
		}
	}
	return false
}

func approvalEvent(runID, eventType string, payload any) (events.Envelope, error) {
	return permissionEvent(runID, "run", eventType, payload)
}

func permissionEvent(aggregateID, aggregateType, eventType string, payload any) (events.Envelope, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return events.Envelope{}, err
	}
	eventID, err := id.New()
	if err != nil {
		return events.Envelope{}, err
	}
	return events.New(eventID, aggregateID, aggregateType, eventType, 0, data), nil
}

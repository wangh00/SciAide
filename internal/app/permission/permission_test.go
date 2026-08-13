package permission

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
)

type memoryRepository struct {
	approvals map[string]Approval
	grants    map[string]Grant
	events    []events.Envelope
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{approvals: map[string]Approval{}, grants: map[string]Grant{}}
}

func (r *memoryRepository) ListActiveGrants(_ context.Context, projectID, runID, toolName string, at time.Time) ([]Grant, error) {
	values := []Grant{}
	for _, value := range r.grants {
		if value.ProjectID != projectID || value.ToolName != toolName || value.RevokedAt != nil || (value.ExpiresAt != nil && !value.ExpiresAt.After(at)) {
			continue
		}
		if value.Scope == ScopeProject || (value.Scope == ScopeRun && value.RunID == runID) {
			values = append(values, value)
		}
	}
	return values, nil
}

func (r *memoryRepository) ListGrantedApprovals(_ context.Context, callID string) ([]Approval, error) {
	values := []Approval{}
	for _, value := range r.approvals {
		if value.ToolCallID == callID && value.Status == ApprovalGranted {
			values = append(values, value)
		}
	}
	return values, nil
}

func (r *memoryRepository) CreateApprovalWithEvent(_ context.Context, value Approval, event events.Envelope) error {
	for _, existing := range r.approvals {
		if existing.ToolCallID == value.ToolCallID && existing.Status == ApprovalPending {
			return errors.New("pending approval already exists")
		}
		if existing.ToolCallID == value.ToolCallID && existing.PermissionKind == value.PermissionKind && existing.Resource == value.Resource {
			return errors.New("approval already exists")
		}
	}
	r.approvals[value.ID] = value
	r.events = append(r.events, event)
	return nil
}

func (r *memoryRepository) GetApproval(_ context.Context, id string) (Approval, error) {
	value, exists := r.approvals[id]
	if !exists {
		return Approval{}, errors.New("approval not found")
	}
	return value, nil
}

func (r *memoryRepository) ListApprovalsByRun(_ context.Context, runID string) ([]Approval, error) {
	values := []Approval{}
	for _, value := range r.approvals {
		if value.RunID == runID {
			values = append(values, value)
		}
	}
	return values, nil
}

func (r *memoryRepository) ListPendingApprovals(_ context.Context, runID string) ([]Approval, error) {
	values := []Approval{}
	for _, value := range r.approvals {
		if value.RunID == runID && value.Status == ApprovalPending {
			values = append(values, value)
		}
	}
	return values, nil
}

func (r *memoryRepository) ListGrantsByProject(_ context.Context, projectID string) ([]Grant, error) {
	values := []Grant{}
	for _, value := range r.grants {
		if value.ProjectID == projectID {
			values = append(values, value)
		}
	}
	return values, nil
}

func (r *memoryRepository) GetGrant(_ context.Context, id string) (Grant, error) {
	value, exists := r.grants[id]
	if !exists {
		return Grant{}, errors.New("grant not found")
	}
	return value, nil
}

func (r *memoryRepository) ResolveApprovalWithGrantAndEvent(_ context.Context, id string, expected, next ApprovalStatus, scope Scope, grant *Grant, at time.Time, event events.Envelope) error {
	value, exists := r.approvals[id]
	if !exists || value.Status != expected {
		return ErrApprovalConflict
	}
	value.Status, value.ResolvedScope, value.ResolvedAt = next, scope, &at
	r.approvals[id] = value
	if grant != nil {
		r.grants[grant.ID] = *grant
	}
	r.events = append(r.events, event)
	return nil
}

func (r *memoryRepository) RevokeGrantWithEvent(_ context.Context, id string, at time.Time, event events.Envelope) error {
	value, exists := r.grants[id]
	if !exists || value.RevokedAt != nil {
		return errors.New("grant not found")
	}
	value.RevokedAt = &at
	r.grants[id] = value
	r.events = append(r.events, event)
	return nil
}

func (r *memoryRepository) ExpirePending(_ context.Context, at time.Time) (int64, error) {
	var count int64
	for id, value := range r.approvals {
		if value.Status == ApprovalPending {
			value.Status, value.ResolvedScope, value.ResolvedAt = ApprovalExpired, ScopeCall, &at
			r.approvals[id] = value
			count++
		}
	}
	return count, nil
}

func testCall(risk tool.RiskLevel, permissions ...tool.PermissionRequirement) tool.Call {
	return tool.Call{ID: "call-1", RunID: "run-1", ToolName: "builtin.workspace.read", ToolVersion: "1", Status: tool.CallPending, Risk: risk, Permissions: permissions}
}

func TestEngineAllowsLowRiskCallWithoutPermissions(t *testing.T) {
	engine := NewEngine(newMemoryRepository())
	evaluation, err := engine.Evaluate(context.Background(), EvaluationRequest{ProjectID: "project-1", RunID: "run-1", Call: testCall(tool.RiskLow)})
	if err != nil || evaluation.Decision != DecisionAllow || len(evaluation.Missing) != 0 {
		t.Fatalf("Evaluate() = %#v, %v", evaluation, err)
	}
}

func TestEngineAddsSyntheticInvokePermission(t *testing.T) {
	engine := NewEngine(newMemoryRepository())
	evaluation, err := engine.Evaluate(context.Background(), EvaluationRequest{ProjectID: "project-1", RunID: "run-1", Call: testCall(tool.RiskModerate)})
	if err != nil || evaluation.Decision != DecisionAsk || len(evaluation.Missing) != 1 {
		t.Fatalf("Evaluate() = %#v, %v", evaluation, err)
	}
	if got := evaluation.Missing[0]; got.Kind != tool.PermissionToolInvoke || got.Resource != "builtin.workspace.read" {
		t.Fatalf("synthetic permission = %#v", got)
	}
}

func TestLegacyGrantsNoLongerBypassCurrentPermissionDecision(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	requirement := tool.PermissionRequirement{Kind: tool.PermissionWorkspaceRead, Resource: "papers/current"}
	call := testCall(tool.RiskLow, requirement)
	request := EvaluationRequest{ProjectID: "project-1", RunID: "run-1", Call: call}
	tests := []struct {
		name  string
		grant Grant
		allow bool
	}{
		{"exact project grant", Grant{ID: "exact", ProjectID: "project-1", ToolName: call.ToolName, PermissionKind: requirement.Kind, Resource: requirement.Resource, Scope: ScopeProject}, false},
		{"different tool", Grant{ID: "other-tool", ProjectID: "project-1", ToolName: "builtin.workspace.other", PermissionKind: requirement.Kind, Resource: requirement.Resource, Scope: ScopeProject}, false},
		{"parent resource", Grant{ID: "parent", ProjectID: "project-1", ToolName: call.ToolName, PermissionKind: requirement.Kind, Resource: "papers", Scope: ScopeProject}, false},
		{"different project", Grant{ID: "other-project", ProjectID: "project-2", ToolName: call.ToolName, PermissionKind: requirement.Kind, Resource: requirement.Resource, Scope: ScopeProject}, false},
		{"different run", Grant{ID: "other-run", ProjectID: "project-1", RunID: "run-2", ToolName: call.ToolName, PermissionKind: requirement.Kind, Resource: requirement.Resource, Scope: ScopeRun}, false},
		{"same run", Grant{ID: "same-run", ProjectID: "project-1", RunID: "run-1", ToolName: call.ToolName, PermissionKind: requirement.Kind, Resource: requirement.Resource, Scope: ScopeRun}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryRepository()
			repository.grants[test.grant.ID] = test.grant
			engine := NewEngine(repository)
			engine.now = func() time.Time { return now }
			evaluation, err := engine.Evaluate(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if got := evaluation.Decision == DecisionAllow; got != test.allow {
				t.Fatalf("allow = %v, want %v; evaluation=%#v", got, test.allow, evaluation)
			}
		})
	}
}

func TestPermissionScopeIsNotRestrictedByRisk(t *testing.T) {
	for _, test := range []struct {
		name       string
		risk       tool.RiskLevel
		permission tool.PermissionKind
	}{
		{"external", tool.RiskLow, tool.PermissionFilesystemExternal},
		{"destructive", tool.RiskDestructive, tool.PermissionDestructive},
		{"process", tool.RiskLow, tool.PermissionProcessExecute},
		{"secret", tool.RiskLow, tool.PermissionSecretUse},
		{"high risk", tool.RiskHigh, tool.PermissionWorkspaceRead},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryRepository()
			call := testCall(test.risk, tool.PermissionRequirement{Kind: test.permission, Resource: "exact"})
			engine := NewEngine(repository)
			request := EvaluationRequest{ProjectID: "project-1", RunID: "run-1", Call: call}
			evaluation, err := engine.Evaluate(context.Background(), request)
			if err != nil || evaluation.Decision != DecisionAsk {
				t.Fatalf("Evaluate() = %#v, %v", evaluation, err)
			}
			approval, err := engine.RequestApproval(context.Background(), request, evaluation)
			if err != nil || approval.RequestedScope != ScopeCall {
				t.Fatalf("RequestApproval() = %#v, %v", approval, err)
			}
			if approval.RequestedScope != ScopeCall {
				t.Fatalf("RequestedScope = %q, want call", approval.RequestedScope)
			}
			if resolved, grant, err := engine.Resolve(context.Background(), ResolveCommand{ApprovalID: approval.ID, Allow: true, Scope: ScopeProject}); err != nil || resolved.ResolvedScope != ScopeCall || grant != nil {
				t.Fatalf("Plan approval must resolve one call: approval=%#v grant=%#v err=%v", resolved, grant, err)
			}
		})
	}
}

func TestPermissionModesApplyPerToolCall(t *testing.T) {
	repository := newMemoryRepository()
	engine := NewEngine(repository)
	call := testCall(tool.RiskHigh, tool.PermissionRequirement{Kind: tool.PermissionProcessExecute, Resource: "python analysis.py"})
	request := EvaluationRequest{ProjectID: "project-1", RunID: "run-1", Call: call}
	plan, err := engine.EvaluateCall(context.Background(), request, conversation.PermissionPlan)
	if err != nil || plan.Decision != DecisionAsk || len(plan.Missing) != 1 || plan.Missing[0].Kind != tool.PermissionToolInvoke {
		t.Fatalf("plan evaluation = %#v, %v", plan, err)
	}
	full, err := engine.EvaluateCall(context.Background(), request, conversation.PermissionFullAccess)
	if err != nil || full.Decision != DecisionAllow || len(full.Missing) != 0 {
		t.Fatalf("full access evaluation = %#v, %v", full, err)
	}
}

func TestLegacyEvaluationFlowHandlesMissingPermissionsOneAtATime(t *testing.T) {
	repository := newMemoryRepository()
	engine := NewEngine(repository)
	call := testCall(tool.RiskModerate, tool.PermissionRequirement{Kind: tool.PermissionNetworkDomain, Resource: "api.example.test:443"})
	request := EvaluationRequest{ProjectID: "project-1", RunID: "run-1", Call: call}

	first, err := engine.Evaluate(context.Background(), request)
	if err != nil || len(first.Missing) != 2 || first.Missing[0].Kind != tool.PermissionToolInvoke {
		t.Fatalf("first Evaluate() = %#v, %v", first, err)
	}
	approval, err := engine.RequestApproval(context.Background(), request, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, grant, err := engine.Resolve(context.Background(), ResolveCommand{ApprovalID: approval.ID, Allow: true, Scope: ScopeCall}); err != nil || grant != nil {
		t.Fatalf("Resolve(call) grant=%#v err=%v", grant, err)
	}

	second, err := engine.Evaluate(context.Background(), request)
	if err != nil || len(second.Missing) != 1 || second.Missing[0].Kind != tool.PermissionNetworkDomain {
		t.Fatalf("second Evaluate() = %#v, %v", second, err)
	}
	approval, err = engine.RequestApproval(context.Background(), request, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, grant, err := engine.Resolve(context.Background(), ResolveCommand{ApprovalID: approval.ID, Allow: true, Scope: ScopeProject}); err != nil || grant != nil {
		t.Fatalf("Resolve(call) grant=%#v err=%v", grant, err)
	}
	final, err := engine.Evaluate(context.Background(), request)
	if err != nil || final.Decision != DecisionAllow {
		t.Fatalf("final Evaluate() = %#v, %v", final, err)
	}
}

func TestDenyDoesNotGrantAndResolutionCannotReplay(t *testing.T) {
	repository := newMemoryRepository()
	engine := NewEngine(repository)
	call := testCall(tool.RiskModerate)
	request := EvaluationRequest{ProjectID: "project-1", RunID: "run-1", Call: call}
	evaluation, _ := engine.Evaluate(context.Background(), request)
	approval, err := engine.RequestApproval(context.Background(), request, evaluation)
	if err != nil {
		t.Fatal(err)
	}
	resolved, grant, err := engine.Resolve(context.Background(), ResolveCommand{ApprovalID: approval.ID, Allow: false, Scope: ScopeProject})
	if err != nil || resolved.Status != ApprovalDenied || grant != nil || len(repository.grants) != 0 {
		t.Fatalf("Resolve(deny) = %#v, %#v, %v", resolved, grant, err)
	}
	if _, _, err := engine.Resolve(context.Background(), ResolveCommand{ApprovalID: approval.ID, Allow: true, Scope: ScopeCall}); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("replayed Resolve() error = %v", err)
	}
}

func TestLegacyGrantAPIsAreDisabled(t *testing.T) {
	repository := newMemoryRepository()
	repository.grants["grant-1"] = Grant{ID: "grant-1", ProjectID: "project-1", ToolName: "builtin.workspace.read", PermissionKind: tool.PermissionWorkspaceRead, Resource: "paper.md", Scope: ScopeProject, GrantedBy: SubjectUser, CreatedAt: time.Now().UTC()}
	engine := NewEngine(repository)
	if values, err := engine.ListGrants(context.Background(), "project-1"); err != nil || len(values) != 0 {
		t.Fatalf("ListGrants() = %#v, %v", values, err)
	}
	if err := engine.Revoke(context.Background(), "grant-1"); err == nil {
		t.Fatal("legacy grant revoke API remained active")
	}
}

func TestRequestApprovalRejectsForgedEvaluation(t *testing.T) {
	engine := NewEngine(newMemoryRepository())
	call := testCall(tool.RiskLow)
	_, err := engine.RequestApproval(context.Background(), EvaluationRequest{ProjectID: "project-1", RunID: "run-1", Call: call}, Evaluation{Decision: DecisionAsk, Missing: []tool.PermissionRequirement{{Kind: tool.PermissionDestructive, Resource: "all"}}})
	if err == nil {
		t.Fatal("forged policy evaluation was accepted")
	}
}

package wails

import "github.com/wangh00/SciAide/internal/app/permission"

// PermissionFacade is the typed UI boundary for P2 approvals and scoped
// grants. The frontend cannot create grants directly; every grant originates
// from resolving a persisted Approval.
type PermissionFacade struct {
	lifecycle   *LifecycleContext
	engine      *permission.Engine
	coordinator *permission.Coordinator
}

func NewPermissionFacade(lifecycle *LifecycleContext, engine *permission.Engine, coordinator *permission.Coordinator) *PermissionFacade {
	return &PermissionFacade{lifecycle: lifecycle, engine: engine, coordinator: coordinator}
}

func (f *PermissionFacade) EvaluateToolCall(projectID, toolCallID string) (permission.Coordination, error) {
	return f.coordinator.EvaluateCall(f.lifecycle.Context(), projectID, toolCallID)
}

func (f *PermissionFacade) ResolveApproval(command permission.ResolveCommand) (permission.Coordination, error) {
	return f.coordinator.Resolve(f.lifecycle.Context(), command)
}

func (f *PermissionFacade) ListApprovals(runID string) ([]permission.Approval, error) {
	return f.engine.ListByRun(f.lifecycle.Context(), runID)
}

func (f *PermissionFacade) ListPendingApprovals(runID string) ([]permission.Approval, error) {
	return f.engine.ListPending(f.lifecycle.Context(), runID)
}

func (f *PermissionFacade) ListPermissionGrants(projectID string) ([]permission.Grant, error) {
	return f.engine.ListGrants(f.lifecycle.Context(), projectID)
}

func (f *PermissionFacade) RevokePermissionGrant(grantID string) error {
	return f.engine.Revoke(f.lifecycle.Context(), grantID)
}

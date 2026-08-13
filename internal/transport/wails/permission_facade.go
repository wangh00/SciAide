package wails

import (
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
)

// PermissionFacade is the typed UI boundary for P2 approvals and scoped
// grants. The frontend cannot create grants directly; every grant originates
// from resolving a persisted Approval. Initial policy evaluation is owned by
// AgentLoop and is not exposed to arbitrary UI callers.
type PermissionFacade struct {
	lifecycle   *LifecycleContext
	engine      *permission.Engine
	coordinator *permission.Coordinator
	chat        *chat.Service
}

func NewPermissionFacade(lifecycle *LifecycleContext, engine *permission.Engine, coordinator *permission.Coordinator, chatService *chat.Service) *PermissionFacade {
	return &PermissionFacade{lifecycle: lifecycle, engine: engine, coordinator: coordinator, chat: chatService}
}

func (f *PermissionFacade) ResolveApproval(command permission.ResolveCommand) (permission.Coordination, error) {
	result, err := f.coordinator.Resolve(f.lifecycle.Context(), command)
	if err != nil {
		return result, err
	}
	if result.Run.Status == chat.RunRunning && (result.ToolCall.Status == tool.CallRunning || result.ToolCall.Status.Terminal()) {
		if err := f.chat.Resume(f.lifecycle.Context(), result.Run.ID, command.ApprovalID); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (f *PermissionFacade) ListApprovals(runID string) ([]permission.Approval, error) {
	return f.engine.ListByRun(f.lifecycle.Context(), runID)
}

func (f *PermissionFacade) ListPendingApprovals(runID string) ([]permission.Approval, error) {
	return f.engine.ListPending(f.lifecycle.Context(), runID)
}

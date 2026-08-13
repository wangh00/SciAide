package wails

import (
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/permission"
)

type RunSnapshot struct {
	chat.Snapshot
	PendingApprovals []permission.Approval `json:"pendingApprovals"`
}

type ChatFacade struct {
	lifecycle *LifecycleContext
	service   *chat.Service
	approvals *permission.Engine
}

func NewChatFacade(lifecycle *LifecycleContext, service *chat.Service, approvals *permission.Engine) *ChatFacade {
	return &ChatFacade{lifecycle: lifecycle, service: service, approvals: approvals}
}
func (f *ChatFacade) StartChat(request chat.StartCommand) (chat.Run, error) {
	return f.service.Start(f.lifecycle.Context(), request)
}
func (f *ChatFacade) CancelRun(runID string) error {
	return f.service.Cancel(f.lifecycle.Context(), runID)
}
func (f *ChatFacade) GetRunSnapshot(runID string) (RunSnapshot, error) {
	base, err := f.service.Snapshot(f.lifecycle.Context(), runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	result := RunSnapshot{Snapshot: base, PendingApprovals: []permission.Approval{}}
	if f.approvals != nil {
		result.PendingApprovals, err = f.approvals.ListPending(f.lifecycle.Context(), runID)
	}
	return result, err
}

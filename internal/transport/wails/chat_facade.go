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
func (f *ChatFacade) SteerChat(activeRunID string, request chat.StartCommand) (chat.Run, error) {
	return f.service.Steer(f.lifecycle.Context(), activeRunID, request)
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

func (f *ChatFacade) GetLatestRunSnapshot(conversationID string) (*RunSnapshot, error) {
	base, err := f.service.LatestSnapshot(f.lifecycle.Context(), conversationID)
	if err != nil || base == nil {
		return nil, err
	}
	result := &RunSnapshot{Snapshot: *base, PendingApprovals: []permission.Approval{}}
	if f.approvals != nil {
		result.PendingApprovals, err = f.approvals.ListPending(f.lifecycle.Context(), base.Run.ID)
	}
	return result, err
}

func (f *ChatFacade) GetModelUsageStatistics(modelProfileID string) (chat.UsageStatistics, error) {
	return f.service.UsageStatistics(f.lifecycle.Context(), modelProfileID)
}

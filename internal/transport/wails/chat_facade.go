package wails

import (
	"context"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/apperr"
)

type RunSnapshot struct {
	chat.Snapshot
	PendingApprovals []permission.Approval `json:"pendingApprovals"`
}

type ChatFacade struct {
	lifecycle *LifecycleContext
	service   *chat.Service
	approvals *permission.Engine
	compactor ConversationCompactor
}

type ConversationCompactor interface {
	CompactConversation(ctx context.Context, conversationID string) (contextmemory.CompactionResult, error)
}

func NewChatFacade(lifecycle *LifecycleContext, service *chat.Service, approvals *permission.Engine, compactor ConversationCompactor) *ChatFacade {
	return &ChatFacade{lifecycle: lifecycle, service: service, approvals: approvals, compactor: compactor}
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

func (f *ChatFacade) RespondToUserInput(response userinput.Response) (RunSnapshot, error) {
	run, err := f.service.RespondToUserInput(f.lifecycle.Context(), response)
	if err != nil {
		// Wails renders Error(), while the debug API projects apperr.Public.
		// Keep both the safe desktop explanation and the typed conflict code.
		var stateErr *apperr.Error
		if errors.As(err, &stateErr) && stateErr.Code == "USER_INPUT_EXPIRED" {
			return RunSnapshot{}, fmt.Errorf("%s: %w", stateErr.UserMessage, err)
		}
		return RunSnapshot{}, err
	}
	return f.GetRunSnapshot(run.ID)
}

func (f *ChatFacade) ListConversationActivity(projectID string) ([]chat.ConversationActivity, error) {
	return f.service.ListConversationActivity(f.lifecycle.Context(), projectID)
}
func (f *ChatFacade) GetRunSnapshot(runID string) (RunSnapshot, error) {
	base, err := f.service.UISnapshot(f.lifecycle.Context(), runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	projectToolCallSnapshot(&base)
	result := RunSnapshot{Snapshot: base, PendingApprovals: []permission.Approval{}}
	if f.approvals != nil {
		var approvals []permission.Approval
		approvals, err = f.approvals.ListPending(f.lifecycle.Context(), runID)
		result.PendingApprovals = permission.SafeApprovals(approvals)
	}
	return result, err
}

func (f *ChatFacade) GetLatestRunSnapshot(conversationID string) (*RunSnapshot, error) {
	base, err := f.service.LatestUISnapshot(f.lifecycle.Context(), conversationID)
	return f.latestRunSnapshot(base, err)
}

func (f *ChatFacade) GetLatestOrdinaryRunSnapshot(conversationID string) (*RunSnapshot, error) {
	base, err := f.service.LatestOrdinaryUISnapshot(f.lifecycle.Context(), conversationID)
	return f.latestRunSnapshot(base, err)
}

func (f *ChatFacade) latestRunSnapshot(base *chat.Snapshot, err error) (*RunSnapshot, error) {
	if err != nil || base == nil {
		return nil, err
	}
	projectToolCallSnapshot(base)
	result := &RunSnapshot{Snapshot: *base, PendingApprovals: []permission.Approval{}}
	if f.approvals != nil {
		var approvals []permission.Approval
		approvals, err = f.approvals.ListPending(f.lifecycle.Context(), base.Run.ID)
		result.PendingApprovals = permission.SafeApprovals(approvals)
	}
	return result, err
}

// projectToolCallSnapshot is the transport boundary for ordinary chat
// activity. The Agent and Workflow bridge still use the complete local Call
// record, while Wails receives only the same bounded, redacted argument view
// used by Workflow activity projections.
func projectToolCallSnapshot(snapshot *chat.Snapshot) {
	if snapshot == nil {
		return
	}
	for index := range snapshot.ToolCalls {
		call := &snapshot.ToolCalls[index]
		call.Arguments = tool.SafeActivityArguments(call.Arguments)
		call.Permissions = tool.SafeActivityPermissions(call.Permissions)
	}
}

func (f *ChatFacade) GetUsageDashboard(query chat.UsageQuery) (chat.UsageDashboard, error) {
	return f.service.UsageDashboard(f.lifecycle.Context(), query)
}

func (f *ChatFacade) GetUsageRequests(query chat.UsageRequestQuery) (chat.UsageRequestPage, error) {
	return f.service.UsageRequests(f.lifecycle.Context(), query)
}

func (f *ChatFacade) CompactConversation(conversationID string) (contextmemory.CompactionResult, error) {
	if f.compactor == nil {
		return contextmemory.CompactionResult{}, fmt.Errorf("conversation compaction is not configured")
	}
	return f.compactor.CompactConversation(f.lifecycle.Context(), conversationID)
}

package permission

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type coordinatorTools struct{ call tool.Call }

func (t *coordinatorTools) Get(context.Context, string) (tool.Call, error) { return t.call, nil }
func (t *coordinatorTools) AwaitApproval(_ context.Context, _ string) (tool.Call, error) {
	if t.call.Status != tool.CallPending {
		return tool.Call{}, tool.ErrTransitionConflict
	}
	t.call.Status = tool.CallAwaitingApproval
	return t.call, nil
}
func (t *coordinatorTools) Start(_ context.Context, _ string) (tool.Call, error) {
	if t.call.Status != tool.CallAwaitingApproval {
		return tool.Call{}, tool.ErrTransitionConflict
	}
	t.call.Status = tool.CallRunning
	return t.call, nil
}
func (t *coordinatorTools) Finish(_ context.Context, _ string, result tool.Result, code, message string) (tool.Call, error) {
	if t.call.Status != tool.CallAwaitingApproval {
		return tool.Call{}, tool.ErrTransitionConflict
	}
	t.call.Status, t.call.Result, t.call.ErrorCode, t.call.ErrorMessage = tool.CallDenied, &result, code, message
	return t.call, nil
}

type coordinatorRuns struct {
	run       chat.Run
	projectID string
}

func (r *coordinatorRuns) Get(context.Context, string) (chat.Run, error) { return r.run, nil }
func (r *coordinatorRuns) ProjectIDForRun(context.Context, string) (string, error) {
	return r.projectID, nil
}
func (r *coordinatorRuns) TransitionStatus(_ context.Context, _ string, expected, next chat.RunStatus, at time.Time) error {
	if r.run.Status != expected {
		return errors.New("run transition conflict")
	}
	r.run.Status, r.run.UpdatedAt = next, at
	return nil
}

func newCoordinatorFixture() (*Coordinator, *memoryRepository, *coordinatorTools, *coordinatorRuns) {
	repository := newMemoryRepository()
	engine := NewEngine(repository)
	tools := &coordinatorTools{call: testCall(tool.RiskModerate, tool.PermissionRequirement{Kind: tool.PermissionNetworkDomain, Resource: "api.example.test:443"})}
	runs := &coordinatorRuns{projectID: "project-1", run: chat.Run{ID: "run-1", Status: chat.RunRunning}}
	return NewCoordinator(engine, tools, runs), repository, tools, runs
}

func TestCoordinatorMovesThroughSequentialApprovalsToReadyTool(t *testing.T) {
	coordinator, _, tools, runs := newCoordinatorFixture()
	ctx := context.Background()
	result, err := coordinator.EvaluateCall(ctx, "project-1", tools.call.ID)
	if err != nil || result.Approval == nil || result.Approval.PermissionKind != tool.PermissionToolInvoke || tools.call.Status != tool.CallAwaitingApproval || runs.run.Status != chat.RunWaitingApproval {
		t.Fatalf("EvaluateCall() = %#v, %v; call=%s run=%s", result, err, tools.call.Status, runs.run.Status)
	}
	result, err = coordinator.Resolve(ctx, ResolveCommand{ApprovalID: result.Approval.ID, Allow: true, Scope: ScopeCall})
	if err != nil || result.Approval == nil || result.Approval.PermissionKind != tool.PermissionNetworkDomain || tools.call.Status != tool.CallAwaitingApproval || runs.run.Status != chat.RunWaitingApproval {
		t.Fatalf("Resolve(first) = %#v, %v; call=%s run=%s", result, err, tools.call.Status, runs.run.Status)
	}
	result, err = coordinator.Resolve(ctx, ResolveCommand{ApprovalID: result.Approval.ID, Allow: true, Scope: ScopeProject})
	if err != nil || result.Evaluation.Decision != DecisionAllow || tools.call.Status != tool.CallRunning || runs.run.Status != chat.RunRunning {
		t.Fatalf("Resolve(second) = %#v, %v; call=%s run=%s", result, err, tools.call.Status, runs.run.Status)
	}
}

func TestCoordinatorDenialTerminatesCallWithoutGrant(t *testing.T) {
	coordinator, repository, tools, runs := newCoordinatorFixture()
	ctx := context.Background()
	result, err := coordinator.EvaluateCall(ctx, "project-1", tools.call.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err = coordinator.Resolve(ctx, ResolveCommand{ApprovalID: result.Approval.ID, Allow: false, Scope: ScopeProject})
	if err != nil || result.Evaluation.Decision != DecisionDeny || tools.call.Status != tool.CallDenied || runs.run.Status != chat.RunRunning || len(repository.grants) != 0 {
		t.Fatalf("Resolve(deny) = %#v, %v; call=%s run=%s grants=%d", result, err, tools.call.Status, runs.run.Status, len(repository.grants))
	}
}

func TestCoordinatorRejectsProjectMismatchBeforeMutation(t *testing.T) {
	coordinator, repository, tools, runs := newCoordinatorFixture()
	if _, err := coordinator.EvaluateCall(context.Background(), "project-2", tools.call.ID); err == nil {
		t.Fatal("project mismatch was accepted")
	}
	if tools.call.Status != tool.CallPending || runs.run.Status != chat.RunRunning || len(repository.approvals) != 0 {
		t.Fatal("state changed before project ownership validation")
	}
}

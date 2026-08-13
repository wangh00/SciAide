package permission

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
)

// ToolCalls is the narrow tool application port used by the approval flow.
type ToolCalls interface {
	Get(ctx context.Context, callID string) (tool.Call, error)
	AwaitApproval(ctx context.Context, callID string) (tool.Call, error)
	Start(ctx context.Context, callID string) (tool.Call, error)
	Finish(ctx context.Context, callID string, result tool.Result, errorCode, errorMessage string) (tool.Call, error)
}

// Runs keeps permission coordination independent of the SQLite adapter.
type Runs interface {
	Get(ctx context.Context, runID string) (chat.Run, error)
	ProjectIDForRun(ctx context.Context, runID string) (string, error)
	TransitionStatus(ctx context.Context, runID string, expected, next chat.RunStatus, at time.Time) error
}

type Coordination struct {
	Evaluation Evaluation `json:"evaluation"`
	Approval   *Approval  `json:"approval,omitempty"`
	Grant      *Grant     `json:"grant,omitempty"`
	ToolCall   tool.Call  `json:"toolCall"`
	Run        chat.Run   `json:"run"`
}

// Coordinator proves the P2.2 safety path from a persisted ToolCall to a
// policy decision and, when needed, a durable user approval. It deliberately
// does not invoke the tool; P2.3's executor consumes the ready/running call.
type Coordinator struct {
	engine *Engine
	tools  ToolCalls
	runs   Runs
	now    func() time.Time
}

func NewCoordinator(engine *Engine, tools ToolCalls, runs Runs) *Coordinator {
	return &Coordinator{engine: engine, tools: tools, runs: runs, now: func() time.Time { return time.Now().UTC() }}
}

func (c *Coordinator) EvaluateCall(ctx context.Context, projectID, callID string) (Coordination, error) {
	if c.engine == nil || c.tools == nil || c.runs == nil {
		return Coordination{}, fmt.Errorf("approval coordinator is not configured")
	}
	projectID, callID = strings.TrimSpace(projectID), strings.TrimSpace(callID)
	call, run, err := c.loadOwnedCall(ctx, projectID, callID)
	if err != nil {
		return Coordination{}, err
	}
	if call.Status != tool.CallPending {
		return Coordination{}, fmt.Errorf("tool call is not pending")
	}
	request := EvaluationRequest{ProjectID: projectID, RunID: call.RunID, Call: call}
	evaluation, err := c.engine.EvaluateCall(ctx, request, run.PermissionMode)
	if err != nil {
		return Coordination{}, err
	}
	result := Coordination{Evaluation: evaluation, ToolCall: call, Run: run}
	if evaluation.Decision == DecisionAllow {
		call, err = c.tools.Start(ctx, call.ID)
		if err != nil {
			return Coordination{}, err
		}
		result.ToolCall = call
		return result, nil
	}
	if evaluation.Decision != DecisionAsk {
		return result, nil
	}
	if run.Status != chat.RunRunning {
		return Coordination{}, fmt.Errorf("run must be running before approval wait")
	}
	approval, err := c.engine.RequestApproval(ctx, request, evaluation)
	if err != nil {
		return Coordination{}, err
	}
	call, err = c.tools.AwaitApproval(ctx, call.ID)
	if err != nil {
		return Coordination{}, fmt.Errorf("mark tool call awaiting approval: %w", err)
	}
	if err := c.runs.TransitionStatus(ctx, run.ID, chat.RunRunning, chat.RunWaitingApproval, c.now()); err != nil {
		return Coordination{}, err
	}
	run, err = c.runs.Get(ctx, run.ID)
	if err != nil {
		return Coordination{}, err
	}
	result.Approval, result.ToolCall, result.Run = &approval, call, run
	return result, nil
}

func (c *Coordinator) Resolve(ctx context.Context, command ResolveCommand) (Coordination, error) {
	if c.engine == nil || c.tools == nil || c.runs == nil {
		return Coordination{}, fmt.Errorf("approval coordinator is not configured")
	}
	pending, err := c.engine.Get(ctx, command.ApprovalID)
	if err != nil {
		return Coordination{}, err
	}
	if pending.Status != ApprovalPending {
		return Coordination{}, ErrApprovalConflict
	}
	call, run, err := c.loadOwnedCall(ctx, pending.ProjectID, pending.ToolCallID)
	if err != nil {
		return Coordination{}, err
	}
	if call.Status != tool.CallAwaitingApproval || run.Status != chat.RunWaitingApproval {
		return Coordination{}, fmt.Errorf("approval state does not match its tool call and run")
	}
	resolved, grant, err := c.engine.Resolve(ctx, command)
	if err != nil {
		return Coordination{}, err
	}
	result := Coordination{Approval: &resolved, Grant: grant, ToolCall: call, Run: run}
	if resolved.Status == ApprovalDenied {
		call, err = c.tools.Finish(ctx, call.ID, tool.Result{Status: tool.ResultDenied, Text: "用户拒绝了工具调用权限。"}, "TOOL_PERMISSION_DENIED", "用户拒绝了工具调用权限")
		if err != nil {
			return Coordination{}, err
		}
		if err := c.runs.TransitionStatus(ctx, run.ID, chat.RunWaitingApproval, chat.RunRunning, c.now()); err != nil {
			return Coordination{}, err
		}
		result.Evaluation = Evaluation{Decision: DecisionDeny, Reason: "用户拒绝了工具调用权限。", Missing: []tool.PermissionRequirement{}}
	} else {
		request := EvaluationRequest{ProjectID: resolved.ProjectID, RunID: call.RunID, Call: call}
		evaluation, err := c.engine.EvaluateCall(ctx, request, run.PermissionMode)
		if err != nil {
			return Coordination{}, err
		}
		result.Evaluation = evaluation
		if evaluation.Decision == DecisionAsk {
			next, err := c.engine.RequestApproval(ctx, request, evaluation)
			if err != nil {
				return Coordination{}, err
			}
			result.Approval = &next
		} else if evaluation.Decision == DecisionAllow {
			call, err = c.tools.Start(ctx, call.ID)
			if err != nil {
				return Coordination{}, err
			}
			if err := c.runs.TransitionStatus(ctx, run.ID, chat.RunWaitingApproval, chat.RunRunning, c.now()); err != nil {
				return Coordination{}, err
			}
		}
	}
	result.ToolCall, err = c.tools.Get(ctx, call.ID)
	if err != nil {
		return Coordination{}, err
	}
	result.Run, err = c.runs.Get(ctx, run.ID)
	return result, err
}

func (c *Coordinator) loadOwnedCall(ctx context.Context, projectID, callID string) (tool.Call, chat.Run, error) {
	if projectID == "" || callID == "" {
		return tool.Call{}, chat.Run{}, fmt.Errorf("project and tool call are required")
	}
	call, err := c.tools.Get(ctx, callID)
	if err != nil {
		return tool.Call{}, chat.Run{}, err
	}
	actualProjectID, err := c.runs.ProjectIDForRun(ctx, call.RunID)
	if err != nil {
		return tool.Call{}, chat.Run{}, err
	}
	if actualProjectID != projectID {
		return tool.Call{}, chat.Run{}, fmt.Errorf("tool call does not belong to project")
	}
	run, err := c.runs.Get(ctx, call.RunID)
	return call, run, err
}

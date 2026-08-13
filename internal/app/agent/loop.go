package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

type ModelResolver interface {
	Resolve(ctx context.Context, profileID, modelID string) (model.ChatModel, error)
}

type Runs interface {
	Get(ctx context.Context, runID string) (chat.Run, error)
	Update(ctx context.Context, value chat.Run) error
	ProjectIDForRun(ctx context.Context, runID string) (string, error)
}

type Conversations interface {
	UpdateMessageText(ctx context.Context, messageID string, status conversation.MessageStatus, text string, updatedAt time.Time) error
	ListMessages(ctx context.Context, conversationID string, limit int) ([]conversation.Message, error)
}

type ToolCalls interface {
	ProposeRegistered(ctx context.Context, registry tool.Registry, toolName string, cmd tool.CreateCommand) (tool.Call, error)
	ListByRun(ctx context.Context, runID string) ([]tool.Call, error)
}

type ApprovalCoordinator interface {
	EvaluateCall(ctx context.Context, projectID, callID string) (permission.Coordination, error)
}

type ToolExecutor interface {
	Execute(ctx context.Context, projectID, callID string) (tool.Execution, error)
}

func (l *Loop) Resume(ctx context.Context, runID string) Outcome {
	run, err := l.runs.Get(context.Background(), strings.TrimSpace(runID))
	if err != nil || run.Status != chat.RunRunning {
		return OutcomeFailed
	}
	return l.Run(ctx, run.ID)
}

type Observer interface {
	RunStarted(run chat.Run)
	ContentStarted(run chat.Run)
	ContentDelta(run chat.Run, delta string)
	UsageUpdated(run chat.Run, usage model.Usage)
	ApprovalRequired(run chat.Run, coordination permission.Coordination)
	RunCompleted(run chat.Run, text string)
	RunFailed(run chat.Run, code, message string)
	RunCancelled(run chat.Run)
}

type NopObserver struct{}

func (NopObserver) RunStarted(chat.Run)                                {}
func (NopObserver) ContentStarted(chat.Run)                            {}
func (NopObserver) ContentDelta(chat.Run, string)                      {}
func (NopObserver) UsageUpdated(chat.Run, model.Usage)                 {}
func (NopObserver) ApprovalRequired(chat.Run, permission.Coordination) {}
func (NopObserver) RunCompleted(chat.Run, string)                      {}
func (NopObserver) RunFailed(chat.Run, string, string)                 {}
func (NopObserver) RunCancelled(chat.Run)                              {}

type Options struct {
	Budget         RunBudget
	ContextBuilder *ContextBuilder
}

type Outcome string

const (
	OutcomeCompleted       Outcome = "completed"
	OutcomeWaitingApproval Outcome = "waiting_approval"
	OutcomeFailed          Outcome = "failed"
	OutcomeCancelled       Outcome = "cancelled"
)

type Loop struct {
	runs          Runs
	conversations Conversations
	tools         ToolCalls
	registry      tool.Registry
	approvals     ApprovalCoordinator
	executor      ToolExecutor
	models        ModelResolver
	observer      Observer
	builder       *ContextBuilder
	budget        RunBudget
	now           func() time.Time
}

func NewLoop(runs Runs, conversations Conversations, tools ToolCalls, registry tool.Registry, approvals ApprovalCoordinator, executor ToolExecutor, models ModelResolver, observer Observer, options Options) *Loop {
	if observer == nil {
		observer = NopObserver{}
	}
	if options.ContextBuilder == nil {
		options.ContextBuilder = NewContextBuilder(0)
	}
	return &Loop{runs: runs, conversations: conversations, tools: tools, registry: registry, approvals: approvals, executor: executor, models: models, observer: observer, builder: options.ContextBuilder, budget: normalizeBudget(options.Budget), now: func() time.Time { return time.Now().UTC() }}
}

func (l *Loop) Run(ctx context.Context, runID string) Outcome {
	run, err := l.runs.Get(context.Background(), strings.TrimSpace(runID))
	if err != nil {
		return OutcomeFailed
	}
	if run.Status != chat.RunQueued && run.Status != chat.RunRunning {
		return OutcomeFailed
	}
	if run.Status == chat.RunQueued {
		now := l.now()
		run.Status, run.StartedAt, run.UpdatedAt = chat.RunRunning, &now, now
		if err := l.runs.Update(context.Background(), run); err != nil {
			return OutcomeFailed
		}
		l.observer.RunStarted(run)
		l.observer.ContentStarted(run)
	}
	if run.Status == chat.RunRunning {
		run.ErrorCode, run.ErrorMessage, run.CompletedAt = "", "", nil
		run.FinishReason = ""
	}
	outcome, err := l.execute(ctx, &run)
	if err == nil {
		return outcome
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		l.cancel(&run)
		return OutcomeCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		l.fail(&run, "RUN_DURATION_BUDGET_EXCEEDED", "Agent 运行已达到时间上限，已停止继续调用。")
		return OutcomeFailed
	}
	public := apperr.Public(err)
	l.fail(&run, public.Code, public.Message)
	return OutcomeFailed
}

func (l *Loop) execute(ctx context.Context, run *chat.Run) (Outcome, error) {
	if l.runs == nil || l.conversations == nil || l.tools == nil || l.registry == nil || l.approvals == nil || l.executor == nil || l.models == nil {
		return OutcomeFailed, fmt.Errorf("agent loop is not configured")
	}
	projectID, err := l.runs.ProjectIDForRun(ctx, run.ID)
	if err != nil {
		return OutcomeFailed, err
	}
	messages, err := l.conversations.ListMessages(ctx, run.ConversationID, 200)
	if err != nil {
		return OutcomeFailed, &apperr.Error{Code: "CONTEXT_LOAD_FAILED", UserMessage: "无法加载会话上下文。", Cause: err}
	}
	definitions, err := l.registry.Definitions(ctx)
	if err != nil {
		return OutcomeFailed, err
	}
	calls, err := l.tools.ListByRun(ctx, run.ID)
	if err != nil {
		return OutcomeFailed, err
	}
	startedAt := run.CreatedAt
	if run.StartedAt != nil {
		startedAt = *run.StartedAt
	}
	budget := newBudgetCounter(l.budget, startedAt, len(calls))
	runCtx, cancelRun := context.WithDeadline(ctx, startedAt.Add(budget.budget.MaxDuration))
	defer cancelRun()
	ctx = runCtx
	text := assistantText(messages, run.AssistantMessageID)
	waiting, err := l.processCalls(ctx, run, projectID, calls)
	if err != nil {
		return OutcomeFailed, err
	} else if waiting {
		return OutcomeWaitingApproval, nil
	}
	calls, err = l.tools.ListByRun(ctx, run.ID)
	if err != nil {
		return OutcomeFailed, err
	}
	if blocked := firstUnresolvedToolCall(calls); blocked != nil {
		return OutcomeFailed, &apperr.Error{Code: "TOOL_STATE_INVALID", UserMessage: "仍有未完成的工具调用，无法继续请求模型。"}
	}
	chatModel, err := l.models.Resolve(ctx, run.ModelProfileID, run.ModelID)
	if err != nil {
		return OutcomeFailed, err
	}

	for {
		if err := budget.beforeModelTurn(); err != nil {
			return OutcomeFailed, budgetError(err)
		}
		request, err := l.builder.Build(ctx, messages, run.AssistantMessageID, definitions, calls)
		if err != nil {
			return OutcomeFailed, err
		}
		stream, err := chatModel.Stream(ctx, request)
		if err != nil {
			return OutcomeFailed, err
		}
		turn, err := l.receiveTurn(ctx, run, stream, &text)
		closeErr := stream.Close()
		if err != nil {
			return OutcomeFailed, err
		}
		if closeErr != nil {
			return OutcomeFailed, closeErr
		}
		now := l.now()
		if err := l.conversations.UpdateMessageText(context.Background(), run.AssistantMessageID, conversation.MessageStreaming, text, now); err != nil {
			return OutcomeFailed, &apperr.Error{Code: "MESSAGE_SAVE_FAILED", UserMessage: "模型中间结果无法保存。", Cause: err}
		}
		run.FinishReason, run.UpdatedAt = turn.finishReason, now
		if err := l.runs.Update(context.Background(), *run); err != nil {
			return OutcomeFailed, err
		}
		if len(turn.toolCalls) == 0 {
			if turn.finishReason == "tool_calls" {
				return OutcomeFailed, &apperr.Error{Code: "MODEL_TOOL_CALL_INVALID", UserMessage: "模型声明调用工具但没有给出有效调用。"}
			}
			return l.complete(run, text, turn.finishReason)
		}
		if turn.finishReason != "" && turn.finishReason != "tool_calls" {
			return OutcomeFailed, &apperr.Error{Code: "MODEL_TOOL_CALL_INVALID", UserMessage: "模型在非工具终止状态中返回了工具调用。"}
		}
		if err := ValidateProviderToolCalls(turn.toolCalls); err != nil {
			return OutcomeFailed, &apperr.Error{Code: "MODEL_TOOL_CALL_INVALID", UserMessage: "模型返回了无效或重复的工具调用。", Cause: err}
		}
		proposed := make([]tool.Call, 0, len(turn.toolCalls))
		for _, providerCall := range turn.toolCalls {
			if err := budget.beforeToolCall(); err != nil {
				return OutcomeFailed, budgetError(err)
			}
			call, err := l.tools.ProposeRegistered(ctx, l.registry, providerCall.Name, tool.CreateCommand{RunID: run.ID, ProviderCallID: providerCall.ID, Arguments: providerCall.Arguments})
			if err != nil {
				return OutcomeFailed, &apperr.Error{Code: "TOOL_CALL_REJECTED", UserMessage: "模型提出了无效或不可用的工具调用。", Cause: err}
			}
			proposed = append(proposed, call)
		}
		waiting, err := l.processCalls(ctx, run, projectID, proposed)
		if err != nil {
			return OutcomeFailed, err
		} else if waiting {
			return OutcomeWaitingApproval, nil
		}
		calls, err = l.tools.ListByRun(ctx, run.ID)
		if err != nil {
			return OutcomeFailed, err
		}
	}
}

func firstUnresolvedToolCall(calls []tool.Call) *tool.Call {
	for index := range calls {
		if !calls[index].Status.Terminal() {
			return &calls[index]
		}
	}
	return nil
}

func (l *Loop) processCalls(ctx context.Context, run *chat.Run, projectID string, calls []tool.Call) (bool, error) {
	for _, call := range calls {
		if call.Status == tool.CallDenied {
			continue
		}
		switch call.Status {
		case tool.CallPending:
			coordination, err := l.approvals.EvaluateCall(ctx, projectID, call.ID)
			if err != nil {
				return false, err
			}
			switch coordination.Evaluation.Decision {
			case permission.DecisionAsk:
				l.observer.ApprovalRequired(coordination.Run, coordination)
				return true, nil
			case permission.DecisionAllow:
				call = coordination.ToolCall
			default:
				return false, &apperr.Error{Code: "TOOL_PERMISSION_DENIED", UserMessage: "工具调用未通过权限策略。"}
			}
		case tool.CallAwaitingApproval:
			return true, nil
		case tool.CallCompleted, tool.CallFailed, tool.CallDenied, tool.CallCancelled, tool.CallInterrupted:
			continue
		}
		if call.Status != tool.CallRunning {
			return false, &apperr.Error{Code: "TOOL_STATE_INVALID", UserMessage: "工具调用没有进入可执行状态。"}
		}
		if _, err := l.executor.Execute(ctx, projectID, call.ID); err != nil {
			return false, err
		}
	}
	return false, nil
}

type modelTurn struct {
	toolCalls    []model.ToolCall
	finishReason string
}

func (l *Loop) receiveTurn(ctx context.Context, run *chat.Run, stream model.Stream, text *string) (modelTurn, error) {
	turn := modelTurn{toolCalls: make([]model.ToolCall, 0)}
	pending := ""
	lastPersist, lastEmit := l.now(), l.now()
	flush := func() {
		if pending != "" {
			l.observer.ContentDelta(*run, pending)
			pending = ""
			lastEmit = l.now()
		}
	}
	for {
		event, recvErr := stream.Recv()
		if event.Type == model.EventTextDelta && event.Text != "" {
			*text += event.Text
			pending += event.Text
			now := l.now()
			if len(pending) >= 64 || now.Sub(lastEmit) >= 35*time.Millisecond {
				flush()
			}
			if len(*text) >= 256 || now.Sub(lastPersist) >= 200*time.Millisecond {
				_ = l.conversations.UpdateMessageText(context.Background(), run.AssistantMessageID, conversation.MessageStreaming, *text, now)
				lastPersist = now
			}
		}
		if event.Type == model.EventToolCall && event.ToolCall != nil {
			turn.toolCalls = append(turn.toolCalls, *event.ToolCall)
		}
		if event.Type == model.EventUsage && event.Usage != nil {
			run.InputTokens += event.Usage.InputTokens
			run.OutputTokens += event.Usage.OutputTokens
			l.observer.UsageUpdated(*run, *event.Usage)
		}
		if event.FinishReason != "" {
			turn.finishReason = event.FinishReason
		}
		if recvErr != nil {
			flush()
			if errors.Is(recvErr, io.EOF) {
				return turn, nil
			}
			return turn, recvErr
		}
		if event.Type == model.EventDone {
			flush()
			return turn, nil
		}
	}
}

func (l *Loop) complete(run *chat.Run, text, finishReason string) (Outcome, error) {
	now := l.now()
	if err := l.conversations.UpdateMessageText(context.Background(), run.AssistantMessageID, conversation.MessageComplete, text, now); err != nil {
		return OutcomeFailed, &apperr.Error{Code: "MESSAGE_SAVE_FAILED", UserMessage: "回答已生成，但保存失败。", Cause: err}
	}
	run.Status, run.FinishReason, run.ErrorCode, run.ErrorMessage, run.UpdatedAt, run.CompletedAt = chat.RunCompleted, finishReason, "", "", now, &now
	if err := l.runs.Update(context.Background(), *run); err != nil {
		return OutcomeFailed, err
	}
	l.observer.RunCompleted(*run, text)
	return OutcomeCompleted, nil
}

func (l *Loop) fail(run *chat.Run, code, message string) {
	now := l.now()
	text := l.currentText(run)
	status := conversation.MessageFailed
	if text != "" {
		status = conversation.MessageIncomplete
	}
	_ = l.conversations.UpdateMessageText(context.Background(), run.AssistantMessageID, status, text, now)
	run.Status, run.ErrorCode, run.ErrorMessage, run.UpdatedAt, run.CompletedAt = chat.RunFailed, code, message, now, &now
	_ = l.runs.Update(context.Background(), *run)
	l.observer.RunFailed(*run, code, message)
}

func (l *Loop) cancel(run *chat.Run) {
	now := l.now()
	text := l.currentText(run)
	_ = l.conversations.UpdateMessageText(context.Background(), run.AssistantMessageID, conversation.MessageIncomplete, text, now)
	run.Status, run.ErrorCode, run.ErrorMessage, run.UpdatedAt, run.CompletedAt = chat.RunCancelled, "RUN_CANCELLED", "已停止生成", now, &now
	_ = l.runs.Update(context.Background(), *run)
	l.observer.RunCancelled(*run)
}

func (l *Loop) currentText(run *chat.Run) string {
	messages, err := l.conversations.ListMessages(context.Background(), run.ConversationID, 200)
	if err != nil {
		return ""
	}
	return assistantText(messages, run.AssistantMessageID)
}

func assistantText(messages []conversation.Message, messageID string) string {
	for _, message := range messages {
		if message.ID == messageID {
			return conversationText(message)
		}
	}
	return ""
}

func budgetError(err error) error {
	code := err.Error()
	message := "Agent 运行已达到安全预算上限，已停止继续调用。"
	return &apperr.Error{Code: code, UserMessage: message, Cause: err}
}

func ValidateProviderToolCalls(calls []model.ToolCall) error {
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" {
			return fmt.Errorf("tool call id and name are required")
		}
		if _, exists := seen[call.ID]; exists {
			return fmt.Errorf("duplicate provider tool call id")
		}
		seen[call.ID] = struct{}{}
		var object map[string]json.RawMessage
		if json.Unmarshal(call.Arguments, &object) != nil || object == nil {
			return fmt.Errorf("tool call arguments must be a JSON object")
		}
	}
	return nil
}

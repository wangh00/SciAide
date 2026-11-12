package agent

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
)

type EventSink interface {
	PublishRunEvent(runID, eventType string, payload any)
}

// ToolActivityObserver is optional so existing embedders can keep the
// original observer contract while receiving durable tool lifecycle events.
type ToolActivityObserver interface {
	ToolActivity(run chat.Run, phase string, call tool.Call, execution *tool.Execution, activityErr error)
}

// Optional: status telemetry must not change existing observer contracts.
type RuntimeActivityObserver interface {
	RuntimeActivity(run chat.Run, activity chat.RuntimeActivity)
}

func (l *Loop) publishRuntimeActivity(run chat.Run, phase string) {
	if observer, ok := l.observer.(RuntimeActivityObserver); ok {
		observer.RuntimeActivity(run, chat.RuntimeActivity{RunID: run.ID, Phase: phase, TurnIndex: run.ModelTurns, StartedAt: l.now()})
	}
}

func (o *EventObserver) RuntimeActivity(run chat.Run, activity chat.RuntimeActivity) {
	o.sink.PublishRunEvent(run.ID, "run.activity", map[string]any{"activity": activity})
}

// EventObserver adapts AgentLoop lifecycle callbacks to durable, versioned
// RunEvents without making the chat package depend on permission types.
type EventObserver struct{ sink EventSink }

func NewEventObserver(sink EventSink) *EventObserver { return &EventObserver{sink: sink} }

func (o *EventObserver) ToolActivity(run chat.Run, phase string, call tool.Call, execution *tool.Execution, activityErr error) {
	// Activity events are notifications, not the audit transport. Keep them
	// bounded and redacted; the complete call/result remains available through
	// the snapshot endpoint, which applies the same projection at the Wails
	// boundary.
	payload := map[string]any{
		"runId": run.ID, "phase": phase,
		"toolCall": map[string]any{
			"id": call.ID, "providerCallId": call.ProviderCallID,
			"toolName": call.ToolName, "status": call.Status, "risk": call.Risk,
			"arguments":    tool.SafeActivityArguments(call.Arguments),
			"permissions":  tool.SafeActivityPermissions(call.Permissions),
			"errorCode":    tool.SafeActivityText(call.ErrorCode, 120),
			"errorMessage": tool.SafeActivityText(call.ErrorMessage, 500),
		},
	}
	if call.Result != nil {
		payload["toolResult"] = map[string]any{
			"status": call.Result.Status, "text": tool.SafeActivityText(call.Result.Text, 900),
			"durationMillis": call.Result.Meta.DurationMillis, "truncated": call.Result.Truncated,
		}
	}
	if activityErr != nil && call.ErrorMessage == "" {
		payload["error"] = "tool execution failed"
	}
	o.sink.PublishRunEvent(run.ID, "tool.activity", payload)
}

func (o *EventObserver) RunStarted(run chat.Run) {
	o.sink.PublishRunEvent(run.ID, "run.started", map[string]any{"runId": run.ID, "status": run.Status})
}
func (o *EventObserver) ContentStarted(run chat.Run) {
	o.sink.PublishRunEvent(run.ID, "content.started", map[string]any{"runId": run.ID, "messageId": run.AssistantMessageID})
}
func (o *EventObserver) ContentDelta(run chat.Run, delta string) {
	o.sink.PublishRunEvent(run.ID, "content.delta", map[string]any{"messageId": run.AssistantMessageID, "delta": delta})
}
func (o *EventObserver) ActivityCompleted(run chat.Run, step chat.RunStep) {
	o.sink.PublishRunEvent(run.ID, "activity.completed", map[string]any{"step": step})
}
func (o *EventObserver) ReasoningUpdated(run chat.Run) {
	o.sink.PublishRunEvent(run.ID, "run.reasoning", map[string]any{"run": run})
}
func (o *EventObserver) UsageUpdated(run chat.Run, usage model.Usage) {
	o.sink.PublishRunEvent(run.ID, "usage.updated", usage)
}
func (o *EventObserver) Retrying(run chat.Run, retry RetryStatus) {
	o.sink.PublishRunEvent(run.ID, "run.retrying", map[string]any{"messageId": run.AssistantMessageID, "retry": retry})
}
func (o *EventObserver) RetryRecovered(run chat.Run) {
	o.sink.PublishRunEvent(run.ID, "run.retry.recovered", map[string]any{"runId": run.ID})
}

func (l *Loop) submissionRejected(run chat.Run, candidate, diagnostic string, attempt int) {
	if observer, ok := l.observer.(interface {
		SubmissionRejected(chat.Run, string, string, int)
	}); ok {
		observer.SubmissionRejected(run, candidate, diagnostic, attempt)
	}
}

func (o *EventObserver) SubmissionRejected(run chat.Run, candidate, diagnostic string, attempt int) {
	hash := sha256.Sum256([]byte(candidate))
	o.sink.PublishRunEvent(run.ID, "run.submission_rejected", map[string]any{
		"turnIndex": run.ModelTurns, "correctionsUsed": attempt, "candidateSha256": hex.EncodeToString(hash[:]),
		"diagnostic": tool.SafeActivityText(diagnostic, 12000),
	})
}

func (o *EventObserver) RetryReceiving(run chat.Run, retry RetryStatus) {
	o.sink.PublishRunEvent(run.ID, "run.retrying", map[string]any{"messageId": run.AssistantMessageID, "retry": retry})
}

func (l *Loop) implementationSaved(run chat.Run, draft chat.ImplementationDraft) {
	if observer, ok := l.observer.(interface {
		ImplementationSaved(chat.Run, chat.ImplementationDraft)
	}); ok {
		observer.ImplementationSaved(run, draft)
	}
}

func (o *EventObserver) ImplementationSaved(run chat.Run, draft chat.ImplementationDraft) {
	// Never emit the checkpoint source/metadata as a UI status payload.
	o.sink.PublishRunEvent(run.ID, "run.implementation_progress", map[string]any{
		"messageId": run.AssistantMessageID, "parts": draft.PartCount, "chars": len([]rune(draft.Code)), "complete": draft.Complete,
	})
}
func (o *EventObserver) ApprovalRequired(run chat.Run, coordination permission.Coordination) {
	o.sink.PublishRunEvent(run.ID, "approval.required", coordination)
}
func (o *EventObserver) RunCompleted(run chat.Run, text string) {
	o.sink.PublishRunEvent(run.ID, "content.completed", map[string]any{"messageId": run.AssistantMessageID, "text": text})
	o.sink.PublishRunEvent(run.ID, "run.completed", map[string]any{"run": run})
}
func (o *EventObserver) RunFailed(run chat.Run, _, _ string) {
	o.sink.PublishRunEvent(run.ID, "run.failed", map[string]any{"run": run})
}
func (o *EventObserver) RunCancelled(run chat.Run) {
	o.sink.PublishRunEvent(run.ID, "run.cancelled", map[string]any{"run": run})
}

func (o *EventObserver) RunInterrupted(run chat.Run) {
	o.sink.PublishRunEvent(run.ID, "run.interrupted", map[string]any{"run": run})
}

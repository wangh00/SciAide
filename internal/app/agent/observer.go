package agent

import (
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/model"
)

type EventSink interface {
	PublishRunEvent(runID, eventType string, payload any)
}

// EventObserver adapts AgentLoop lifecycle callbacks to durable, versioned
// RunEvents without making the chat package depend on permission types.
type EventObserver struct{ sink EventSink }

func NewEventObserver(sink EventSink) *EventObserver { return &EventObserver{sink: sink} }

func (o *EventObserver) RunStarted(run chat.Run) {
	o.sink.PublishRunEvent(run.ID, "run.started", map[string]any{"runId": run.ID, "status": run.Status})
}
func (o *EventObserver) ContentStarted(run chat.Run) {
	o.sink.PublishRunEvent(run.ID, "content.started", map[string]any{"runId": run.ID, "messageId": run.AssistantMessageID})
}
func (o *EventObserver) ContentDelta(run chat.Run, delta string) {
	o.sink.PublishRunEvent(run.ID, "content.delta", map[string]any{"messageId": run.AssistantMessageID, "delta": delta})
}
func (o *EventObserver) UsageUpdated(run chat.Run, usage model.Usage) {
	o.sink.PublishRunEvent(run.ID, "usage.updated", usage)
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

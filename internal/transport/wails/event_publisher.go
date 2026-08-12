package wails

import (
	"context"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/events"
)

const RunEventName = "sciaide:run-event"

type EventPublisher struct{ lifecycle *LifecycleContext }

func NewEventPublisher(lifecycle *LifecycleContext) *EventPublisher {
	return &EventPublisher{lifecycle: lifecycle}
}
func (p *EventPublisher) Publish(_ context.Context, event events.Envelope) {
	wailsruntime.EventsEmit(p.lifecycle.Context(), RunEventName, event)
}

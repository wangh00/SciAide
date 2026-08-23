package agent

import (
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
)

type capturedRunEvent struct {
	runID     string
	eventType string
	payload   any
}

type captureEventSink struct{ event capturedRunEvent }

func (s *captureEventSink) PublishRunEvent(runID, eventType string, payload any) {
	s.event = capturedRunEvent{runID: runID, eventType: eventType, payload: payload}
}

func TestRetryEventIdentifiesStreamingMessage(t *testing.T) {
	sink := &captureEventSink{}
	NewEventObserver(sink).Retrying(chat.Run{ID: "run", AssistantMessageID: "assistant"}, RetryStatus{Phase: "stream", Attempt: 1, MaxAttempts: 5})
	payload, ok := sink.event.payload.(map[string]any)
	if !ok || sink.event.runID != "run" || sink.event.eventType != "run.retrying" || payload["messageId"] != "assistant" {
		t.Fatalf("retry event = %#v", sink.event)
	}
}

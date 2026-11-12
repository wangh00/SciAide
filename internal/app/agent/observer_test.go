package agent

import (
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
)

type capturedRunEvent struct {
	runID     string
	eventType string
	payload   any
}

func TestRuntimeActivityEventContainsOnlyDisplayStatus(t *testing.T) {
	sink := &captureEventSink{}
	a := chat.RuntimeActivity{RunID: "run", Phase: "compaction", TurnIndex: 7, StartedAt: time.Now().UTC()}
	NewEventObserver(sink).RuntimeActivity(chat.Run{ID: "run", ReasoningSummary: "not status content"}, a)
	payload, ok := sink.event.payload.(map[string]any)
	if !ok || len(payload) != 1 || payload["activity"] != a || sink.event.eventType != "run.activity" || sink.event.runID != "run" {
		t.Fatal(sink.event)
	}
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

func TestSubmissionRejectionStoresDiagnosisAndIdentityNotWholeCandidate(t *testing.T) {
	sink := &captureEventSink{}
	NewEventObserver(sink).SubmissionRejected(chat.Run{ID: "run", ModelTurns: 2}, "private full candidate", "/candidateAssessments/3/candidateId not offered", 1)
	p := sink.event.payload.(map[string]any)
	if sink.event.eventType != "run.submission_rejected" || len(p["candidateSha256"].(string)) != 64 || p["turnIndex"] != 2 || len(p) != 4 {
		t.Fatal(sink.event)
	}
}

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model/fake"
)

func TestApplicationShutdownDoesNotBecomeUserCancellation(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	provider := &openingFailureModel{inner: fake.New(), remaining: 10, err: &apperr.Error{Code: "MODEL_UNAVAILABLE", UserMessage: "temporary", Retryable: true}}
	loop.models = fakeResolver{model: provider}
	loop.retryDelay = func(int) time.Duration { return time.Hour }
	observer := &retryCaptureObserver{retrying: make(chan RetryStatus, 1)}
	loop.observer = observer
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan Outcome, 1)
	go func() { done <- loop.Run(ctx, "run") }()
	select {
	case <-observer.retrying:
		cancel(chat.ErrApplicationClosing)
	case <-time.After(time.Second):
		cancel(context.Canceled)
		t.Fatal("retry did not start")
	}
	select {
	case outcome := <-done:
		if outcome != OutcomeInterrupted || state.run.Status != chat.RunInterrupted || state.run.ErrorCode != "APP_CLOSING" {
			t.Fatal("shutdown became user cancellation", outcome, state.run)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not stop retry wait")
	}
}

func TestInterruptedObserverUsesExistingTerminalEvent(t *testing.T) {
	sink := &captureEventSink{}
	NewEventObserver(sink).RunInterrupted(chat.Run{ID: "run", Status: chat.RunInterrupted, ErrorCode: "APP_CLOSING"})
	if sink.event.eventType != "run.interrupted" {
		t.Fatal(sink.event)
	}
}

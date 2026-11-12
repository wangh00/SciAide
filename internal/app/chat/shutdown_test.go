package chat

import (
	"context"
	"errors"
	"testing"
	"time"
)

type shutdownCauseRunner struct {
	started chan struct{}
	cause   chan error
}

func (r shutdownCauseRunner) Execute(ctx context.Context, _ string) {
	close(r.started)
	<-ctx.Done()
	r.cause <- context.Cause(ctx)
}
func (r shutdownCauseRunner) ResumeExecute(ctx context.Context, runID string) { r.Execute(ctx, runID) }

func TestShutdownAndExplicitCancelHaveDifferentCauses(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		repo := &memoryRepo{run: Run{ID: "run", Status: RunRunning}}
		s := NewService(repo, repo, repo, nil)
		r := shutdownCauseRunner{make(chan struct{}), make(chan error, 1)}
		if err := s.SetRunner(r); err != nil {
			t.Fatal(err)
		}
		if err := s.launch("run", false); err != nil {
			t.Fatal(err)
		}
		<-r.started
		if shutdown {
			s.BeginShutdown()
		} else if err := s.Cancel(context.Background(), "run"); err != nil {
			t.Fatal(err)
		}
		select {
		case cause := <-r.cause:
			want := context.Canceled
			if shutdown {
				want = ErrApplicationClosing
			}
			if !errors.Is(cause, want) {
				t.Fatal("wrong cancellation cause", shutdown, cause)
			}
		case <-time.After(time.Second):
			t.Fatal("runner did not stop")
		}
		s.Close()
		if err := s.launch("other", false); err == nil {
			t.Fatal("new runner launched after shutdown")
		}
	}
}

func TestInterruptPreservesEarlierTerminalOutcome(t *testing.T) {
	for _, status := range []RunStatus{RunCompleted, RunCancelled, RunFailed, RunInterrupted} {
		repo := &memoryRepo{run: Run{ID: "run", Status: status, ErrorCode: "original"}}
		result, err := NewTerminator(repo, nil).Interrupt(context.Background(), "run")
		if err != nil || result.Status != status || result.ErrorCode != "original" || len(repo.envelopes) != 0 {
			t.Fatal("shutdown overwrote terminal outcome", result, err)
		}
	}
}

package chat

import (
	"context"
	"errors"
	"testing"
	"time"
)

type activityMemoryRepo struct {
	*memoryRepo
	activity RuntimeActivity
	err      error
}

func (r *activityMemoryRepo) LatestModelRetryStatus(context.Context, string) (ModelRetryStatus, int64, bool, error) {
	return ModelRetryStatus{State: "receiving", Attempt: 2, MaxAttempts: 5, Message: "reconnected but incomplete"}, 3, true, r.err
}

func TestSnapshotRetryProjectionDoesNotBlockRecoveryAndIgnoresTerminalStates(t *testing.T) {
	r := &activityMemoryRepo{memoryRepo: &memoryRepo{run: Run{ID: "run", ConversationID: "conversation", Status: RunRunning}}}
	s := NewService(r, r, r, nil)
	defer s.Close()
	for _, status := range []RunStatus{RunRunning, RunCompleted, RunFailed, RunCancelled, RunInterrupted, RunWaitingUserInput} {
		r.run.Status = status
		snapshot, err := s.Snapshot(context.Background(), "run")
		if err != nil || (snapshot.Retry != nil) != (status == RunRunning) {
			t.Fatal(status, snapshot, err)
		}
	}
	r.run.Status = RunRunning
	r.err = errors.New("display unavailable")
	if snapshot, err := s.Snapshot(context.Background(), "run"); err != nil || snapshot.Retry != nil {
		t.Fatal(snapshot, err)
	}
}

func (r *activityMemoryRepo) LatestRuntimeActivity(context.Context, string) (RuntimeActivity, bool, error) {
	return r.activity, true, r.err
}

func TestSnapshotRuntimeActivityIsOptionalScopedAndTerminalSafe(t *testing.T) {
	r := &activityMemoryRepo{memoryRepo: &memoryRepo{run: Run{ID: "run", ConversationID: "conversation", ModelTurns: 3, Status: RunRunning}}, activity: RuntimeActivity{RunID: "run", TurnIndex: 3, Phase: "compaction", StartedAt: time.Now().UTC()}}
	s := NewService(r, r, r, nil)
	defer s.Close()
	snapshot, err := s.Snapshot(context.Background(), "run")
	if err != nil || snapshot.Activity == nil || snapshot.Activity.Phase != "compaction" {
		t.Fatal(snapshot, err)
	}
	for _, mutate := range []func(){
		func() { r.activity.RunID = "other" },
		func() { r.activity.RunID = "run"; r.activity.TurnIndex = 2 },
		func() { r.activity.TurnIndex = 3; r.activity.Phase = "unknown" },
		func() { r.activity.Phase = "compaction"; r.run.Status = RunCompleted },
		func() { r.run.Status = RunWaitingUserInput },
		func() { r.run.Status = RunRunning; r.err = errors.New("telemetry unavailable") },
	} {
		mutate()
		snapshot, err = s.Snapshot(context.Background(), "run")
		if err != nil || snapshot.Activity != nil {
			t.Fatal("stale telemetry blocked recovery", snapshot, err)
		}
	}
}

type interleavedRetryRepo struct {
	*memoryRepo
	sequence int64
	cleared  bool
}

func (r *interleavedRetryRepo) LatestEventSequence(context.Context, string) (int64, error) {
	return r.sequence, nil
}

func (r *interleavedRetryRepo) LatestModelRetryStatus(context.Context, string) (ModelRetryStatus, int64, bool, error) {
	sequence := r.sequence
	// Deterministic event insertion at the uncertain boundary: the status was
	// read just before recovery. Its own revision must stay with the old value.
	r.sequence++
	if r.cleared {
		return ModelRetryStatus{}, sequence, false, nil
	}
	return ModelRetryStatus{Attempt: 2, MaxAttempts: 5, Message: "incomplete reconnect", State: "receiving"}, sequence, true, nil
}

func TestSnapshotBindsRetryAndClearToOwnEventSequence(t *testing.T) {
	for _, cleared := range []bool{false, true} {
		r := &interleavedRetryRepo{memoryRepo: &memoryRepo{run: Run{ID: "run", ConversationID: "conversation", Status: RunRunning}}, sequence: 20, cleared: cleared}
		s := NewService(r, r, r, nil)
		snapshot, err := s.Snapshot(context.Background(), "run")
		s.Close()
		if err != nil || snapshot.RetrySequence == nil || *snapshot.RetrySequence != 20 || snapshot.Sequence != 20 || r.sequence != 21 || (snapshot.Retry == nil) != cleared {
			t.Fatal(cleared, snapshot, err)
		}
	}
}

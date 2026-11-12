package chat

import (
	"context"
	"time"
)

// RuntimeActivity is display-only telemetry, never model reasoning or input.
// TurnIndex identifies the owning loop turn, not a count of HTTP retries.
type RuntimeActivity struct {
	RunID     string    `json:"runId"`
	Phase     string    `json:"phase"`
	TurnIndex int       `json:"turnIndex"`
	StartedAt time.Time `json:"startedAt"`
}

// Safe display projection of the latest retry lifecycle event, not model data.
type ModelRetryStatus struct {
	Phase       string `json:"phase"`
	Reason      string `json:"reason,omitempty"`
	State       string `json:"state,omitempty"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	DelayMillis int64  `json:"delayMillis"`
	Message     string `json:"message"`
}

type ModelRetryStatusReader interface {
	// Sequence belongs to the status or clearing event, including absent status.
	// Both are read atomically; an unrelated newer event is not its revision.
	LatestModelRetryStatus(context.Context, string) (ModelRetryStatus, int64, bool, error)
}

func (a RuntimeActivity) Matches(run Run) bool {
	if run.Status != RunRunning || a.RunID != run.ID || a.TurnIndex != run.ModelTurns || a.StartedAt.IsZero() {
		return false
	}
	switch a.Phase {
	case "model_wait", "model_processing", "implementation_wait", "implementation_validation", "compaction", "compaction_completed", "tools":
		return true
	}
	return false
}

type RuntimeActivityReader interface {
	LatestRuntimeActivity(context.Context, string) (RuntimeActivity, bool, error)
}

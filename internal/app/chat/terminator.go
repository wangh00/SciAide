package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/id"
)

type TerminationRepository interface {
	CancelRun(ctx context.Context, runID, errorCode, errorMessage string, at time.Time, event events.Envelope) (Run, bool, error)
	FailRun(ctx context.Context, runID, errorCode, errorMessage string, at time.Time, event events.Envelope) (Run, bool, error)
	ListToolCallIDs(ctx context.Context, runID string) ([]string, error)
}

// Terminator owns the durable, cross-aggregate cancellation boundary. It is
// deliberately separate from the runner scheduler so a waiting-approval Run
// can be cancelled even when no goroutine is active.
type Terminator struct {
	repository TerminationRepository
	publisher  Publisher
	cancelTool func(string) bool
	now        func() time.Time
}

func NewTerminator(repository TerminationRepository, publisher Publisher) *Terminator {
	return &Terminator{repository: repository, publisher: publisher, now: func() time.Time { return time.Now().UTC() }}
}

func (t *Terminator) SetToolCanceller(cancel func(string) bool) {
	if t != nil {
		t.cancelTool = cancel
	}
}

func (t *Terminator) Cancel(ctx context.Context, runID string) (Run, error) {
	if t == nil || t.repository == nil {
		return Run{}, fmt.Errorf("run terminator is not configured")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Run{}, fmt.Errorf("run id is required")
	}
	at := t.now()
	t.cancelActiveTools(ctx, runID)
	payload, err := json.Marshal(map[string]any{"runId": runID, "status": RunCancelled, "errorCode": "RUN_CANCELLED"})
	if err != nil {
		return Run{}, err
	}
	eventID, err := id.New()
	if err != nil {
		return Run{}, err
	}
	event := events.New(eventID, runID, "run", "run.cancelled", 0, payload)
	event.Timestamp = at
	run, changed, err := t.repository.CancelRun(ctx, runID, "RUN_CANCELLED", "已停止生成", at, event)
	if err == nil && changed && t.publisher != nil {
		t.publisher.Publish(ctx, event)
	}
	return run, err
}

func (t *Terminator) Fail(ctx context.Context, runID, errorCode, errorMessage string) (Run, error) {
	if t == nil || t.repository == nil {
		return Run{}, fmt.Errorf("run terminator is not configured")
	}
	runID, errorCode, errorMessage = strings.TrimSpace(runID), strings.TrimSpace(errorCode), strings.TrimSpace(errorMessage)
	if runID == "" || errorCode == "" || errorMessage == "" {
		return Run{}, fmt.Errorf("run id, error code and error message are required")
	}
	at := t.now()
	t.cancelActiveTools(ctx, runID)
	payload, err := json.Marshal(map[string]any{"runId": runID, "status": RunFailed, "errorCode": errorCode, "errorMessage": errorMessage})
	if err != nil {
		return Run{}, err
	}
	eventID, err := id.New()
	if err != nil {
		return Run{}, err
	}
	event := events.New(eventID, runID, "run", "run.failed", 0, payload)
	event.Timestamp = at
	run, changed, err := t.repository.FailRun(ctx, runID, errorCode, errorMessage, at, event)
	if err == nil && changed && t.publisher != nil {
		t.publisher.Publish(ctx, event)
	}
	return run, err
}

func (t *Terminator) cancelActiveTools(ctx context.Context, runID string) {
	if t.cancelTool == nil {
		return
	}
	callIDs, err := t.repository.ListToolCallIDs(ctx, runID)
	if err != nil {
		return
	}
	for _, callID := range callIDs {
		t.cancelTool(callID)
	}
}

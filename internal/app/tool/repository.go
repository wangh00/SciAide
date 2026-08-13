package tool

import (
	"context"
	"time"

	"github.com/wangh00/SciAide/internal/events"
)

type Repository interface {
	Get(ctx context.Context, id string) (Call, error)
	ListByRun(ctx context.Context, runID string) ([]Call, error)
	InterruptActive(ctx context.Context, at time.Time) (int64, error)
	CreateWithEvent(ctx context.Context, value Call, event events.Envelope) error
	TransitionWithEvent(ctx context.Context, id string, expected, next CallStatus, errorCode, errorMessage string, at time.Time, event events.Envelope) error
	FinishWithEvent(ctx context.Context, id string, expected, next CallStatus, result Result, errorCode, errorMessage string, at time.Time, event events.Envelope) error
}

type SchemaValidator interface {
	Validate(schema, instance []byte) error
}

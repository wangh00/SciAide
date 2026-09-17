package skill

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type HistoricalRunContextLoader interface {
	GetRunContext(ctx context.Context, runID string) (RunContext, error)
}

// HistoricalRunContexts replays immutable snapshots stored by the retired
// package selector. It is a read-only archive path and never selects a Skill
// or creates state for a new Run.
type HistoricalRunContexts struct {
	loader HistoricalRunContextLoader
}

func NewHistoricalRunContexts(loader HistoricalRunContextLoader) *HistoricalRunContexts {
	return &HistoricalRunContexts{loader: loader}
}

func (h *HistoricalRunContexts) PrepareRunContext(ctx context.Context, runID, projectID, _ string, _ int) (RunContext, error) {
	if h == nil || h.loader == nil {
		return RunContext{}, nil
	}
	runID, projectID = strings.TrimSpace(runID), strings.TrimSpace(projectID)
	value, err := h.loader.GetRunContext(ctx, runID)
	if errors.Is(err, ErrRunContextNotFound) {
		return RunContext{}, nil
	}
	if err != nil {
		return RunContext{}, fmt.Errorf("load archived Run Skill context: %w", err)
	}
	if value.ProjectID != projectID {
		return RunContext{}, fmt.Errorf("archived Run Skill context belongs to another project")
	}
	return value, nil
}

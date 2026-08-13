package agent

import (
	"context"
	"fmt"
	"time"
)

const (
	DefaultMaxModelTurns = 8
	DefaultMaxToolCalls  = 12
	DefaultMaxDuration   = 5 * time.Minute
)

type RunBudget struct {
	MaxModelTurns int           `json:"maxModelTurns"`
	MaxToolCalls  int           `json:"maxToolCalls"`
	MaxDuration   time.Duration `json:"maxDuration"`
}

type budgetCounter struct {
	budget    RunBudget
	startedAt time.Time
	toolCalls int
	now       func() time.Time
}

type runDeadlineError struct{ cause error }

func (e runDeadlineError) Error() string { return "RUN_DURATION_BUDGET_EXCEEDED" }
func (e runDeadlineError) Unwrap() error { return e.cause }

func normalizeBudget(value RunBudget) RunBudget {
	if value.MaxModelTurns <= 0 {
		value.MaxModelTurns = DefaultMaxModelTurns
	}
	if value.MaxToolCalls <= 0 {
		value.MaxToolCalls = DefaultMaxToolCalls
	}
	if value.MaxDuration <= 0 {
		value.MaxDuration = DefaultMaxDuration
	}
	return value
}

func newBudgetCounter(value RunBudget, startedAt time.Time, priorToolCalls int) *budgetCounter {
	return &budgetCounter{budget: normalizeBudget(value), startedAt: startedAt, toolCalls: priorToolCalls, now: func() time.Time { return time.Now().UTC() }}
}

func (b *budgetCounter) beforeToolCall() error {
	if err := b.checkDuration(); err != nil {
		return err
	}
	if b.toolCalls >= b.budget.MaxToolCalls {
		return fmt.Errorf("TOOL_CALL_BUDGET_EXCEEDED")
	}
	b.toolCalls++
	return nil
}

func (b *budgetCounter) checkDuration() error {
	if b.now().Sub(b.startedAt) >= b.budget.MaxDuration {
		return runDeadlineError{cause: context.DeadlineExceeded}
	}
	return nil
}

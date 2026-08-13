package agent

import "context"

// Runner adapts Loop to chat.RunExecutor without exposing AgentLoop internals
// through the Wails boundary.
type Runner struct{ loop *Loop }

func NewRunner(loop *Loop) *Runner { return &Runner{loop: loop} }

func (r *Runner) Execute(ctx context.Context, runID string) {
	r.loop.Run(ctx, runID)
}

func (r *Runner) ResumeExecute(ctx context.Context, runID string) {
	r.loop.Resume(ctx, runID)
}

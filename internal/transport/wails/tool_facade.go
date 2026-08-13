package wails

import "github.com/wangh00/SciAide/internal/app/tool"

// ToolFacade exposes discovery and cancellation only. Execution is deliberately
// owned by AgentLoop so UI callers cannot bypass its orchestration boundary.
type ToolFacade struct {
	lifecycle *LifecycleContext
	executor  *tool.Executor
	registry  tool.Registry
}

func NewToolFacade(lifecycle *LifecycleContext, executor *tool.Executor, registry tool.Registry) *ToolFacade {
	return &ToolFacade{lifecycle: lifecycle, executor: executor, registry: registry}
}

func (f *ToolFacade) ListTools() ([]tool.Definition, error) {
	return f.registry.Definitions(f.lifecycle.Context())
}

func (f *ToolFacade) CancelToolCall(toolCallID string) bool {
	return f.executor.Cancel(toolCallID)
}

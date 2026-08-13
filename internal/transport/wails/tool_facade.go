package wails

import "github.com/wangh00/SciAide/internal/app/tool"

// ToolFacade exposes only the bounded executor controls. ToolCall creation and
// approval still go through their dedicated application services.
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

func (f *ToolFacade) ExecuteToolCall(projectID, toolCallID string) (tool.Execution, error) {
	return f.executor.Execute(f.lifecycle.Context(), projectID, toolCallID)
}

func (f *ToolFacade) CancelToolCall(toolCallID string) bool {
	return f.executor.Cancel(toolCallID)
}

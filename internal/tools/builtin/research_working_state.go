package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type ResearchWorkingStateUpdate struct{ runtime *workflow.RuntimeService }

func NewResearchWorkingStateUpdate(runtime *workflow.RuntimeService) *ResearchWorkingStateUpdate {
	return &ResearchWorkingStateUpdate{runtime: runtime}
}

func (*ResearchWorkingStateUpdate) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: workflow.ResearchStateUpdateTool, Version: "2", Risk: tool.RiskModerate, Idempotent: false,
		Description:  "Patch unverified note (<=2048 UTF-8 bytes) at expectedVersion (0=absent). Initially need currentFocus/rationale; omit unchanged fields. Optional basis: {changedField:[prior discovery.read toolCallId,...]}, 1-2 per field, <=4 total; abstract pages only. Links are model-selected, not approved support. Success publishes; no goal/route/execution change.",
		InputSchema:  json.RawMessage(workflow.ResearchWorkingStateUpdateSchema),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id","version","writeStatus"],"properties":{"id":{"type":"string"},"version":{"type":"integer","minimum":1},"writeStatus":{"type":"string","enum":["pending_tool_completion"]}}}`)}, nil
}

func (t *ResearchWorkingStateUpdate) Invoke(ctx context.Context, inv tool.Invocation) (tool.Result, error) {
	if t == nil || t.runtime == nil || tool.NormalizeSubjectKind(inv.SubjectKind) != tool.SubjectChatRun {
		return tool.Result{}, tool.NewUserFacingError("需要当前科研任务的真实用户讨论和工具调用")
	}
	command, err := workflow.ParseResearchWorkingStateUpdate(inv.Arguments)
	if err != nil {
		return tool.Result{}, tool.NewUserFacingError(fmt.Sprint(err))
	}
	state, err := t.runtime.UpdateResearchWorkingState(ctx, inv.ProjectID, inv.ResearchTaskID, inv.RunID, inv.CallID, command)
	if err != nil {
		return tool.Result{}, tool.NewUserFacingError(fmt.Sprint(err))
	}
	projection, err := json.Marshal(map[string]any{"id": state.ID, "version": state.Version, "writeStatus": "pending_tool_completion"})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: "Model working note recorded, pending successful tool completion. It is unverified, does not change the research goal or authorize execution, and is not a scientific approval.", Structured: projection}, nil
}

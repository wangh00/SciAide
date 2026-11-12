package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

type ResearchPlanPrepare struct{ starter *workflow.StarterService }

func NewResearchPlanPrepare(starter *workflow.StarterService) *ResearchPlanPrepare {
	return &ResearchPlanPrepare{starter: starter}
}

func (*ResearchPlanPrepare) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{QualifiedName: workflow.ResearchPlanPrepareTool, Version: "1", Risk: tool.RiskLow, Idempotent: true,
		Description:  "Prepare candidate research execution routes for the current discussion only when the user wants research work and its complete negotiated goal is sufficiently clear. Retain earlier requirements and desired outputs. This calls the existing planner, not research execution; adoption remains the user's route-card action. Never use for greetings, single-file explanation or every turn. It returns a pending planner run, not an executed report. Reference selections are explicitly chosen shared/task library material IDs; do not promote discussion-private uploads through this tool. Omit references to retain existing selections; [] intentionally clears them.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["researchIdea"],"properties":{"researchIdea":{"type":"string","minLength":1,"maxLength":8000},"referenceAttachmentIds":{"type":"array","maxItems":16,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}}}}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`)}, nil
}

func (t *ResearchPlanPrepare) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t.starter == nil || tool.NormalizeSubjectKind(invocation.SubjectKind) != tool.SubjectChatRun {
		return tool.Result{}, tool.NewUserFacingError("需要当前研究需求讨论的真实工具调用")
	}
	var command workflow.PrepareResearchPlanCommand
	if err := json.Unmarshal(invocation.Arguments, &command); err != nil {
		return tool.Result{}, err
	}
	value, err := t.starter.PrepareFromDiscussion(ctx, invocation.RunID, invocation.CallID, command)
	if err != nil {
		return tool.Result{}, tool.NewUserFacingError(fmt.Sprint(err))
	}
	projection, _ := json.Marshal(map[string]any{"runId": value.Run.ID, "status": value.Run.Status, "researchTaskId": value.Run.ResearchTaskID, "executionStarted": false})
	return tool.Result{Status: tool.ResultSuccess, Text: "Candidate route planning has started. No research execution has started. The route card will appear in this same task; only the user can adopt it.", Structured: projection}, nil
}

package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
)

func TestPlannerSkillFeedbackUsesOnlyOfferedCurrentActions(t *testing.T) {
	const current = "res_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const foreign = "res_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	err := &workflow.ResearchPlannerSkillsMissingError{Names: []string{"scientific-writing", "writing"}}
	view := resource.View{SkillActions: map[string]string{"scientific-writing": current, "writing": foreign}, OpenIDs: map[string]bool{current: true}}
	request := model.ChatRequest{Tools: []model.ToolDefinition{{Name: resource.OpenTool}}}
	feedback := plannerSkillValidationFeedback(err, request, view)
	if !strings.Contains(feedback, resource.OpenTool) || !strings.Contains(feedback, current) || strings.Contains(feedback, foreign) || strings.Contains(feedback, "builtin.skill.load") || !strings.Contains(feedback, "没有对应加载动作") {
		t.Fatal("resource feedback guessed identity, leaked a foreign action or used a hidden tool", feedback)
	}
	request.Tools = []model.ToolDefinition{{Name: "builtin.skill.load"}}
	feedback = plannerSkillValidationFeedback(err, request, view)
	if !strings.Contains(feedback, "builtin.skill.load") || strings.Contains(feedback, current) || strings.Contains(feedback, resource.OpenTool) {
		t.Fatal("native Skill feedback used the wrong interface", feedback)
	}
	request.Tools = []model.ToolDefinition{{Name: stageSubmissionTool}}
	feedback = plannerSkillValidationFeedback(err, request, view)
	if !strings.Contains(feedback, "没有提供 Skill 加载入口") || strings.Contains(feedback, current) || strings.Contains(feedback, resource.OpenTool) || strings.Contains(feedback, "builtin.skill.load") || strings.Contains(feedback, "完成必要读取") {
		t.Fatal("submission-locked feedback reopened or promised unavailable IO", feedback)
	}
	for _, request := range []model.ChatRequest{
		{Tools: []model.ToolDefinition{{Name: resource.OpenTool}}, DisableTools: true},
		{Tools: []model.ToolDefinition{{Name: resource.OpenTool}, {Name: stageSubmissionTool}}, ForcedTool: stageSubmissionTool},
	} {
		if got := plannerSkillValidationFeedback(err, request, view); strings.Contains(got, current) || !strings.Contains(got, "没有提供 Skill 加载入口") {
			t.Fatal("tool-choice restriction was ignored", got)
		}
	}
	fatal := errors.New("读取本次规划 Skill 加载记录失败")
	if got := plannerSkillValidationFeedback(fatal, model.ChatRequest{}, view); got != "研究规划尚未完成。宿主核验反馈："+fatal.Error() {
		t.Fatal("infrastructure error was converted into a model loading request", got)
	}
}

func TestPlannerSkillFeedbackActionMenuIsBounded(t *testing.T) {
	missing := &workflow.ResearchPlannerSkillsMissingError{}
	view := resource.View{SkillActions: map[string]string{}, OpenIDs: map[string]bool{}}
	for i := 0; i < 20; i++ {
		name, action := fmt.Sprintf("skill-%d", i), fmt.Sprintf("res_%032x", i+1)
		missing.Names = append(missing.Names, name)
		view.SkillActions[name], view.OpenIDs[action] = action, true
	}
	message := plannerSkillValidationFeedback(missing, model.ChatRequest{Tools: []model.ToolDefinition{{Name: resource.OpenTool}}}, view)
	if strings.Count(message, `"actionId"`) != 12 || !strings.Contains(message, "最多列出 12 项") {
		t.Fatal("unbounded or silently truncated menu", message)
	}
}

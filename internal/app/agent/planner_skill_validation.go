package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
)

func (l *Loop) validatePlannerCompletion(ctx context.Context, runID, text string, schema json.RawMessage) error {
	names, err := workflow.ResearchPlannerSkillClaims(text, schema)
	if err != nil {
		// The caller already normalized and validated the candidate. Never
		// silently approve a candidate if that invariant is broken.
		return fmt.Errorf("研究规划候选无法核验 Skill 声明：%w", err)
	}
	if len(names) == 0 {
		return nil
	}
	loader, ok := l.skillRouter.(workflow.StarterSkillLoader)
	if !ok {
		return fmt.Errorf("科研 Skill 加载记录读取器不可用，不能确认规划完成")
	}
	loaded, err := loader.ListRunSkillSnapshots(ctx, runID)
	if err != nil {
		return fmt.Errorf("读取本次规划 Skill 加载记录失败：%w", err)
	}
	return workflow.ValidateResearchPlannerSkillClaims(names, loaded)
}

func plannerSkillValidationFeedback(err error, request model.ChatRequest, view resource.View) string {
	message := "研究规划尚未完成。宿主核验反馈：" + err.Error()
	var missing *workflow.ResearchPlannerSkillsMissingError
	if !errors.As(err, &missing) {
		return message
	}
	offered := map[string]bool{}
	for _, definition := range request.Tools {
		if !request.DisableTools && (request.ForcedTool == "" || request.ForcedTool == definition.Name) {
			offered[definition.Name] = true
		}
	}
	switch {
	case offered[resource.OpenTool]:
		message += " 当前加载入口是 builtin.resource.open，请使用本轮资源菜单签发的 actionId，不把 Skill 名称、分类或路径填入 actionId，也不要调用当前未提供的旧工具。"
		actions := []map[string]string{}
		unresolved := []string{}
		for _, name := range missing.Names {
			id := view.SkillActions[name]
			args, _ := json.Marshal(map[string]string{"actionId": id})
			if id == "" || !view.Allows(resource.OpenTool, args) {
				unresolved = append(unresolved, name)
				continue
			}
			if len(actions) < 12 {
				actions = append(actions, map[string]string{"skillName": name, "tool": resource.OpenTool, "actionId": id})
			}
		}
		if len(actions) > 0 {
			encoded, _ := json.Marshal(actions)
			message += " 当前已签发的对应加载动作（只是选项，尚未执行）：" + string(encoded)
			if len(actions) == 12 {
				message += " 此处最多列出 12 项，其他声明仍须依据当前资源菜单核验，未列出不表示已加载。"
			}
		}
		if len(unresolved) > 0 {
			message += " 当前没有对应加载动作的声明：" + strings.Join(unresolved, ", ") + "。请先从本轮资源目录查找准确 Skill 名称和必要章节；不要把分类或别名当成具体 Skill，也不能猜造 actionId。"
		}
	case offered["builtin.skill.load"]:
		message += " 请按准确 name 调用当前提供的 builtin.skill.load，实际加载所声明的 Skill 并读取必要正文。"
	default:
		return message + " 当前请求没有提供 Skill 加载入口，不能承诺新增加载或调用隐藏工具；只能依据本次实际加载记录修正方案及其所有相关声明后重新提交，无法满足要求则明确停止，不能为绕过核验伪造已加载状态。"
	}
	return message + " 完成必要读取后重新提交完整路线 JSON；若不再采用某 Skill，须明确修正方案及所有相关声明，原加载核验仍然保留。"
}

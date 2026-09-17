package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/workflow"
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

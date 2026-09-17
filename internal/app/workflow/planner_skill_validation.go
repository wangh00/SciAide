package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/skillrun"
)

func IsResearchPlannerSchema(schema json.RawMessage) bool {
	return schemaDeclaresProperty(schema, "routes") && schemaDeclaresProperty(schema, "selectedSkills") && schemaDeclaresProperty(schema, "normalizedQuestion")
}

// ResearchPlannerSkillClaims reads both summary claims and stage references.
// Browsing a catalog never supplies evidence that instructions were loaded.
func ResearchPlannerSkillClaims(text string, schema json.RawMessage) ([]string, error) {
	if !IsResearchPlannerSchema(schema) {
		return nil, nil
	}
	structured, err := extractWorkflowAIStageOutput(text, CompiledNode{ID: "explore", OutputSchema: schema})
	if err != nil {
		return nil, err
	}
	var plan ResearchStarterPlan
	if err := json.Unmarshal(structured, &plan); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, selected := range plan.SelectedSkills {
		names[selected.Name] = true
	}
	for _, route := range plan.Routes {
		for _, stage := range route.StagePlans {
			for _, name := range stage.SkillNames {
				names[name] = true
			}
		}
		for _, layer := range route.Layers {
			for _, stage := range layer.Stages {
				for _, name := range stage.SkillNames {
					names[name] = true
				}
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		if !validResearchSkillName(name) {
			return nil, fmt.Errorf("研究规划声明了无效 Skill 名称 %q", name)
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func ValidateResearchPlannerSkillClaims(names []string, loaded []skillrun.Snapshot) error {
	if err := validateRouteSkillLimit(len(names)); err != nil {
		return err
	}
	actual := map[string]skillrun.Snapshot{}
	for _, snapshot := range loaded {
		if _, exists := actual[snapshot.Name]; exists {
			return fmt.Errorf("实际 Skill 加载记录重复：%s", snapshot.Name)
		}
		actual[snapshot.Name] = snapshot
	}
	var missing []string
	for _, name := range names {
		snapshot, ok := actual[name]
		if !ok || strings.TrimSpace(snapshot.ContentHash) == "" || strings.TrimSpace(snapshot.PackageHash) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("WORKFLOW_PLANNER_SKILLS_MISSING: 本次规划声明但尚未实际加载的 Skill：%s。浏览分类或列出候选不等于加载。请按准确 name 调用 builtin.skill.load 加载这些 Skill，并读取必要正文后重新提交完整路线 JSON；若不再采用某 Skill，须明确修正方案及其声明，不得伪造已加载状态。", strings.Join(missing, ", "))
	}
	return nil
}

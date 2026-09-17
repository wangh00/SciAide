package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const literatureQualityInstruction = "初筛不是全文质量认证。batch 阶段仅逐项判断：core=摘要明确支持冻结人群、干预、对照、结局及研究类型均匹配；support=相关但信息不全、需核验；exclude=有明确不符合依据。无摘要、仅题名相关或研究类型不明时不得标为core。每条reason简短说明匹配依据或待核验项，不重复课题背景，不抄写摘要数字。batch的summary只报告本批统计，supplementalQueries必须为空，不做全局覆盖讨论；全局缺口和补检索仅由coverage阶段统一处理。coverage阶段依据保留候选的原始摘要与先前理由复核，不得把初筛标签当成已验证全文结论。无需为完成阶段调用额外工具，不改变冻结研究标准。 coverage可通过recheckCandidateIds申请复核excludedAssessments中的可疑误排项，宿主将把原始摘要重新送入batch；本次不要直接推荐未复核的排除项。最多两轮复核，未解决时保持覆盖未完成。"

func validateLiteratureCore(output, input json.RawMessage) error {
	var in struct {
		Literature *literatureInput       `json:"_literature"`
		Candidates []literatureCandidate  `json:"candidates"`
		Excluded   []literatureAssessment `json:"excludedAssessments"`
	}
	if json.Unmarshal(input, &in) != nil || in.Literature == nil {
		return nil
	}
	var result literatureScreening
	if json.Unmarshal(output, &result) != nil {
		return fmt.Errorf("invalid literature screening")
	}
	if len(result.RecheckCandidateIDs) > 0 {
		if in.Literature.Phase != "coverage" {
			return fmt.Errorf("only coverage may request excluded candidate rechecks")
		}
		excluded := map[string]bool{}
		for _, a := range in.Excluded {
			excluded[a.CandidateID] = true
		}
		for _, id := range result.RecheckCandidateIDs {
			if !excluded[id] {
				return fmt.Errorf("recheck candidate was not in the excluded inventory: %s", id)
			}
		}
	}
	byID := map[string]literatureCandidate{}
	for _, c := range in.Candidates {
		byID[c.ID] = c
	}
	for _, a := range result.CandidateAssessments {
		if a.Decision == "core" && strings.TrimSpace(byID[a.CandidateID].Abstract) == "" {
			return fmt.Errorf("候选 %s 没有可核验摘要，不能标为核心候选；请保留为待核验材料或依据明确理由排除", a.CandidateID)
		}
	}
	return nil
}
